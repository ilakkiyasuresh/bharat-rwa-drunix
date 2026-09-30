package waterfall

import (
	"testing"
	"time"

	"bharat-rwa/chaincode/dr/internal/dr"
)

var (
	from = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to   = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
)

func base(lots []dr.Lot) Input {
	classOf := map[string]dr.InvestorClass{}
	for _, l := range lots {
		classOf[l.InvestorID] = dr.ClassInstitutional
	}
	return Input{
		SchemeID: "S1", PeriodFrom: from, PeriodTo: to, RecordDate: to,
		PreferredReturnBps: 800, PromoteBps: 2000,
		Lots: lots, ClassOf: classOf,
		TDSRateBps: map[dr.InvestorClass]int{dr.ClassInstitutional: 0},
	}
}

func lot(id, investor string, units dr.Units, basis dr.Paise, acquired time.Time) dr.Lot {
	return dr.Lot{LotID: id, SchemeID: "S1", InvestorID: investor, Units: units, CostBasis: basis, AcquiredAt: acquired}
}

// TestConservation is the test that matters most. Whatever the inputs, money
// may not be created or destroyed: everything that came in is either a
// deduction, the manager's promote, a holder's gross, or the residual.
func TestConservation(t *testing.T) {
	cases := []struct {
		name        string
		gross, opex dr.Paise
	}{
		{"below hurdle", 10_000_00, 1_000_00},
		{"at hurdle", 200_000_00, 1_000_00},
		{"above hurdle", 10_000_000_00, 1_000_00},
		// A prime gross against three unequal holders is where naive
		// rounding leaks paise.
		{"awkward rounding", 999_999_99, 7_77},
		{"zero income", 0, 0},
		{"loss making", 1_000_00, 5_000_00},
	}
	lots := []dr.Lot{
		lot("L1", "A", 40_000, 40_000_000_00, from),
		lot("L2", "B", 33_333, 33_333_000_00, from),
		lot("L3", "C", 26_667, 26_667_000_00, from),
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base(lots)
			in.GrossRent, in.OperatingExpense = c.gross, c.opex
			in.InvestedCapital = 100_000_000_00
			d, err := Compute(in)
			if err != nil {
				t.Fatalf("compute: %v", err)
			}
			var allocated dr.Paise
			for _, e := range d.Entitlements {
				allocated += e.Gross
			}
			if got, want := allocated+d.Residual+d.Promote, d.Distributable; got != want {
				t.Errorf("money leaked: allocated+residual+promote = %d, distributable = %d (delta %d paise)",
					got, want, got-want)
			}
			if err := Reconcile(d); err != nil {
				t.Errorf("reconcile: %v", err)
			}
		})
	}
}

// TestDeterminism guards the property that makes this safe to run as
// chaincode. Every endorsing peer must produce byte-identical results, so the
// same input must give the same output every time, including the order of
// entitlements and which holders received the residual paise.
func TestDeterminism(t *testing.T) {
	lots := []dr.Lot{
		lot("L1", "zeta", 1_001, 1_001_00, from),
		lot("L2", "alpha", 999, 999_00, from),
		lot("L3", "mu", 1_000, 1_000_00, from),
	}
	in := base(lots)
	in.GrossRent, in.InvestedCapital = 1_000_000_01, 3_000_00

	first, err := Compute(in)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	for i := 0; i < 200; i++ {
		got, err := Compute(in)
		if err != nil {
			t.Fatalf("compute: %v", err)
		}
		if len(got.Entitlements) != len(first.Entitlements) {
			t.Fatalf("entitlement count changed between runs")
		}
		for j := range got.Entitlements {
			if got.Entitlements[j] != first.Entitlements[j] {
				t.Fatalf("run %d differs at entitlement %d: %+v vs %+v",
					i, j, got.Entitlements[j], first.Entitlements[j])
			}
		}
	}
}

