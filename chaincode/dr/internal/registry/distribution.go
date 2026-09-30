package registry

import (
	"fmt"
	"time"

	"bharat-rwa/chaincode/dr/internal/dr"
	"bharat-rwa/chaincode/dr/internal/waterfall"
)

// DistributionRequest is one income cycle as the manager submits it.
type DistributionRequest struct {
	SchemeID   string    `json:"schemeId"`
	PeriodFrom time.Time `json:"periodFrom"`
	PeriodTo   time.Time `json:"periodTo"`
	RecordDate time.Time `json:"recordDate"`

	GrossRent        dr.Paise `json:"grossRent"`
	OperatingExpense dr.Paise `json:"operatingExpense"`
	PropertyMgmt     dr.Paise `json:"propertyMgmt"`
	DebtService      dr.Paise `json:"debtService"`
	Reserves         dr.Paise `json:"reserves"`
	CustodyFee       dr.Paise `json:"custodyFee"`
	AssetMgmtFee     dr.Paise `json:"assetMgmtFee"`
}

// TDSRates is withholding by investor class, in basis points.
//
// Held as data rather than compiled in, because a rate change is a budget
// announcement and should never require redeploying chaincode.
var TDSRates = map[dr.InvestorClass]int{
	dr.ClassInstitutional: 0,
	dr.ClassHNI:           1000,
	dr.ClassRetail:        1000,
}

// RunDistribution computes and records one distribution.
//
// It does not release payment. The sequence in production is compute,
// reconcile, review, then release, and keeping the release outside this
// function is what makes the review step real rather than ceremonial.
func (svc Service) RunDistribution(req DistributionRequest) (*dr.Distribution, error) {
	sch, err := svc.S.GetScheme(req.SchemeID)
	if err != nil {
		return nil, err
	}
	if sch.Status != dr.StatusIssued {
		return nil, fmt.Errorf("scheme %s is %s; distributions require an ISSUED scheme", sch.ID, sch.Status)
	}

	lots, err := svc.S.AllLots(req.SchemeID)
	if err != nil {
		return nil, err
	}
	if len(lots) == 0 {
		return nil, fmt.Errorf("scheme %s has no holders to distribute to", sch.ID)
	}

	// Invested capital is the sum of cost basis across the register, which is
	// the base the preferred return accrues on.
	var invested dr.Paise
	classOf := map[string]dr.InvestorClass{}
	for _, l := range lots {
		invested += l.CostBasis
		if _, seen := classOf[l.InvestorID]; !seen {
			inv, err := svc.S.GetInvestor(l.InvestorID)
			if err != nil {
				return nil, err
			}
			classOf[l.InvestorID] = inv.Class
		}
	}

	d, err := waterfall.Compute(waterfall.Input{
		SchemeID:           req.SchemeID,
		PeriodFrom:         req.PeriodFrom,
		PeriodTo:           req.PeriodTo,
		RecordDate:         req.RecordDate,
		GrossRent:          req.GrossRent,
		OperatingExpense:   req.OperatingExpense,
		PropertyMgmt:       req.PropertyMgmt,
		DebtService:        req.DebtService,
		Reserves:           req.Reserves,
		CustodyFee:         req.CustodyFee,
		AssetMgmtFee:       req.AssetMgmtFee,
		PreferredReturnBps: sch.PreferredReturnBps,
		PromoteBps:         sch.PromoteBps,
		InvestedCapital:    invested,
		Lots:               lots,
		TDSRateBps:         TDSRates,
		ClassOf:            classOf,
	})
	if err != nil {
		return nil, err
	}

	d.ID = fmt.Sprintf("%s-DIST-%s", req.SchemeID, svc.S.TxID())
	if err := svc.S.PutDistribution(d); err != nil {
		return nil, err
	}
	_ = svc.S.Emit("DistributionRecorded", map[string]any{
		"distributionId": d.ID, "schemeId": d.SchemeID,
		"distributable": d.Distributable, "toHolders": d.ToHolders,
		"promote": d.Promote, "totalTds": d.TotalTDS,
		"holders": len(d.Entitlements),
	})
	return &d, nil
}
