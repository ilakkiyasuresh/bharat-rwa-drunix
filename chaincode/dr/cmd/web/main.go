// Command web serves the Bharat RWA demo on http://localhost:8080.
//
// It does not change any chaincode. Each click on "Run demo" executes the
// existing `go run ./cmd/demo` and shows its output in the browser, with
// refusals and allowed transfers colour-coded.
//
// Run from chaincode/dr:
//
//	go run ./cmd/web
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"
)

func runDemo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/demo")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	if err != nil {
		fmt.Fprintf(&out, "\n[demo exited with an error: %v]\n", err)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(out.Bytes())
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(page))
}

func main() {
	if _, err := os.Stat("cmd/demo"); err != nil {
		log.Fatal("run this from the chaincode/dr folder (cmd/demo not found here)")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := "127.0.0.1:" + port

	mux := http.NewServeMux()
	mux.HandleFunc("/", index)
	mux.HandleFunc("/run", runDemo)

	log.Printf("Bharat RWA demo running at http://localhost:%s  (Ctrl+C to stop)", port)
	log.Fatal(http.ListenAndServe(addr, mux))
}

const page = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Bharat RWA — live demo</title>
<style>
  :root {
    --bg: #f3f6fc; --card: #ffffff; --text: #1e3a5f; --muted: #64748b;
    --accent: #2563eb; --refused: #b91c1c; --refused-bg: #fee2e2;
    --allowed: #15803d; --allowed-bg: #dcfce7; --rule: #cbd5e1; --code-bg: #f8fafc;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0f172a; --card: #1e293b; --text: #e2e8f0; --muted: #94a3b8;
      --accent: #60a5fa; --refused: #fca5a5; --refused-bg: #450a0a;
      --allowed: #86efac; --allowed-bg: #052e16; --rule: #334155; --code-bg: #0b1220;
    }
  }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--bg); color: var(--text);
         font-family: Calibri, Arial, sans-serif; }
  main { max-width: 980px; margin: 0 auto; padding: 28px 20px 60px; }
  .kicker { color: var(--muted); font-size: 12px; letter-spacing: .08em; text-transform: uppercase; }
  h1 { margin: 6px 0 4px; font-size: 30px; color: var(--accent); }
  p.sub { margin: 0 0 18px; color: var(--muted); }
  .bar { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; margin-bottom: 16px; }
  button { background: var(--accent); color: #fff; border: 0; border-radius: 8px;
           padding: 10px 18px; font-size: 15px; font-weight: 700; cursor: pointer; }
  button:disabled { opacity: .6; cursor: wait; }
  .chip { background: var(--card); border: 1px solid var(--rule); border-radius: 999px;
          padding: 6px 12px; font-size: 13px; }
  .chip b { color: var(--accent); }
  pre { margin: 0; background: var(--code-bg); border: 1px solid var(--rule); border-radius: 10px;
        padding: 16px; overflow-x: auto; font-family: Consolas, "Courier New", monospace;
        font-size: 13px; line-height: 1.5; }
  .l { display: block; white-space: pre; }
  .step { color: var(--accent); font-weight: 700; margin-top: 10px; }
  .rule { color: var(--rule); }
  .refused { color: var(--refused); background: var(--refused-bg); font-weight: 700; }
  .allowed { color: var(--allowed); background: var(--allowed-bg); font-weight: 700; }
  .ok { color: var(--allowed); }
</style>
</head>
<body>
<main>
  <div class="kicker">Drunix Hackathon · CHL-7007</div>
  <h1>Bharat RWA — live demo</h1>
  <p class="sub">Compliance runs at endorsement: every refusal below is never ordered and never enters a block.</p>
  <div class="bar">
    <button id="run">Run demo</button>
    <span class="chip">Refusals: <b id="nref">–</b></span>
    <span class="chip">Allowed: <b id="nall">–</b></span>
    <span class="chip">Run time: <b id="ms">–</b></span>
  </div>
  <pre id="out">Press "Run demo".</pre>
</main>
<script>
(function () {
  var btn = document.getElementById("run");
  var out = document.getElementById("out");

  function classify(line) {
    if (/^\s*\[\d+\]/.test(line)) return "step";
    if (/^\s*REFUSED/.test(line)) return "refused";
    if (/^\s*ALLOWED/.test(line)) return "allowed";
    if (/^\s*[=\-]{10,}\s*$/.test(line)) return "rule";
    if (/Reconciliation passed/.test(line)) return "ok";
    return "";
  }

  function render(text) {
    out.textContent = "";
    var lines = text.split("\n");
    var refused = 0, allowed = 0;
    for (var i = 0; i < lines.length; i++) {
      var c = classify(lines[i]);
      if (c === "refused") refused++;
      if (c === "allowed") allowed++;
      var span = document.createElement("span");
      span.className = "l" + (c ? " " + c : "");
      span.textContent = lines[i] === "" ? " " : lines[i];
      out.appendChild(span);
    }
    document.getElementById("nref").textContent = refused;
    document.getElementById("nall").textContent = allowed;
  }

  function run() {
    btn.disabled = true;
    btn.textContent = "Running…";
    out.textContent = "Running go run ./cmd/demo …";
    var t0 = performance.now();
    fetch("/run", { cache: "no-store" })
      .then(function (r) { return r.text(); })
      .then(function (text) {
        render(text);
        document.getElementById("ms").textContent =
          ((performance.now() - t0) / 1000).toFixed(1) + " s";
      })
      .catch(function (e) { out.textContent = "Could not reach the server: " + e; })
      .then(function () { btn.disabled = false; btn.textContent = "Run demo again"; });
  }

  btn.addEventListener("click", run);
  run();
})();
</script>
</body>
</html>
`
