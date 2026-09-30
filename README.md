# Bharat RWA

**Tokenised Indian real assets on Drunix, with a bank as depositary.**

Drunix Hackathon in collaboration with Citi · CHL-7007

---

## The claim

A token is worth exactly what the legal chain behind it can enforce. Most
tokenisation projects build the token first and discover the legal chain
later. This one starts from the structure a regulated custodian can actually
operate, and puts on the ledger only the things a ledger is good at.

Two decisions follow from that, and they are the whole design:

**1. The investor holds a depositary receipt, not "a token that is the
property."** Citi already runs this model in production — its Digital
Depositary Receipts, launched June 2026, apply its Issuer Services depositary
receipt product to private-market shares with the bank acting as both issuer
and custodian, and the bank positioned it explicitly against structures that
rely on special-purpose vehicles. We adopt that pattern rather than inventing
one. The receipt is evidence of a claim on units held in custody. If this
ledger disappeared tomorrow, the holder's position would still be enforceable
against the register and the custody arrangement behind it.

**2. Compliance runs at endorsement, not in a transfer hook.** The compliance
organisation is an endorsing member of the network. A transfer it will not
sign never satisfies the endorsement policy, never reaches the ordering
service, and never enters a block. There is nothing to revert, because there
is nothing to revert from.

That second one is not available on a public chain, and it is the reason this
belongs on Drunix rather than beside it.

---

## Run it

```bash
make demo     # the full narrative, no Docker, no network, about a second
make test     # the domain test suite
make check    # vet, test, demo
```

`make demo` prints a fifteen-step lifecycle: scheme creation, investor
registration, issuance, **six compliance refusals each for a different
reason**, an attestation renewal that unblocks one of them, a court-ordered
force transfer, a NAV markdown, two quarterly distributions reconciled to the
paisa, and the event log a supervisor's observer node would see.

A representative refusal:

```
[07] Transfer that would breach the 25% concentration cap
------------------------------------------------------------------
  REFUSED   INV-ALPHA → INV-GAMMA, 12,000 units
            CONCENTRATION_CAP_EXCEEDED
            transferee would hold 27000 units (27.00%), above the cap of 25.00%
            refused at predicate 11 of 11 — not endorsed, not ordered, not committed
```

---

## Why Drunix, specifically

Not "because it is mandated." Four properties of this platform are load-bearing
for this use case:

| Drunix property | What it does here |
| --- | --- |
| **Lite Peer / Committing Peer split** | A distribution partner carries high read volume and no settlement obligation. As a Lite Peer it joins for the cost of an endorsement node instead of a full state database. That is the difference between a network and one bank's database with extra latency |
| **SQL state database on YugabyteDB** | The register becomes SQL-queryable. Beneficial-ownership lookups, concentration checks and regulatory extracts are joins, and reconciliation to the bank's books of record stops being a nightly batch problem |
| **Shared transient store for private data** | Private data dissemination to a wide membership is the pattern that makes stock Fabric slow, because gossip call volume grows with every collection member. Adding the twelfth distribution partner does not cost what it does upstream |
| **Permissioned identity, no gas, no mempool** | Investors authenticate with bank credentials. No wallets, no seed phrases, no self-custody, and no transaction fee an investor has to understand |

Drunix is an enhanced fork of Hyperledger Fabric v2.5.x maintained by NPCI,
Apache 2.0 licensed, backward compatible with the Fabric chaincode lifecycle.

---

## Architecture

```
                     ┌──────────────── Drunix network ─────────────────┐
  Investor portal ──►│  Lite Peers (endorse)      Committing Peers     │
                     │  ├ Compliance ★            ├ Citi depositary    │
                     │  ├ Sponsor                 ├ SEBI-reg. RTA      │
                     │  └ Valuer                  ├ Trustee            │
                     │                            └ Regulator (observe)│
                     │        │                          │             │
                     │   endorsement               YugabyteDB          │
                     │   policy = the control      SQL state           │
                     └────────────────────────────────────────────────┘
        Off-chain PII vault ──hash pointer only──► ledger
```

★ The compliance peer is the control point. See
[`network/endorsement-policies.md`](network/endorsement-policies.md).

### Code layout

