package compliance

import (
	"testing"
	"time"

	"bharat-rwa/chaincode/dr/internal/dr"
)

var now = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// okRequest is a transfer that passes every predicate. Each test mutates one
// field, so a failure names exactly which rule fired and nothing else can be
// responsible.
func okRequest() Request {
	seasoned := now.AddDate(0, 0, -120)
	return Request{
		Scheme: Scheme{
			ID: "S1", Status: dr.StatusIssued, TotalUnits: 100_000,
			MinTicketUnits: 1_000, LockupDays: 90, HolderCap: 200,
			ConcentrationCapBps:    2500,
			EligibleClasses:        []dr.InvestorClass{dr.ClassInstitutional, dr.ClassHNI},
			PermittedJurisdictions: []string{"IN"},
		},
		From: Party{ID: "A", Class: dr.ClassInstitutional, Jurisdiction: "IN",
			EligibilityExpiry: now.AddDate(1, 0, 0), SanctionsClear: true},
		To: Party{ID: "B", Class: dr.ClassHNI, Jurisdiction: "IN",
			EligibilityExpiry: now.AddDate(1, 0, 0), SanctionsClear: true},
		Units: 5_000,
		FromLots: []dr.Lot{
			{LotID: "L1", SchemeID: "S1", InvestorID: "A", Units: 40_000, AcquiredAt: seasoned},
		},
		HolderCount: 12, ToExistingUnits: 10_000, Now: now,
	}
}

func TestBaselineTransferIsAllowed(t *testing.T) {
	d := Evaluate(okRequest())
	if !d.Allowed {
		t.Fatalf("baseline transfer refused: %s — %s", d.Code, d.Reason)
	}
	if len(d.Checked) != 11 {
		t.Errorf("evaluated %d predicates, expected all 11 to run on a passing transfer", len(d.Checked))
	}
}

func TestPredicates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Request)
		want   Code
	}{
		{"scheme not yet issued", func(r *Request) {
			r.Scheme.Status = dr.StatusSubscribing
		}, CodeSchemeState},

		{"transferor frozen", func(r *Request) {
			r.From.Frozen = true
		}, CodeFrozen},

		{"transferee frozen", func(r *Request) {
			r.To.Frozen = true
		}, CodeFrozen},

		{"sanctions hit on transferee", func(r *Request) {
			r.To.SanctionsClear = false
		}, CodeSanctions},

		{"transferor attestation expired", func(r *Request) {
			r.From.EligibilityExpiry = now.AddDate(0, 0, -1)
		}, CodeEligibility},

		// The boundary case: an attestation expiring exactly now is expired.
		// Eligibility must be strictly in the future.
		{"attestation expiring this instant", func(r *Request) {
			r.To.EligibilityExpiry = now
		}, CodeEligibility},

		{"transferee in a restricted jurisdiction", func(r *Request) {
			r.To.Jurisdiction = "SG"
		}, CodeJurisdiction},

		{"retail investor in an institutional scheme", func(r *Request) {
			r.To.Class = dr.ClassRetail
		}, CodeSuitability},

		{"transferor lacks the units", func(r *Request) {
			r.Units = 50_000
		}, CodeInsufficient},

		{"below the minimum ticket", func(r *Request) {
			r.Units = 400
		}, CodeMinTicket},

		// A transfer that leaves an unsellable stub behind is as much a
		// breach as a purchase below the minimum.
		{"leaves a stub below the minimum ticket", func(r *Request) {
			r.Units = 39_500 // holder has 40,000; 500 would remain
		}, CodeMinTicket},

		{"units still inside the lockup", func(r *Request) {
			r.FromLots[0].AcquiredAt = now.AddDate(0, 0, -30)
		}, CodeLockup},

		{"only some units have seasoned", func(r *Request) {
			r.FromLots = []dr.Lot{
				{LotID: "L1", InvestorID: "A", Units: 2_000, AcquiredAt: now.AddDate(0, 0, -120)},
				{LotID: "L2", InvestorID: "A", Units: 38_000, AcquiredAt: now.AddDate(0, 0, -10)},
			}
			r.Units = 5_000 // only 2,000 have cleared
		}, CodeLockup},

		{"new holder at the holder cap", func(r *Request) {
			r.To.IsNewHolder = true
			r.ToExistingUnits = 0
			r.HolderCount = 200
		}, CodeHolderCap},

		{"breaches the concentration cap", func(r *Request) {
			r.ToExistingUnits = 21_000 // +5,000 = 26%
		}, CodeConcentration},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := okRequest()
			c.mutate(&r)
			d := Evaluate(r)
			if d.Allowed {
				t.Fatalf("transfer allowed, expected refusal with %s", c.want)
			}
			if d.Code != c.want {
				t.Errorf("refused with %s (%s), expected %s", d.Code, d.Reason, c.want)
			}
			if d.Reason == "" {
				t.Error("refusal carried no reason; the exception queue cannot be worked from a bare code")
			}
		})
	}
}

// TestExistingHolderDoesNotTripHolderCap checks that moving units between
// existing holders is permitted at the cap, since it cannot increase the count.
func TestExistingHolderDoesNotTripHolderCap(t *testing.T) {
	r := okRequest()
	r.HolderCount = 200
	r.To.IsNewHolder = false
	if d := Evaluate(r); !d.Allowed {
		t.Errorf("transfer between existing holders refused at the cap: %s — %s", d.Code, d.Reason)
	}
}

// TestLockupBoundary checks the exact lockup edge: a unit held for precisely
// the lockup period has cleared it.
func TestLockupBoundary(t *testing.T) {
	r := okRequest()
	r.FromLots[0].AcquiredAt = now.AddDate(0, 0, -90)
	if d := Evaluate(r); !d.Allowed {
		t.Errorf("unit held exactly 90 days refused by a 90-day lockup: %s", d.Reason)
	}

	r.FromLots[0].AcquiredAt = now.AddDate(0, 0, -89)
	if d := Evaluate(r); d.Allowed {
		t.Error("unit held 89 days passed a 90-day lockup")
	}
}

// TestDecisionRecordsWhatWasChecked guards the audit property: a passing
// transfer must record that the predicates ran, not merely that it passed.
func TestDecisionRecordsWhatWasChecked(t *testing.T) {
	d := Evaluate(okRequest())
	if len(d.Checked) == 0 {
		t.Fatal("a successful decision recorded no predicates; an inspection could not prove the checks ran")
	}
	if d.EvaluatedAt.IsZero() {
		t.Error("decision carried no evaluation timestamp")
	}
}
