// Command demo runs the Bharat RWA narrative end to end and prints the
// transaction log.
//
// It runs against the in-memory Store, so it needs no peers, no Docker and no
// network. The logic it exercises is the same logic the chaincode runs,
// because both sit behind the same Store interface; what it does not exercise
// is endorsement, ordering and private data dissemination, which need the real
// network and are covered in network/README.md.
//
//	go run ./cmd/demo
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"bharat-rwa/chaincode/dr/internal/compliance"
	"bharat-rwa/chaincode/dr/internal/dr"
	"bharat-rwa/chaincode/dr/internal/memstore"
	"bharat-rwa/chaincode/dr/internal/registry"
)

const (
	schemeID = "CRE-BLR-001"
	crore    = dr.Paise(10_000_000_00) // one crore rupees, in paise
	lakh     = dr.Paise(100_000_00)
)

func main() {
	start := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	store := memstore.New(start)
	svc := registry.Service{S: store}

	header("BHARAT RWA — DEPOSITARY RECEIPT PROGRAMME ON DRUNIX")
	fmt.Println("  Ledger time starts 2026-01-15. All amounts in rupees; the ledger holds paise.")

	// ---------------------------------------------------------------
	step(1, "Create the scheme")
	// One asset, one city, institutional and HNI only. The compliance
	// parameters are scheme terms, not code constants.
	sch := &dr.Scheme{
		ID: schemeID, AssetName: "Prestige Tech Park, Tower C", City: "Bengaluru",
		TotalUnits: 100_000, UnitPrice: 10_000_00, MinTicketUnits: 1_000,
		LockupDays: 90, HolderCap: 200, ConcentrationCapBps: 2500,
		EligibleClasses:        []dr.InvestorClass{dr.ClassInstitutional, dr.ClassHNI},
		PermittedJurisdictions: []string{"IN"},
		PreferredReturnBps:     800, PromoteBps: 2000,
		Status: dr.StatusSubscribing,
	}
	must(store.PutScheme(sch))
	fmt.Printf("  %s — %s, %s\n", sch.ID, sch.AssetName, sch.City)
	fmt.Printf("  %s units at %s each. Lockup %d days, holder cap %d, concentration cap %.0f%%.\n",
		commas(int64(sch.TotalUnits)), rupees(sch.UnitPrice), sch.LockupDays, sch.HolderCap,
		float64(sch.ConcentrationCapBps)/100)
	fmt.Printf("  Preferred return %.0f%%, promote %.0f%% above the hurdle.\n",
		float64(sch.PreferredReturnBps)/100, float64(sch.PromoteBps)/100)

	// ---------------------------------------------------------------
	step(2, "Register investors")
	// The ledger records a KYC hash, a class, a jurisdiction and an expiry.
	// No name, no PAN, no address: those live in the off-chain vault, and
	// erasing the vault record and its salt makes the hash unresolvable.
	investors := []*dr.Investor{
		{ID: "INV-ALPHA", KYCHash: "sha256:3f9a…c17b", Class: dr.ClassInstitutional, Jurisdiction: "IN",
			EligibilityExpiry: start.AddDate(1, 0, 0), SanctionsClear: true},
		{ID: "INV-BETA", KYCHash: "sha256:88d2…41ef", Class: dr.ClassHNI, Jurisdiction: "IN",
			// Beta's attestation lapses in March. Nobody notices until a
			// transfer is attempted, which is exactly the point.
			EligibilityExpiry: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), SanctionsClear: true},
		{ID: "INV-GAMMA", KYCHash: "sha256:a104…9b30", Class: dr.ClassHNI, Jurisdiction: "IN",
			EligibilityExpiry: start.AddDate(1, 0, 0), SanctionsClear: true},
		{ID: "INV-DELTA", KYCHash: "sha256:7e55…02aa", Class: dr.ClassRetail, Jurisdiction: "IN",
			EligibilityExpiry: start.AddDate(1, 0, 0), SanctionsClear: true},
	}
	for _, i := range investors {
		must(store.PutInvestor(i))
		fmt.Printf("  %-10s %-14s %s   eligibility to %s\n", i.ID, i.Class, i.Jurisdiction,
			i.EligibilityExpiry.Format("2006-01-02"))
	}

	// ---------------------------------------------------------------
	step(3, "Issue receipts against confirmed custody")
	// In the network this needs depositary, trustee and registrar signatures
	// together. The trustee's signature is what ties each receipt to units it
	// has accepted into custody.
	issue(svc, "INV-ALPHA", 40_000, 40*crore)
	issue(svc, "INV-BETA", 20_000, 20*crore)
	issue(svc, "INV-GAMMA", 15_000, 15*crore)
	must(store.PutScheme(&dr.Scheme{ID: sch.ID, AssetName: sch.AssetName, City: sch.City,
		TotalUnits: sch.TotalUnits, UnitPrice: sch.UnitPrice, MinTicketUnits: sch.MinTicketUnits,
		LockupDays: sch.LockupDays, HolderCap: sch.HolderCap, ConcentrationCapBps: sch.ConcentrationCapBps,
		EligibleClasses: sch.EligibleClasses, PermittedJurisdictions: sch.PermittedJurisdictions,
		PreferredReturnBps: sch.PreferredReturnBps, PromoteBps: sch.PromoteBps,
		Status: dr.StatusIssued}))
	fmt.Println("  Scheme is now ISSUED. Transfers are live.")

	// ---------------------------------------------------------------
	header("THE POINT OF THE DEMO: COMPLIANCE RUNS AT ENDORSEMENT")
	fmt.Println("  Each refusal below is a transfer that is never ordered and never")
	fmt.Println("  enters a block. There is nothing to revert, because the endorsement")
	fmt.Println("  policy was never satisfied. On a public chain each of these would")
	fmt.Println("  have been submitted, ordered, paid for, and then reverted.")

	// Day 30: inside the lockup.
	store.Advance(15 * 24 * time.Hour)
	step(4, "Transfer inside the 90-day lockup")
	attempt(svc, "INV-ALPHA", "INV-GAMMA", 5_000)

	// Day 100: the lockup has cleared, but Beta's eligibility has lapsed.
	store.Advance(85 * 24 * time.Hour)
	step(5, "Transfer from a holder whose KYC attestation has lapsed")
	attempt(svc, "INV-BETA", "INV-GAMMA", 5_000)

	step(6, "Transfer to a retail investor in an institutional-and-HNI scheme")
	attempt(svc, "INV-ALPHA", "INV-DELTA", 2_000)

	step(7, "Transfer that would breach the 25% concentration cap")
	// Gamma holds 15,000 of 100,000. Another 12,000 would take them to 27%.
	attempt(svc, "INV-ALPHA", "INV-GAMMA", 12_000)

	step(8, "Transfer below the minimum ticket")
	attempt(svc, "INV-ALPHA", "INV-GAMMA", 400)

	step(9, "Transfer from a frozen holding")
	must(svc.SetFrozen("INV-ALPHA", true, "regulatory direction pending review"))
	fmt.Println("  INV-ALPHA frozen: regulatory direction pending review.")
	attempt(svc, "INV-ALPHA", "INV-GAMMA", 5_000)
	must(svc.SetFrozen("INV-ALPHA", false, ""))
	fmt.Println("  INV-ALPHA released.")

	// ---------------------------------------------------------------
	step(10, "Renew the lapsed attestation, then retry the same transfer")
	must(svc.RenewEligibility("INV-BETA", store.Now().AddDate(1, 0, 0)))
	fmt.Printf("  INV-BETA attestation renewed to %s by the re-verification job.\n",
		store.Now().AddDate(1, 0, 0).Format("2006-01-02"))
	attempt(svc, "INV-BETA", "INV-GAMMA", 5_000)

	// ---------------------------------------------------------------
	step(11, "Force transfer on a court order")
	// The capability that a regulated custodian cannot operate without, and
	// that a public chain cannot offer cleanly. Three organisations must sign
	// and the reason is on the record.
	must(svc.ForceTransfer(schemeID, "INV-GAMMA", "INV-ALPHA", 3_000,
		"order of the City Civil Court, Bengaluru, OS 4412/2026"))
	fmt.Println("  3,000 units moved GAMMA → ALPHA under court order.")
	fmt.Println("  Endorsed by depositary + trustee + registrar. Event on the supervisor's node.")

	// ---------------------------------------------------------------
	step(12, "Record an independent valuation, including a markdown")
	must(svc.RecordValuation(schemeID, 10_400_00, store.Now(), "sha256:d41f…8c02"))
	fmt.Println("  NAV published at ₹10,400.00 per unit — up from the ₹10,000.00 issue price.")
	must(svc.RecordValuation(schemeID, 9_850_00, store.Now().AddDate(0, 3, 0), "sha256:b77e…1194"))
	fmt.Println("  Next cycle: NAV marked down to ₹9,850.00. The event carries markdown=true,")
	fmt.Println("  which is what triggers the disclosure obligation. A platform that cannot")
	fmt.Println("  publish a fall in value has not been tested.")

	// ---------------------------------------------------------------
	step(13, "Q2 distribution — a normal quarter")
	q2, err := svc.RunDistribution(registry.DistributionRequest{
		SchemeID:   schemeID,
		PeriodFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:   time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
		RecordDate: time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
		GrossRent:  160 * lakh, OperatingExpense: 24 * lakh, PropertyMgmt: 5 * lakh,
		Reserves: 8 * lakh, CustodyFee: 470_000_00, AssetMgmtFee: 1875_000_00,
	})
	must(err)
	printDistribution(q2)
	fmt.Println("    INV-GAMMA's unit-days are the pro-ration at work: Gamma's position")
	fmt.Println("    changed mid-period, so the entitlement is weighted by days held,")
	fmt.Println("    computed lot by lot from the register. A closing balance could not")
	fmt.Println("    tell you when the units arrived.")
	fmt.Println("    The promote is zero, and that is the correct answer. Distributable")
	fmt.Println("    income of ₹99.55 lakh did not clear the 8% preferred return on ₹75")
	fmt.Println("    crore of invested capital, so the manager takes nothing. A waterfall")
	fmt.Println("    that paid a promote here would be taking the holders' money.")

	// ---------------------------------------------------------------
	step(14, "Q3 distribution — a quarter that clears the hurdle")
	q3, err := svc.RunDistribution(registry.DistributionRequest{
		SchemeID:   schemeID,
		PeriodFrom: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:   time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
		RecordDate: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
		GrossRent:  320 * lakh, OperatingExpense: 24 * lakh, PropertyMgmt: 5 * lakh,
		Reserves: 8 * lakh, CustodyFee: 470_000_00, AssetMgmtFee: 1875_000_00,
	})
	must(err)
	printDistribution(q3)
	fmt.Println("    Now all three tiers run: the preferred return fills first, the")
	fmt.Println("    catch-up brings the manager to its full promote rate on the whole")
	fmt.Printf("    profit, and the residual splits 80/20. Promote is %.1f%% of distributable.\n",
		float64(q3.Promote)*100/float64(q3.Distributable))

	// ---------------------------------------------------------------
	step(15, "Final register")
	fmt.Printf("  %-12s %10s   %s\n", "HOLDER", "UNITS", "SHARE")
	for _, p := range store.Positions(schemeID) {
		fmt.Printf("  %-12s %10s   %5.2f%%\n", p.InvestorID, commas(int64(p.Units)),
			float64(p.Units)*100/float64(sch.TotalUnits))
	}

	// ---------------------------------------------------------------
	header("LEDGER EVENT LOG — WHAT THE SUPERVISOR'S OBSERVER NODE SEES")
	fmt.Println("  Every privileged action and every compliance refusal, in real time,")
	fmt.Println("  rather than in a quarterly return.")
	fmt.Println()
	for _, e := range store.Events {
		fmt.Printf("  %3d  %-24s %s\n", e.Seq, e.Name, summarise(e.Payload))
	}

	fmt.Printf("\n  %d events, %d compliance refusals, distribution reconciled to the paisa.\n",
		len(store.Events), countRejections(store.Events))
	fmt.Println()
}

