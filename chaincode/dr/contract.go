package main

import (
	"fmt"
	"time"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"

	"bharat-rwa/chaincode/dr/internal/dr"
	"bharat-rwa/chaincode/dr/internal/registry"
)

// DRContract is the transaction surface of the depositary receipt programme.
//
// Every method here is a thin wrapper: unmarshal, call the domain, flush the
// event. There is no business rule in this file. That is the point of the
// layering, and it is what lets the same rules be unit tested without a peer
// and carried to another infrastructure if the receipt programme ever needs a
// second one.
//
// Which organisations must endorse which of these functions is not expressed
// in this code at all. It lives in the endorsement policy, applied at deploy
// time, documented in network/endorsement-policies.md. That separation is
// deliberate: a compliance rule enforced by an endorsement policy cannot be
// bypassed by a bug in the chaincode, because a transaction that does not
// collect the required signatures never reaches the ordering service.
type DRContract struct {
	contractapi.Contract
}

func svc(ctx contractapi.TransactionContextInterface) (registry.Service, *ledgerStore) {
	ls := &ledgerStore{ctx: ctx}
	return registry.Service{S: ls}, ls
}

// CreateScheme registers a new depositary receipt programme over one asset.
//
// Endorsement: depositary AND trustee AND registrar.
func (c *DRContract) CreateScheme(ctx contractapi.TransactionContextInterface, schemeJSON string) error {
	s, ls := svc(ctx)
	var sch dr.Scheme
	if err := unmarshal(schemeJSON, &sch); err != nil {
		return err
	}
	if sch.ID == "" {
		return fmt.Errorf("scheme id is required")
	}
	if sch.Status == "" {
		sch.Status = dr.StatusDraft
	}
	if err := s.S.PutScheme(&sch); err != nil {
		return err
	}
	if err := s.S.Emit("SchemeCreated", map[string]any{"schemeId": sch.ID, "asset": sch.AssetName}); err != nil {
		return err
	}
	return ls.flush()
}

// SetSchemeStatus advances the scheme lifecycle.
//
// Endorsement: depositary AND trustee.
func (c *DRContract) SetSchemeStatus(ctx contractapi.TransactionContextInterface, schemeID, status string) error {
	s, ls := svc(ctx)
	sch, err := s.S.GetScheme(schemeID)
	if err != nil {
		return err
	}
	sch.Status = dr.SchemeStatus(status)
	if err := s.S.PutScheme(sch); err != nil {
		return err
	}
	if err := s.S.Emit("SchemeStatusChanged", map[string]any{"schemeId": schemeID, "status": status}); err != nil {
		return err
	}
	return ls.flush()
}

// RegisterInvestor writes the compliance record for a holder.
//
// The payload carries a KYC hash, never personal data. A caller that sends a
// name or a PAN in this field has misunderstood the architecture, and the
// off-chain vault is where that data belongs.
//
// Endorsement: depositary AND registrar.
func (c *DRContract) RegisterInvestor(ctx contractapi.TransactionContextInterface, investorJSON string) error {
	s, ls := svc(ctx)
	var inv dr.Investor
	if err := unmarshal(investorJSON, &inv); err != nil {
		return err
	}
	if inv.ID == "" || inv.KYCHash == "" {
		return fmt.Errorf("investor id and kyc hash are both required")
	}
	if err := s.S.PutInvestor(&inv); err != nil {
		return err
	}
	if err := s.S.Emit("InvestorRegistered", map[string]any{"investorId": inv.ID, "class": inv.Class}); err != nil {
		return err
	}
	return ls.flush()
}

// IssueDR mints receipts against confirmed custody of the underlying units.
//
// Endorsement: depositary AND trustee AND registrar. The trustee's signature
// is what ties the minted receipt to units it has accepted into custody.
func (c *DRContract) IssueDR(ctx contractapi.TransactionContextInterface, schemeID, investorID string, units int64, pricePaidPaise int64) (*dr.Lot, error) {
	s, ls := svc(ctx)
	lot, err := s.IssueDR(schemeID, investorID, dr.Units(units), dr.Paise(pricePaidPaise))
	if err != nil {
		return nil, err
	}
	if err := ls.flush(); err != nil {
		return nil, err
	}
	return lot, nil
}