```
chaincode/dr/
├── main.go, contract.go, ledger.go   ← the only files that know Fabric exists
└── internal/
    ├── dr/          domain model: schemes, investors, lots, distributions
    ├── compliance/  eleven transfer predicates, pure functions
    ├── waterfall/   distribution engine, integer paise, deterministic
    ├── registry/    receipt lifecycle over a Store interface
    └── memstore/    in-memory Store, so the demo runs without a network
network/
├── configtx.yaml              7 orgs, 3 channels, 5 Raft orderers
├── collections_config.json    private data collections
├── endorsement-policies.md    which signatures, and why
└── README.md                  deployment against the Drunix test network
```

`internal/` imports nothing from Fabric. The ledger is an adapter behind a
`Store` interface, which is why the same rules can be unit tested without a
peer and why the receipt lifecycle could be carried to a second market
infrastructure — which matters, because Citi has said it is considering
extending its DDR offering across other infrastructures and networks. This is
the India node, not a replacement for the Swiss one.

---

## The eleven transfer predicates

Every one must pass before compliance will endorse.

| Predicate | Fails when |
| --- | --- |
| Scheme live | Transfers attempted outside the ISSUED state |
| Not frozen | Either party under a freeze |
| Sanctions clear | Open screening hit on either side |
| Eligibility current | KYC attestation lapsed — **the one most platforms forget** |
| Jurisdiction permitted | Recipient outside the permitted set |
| Class suitable | Retail investor in an accredited-only scheme |
| Sufficient units | Transferor does not hold them |
| Minimum ticket | Below the minimum, or leaves an unsellable stub |
| Lockup satisfied | Units have not seasoned; checked lot by lot |
| Holder cap | A new holder would breach the statutory ceiling |
| Concentration cap | One holder would exceed the position limit |

Eligibility expiry deserves its own note. An investor verified at onboarding
is not verified forever. An expired attestation blocks transfers without
blocking the distributions they are already entitled to, and it is cleared by
a scheduled re-verification job rather than by someone remembering.

---

## The waterfall

Written as one line in most specifications, six weeks of work in practice, and
the thing an investor never forgives being wrong.

```
Gross rent
  − operating expenses, property management, debt service, reserves   (property level)
  − depositary and custody fee, asset management fee                  (scheme level)
  = distributable
      → preferred return to holders      (8%, accrued for the actual period)
      → manager catch-up                 (to the full promote rate on the whole profit)
      → residual split                   (80/20)
```

Four properties, each covered by a test:

- **Integer paise throughout.** No float touches the money path.
- **Deterministic.** Every endorsing peer must compute byte-identical results,
  so nothing iterates a map and every tie breaks on a stable key. A chaincode
  that iterates a Go map produces a different read-write set on each peer and
  fails validation for reasons that look like anything but a map.
- **Pro-rated by holding period.** An investor who bought mid-quarter receives
  a partial period, computed lot by lot from the register. A closing balance
  cannot tell you when the units arrived.
- **Conserved.** `Reconcile` refuses to return a distribution that does not
  balance, and it runs before any payment is released.

The demo shows a quarter where the promote is **zero** — income did not clear
the preferred return, so the manager takes nothing — and a quarter where all
three tiers run. A waterfall that paid a promote in the first case would be
taking the holders' money.

---

## Privacy: the DPDP problem

An immutable ledger and a statutory right to erasure are structurally
incompatible. The resolution is architectural, not a paragraph in a privacy
notice:

- Personal data lives in an off-chain vault with ordinary delete semantics
- The ledger holds a salted hash commitment and nothing else
- Erasure destroys the vault record **and the salt**, making the on-chain
  commitment permanently unresolvable
- Statutory register retention is satisfied by the RTA under its own regime

The investor record on-ledger carries a hash, a class, a jurisdiction, an
expiry and two flags. That is everything the predicates need and nothing a
data subject could ask to have erased.

This design needs privacy counsel sign-off before real investor data touches
it. We treat that as a launch blocker.

---

## Built, designed, not done

Judges should be able to tell these apart at a glance.

| | Status |
| --- | --- |
| Domain model, 11 compliance predicates, waterfall engine | **Built.** Compiles, vetted, tested |
| Receipt lifecycle: issue, transfer, force transfer, freeze, valuation, distribution | **Built** |
| Fabric adapter: composite keys, private data collections, events, tx-timestamp clock | **Built.** Compiles against `fabric-contract-api-go/v2` |
| End-to-end demo with transaction log | **Built.** `make demo` |
| Test suite: conservation, determinism, pro-ration, record-date freeze, lockup boundary | **Built.** `make test` |
| Channel config, collections config, endorsement policies | **Written, not applied.** No network was stood up for this submission |
| Deployment against the Drunix test network | **Documented, not executed.** `network/README.md` |
| Investor portal, admin console, regulator console | **Designed, not built.** Deliberately: the ledger layer is where the argument is |
| Escrow cash leg, UPI integration, tokenized rupee deposits | **Roadmap.** See below |