// TestProRationByHoldingPeriod checks that a holder who bought halfway through
// the period receives roughly half of what an identical full-period holder
// receives.
func TestProRationByHoldingPeriod(t *testing.T) {
	mid := from.AddDate(0, 0, 45)
	lots := []dr.Lot{
		lot("L1", "full", 1_000, 1_000_000_00, from),
		lot("L2", "half", 1_000, 1_000_000_00, mid),
	}
	in := base(lots)
	in.GrossRent, in.InvestedCapital = 1_000_000_00, 2_000_000_00
	d, err := Compute(in)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	var full, half dr.Paise
	for _, e := range d.Entitlements {
		switch e.InvestorID {
		case "full":
			full = e.Gross
		case "half":
			half = e.Gross
		}
	}
	if half == 0 || full == 0 {
		t.Fatalf("expected both holders to receive something, got full=%d half=%d", full, half)
	}
	ratio := float64(half) / float64(full)
	if ratio < 0.48 || ratio > 0.52 {
		t.Errorf("half-period holder received %.3f of the full-period holder, want about 0.50", ratio)
	}
}

// TestRecordDateFreeze checks that units acquired after the record date earn
// nothing in that cycle.
func TestRecordDateFreeze(t *testing.T) {
	lots := []dr.Lot{
		lot("L1", "holder", 1_000, 1_000_000_00, from),
		lot("L2", "latecomer", 1_000, 1_000_000_00, to.AddDate(0, 0, 5)),
	}
	in := base(lots)
	in.GrossRent, in.InvestedCapital = 1_000_000_00, 2_000_000_00
	d, err := Compute(in)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	for _, e := range d.Entitlements {
		if e.InvestorID == "latecomer" {
			t.Errorf("a holder who acquired after the record date received %d paise", e.Gross)
		}
	}
}

// TestPromoteOnlyAboveHurdle checks the tier order: no promote until the
// preferred return is satisfied.
func TestPromoteOnlyAboveHurdle(t *testing.T) {
	lots := []dr.Lot{lot("L1", "A", 10_000, 100_000_000_00, from)}

	below := base(lots)
	below.InvestedCapital = 100_000_000_00
	below.GrossRent = 100_00 // trivially small
	d, err := Compute(below)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if d.Promote != 0 {
		t.Errorf("manager took %d paise below the hurdle; the promote must be zero there", d.Promote)
	}

	above := base(lots)
	above.InvestedCapital = 100_000_000_00
	above.GrossRent = 50_000_000_00
	d2, err := Compute(above)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if d2.Promote == 0 {
		t.Fatal("manager took nothing well above the hurdle")
	}
	// With a full catch-up the manager ends at its promote rate on the whole
	// distributable amount, not just on the excess.
	share := float64(d2.Promote) / float64(d2.Distributable)
	if share < 0.199 || share > 0.201 {
		t.Errorf("promote settled at %.4f of distributable, want about 0.2000 after catch-up", share)
	}
}

// TestNoNegativeDistribution checks that a loss-making period distributes
// nothing rather than a negative amount.
func TestNoNegativeDistribution(t *testing.T) {
	lots := []dr.Lot{lot("L1", "A", 1_000, 1_000_000_00, from)}
	in := base(lots)
	in.GrossRent, in.OperatingExpense, in.InvestedCapital = 10_000_00, 50_000_00, 1_000_000_00
	d, err := Compute(in)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if d.Distributable < 0 || d.ToHolders < 0 || d.Promote < 0 {
		t.Errorf("negative amounts in a loss-making period: %+v", d)
	}
}

// TestTDSWithheldByClass checks that withholding follows the holder's class.
func TestTDSWithheldByClass(t *testing.T) {
	lots := []dr.Lot{
		lot("L1", "inst", 1_000, 1_000_000_00, from),
		lot("L2", "hni", 1_000, 1_000_000_00, from),
	}
	in := base(lots)
	in.ClassOf = map[string]dr.InvestorClass{"inst": dr.ClassInstitutional, "hni": dr.ClassHNI}
	in.TDSRateBps = map[dr.InvestorClass]int{dr.ClassInstitutional: 0, dr.ClassHNI: 1000}
	in.GrossRent, in.InvestedCapital = 10_000_000_00, 2_000_000_00
	d, err := Compute(in)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	for _, e := range d.Entitlements {
		switch e.InvestorID {
		case "inst":
			if e.TDS != 0 {
				t.Errorf("withheld %d from an institutional holder at a 0%% rate", e.TDS)
			}
		case "hni":
			want := e.Gross / 10
			if e.TDS != want {
				t.Errorf("withheld %d from an HNI holder, want %d", e.TDS, want)
			}
		}
		if e.Gross != e.TDS+e.Net {
			t.Errorf("entitlement for %s does not split: %d != %d + %d", e.InvestorID, e.Gross, e.TDS, e.Net)
		}
	}
}