// TransferDR moves receipts between holders, subject to every compliance
// predicate.
//
// Endorsement: depositary AND registrar AND compliance.
//
// The third signature is the one that matters. The compliance organisation
// runs this same chaincode during endorsement; if its evaluation refuses the
// transfer it does not sign, the endorsement policy is not satisfied, and the
// transaction is never ordered. Nothing has to be reverted because nothing was
// ever committed.
//
// Note the return contract: a refused transfer is not an error. It is a
// recorded decision with a code and a reason, because the exception queue is
// worked from those and "transaction failed" is not something a compliance
// officer can act on.
func (c *DRContract) TransferDR(ctx contractapi.TransactionContextInterface, schemeID, fromID, toID string, units int64) (*registry.TransferResult, error) {
	s, ls := svc(ctx)
	res, err := s.Transfer(schemeID, fromID, toID, dr.Units(units))
	if err != nil {
		return nil, err
	}
	if err := ls.flush(); err != nil {
		return nil, err
	}
	return res, nil
}

// ForceTransfer moves receipts without the compliance predicates, for a
// recorded reason.
//
// Endorsement: depositary AND trustee AND registrar. Three organisations, none
// of which can act alone, and an event that reaches the supervisor's observer
// node as it happens.
//
// A regulated custodian must be able to give effect to a court order, settle a
// deceased holder's estate, and restore a holding whose key was lost. A ledger
// that cannot do these is not safer; it simply pushes the problem onto the
// holder, who has the least ability to solve it.
func (c *DRContract) ForceTransfer(ctx contractapi.TransactionContextInterface, schemeID, fromID, toID string, units int64, reason string) error {
	s, ls := svc(ctx)
	if err := s.ForceTransfer(schemeID, fromID, toID, dr.Units(units), reason); err != nil {
		return err
	}
	return ls.flush()
}

// SetFrozen freezes or releases a holding.
//
// Endorsement: depositary AND trustee.
func (c *DRContract) SetFrozen(ctx contractapi.TransactionContextInterface, investorID string, frozen bool, reason string) error {
	s, ls := svc(ctx)
	if err := s.SetFrozen(investorID, frozen, reason); err != nil {
		return err
	}
	return ls.flush()
}

// RenewEligibility extends a holder's attestation.
//
// Endorsement: depositary AND compliance.
func (c *DRContract) RenewEligibility(ctx contractapi.TransactionContextInterface, investorID, untilRFC3339 string) error {
	s, ls := svc(ctx)
	until, err := time.Parse(time.RFC3339, untilRFC3339)
	if err != nil {
		return fmt.Errorf("eligibility expiry must be RFC3339: %w", err)
	}
	if err := s.RenewEligibility(investorID, until); err != nil {
		return err
	}
	return ls.flush()
}

// RecordValuation publishes a NAV against the hash of the valuer's report.
//
// Endorsement: valuer AND depositary. The valuer signs because the valuation
// is theirs; the depositary signs because it is publishing it.
func (c *DRContract) RecordValuation(ctx contractapi.TransactionContextInterface, schemeID string, navPerUnitPaise int64, asOfRFC3339, reportHash string) error {
	s, ls := svc(ctx)
	asOf, err := time.Parse(time.RFC3339, asOfRFC3339)
	if err != nil {
		return fmt.Errorf("valuation date must be RFC3339: %w", err)
	}
	if err := s.RecordValuation(schemeID, dr.Paise(navPerUnitPaise), asOf, reportHash); err != nil {
		return err
	}
	return ls.flush()
}

// RunDistribution computes and records one income cycle.
//
// Endorsement: depositary AND registrar.
//
// It records; it does not pay. Payment release is a separate, dual-authorised
// step after the reconciliation has been reviewed, and collapsing the two is
// how a wrong distribution reaches a holder's bank account.
func (c *DRContract) RunDistribution(ctx contractapi.TransactionContextInterface, requestJSON string) (*dr.Distribution, error) {
	s, ls := svc(ctx)
	var req registry.DistributionRequest
	if err := unmarshal(requestJSON, &req); err != nil {
		return nil, err
	}
	d, err := s.RunDistribution(req)
	if err != nil {
		return nil, err
	}
	if err := ls.flush(); err != nil {
		return nil, err
	}
	return d, nil
}

// GetScheme reads one scheme. Query only, no endorsement required.
func (c *DRContract) GetScheme(ctx contractapi.TransactionContextInterface, schemeID string) (*dr.Scheme, error) {
	s, _ := svc(ctx)
	return s.S.GetScheme(schemeID)
}

// GetPosition reads one holder's position, from the private data collection.
// A caller whose organisation is not a member of the collection receives
// nothing, which is enforced by the collection policy rather than by this code.
func (c *DRContract) GetPosition(ctx contractapi.TransactionContextInterface, schemeID, investorID string) (*dr.Position, error) {
	s, _ := svc(ctx)
	lots, err := s.S.LotsFor(schemeID, investorID)
	if err != nil {
		return nil, err
	}
	p := &dr.Position{InvestorID: investorID, SchemeID: schemeID, Lots: lots}
	for _, l := range lots {
		p.Units += l.Units
	}
	return p, nil
}
