// Package dr holds the depositary receipt domain model.
//
// Nothing in this package imports Hyperledger Fabric or Drunix. The ledger is
// an adapter around this logic, not the other way round. That keeps the
// business rules testable without a network and portable if the receipt
// lifecycle ever has to run on another market infrastructure.
package dr

import "time"

// Paise is the money unit for the whole system. Every amount is an integer
// number of paise. There are no floats anywhere in the money path: a float
// rupee amount cannot represent 0.01 exactly, and a distribution that does not
// reconcile to the paisa is a distribution that gets disputed.
type Paise int64

// Units are whole depositary receipts. Fractional receipts are not issued;
// fractionality comes from the unit price, not from splitting a unit.
type Units int64

// InvestorClass drives the suitability predicate. The SM REIT regime and the
// IFSCA regimes admit different classes, so the scheme declares which classes
// it will accept and the compliance layer enforces it at transfer time.
type InvestorClass string

const (
	ClassInstitutional InvestorClass = "INSTITUTIONAL"
	ClassHNI           InvestorClass = "HNI"
	ClassRetail        InvestorClass = "RETAIL"
)

// SchemeStatus is the lifecycle state of one tokenised asset.
type SchemeStatus string

const (
	StatusDraft       SchemeStatus = "DRAFT"       // terms configured, not open
	StatusSubscribing SchemeStatus = "SUBSCRIBING" // accepting commitments into escrow
	StatusIssued      SchemeStatus = "ISSUED"      // receipts minted, live
	StatusWoundDown   SchemeStatus = "WOUND_DOWN"  // final distribution paid, receipts burnt
)

// Scheme is one asset wrapped in one depositary receipt programme.
//
// The investor holds a receipt issued by the depositary over units held in
// custody. The receipt is evidence of that claim; it is not the claim itself.
// If this ledger vanished tomorrow the holder's position would still be
// enforceable against the register and the custody arrangement behind it.
type Scheme struct {
	ID        string `json:"id"`
	AssetName string `json:"assetName"`
	City      string `json:"city"`

	TotalUnits     Units `json:"totalUnits"`
	UnitPrice      Paise `json:"unitPrice"`
	MinTicketUnits Units `json:"minTicketUnits"`

	// Compliance parameters. These are scheme terms, not code constants,
	// because the regime they answer to changes and chaincode should not be
	// redeployed to track a circular.
	LockupDays             int             `json:"lockupDays"`
	HolderCap              int             `json:"holderCap"`
	ConcentrationCapBps    int             `json:"concentrationCapBps"`
	EligibleClasses        []InvestorClass `json:"eligibleClasses"`
	PermittedJurisdictions []string        `json:"permittedJurisdictions"`

	// Waterfall parameters, in basis points.
	PreferredReturnBps int `json:"preferredReturnBps"`
	CatchUpBps         int `json:"catchUpBps"`
	PromoteBps         int `json:"promoteBps"`

	Status SchemeStatus `json:"status"`

	// NAV is republished on each independent valuation. It can go down; the
	// markdown policy is a disclosure obligation, not an edge case.
	NAVPerUnit    Paise     `json:"navPerUnit"`
	NAVAsOf       time.Time `json:"navAsOf"`
	ValuationHash string    `json:"valuationHash"` // SHA-256 of the valuer's report
}

// Investor is the compliance view of a holder. Note what is absent: no name,
// no address, no PAN, no date of birth. Personal data lives in an off-chain
// vault under the depositary's existing controls; the ledger holds a salted
// hash pointer and the attributes the transfer predicates actually need.
//
// This is not squeamishness. An immutable ledger and a statutory right to
// erasure are structurally incompatible, and the only architecture that
// satisfies both is one where erasing the vault record and its salt renders
// the on-chain commitment permanently unresolvable.
type Investor struct {
	ID string `json:"id"`

	// KYCHash commits to the vault record. Destroying the vault record and
	// the salt makes this value meaningless, which is how erasure is honoured
	// without rewriting history.
	KYCHash string `json:"kycHash"`

	Class        InvestorClass `json:"class"`
	Jurisdiction string        `json:"jurisdiction"`

	// EligibilityExpiry is the reason this platform does not fail in year two.
	// An investor verified at onboarding is not verified forever, and an
	// expired attestation must block transfers without blocking the
	// distributions they are already entitled to.
	EligibilityExpiry time.Time `json:"eligibilityExpiry"`

	SanctionsClear bool   `json:"sanctionsClear"`
	Frozen         bool   `json:"frozen"`
	FreezeReason   string `json:"freezeReason,omitempty"`
}

// Lot is one acquisition of receipts by one holder.
//
// Holdings are tracked lot by lot rather than as a single balance because two
// obligations require it: the lockup predicate needs the acquisition date of
// the specific receipts being moved, and capital gains reporting needs
// lot-level cost basis. A running balance loses both.
type Lot struct {
	LotID      string    `json:"lotId"`
	SchemeID   string    `json:"schemeId"`
	InvestorID string    `json:"investorId"`
	Units      Units     `json:"units"`
	CostBasis  Paise     `json:"costBasis"` // total paid for this lot
	AcquiredAt time.Time `json:"acquiredAt"`
}

// HeldFor reports how long this lot has been held as at t.
func (l Lot) HeldFor(t time.Time) time.Duration {
	return t.Sub(l.AcquiredAt)
}

// Position is the aggregate of one holder's lots in one scheme.
type Position struct {
	InvestorID string `json:"investorId"`
	SchemeID   string `json:"schemeId"`
	Units      Units  `json:"units"`
	Lots       []Lot  `json:"lots"`
}

// Entitlement is one holder's share of one distribution, with the arithmetic
// that produced it. The workings are kept because "why is my payment this
// number" is a question that gets asked, and an answer of "the system computed
// it" is not an answer.
type Entitlement struct {
	InvestorID   string `json:"investorId"`
	WeightedDays int64  `json:"weightedDays"` // units x days held in the period
	Gross        Paise  `json:"gross"`
	TDS          Paise  `json:"tds"`
	Net          Paise  `json:"net"`
}

// Distribution is one income cycle: what came in, what was taken out, and
// where the remainder went.
type Distribution struct {
	ID         string    `json:"id"`
	SchemeID   string    `json:"schemeId"`
	PeriodFrom time.Time `json:"periodFrom"`
	PeriodTo   time.Time `json:"periodTo"`
	RecordDate time.Time `json:"recordDate"`

	GrossRent        Paise `json:"grossRent"`
	OperatingExpense Paise `json:"operatingExpense"`
	PropertyMgmt     Paise `json:"propertyMgmt"`
	DebtService      Paise `json:"debtService"`
	Reserves         Paise `json:"reserves"`
	CustodyFee       Paise `json:"custodyFee"`
	AssetMgmtFee     Paise `json:"assetMgmtFee"`

	Distributable Paise `json:"distributable"`
	ToHolders     Paise `json:"toHolders"`
	Promote       Paise `json:"promote"`

	Entitlements []Entitlement `json:"entitlements"`
	TotalTDS     Paise         `json:"totalTds"`
	Residual     Paise         `json:"residual"`
}
