package registry

import (
	"fmt"
	"sort"
	"time"

	"bharat-rwa/chaincode/dr/internal/compliance"
	"bharat-rwa/chaincode/dr/internal/dr"
)

// Store is everything the receipt lifecycle needs from a ledger, expressed as
// an interface so the domain never imports one.
//
// Two implementations exist. The chaincode adapter backs it with the Drunix
// state database and private data collections. The demo harness backs it with
// maps in memory, which is how the full narrative runs and is verified without
// a network standing up first.
//
// The interface is deliberately narrow. If a method here started taking a
// Fabric type, the separation would be lost and the business rules would stop
// being portable.
type Store interface {
	GetScheme(id string) (*dr.Scheme, error)
	PutScheme(*dr.Scheme) error

	GetInvestor(id string) (*dr.Investor, error)
	PutInvestor(*dr.Investor) error

	// LotsFor returns one holder's lots in one scheme, oldest first.
	LotsFor(schemeID, investorID string) ([]dr.Lot, error)
	// AllLots returns every lot in a scheme, ordered deterministically.
	AllLots(schemeID string) ([]dr.Lot, error)
	PutLot(dr.Lot) error
	DeleteLot(schemeID, lotID string) error

	HolderCount(schemeID string) (int, error)
	PutDistribution(dr.Distribution) error

	// Emit publishes a ledger event. Every privileged action emits one so the
	// supervisor's observer node sees it as it happens rather than in a
	// quarterly return.
	Emit(name string, payload any) error

	TxID() string
	Now() time.Time
}

// Service is the receipt lifecycle.
type Service struct{ S Store }

// TransferResult carries the compliance decision alongside the outcome, so a
// refusal can be recorded with its reason rather than surfacing as a bare
// error string.
type TransferResult struct {
	Decision compliance.Decision `json:"decision"`
	TxID     string              `json:"txId"`
	Moved    dr.Units            `json:"moved"`
}

// IssueDR mints receipts to a holder against confirmed custody of the
// underlying units.
//
// The custody confirmation is a precondition, not a parameter this function
// verifies: in the network the endorsement policy requires the depositary,
// the trustee and the registrar to all sign, so an issuance that custody has
// not confirmed cannot collect the endorsements it needs. The policy is the
// control; this code is the bookkeeping.
func (svc Service) IssueDR(schemeID, investorID string, units dr.Units, pricePaid dr.Paise) (*dr.Lot, error) {
	sch, err := svc.S.GetScheme(schemeID)
	if err != nil {
		return nil, err
	}
	if sch.Status != dr.StatusSubscribing && sch.Status != dr.StatusIssued {
		return nil, fmt.Errorf("scheme %s is %s; receipts can only be issued while SUBSCRIBING or ISSUED", schemeID, sch.Status)
	}
	if _, err := svc.S.GetInvestor(investorID); err != nil {
		return nil, err
	}

	issued, err := svc.issuedUnits(schemeID)
	if err != nil {
		return nil, err
	}
	if issued+units > sch.TotalUnits {
		return nil, fmt.Errorf("issuing %d units would take the scheme to %d, above its total supply of %d",
			units, issued+units, sch.TotalUnits)
	}

	lot := dr.Lot{
		LotID:      fmt.Sprintf("%s-%s", schemeID, svc.S.TxID()),
		SchemeID:   schemeID,
		InvestorID: investorID,
		Units:      units,
		CostBasis:  pricePaid,
		AcquiredAt: svc.S.Now(),
	}
	if err := svc.S.PutLot(lot); err != nil {
		return nil, err
	}
	_ = svc.S.Emit("DRIssued", lot)
	return &lot, nil
}

