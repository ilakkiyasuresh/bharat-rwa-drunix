// Package compliance evaluates the predicates that must all pass before a
// transfer is endorsed.
//
// The placement matters more than the logic. On a public chain a transfer hook
// rejects a bad transfer after it has been submitted, ordered and paid for. In
// a Drunix network the compliance service is an endorsing organisation: a
// transfer it will not sign never collects the endorsement policy, never
// reaches the ordering service, and never enters a block. The transaction is
// not reverted, because it never existed.
//
// This package imports nothing from Fabric. It is a pure function from state
// to a decision, which is what makes it testable and what makes the same rules
// reusable off-ledger in the portal for pre-trade checks.
package compliance

import (
	"fmt"
	"time"

	"bharat-rwa/chaincode/dr/internal/dr"
)

// Code identifies which predicate rejected a transfer. Rejections are recorded
// with their code so the exception queue can be worked and so a supervisor can
// ask "show me every lockup rejection this quarter" and get an answer.
type Code string

const (
	CodeEligibility   Code = "ELIGIBILITY_EXPIRED"
	CodeSanctions     Code = "SANCTIONS_HIT"
	CodeLockup        Code = "LOCKUP_NOT_SATISFIED"
	CodeHolderCap     Code = "HOLDER_CAP_EXCEEDED"
	CodeConcentration Code = "CONCENTRATION_CAP_EXCEEDED"
	CodeJurisdiction  Code = "JURISDICTION_NOT_PERMITTED"
	CodeSuitability   Code = "INVESTOR_CLASS_NOT_PERMITTED"
	CodeFrozen        Code = "HOLDING_FROZEN"
	CodeInsufficient  Code = "INSUFFICIENT_UNITS"
	CodeMinTicket     Code = "BELOW_MINIMUM_TICKET"
	CodeSchemeState   Code = "SCHEME_NOT_LIVE"
)