// --- narrative helpers ---

func issue(svc registry.Service, investorID string, units dr.Units, paid dr.Paise) {
	lot, err := svc.IssueDR(schemeID, investorID, units, paid)
	must(err)
	fmt.Printf("  %-10s %8s units for %s   lot %s\n",
		investorID, commas(int64(units)), rupees(paid), lot.LotID)
}

// attempt runs a transfer and prints the compliance decision.
func attempt(svc registry.Service, from, to string, units dr.Units) {
	res, err := svc.Transfer(schemeID, from, to, units)
	must(err)
	if res.Decision.Allowed {
		fmt.Printf("  ALLOWED   %s → %s, %s units\n", from, to, commas(int64(units)))
		fmt.Printf("            all %d predicates passed; transaction endorsed and ordered\n",
			len(res.Decision.Checked))
		return
	}
	fmt.Printf("  REFUSED   %s → %s, %s units\n", from, to, commas(int64(units)))
	fmt.Printf("            %s\n", res.Decision.Code)
	fmt.Printf("            %s\n", res.Decision.Reason)
	fmt.Printf("            refused at predicate %d of %d — not endorsed, not ordered, not committed\n",
		len(res.Decision.Checked), 11)
}

func printDistribution(d *dr.Distribution) {
	fmt.Printf("  Period %s to %s, record date %s\n\n",
		d.PeriodFrom.Format("2 Jan 2006"), d.PeriodTo.Format("2 Jan 2006"), d.RecordDate.Format("2 Jan 2006"))
	line := func(label string, v dr.Paise, sign string) {
		fmt.Printf("    %-28s %s%14s\n", label, sign, rupees(v))
	}
	line("Gross rental income", d.GrossRent, " ")
	line("Operating expenses", d.OperatingExpense, "-")
	line("Property management", d.PropertyMgmt, "-")
	line("Debt service", d.DebtService, "-")
	line("Capex and reserves", d.Reserves, "-")
	line("Depositary and custody fee", d.CustodyFee, "-")
	line("Asset management fee", d.AssetMgmtFee, "-")
	fmt.Printf("    %-28s %s\n", "", strings.Repeat("-", 15))
	line("Distributable", d.Distributable, " ")
	fmt.Println()
	line("To holders", d.ToHolders, " ")
	line("Manager promote", d.Promote, " ")
	fmt.Println()
	fmt.Printf("    %-12s %12s %14s %12s %14s\n", "HOLDER", "UNIT-DAYS", "GROSS", "TDS", "NET")
	for _, e := range d.Entitlements {
		fmt.Printf("    %-12s %12s %14s %12s %14s\n", e.InvestorID, commas(e.WeightedDays),
			rupees(e.Gross), rupees(e.TDS), rupees(e.Net))
	}
	fmt.Printf("\n    Residual after largest-remainder allocation: %s\n", rupees(d.Residual))
	fmt.Println("    Reconciliation passed: gross less deductions equals holders plus promote,")
	fmt.Println("    and allocations plus residual equal the holder share. To the paisa.")
}

