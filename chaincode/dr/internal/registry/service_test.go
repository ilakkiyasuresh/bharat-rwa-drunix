package registry

import (
	"testing"
	"time"

	"bharat-rwa/chaincode/dr/internal/compliance"
	"bharat-rwa/chaincode/dr/internal/dr"
	"bharat-rwa/chaincode/dr/internal/memstore"
)

var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func fixture(t *testing.T) (*memstore.Store, Service) {
	t.Helper()
	st := memstore.New(start)
	if err := st.PutScheme(&dr.Scheme{
		ID: "S1", TotalUnits: 100_000, UnitPrice: 10_000_00, MinTicketUnits: 1_000,
		LockupDays: 90, HolderCap: 200, ConcentrationCapBps: 5000,
		EligibleClasses:        []dr.InvestorClass{dr.ClassInstitutional, dr.ClassHNI},
		PermittedJurisdictions: []string{"IN"},
		PreferredReturnBps:     800, PromoteBps: 2000, Status: dr.StatusIssued,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B"} {
		if err := st.PutInvestor(&dr.Investor{
			ID: id, KYCHash: "sha256:test", Class: dr.ClassInstitutional, Jurisdiction: "IN",
			EligibilityExpiry: start.AddDate(2, 0, 0), SanctionsClear: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return st, Service{S: st}
}

func TestIssueRespectsTotalSupply(t *testing.T) {
	st, svc := fixture(t)
	if _, err := svc.IssueDR("S1", "A", 60_000, 60_000_000_00); err != nil {
		t.Fatalf("first issuance: %v", err)
	}
	if _, err := svc.IssueDR("S1", "B", 50_000, 50_000_000_00); err == nil {
		t.Fatal("issuing past total supply was permitted")
	}
	lots, _ := st.AllLots("S1")
	var total dr.Units
	for _, l := range lots {
		total += l.Units
	}
	if total != 60_000 {
		t.Errorf("supply is %d units after a rejected issuance, want 60000", total)
	}
}

// TestTransferConservesUnits is the invariant that matters on the register:
// a transfer moves units, it does not mint or burn them.
func TestTransferConservesUnits(t *testing.T) {
	st, svc := fixture(t)
	if _, err := svc.IssueDR("S1", "A", 40_000, 40_000_000_00); err != nil {
		t.Fatal(err)
	}
	st.Advance(120 * 24 * time.Hour)

	res, err := svc.Transfer("S1", "A", "B", 15_000)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Decision.Allowed {
		t.Fatalf("transfer refused: %s — %s", res.Decision.Code, res.Decision.Reason)
	}

	var total dr.Units
	byHolder := map[string]dr.Units{}
	for _, p := range st.Positions("S1") {
		total += p.Units
		byHolder[p.InvestorID] = p.Units
	}
	if total != 40_000 {
		t.Errorf("register holds %d units after the transfer, want 40000", total)
	}
	if byHolder["A"] != 25_000 || byHolder["B"] != 15_000 {
		t.Errorf("positions are A=%d B=%d, want A=25000 B=15000", byHolder["A"], byHolder["B"])
	}
}

// TestCostBasisFollowsUnits checks that a partial transfer splits the lot's
// cost basis proportionally. Capital gains reporting depends on this.
func TestCostBasisFollowsUnits(t *testing.T) {
	st, svc := fixture(t)
	if _, err := svc.IssueDR("S1", "A", 40_000, 40_000_000_00); err != nil {
		t.Fatal(err)
	}
	st.Advance(120 * 24 * time.Hour)
	if _, err := svc.Transfer("S1", "A", "B", 10_000); err != nil {
		t.Fatal(err)
	}

	basis := map[string]dr.Paise{}
	for _, p := range st.Positions("S1") {
		for _, l := range p.Lots {
			basis[p.InvestorID] += l.CostBasis
		}
	}
	// A quarter of the units carries a quarter of the basis.
	if want := dr.Paise(10_000_000_00); basis["B"] != want {
		t.Errorf("transferee basis is %d, want %d", basis["B"], want)
	}
	if want := dr.Paise(30_000_000_00); basis["A"] != want {
		t.Errorf("transferor basis is %d, want %d", basis["A"], want)
	}
}

// TestLockupClockRestartsOnTransfer checks that a recipient's lockup runs from
// their own acquisition. If the clock travelled with the units, a holder could
// wash a fresh position through a seasoned one and exit immediately.
func TestLockupClockRestartsOnTransfer(t *testing.T) {
	st, svc := fixture(t)
	if _, err := svc.IssueDR("S1", "A", 40_000, 40_000_000_00); err != nil {
		t.Fatal(err)
	}
	st.Advance(120 * 24 * time.Hour)
	if _, err := svc.Transfer("S1", "A", "B", 20_000); err != nil {
		t.Fatal(err)
	}

	// B received the units just now, so they are inside B's own lockup.
	res, err := svc.Transfer("S1", "B", "A", 20_000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision.Allowed {
		t.Fatal("freshly received units cleared the lockup immediately")
	}
	if res.Decision.Code != compliance.CodeLockup {
		t.Errorf("refused with %s, want %s", res.Decision.Code, compliance.CodeLockup)
	}
}

// TestForceTransferBypassesPredicatesButNotTheRecord checks the privileged
// path: it moves units inside a lockup, and it refuses to run without a reason.
func TestForceTransferBypassesPredicatesButNotTheRecord(t *testing.T) {
	st, svc := fixture(t)
	if _, err := svc.IssueDR("S1", "A", 40_000, 40_000_000_00); err != nil {
		t.Fatal(err)
	}

	// Inside the lockup, an ordinary transfer is refused.
	res, err := svc.Transfer("S1", "A", "B", 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision.Allowed {
		t.Fatal("ordinary transfer cleared a lockup it should not have")
	}

	// A force transfer with no reason is refused outright.
	if err := svc.ForceTransfer("S1", "A", "B", 5_000, ""); err == nil {
		t.Error("force transfer ran without a recorded reason")
	}

	if err := svc.ForceTransfer("S1", "A", "B", 5_000, "court order OS 1/2026"); err != nil {
		t.Fatalf("force transfer: %v", err)
	}

	byHolder := map[string]dr.Units{}
	for _, p := range st.Positions("S1") {
		byHolder[p.InvestorID] = p.Units
	}
	if byHolder["B"] != 5_000 {
		t.Errorf("transferee holds %d units after the force transfer, want 5000", byHolder["B"])
	}

	var found bool
	for _, e := range st.Events {
		if e.Name == "ForceTransferExecuted" {
			found = true
		}
	}
	if !found {
		t.Error("force transfer emitted no event; the supervisor's node would never see it")
	}
}

// TestRejectedTransferIsRecordedNotSwallowed checks that a refusal reaches the
// event log. A refusal that leaves no trace cannot be defended in an
// inspection and cannot be worked in the exception queue.
func TestRejectedTransferIsRecordedNotSwallowed(t *testing.T) {
	st, svc := fixture(t)
	if _, err := svc.IssueDR("S1", "A", 40_000, 40_000_000_00); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transfer("S1", "A", "B", 5_000); err != nil {
		t.Fatal(err)
	}
	for _, e := range st.Events {
		if e.Name == "TransferRejected" {
			return
		}
	}
	t.Error("a refused transfer emitted no TransferRejected event")
}

// TestValuationRequiresReportHash checks that a NAV cannot be published
// without committing to the valuer's report.
func TestValuationRequiresReportHash(t *testing.T) {
	_, svc := fixture(t)
	if err := svc.RecordValuation("S1", 10_400_00, start, ""); err == nil {
		t.Error("NAV published with no report hash; a holder could not verify the valuation")
	}
	if err := svc.RecordValuation("S1", 10_400_00, start, "sha256:abc"); err != nil {
		t.Errorf("valid valuation refused: %v", err)
	}
}

// TestMarkdownIsFlagged checks that a fall in NAV is marked on the event, since
// it carries a disclosure obligation and should not have to be inferred.
func TestMarkdownIsFlagged(t *testing.T) {
	st, svc := fixture(t)
	if err := svc.RecordValuation("S1", 10_400_00, start, "sha256:up"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordValuation("S1", 9_850_00, start, "sha256:down"); err != nil {
		t.Fatal(err)
	}
	last := st.Events[len(st.Events)-1]
	m, ok := last.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected valuation payload %T", last.Payload)
	}
	if m["markdown"] != true {
		t.Error("a downward revaluation was not flagged as a markdown")
	}
}