// Decision is the outcome of evaluating every predicate. Checked is retained
// even on success: an audit that can only see failures cannot prove that the
// passing transfers were checked at all.
type Decision struct {
	Allowed     bool      `json:"allowed"`
	Code        Code      `json:"code,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Checked     []string  `json:"checked"`
	EvaluatedAt time.Time `json:"evaluatedAt"`
}

// Request is everything the predicates need. It is assembled by the adapter
// from ledger state and passed in whole, so that evaluation itself performs no
// lookups and cannot behave differently depending on read order.
type Request struct {
	Scheme Scheme
	From   Party
	To     Party
	Units  dr.Units
	// FromLots are the transferor's lots, oldest first. The lockup predicate
	// walks them to decide whether enough seasoned units exist.
	FromLots []dr.Lot
	// HolderCount is the scheme's current distinct holder count.
	HolderCount int
	// ToExistingUnits is what the recipient already holds in this scheme,
	// needed for the concentration cap.
	ToExistingUnits dr.Units
	Now             time.Time
}

// Scheme is the subset of scheme terms the predicates read.
type Scheme struct {
	ID                     string
	Status                 dr.SchemeStatus
	TotalUnits             dr.Units
	MinTicketUnits         dr.Units
	LockupDays             int
	HolderCap              int
	ConcentrationCapBps    int
	EligibleClasses        []dr.InvestorClass
	PermittedJurisdictions []string
}

// Party is the subset of an investor the predicates read.
type Party struct {
	ID                string
	Class             dr.InvestorClass
	Jurisdiction      string
	EligibilityExpiry time.Time
	SanctionsClear    bool
	Frozen            bool
	// IsNewHolder is true when this party holds nothing in the scheme yet, so
	// the transfer would increase the holder count.
	IsNewHolder bool
}

// predicate is one rule. Every rule has the same shape so that the set can be
// extended without touching the evaluation loop, and so that each one can be
// tested in isolation.
type predicate struct {
	name string
	eval func(Request) (bool, Code, string)
}

// predicates run in this order deliberately: cheapest and most fundamental
// first, so that a frozen holding or a dead scheme is rejected before the
// expensive lot walk. Order does not change the outcome, only the work done.
func predicates() []predicate {
	return []predicate{
		{"scheme_live", schemeLive},
		{"holding_not_frozen", notFrozen},
		{"sanctions_clear", sanctionsClear},
		{"eligibility_current", eligibilityCurrent},
		{"jurisdiction_permitted", jurisdictionPermitted},
		{"investor_class_suitable", classSuitable},
		{"sufficient_units", sufficientUnits},
		{"minimum_ticket", minimumTicket},
		{"lockup_satisfied", lockupSatisfied},
		{"holder_cap", holderCap},
		{"concentration_cap", concentrationCap},
	}
}

// Evaluate runs every predicate and returns the first rejection, along with
// the list of predicates that were reached. It stops at the first failure
// because a transfer needs only one reason to be refused and the holder is
// better served by one clear reason than by a list.
func Evaluate(r Request) Decision {
	d := Decision{Allowed: true, EvaluatedAt: r.Now}
	for _, p := range predicates() {
		d.Checked = append(d.Checked, p.name)
		ok, code, reason := p.eval(r)
		if !ok {
			d.Allowed = false
			d.Code = code
			d.Reason = reason
			return d
		}
	}
	return d
}

func schemeLive(r Request) (bool, Code, string) {
	if r.Scheme.Status != dr.StatusIssued {
		return false, CodeSchemeState, fmt.Sprintf("scheme %s is %s, transfers are only permitted while ISSUED", r.Scheme.ID, r.Scheme.Status)
	}
	return true, "", ""
}

func notFrozen(r Request) (bool, Code, string) {
	if r.From.Frozen {
		return false, CodeFrozen, fmt.Sprintf("transferor %s is frozen", r.From.ID)
	}
	if r.To.Frozen {
		return false, CodeFrozen, fmt.Sprintf("transferee %s is frozen", r.To.ID)
	}
	return true, "", ""
}

func sanctionsClear(r Request) (bool, Code, string) {
	if !r.From.SanctionsClear {
		return false, CodeSanctions, fmt.Sprintf("transferor %s has an open sanctions hit", r.From.ID)
	}
	if !r.To.SanctionsClear {
		return false, CodeSanctions, fmt.Sprintf("transferee %s has an open sanctions hit", r.To.ID)
	}
	return true, "", ""
}

// eligibilityCurrent is the predicate most platforms forget. Onboarding
// verifies an investor once; the obligation is continuous. An expired
// attestation blocks the transfer and the re-verification job, not a human,
// is what clears it.
func eligibilityCurrent(r Request) (bool, Code, string) {
	if !r.From.EligibilityExpiry.After(r.Now) {
		return false, CodeEligibility, fmt.Sprintf("transferor %s eligibility expired %s", r.From.ID, r.From.EligibilityExpiry.Format("2006-01-02"))
	}
	if !r.To.EligibilityExpiry.After(r.Now) {
		return false, CodeEligibility, fmt.Sprintf("transferee %s eligibility expired %s", r.To.ID, r.To.EligibilityExpiry.Format("2006-01-02"))
	}
	return true, "", ""
}

func jurisdictionPermitted(r Request) (bool, Code, string) {
	permitted := func(j string) bool {
		if len(r.Scheme.PermittedJurisdictions) == 0 {
			return true
		}
		for _, p := range r.Scheme.PermittedJurisdictions {
			if p == j {
				return true
			}
		}
		return false
	}
	if !permitted(r.To.Jurisdiction) {
		return false, CodeJurisdiction, fmt.Sprintf("transferee jurisdiction %s is not permitted for scheme %s", r.To.Jurisdiction, r.Scheme.ID)
	}
	return true, "", ""
}

func classSuitable(r Request) (bool, Code, string) {
	for _, c := range r.Scheme.EligibleClasses {
		if c == r.To.Class {
			return true, "", ""
		}
	}
	return false, CodeSuitability, fmt.Sprintf("investor class %s is not admitted to scheme %s", r.To.Class, r.Scheme.ID)
}

func sufficientUnits(r Request) (bool, Code, string) {
	var held dr.Units
	for _, l := range r.FromLots {
		held += l.Units
	}
	if held < r.Units {
		return false, CodeInsufficient, fmt.Sprintf("transferor holds %d units, transfer is for %d", held, r.Units)
	}
	return true, "", ""
}

// minimumTicket guards both sides. A transfer that leaves the transferor with
// a stub below the minimum ticket is as much a breach as a purchase below it,
// and permitting the stub is how a holder register fills with unsellable
// fragments.
func minimumTicket(r Request) (bool, Code, string) {
	if r.Units < r.Scheme.MinTicketUnits {
		return false, CodeMinTicket, fmt.Sprintf("transfer of %d units is below the minimum ticket of %d", r.Units, r.Scheme.MinTicketUnits)
	}
	var held dr.Units
	for _, l := range r.FromLots {
		held += l.Units
	}
	if remainder := held - r.Units; remainder > 0 && remainder < r.Scheme.MinTicketUnits {
		return false, CodeMinTicket, fmt.Sprintf("transfer would leave the transferor with %d units, below the minimum ticket of %d", remainder, r.Scheme.MinTicketUnits)
	}
	return true, "", ""
}

// lockupSatisfied walks the transferor's lots oldest first and counts how many
// units have seasoned past the lockup. Transferring FIFO from seasoned lots is
// the documented identification method; it is stated here because the choice
// changes both this predicate and the capital gains computation, and an
// undocumented choice is a dispute waiting to happen.
func lockupSatisfied(r Request) (bool, Code, string) {
	lockup := time.Duration(r.Scheme.LockupDays) * 24 * time.Hour
	var seasoned dr.Units
	for _, l := range r.FromLots {
		if l.HeldFor(r.Now) >= lockup {
			seasoned += l.Units
		}
	}
	if seasoned < r.Units {
		return false, CodeLockup, fmt.Sprintf(
			"only %d of %d units requested have cleared the %d-day lockup",
			seasoned, r.Units, r.Scheme.LockupDays)
	}
	return true, "", ""
}

// holderCap enforces the statutory ceiling on distinct holders. It only bites
// when the transferee is new to the scheme, because moving units between
// existing holders cannot increase the count.
func holderCap(r Request) (bool, Code, string) {
	if r.Scheme.HolderCap <= 0 || !r.To.IsNewHolder {
		return true, "", ""
	}
	if r.HolderCount+1 > r.Scheme.HolderCap {
		return false, CodeHolderCap, fmt.Sprintf(
			"scheme %s is at its holder cap of %d; admitting a new holder would breach it",
			r.Scheme.ID, r.Scheme.HolderCap)
	}
	return true, "", ""
}

// concentrationCap limits any single holder's share of the scheme. Expressed
// in basis points of total units so that the arithmetic stays in integers.
func concentrationCap(r Request) (bool, Code, string) {
	if r.Scheme.ConcentrationCapBps <= 0 || r.Scheme.TotalUnits == 0 {
		return true, "", ""
	}
	post := r.ToExistingUnits + r.Units
	postBps := int(int64(post) * 10000 / int64(r.Scheme.TotalUnits))
	if postBps > r.Scheme.ConcentrationCapBps {
		return false, CodeConcentration, fmt.Sprintf(
			"transferee would hold %d units (%.2f%%), above the concentration cap of %.2f%%",
			post, float64(postBps)/100, float64(r.Scheme.ConcentrationCapBps)/100)
	}
	return true, "", ""
}