// Transfer moves receipts between holders if every compliance predicate passes.
//
// In the deployed network this function runs during endorsement, on a Lite
// Peer, before the transaction has been ordered. A refusal here means the
// transaction never enters a block: there is nothing to revert, because there
// is nothing to revert from. That guarantee is the reason the compliance
// service is an endorsing organisation rather than a service the portal calls
// on its way in.
func (svc Service) Transfer(schemeID, fromID, toID string, units dr.Units) (*TransferResult, error) {
	sch, err := svc.S.GetScheme(schemeID)
	if err != nil {
		return nil, err
	}
	from, err := svc.S.GetInvestor(fromID)
	if err != nil {
		return nil, err
	}
	to, err := svc.S.GetInvestor(toID)
	if err != nil {
		return nil, err
	}
	fromLots, err := svc.S.LotsFor(schemeID, fromID)
	if err != nil {
		return nil, err
	}
	toLots, err := svc.S.LotsFor(schemeID, toID)
	if err != nil {
		return nil, err
	}
	holders, err := svc.S.HolderCount(schemeID)
	if err != nil {
		return nil, err
	}

	var toUnits dr.Units
	for _, l := range toLots {
		toUnits += l.Units
	}

	req := compliance.Request{
		Scheme: compliance.Scheme{
			ID:                     sch.ID,
			Status:                 sch.Status,
			TotalUnits:             sch.TotalUnits,
			MinTicketUnits:         sch.MinTicketUnits,
			LockupDays:             sch.LockupDays,
			HolderCap:              sch.HolderCap,
			ConcentrationCapBps:    sch.ConcentrationCapBps,
			EligibleClasses:        sch.EligibleClasses,
			PermittedJurisdictions: sch.PermittedJurisdictions,
		},
		From:            party(from, len(fromLots) == 0),
		To:              party(to, len(toLots) == 0),
		Units:           units,
		FromLots:        fromLots,
		HolderCount:     holders,
		ToExistingUnits: toUnits,
		Now:             svc.S.Now(),
	}

	decision := compliance.Evaluate(req)
	res := &TransferResult{Decision: decision, TxID: svc.S.TxID()}
	if !decision.Allowed {
		// The rejection is emitted, not swallowed. A compliance refusal that
		// leaves no trace is a refusal that cannot be defended in an
		// inspection, and the exception queue is worked from these events.
		_ = svc.S.Emit("TransferRejected", map[string]any{
			"schemeId": schemeID, "from": fromID, "to": toID,
			"units": units, "code": decision.Code, "reason": decision.Reason,
		})
		return res, nil
	}

	if err := svc.moveUnits(schemeID, fromID, toID, units, sch.LockupDays, false); err != nil {
		return nil, err
	}
	res.Moved = units
	_ = svc.S.Emit("DRTransferred", map[string]any{
		"schemeId": schemeID, "from": fromID, "to": toID, "units": units,
	})
	return res, nil
}

// ForceTransfer moves receipts without the compliance predicates.
//
// This exists because the alternatives are worse. A court orders a transfer; a
// holder dies and the estate must be settled; a key is lost; a holding is
// found to be the proceeds of fraud. A system that cannot execute these is not
// more trustworthy than one that can, it is merely unusable by a regulated
// custodian, and the holder is the one who suffers.
//
// What makes it safe is not that it is rare but that it is visible: the
// endorsement policy requires the depositary, the trustee and the registrar to
// sign together, the reason is recorded, and the event reaches the
// supervisor's node in real time.
func (svc Service) ForceTransfer(schemeID, fromID, toID string, units dr.Units, reason string) error {
	if reason == "" {
		return fmt.Errorf("a force transfer requires a recorded reason")
	}
	if _, err := svc.S.GetInvestor(toID); err != nil {
		return err
	}
	sch, err := svc.S.GetScheme(schemeID)
	if err != nil {
		return err
	}
	if err := svc.moveUnits(schemeID, fromID, toID, units, sch.LockupDays, true); err != nil {
		return err
	}
	return svc.S.Emit("ForceTransferExecuted", map[string]any{
		"schemeId": schemeID, "from": fromID, "to": toID,
		"units": units, "reason": reason, "txId": svc.S.TxID(),
	})
}

// SetFrozen freezes or unfreezes a holder.
func (svc Service) SetFrozen(investorID string, frozen bool, reason string) error {
	inv, err := svc.S.GetInvestor(investorID)
	if err != nil {
		return err
	}
	if frozen && reason == "" {
		return fmt.Errorf("a freeze requires a recorded reason")
	}
	inv.Frozen = frozen
	inv.FreezeReason = reason
	if err := svc.S.PutInvestor(inv); err != nil {
		return err
	}
	return svc.S.Emit("HoldingFreezeChanged", map[string]any{
		"investorId": investorID, "frozen": frozen, "reason": reason,
	})
}

// RenewEligibility extends a holder's attestation. In production this is
// driven by the scheduled re-verification job, not by a person remembering.
func (svc Service) RenewEligibility(investorID string, until time.Time) error {
	inv, err := svc.S.GetInvestor(investorID)
	if err != nil {
		return err
	}
	inv.EligibilityExpiry = until
	if err := svc.S.PutInvestor(inv); err != nil {
		return err
	}
	return svc.S.Emit("EligibilityRenewed", map[string]any{
		"investorId": investorID, "until": until.Format("2006-01-02"),
	})
}