### Known limitations

- **Preferred return does not accrue cumulatively.** A quarter that falls short
  of the hurdle does not carry the shortfall into the next one. Real waterfalls
  do. Fixing it means storing accrued-unpaid pref per scheme, which is a
  schema change we did not make under time pressure.
- **Wind-down is modelled but not implemented.** Return of capital before
  profit split is a different tier order from an income distribution.
- **No cash leg.** Settlement is assumed, not executed. See below.
- **Single-scheme concentration only.** Cross-scheme exposure per investor is
  not aggregated.

---

## The honest gap: settlement

There is no atomic delivery-versus-payment without money on the same ledger,
and Citi Token Services — live in seven markets as of September 2026 — does not
operate in India.

| Option | Settlement risk | Verdict |
| --- | --- | --- |
| Bank escrow with attested confirmation | Open during the confirmation window | What ships now |
| UPI / NEFT / RTGS with attested payment | Same window, better retail experience | Phase 2 |
| **Tokenized rupee deposit on Drunix** | None. True atomic DvP | **The actual product.** Needs RBI and NPCI |
| Citi Token Services extended to India | None | The strategic prize |

Overclaiming atomic DvP to a bank's markets team is how a project loses the
room in one meeting. The escrow model means T+1 settlement and partly manual
reconciliation, and the blockchain-settlement-speed headline does not survive
contact with it.

But this is also why the platform is interesting. Drunix exists because NPCI
wants India's tokenisation infrastructure to be domestic. A rupee deposit token
on that infrastructure, with a bank like Citi as an issuing member, turns this
from a tokenisation platform into settlement infrastructure. That is the thing
worth building toward.

---

## Roadmap

| Phase | What | Gate |
| --- | --- | --- |
| **Foundation** | Legal structure, RTA appointment, 7-org network live | Regulatory approval and third-party risk sign-off |
| **Pilot** | One asset, institutional only, escrow settlement | A full distribution cycle and a simulated wind-down |
| **Scale** | Retail via UPI, periodic call auctions, second asset class | Secondary liquidity demonstrated |
| **Rails** | Rupee deposit token with RBI and NPCI, true atomic DvP | — |

Liquidity note: a continuous order book with forty holders is a wide spread
with extra steps. Periodic call auctions concentrate liquidity instead of
diffusing it, which is the right primitive for an illiquid real asset.

---

## Regulatory route

SEBI's SM REIT framework for domestic fractional real estate, with GIFT
City / IFSCA as the fallback for a cross-border structure. Mandatory roles:
investment manager, independent trustee, SEBI-registered RTA, registered
valuer, statutory auditor, and the depositary.

Specific requirements on minimum investment, leverage, distribution frequency
and valuation cadence must be confirmed with Indian securities counsel. Nothing
in this repository is a legal opinion.

---

## Build

Go 1.24. The domain packages have **zero external dependencies**, so `make
demo` and `make test` need nothing but a Go toolchain. `make build` compiles
the chaincode and needs the Fabric modules:

```bash
cd chaincode/dr && go mod download && go build -o dr .
```

---

## Sources

- [Citi launches market-first tokenized depositary receipts](https://www.citigroup.com/global/news/press-release/2026/citi-market-first-tokenized-depositary-receipts-connect-private-companies-investors) — Citi, 11 June 2026
- [Citi opens new route into private markets](https://www.coindesk.com/business/2026/06/11/citi-opens-new-route-into-private-markets-with-tokenized-share-offering) — on Citi as issuer and custodian, and the comparison to SPV structures
- [Citi Token Services expands to Japan and the UAE](https://www.ccn.com/news/crypto/citi-token-services-blockchain-payments-japan-uae-crypto/) — the seven live markets
- [NPCI unveils Drunix](https://www.business-standard.com/finance/news/npci-unveils-drunix-to-support-blockchain-and-tokenisation-ecosystems-126061701135_1.html) — Business Standard, 17 June 2026
- [npci/drunix architecture](https://deepwiki.com/npci/drunix) — the Fabric v2.5.x fork, LP/CP split, stateless validation, YugabyteDB state database, shared transient store