// --- formatting ---

func header(s string) {
	fmt.Printf("\n%s\n%s\n%s\n", strings.Repeat("=", 74), s, strings.Repeat("=", 74))
}

func step(n int, s string) {
	fmt.Printf("\n[%02d] %s\n%s\n", n, s, strings.Repeat("-", 74))
}

// rupees renders paise as rupees with Indian digit grouping.
func rupees(p dr.Paise) string {
	neg := p < 0
	if neg {
		p = -p
	}
	s := fmt.Sprintf("%s.%02d", commas(int64(p)/100), int64(p)%100)
	if neg {
		return "-" + s
	}
	return s
}

// commas groups digits in the Indian convention: last three, then pairs.
func commas(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	head, tail := s[:len(s)-3], s[len(s)-3:]
	var parts []string
	for len(head) > 2 {
		parts = append([]string{head[len(head)-2:]}, parts...)
		head = head[:len(head)-2]
	}
	if head != "" {
		parts = append([]string{head}, parts...)
	}
	out := strings.Join(parts, ",") + "," + tail
	if neg {
		return "-" + out
	}
	return out
}

func summarise(p any) string {
	m, ok := p.(map[string]any)
	if !ok {
		if l, ok := p.(dr.Lot); ok {
			return fmt.Sprintf("%s %s units", l.InvestorID, commas(int64(l.Units)))
		}
		return ""
	}
	if code, ok := m["code"]; ok {
		return fmt.Sprintf("%v", code)
	}
	var keys []string
	for _, k := range []string{"investorId", "schemeId", "distributionId", "from"} {
		if v, ok := m[k]; ok {
			keys = append(keys, fmt.Sprintf("%v", v))
		}
	}
	return strings.Join(keys, " ")
}

func countRejections(evs []memstore.Event) int {
	n := 0
	for _, e := range evs {
		if e.Name == "TransferRejected" {
			n++
		}
	}
	return n
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo failed: %v\n", err)
		os.Exit(1)
	}
}

var _ = compliance.CodeLockup // keep the compliance codes visible to readers of this file