// RecordValuation publishes a NAV against a hash of the valuer's report.
//
// The hash is the point. It lets a holder verify that the report in the data
// room is the report the valuer signed, without the report itself ever
// touching the ledger.
func (svc Service) RecordValuation(schemeID string, navPerUnit dr.Paise, asOf time.Time, reportHash string) error {
	sch, err := svc.S.GetScheme(schemeID)
	if err != nil {
		return err
	}
	if reportHash == "" {
		return fmt.Errorf("a valuation must carry the hash of the valuer's report")
	}
	previous := sch.NAVPerUnit
	sch.NAVPerUnit, sch.NAVAsOf, sch.ValuationHash = navPerUnit, asOf, reportHash
	if err := svc.S.PutScheme(sch); err != nil {
		return err
	}
	return svc.S.Emit("ValuationRecorded", map[string]any{
		"schemeId": schemeID, "navPerUnit": navPerUnit, "previous": previous,
		"asOf": asOf.Format("2006-01-02"), "reportHash": reportHash,
		// Flagged explicitly: a markdown triggers a disclosure obligation and
		// should not have to be inferred by diffing two events.
		"markdown": navPerUnit < previous,
	})
}

// issuedUnits totals everything minted in a scheme.
func (svc Service) issuedUnits(schemeID string) (dr.Units, error) {
	lots, err := svc.S.AllLots(schemeID)
	if err != nil {
		return 0, err
	}
	var n dr.Units
	for _, l := range lots {
		n += l.Units
	}
	return n, nil
}

// moveUnits consumes the transferor's lots and creates a lot for the
// transferee.
//
// Lots are consumed oldest-seasoned-first. The identification method is
// documented rather than implicit because it determines both which units clear
// the lockup and what cost basis follows them into the recipient's hands, and
// an undocumented choice here becomes a dispute later.
func (svc Service) moveUnits(schemeID, fromID, toID string, units dr.Units, lockupDays int, ignoreLockup bool) error {
	lots, err := svc.S.LotsFor(schemeID, fromID)
	if err != nil {
		return err
	}
	sort.SliceStable(lots, func(i, j int) bool {
		if lots[i].AcquiredAt.Equal(lots[j].AcquiredAt) {
			return lots[i].LotID < lots[j].LotID
		}
		return lots[i].AcquiredAt.Before(lots[j].AcquiredAt)
	})

	lockup := time.Duration(lockupDays) * 24 * time.Hour
	now := svc.S.Now()
	remaining := units
	var basisMoved dr.Paise

	for i := range lots {
		if remaining == 0 {
			break
		}
		l := lots[i]
		if !ignoreLockup && l.HeldFor(now) < lockup {
			continue
		}
		take := l.Units
		if take > remaining {
			take = remaining
		}
		// Cost basis follows the units proportionally.
		basis := dr.Paise(int64(l.CostBasis) * int64(take) / int64(l.Units))
		basisMoved += basis

		if take == l.Units {
			if err := svc.S.DeleteLot(schemeID, l.LotID); err != nil {
				return err
			}
		} else {
			l.Units -= take
			l.CostBasis -= basis
			if err := svc.S.PutLot(l); err != nil {
				return err
			}
		}
		remaining -= take
	}
	if remaining > 0 {
		return fmt.Errorf("could not source %d units from the transferor's lots", remaining)
	}

	// The recipient's lot is dated now. The acquisition date is when they
	// acquired it, which restarts their own lockup clock: a lockup that
	// travelled with the units would let a holder wash a fresh position
	// through a seasoned one.
	return svc.S.PutLot(dr.Lot{
		LotID:      fmt.Sprintf("%s-%s-in", schemeID, svc.S.TxID()),
		SchemeID:   schemeID,
		InvestorID: toID,
		Units:      units,
		CostBasis:  basisMoved,
		AcquiredAt: now,
	})
}

func party(i *dr.Investor, isNew bool) compliance.Party {
	return compliance.Party{
		ID:                i.ID,
		Class:             i.Class,
		Jurisdiction:      i.Jurisdiction,
		EligibilityExpiry: i.EligibilityExpiry,
		SanctionsClear:    i.SanctionsClear,
		Frozen:            i.Frozen,
		IsNewHolder:       isNew,
	}
}
