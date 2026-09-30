// Package waterfall computes one distribution cycle.
//
// This is the part of a tokenisation platform that is routinely written as one
// line in a specification and then takes six weeks. It is also the part that
// cannot be got wrong even once: an investor forgives a slow portal and never
// forgives a payment that is short by eleven rupees with no explanation.
//
// Three properties are load-bearing:
//
//   - Integer arithmetic throughout. Money is paise, never a float.
//   - Determinism. Every endorsing peer must compute byte-identical results
//     from the same inputs, so nothing here iterates a map and every tie is
//     broken on a stable key.
//   - Conservation. What arrives equals what leaves. The result carries its
//     own proof and Reconcile refuses to return a distribution that does not
//     balance.
package waterfall

import (
	"fmt"
	"math/big"
	"sort"
	"time"

	"bharat-rwa/chaincode/dr/internal/dr"
)

// Input is one income cycle's raw figures plus the terms that govern the split.
type Input struct {
	SchemeID   string
	PeriodFrom time.Time
	PeriodTo   time.Time
	RecordDate time.Time

	// Property level, deducted before the scheme sees anything.
	GrossRent        dr.Paise
	OperatingExpense dr.Paise
	PropertyMgmt     dr.Paise
	DebtService      dr.Paise
	Reserves         dr.Paise

	// Scheme level.
	CustodyFee   dr.Paise
	AssetMgmtFee dr.Paise

	// Split terms.
	PreferredReturnBps int
	PromoteBps         int

	// InvestedCapital is the base the preferred return accrues on.
	InvestedCapital dr.Paise

	// Lots are every lot in the scheme. Entitlement is computed from these,
	// frozen as at RecordDate.
	Lots []dr.Lot

	// TDSRateBps is withholding by investor class.
	TDSRateBps map[dr.InvestorClass]int
	// ClassOf resolves an investor to a class for withholding. Passed in
	// rather than looked up so this package stays free of state access.
	ClassOf map[string]dr.InvestorClass
}

// Compute runs the waterfall and allocates the holder share.
func Compute(in Input) (dr.Distribution, error) {
	if !in.PeriodTo.After(in.PeriodFrom) {
		return dr.Distribution{}, fmt.Errorf("period end %s is not after period start %s",
			in.PeriodTo.Format("2006-01-02"), in.PeriodFrom.Format("2006-01-02"))
	}
	if in.RecordDate.Before(in.PeriodFrom) {
		return dr.Distribution{}, fmt.Errorf("record date precedes the period it distributes")
	}

	d := dr.Distribution{
		SchemeID:         in.SchemeID,
		PeriodFrom:       in.PeriodFrom,
		PeriodTo:         in.PeriodTo,
		RecordDate:       in.RecordDate,
		GrossRent:        in.GrossRent,
		OperatingExpense: in.OperatingExpense,
		PropertyMgmt:     in.PropertyMgmt,
		DebtService:      in.DebtService,
		Reserves:         in.Reserves,
		CustodyFee:       in.CustodyFee,
		AssetMgmtFee:     in.AssetMgmtFee,
	}

	// Band 1: property level.
	noi := in.GrossRent - in.OperatingExpense - in.PropertyMgmt - in.DebtService - in.Reserves
	// Band 2: scheme level.
	distributable := noi - in.CustodyFee - in.AssetMgmtFee
	if distributable < 0 {
		// A loss-making period distributes nothing. It does not distribute a
		// negative amount and it does not silently clamp a shortfall: the
		// shortfall is the finding.
		distributable = 0
	}
	d.Distributable = distributable

	// Band 3: the split, strictly in order.
	toHolders, promote := split(distributable, in)
	d.ToHolders = toHolders
	d.Promote = promote

	// Allocate the holder share across positions.
	ents, residual, err := allocate(toHolders, in)
	if err != nil {
		return dr.Distribution{}, err
	}
	d.Entitlements = ents
	d.Residual = residual
	for _, e := range ents {
		d.TotalTDS += e.TDS
	}

	if err := Reconcile(d); err != nil {
		return dr.Distribution{}, err
	}
	return d, nil
}

// split applies the three-tier waterfall: preferred return to holders, then a
// full catch-up to the manager, then the promote split on everything above.
//
// The catch-up tier is what makes the promote honest. Without it a manager on
// a 20% promote receives 20% only of the excess above the hurdle, which is not
// what a 20% promote means; the catch-up brings the manager up to 20% of the
// whole profit before the residual is shared.
func split(distributable dr.Paise, in Input) (toHolders, promote dr.Paise) {
	remaining := distributable

	// Tier 1: preferred return, accrued for the actual length of the period.
	days := int64(in.PeriodTo.Sub(in.PeriodFrom).Hours() / 24)
	hurdle := dr.Paise(mulDiv(int64(in.InvestedCapital), int64(in.PreferredReturnBps)*days, 10000*365))
	pref := min64(remaining, hurdle)
	toHolders += pref
	remaining -= pref
	if remaining == 0 {
		return toHolders, 0
	}

	// Tier 2: catch-up. Solve c = p(pref + c) for c, where p is the promote
	// rate, giving c = p*pref / (1 - p).
	p := int64(in.PromoteBps)
	if p > 0 && p < 10000 {
		target := dr.Paise(mulDiv(int64(pref), p, 10000-p))
		catchUp := min64(remaining, target)
		promote += catchUp
		remaining -= catchUp
	}
	if remaining == 0 {
		return toHolders, promote
	}

	// Tier 3: residual split.
	share := dr.Paise(mulDiv(int64(remaining), p, 10000))
	promote += share
	toHolders += remaining - share
	return toHolders, promote
}

// allocate divides the holder share by weighted holding days.
//
// An investor who bought halfway through the quarter receives half the
// quarter. That is computed lot by lot from the register rather than from a
// closing balance, because a closing balance cannot tell you when the units
// arrived.
//
// The residual paise that integer division leaves over is assigned by the
// largest-remainder method, with ties broken on investor ID so that every peer
// reaches the same answer. Rounding must not create or destroy money.
func allocate(pool dr.Paise, in Input) ([]dr.Entitlement, dr.Paise, error) {
	periodDays := int64(in.PeriodTo.Sub(in.PeriodFrom).Hours() / 24)
	if periodDays <= 0 {
		return nil, 0, fmt.Errorf("period is zero days long")
	}

	// Weighted days per investor, built from lots held as at the record date.
	weights := map[string]int64{}
	for _, l := range in.Lots {
		if l.AcquiredAt.After(in.RecordDate) {
			// Acquired after the record date: holdings are frozen at the
			// record date, so this lot has no entitlement in this cycle.
			continue
		}
		start := l.AcquiredAt
		if start.Before(in.PeriodFrom) {
			start = in.PeriodFrom
		}
		held := int64(in.PeriodTo.Sub(start).Hours() / 24)
		if held <= 0 {
			continue
		}
		if held > periodDays {
			held = periodDays
		}
		weights[l.InvestorID] += int64(l.Units) * held
	}

	// Sort the investor IDs. Map iteration order in Go is randomised, and a
	// chaincode that iterates a map produces a different read-write set on
	// each endorsing peer, which shows up as an endorsement mismatch that is
	// painful to diagnose. Sorting is not a style preference here.
	ids := make([]string, 0, len(weights))
	var total int64
	for id, w := range weights {
		ids = append(ids, id)
		total += w
	}
	sort.Strings(ids)

	if total == 0 || pool == 0 {
		return nil, pool, nil
	}

	type part struct {
		id        string
		weighted  int64
		base      int64
		remainder *big.Int
	}
	parts := make([]part, 0, len(ids))
	var assigned int64

	for _, id := range ids {
		// big.Int because pool x weight overflows int64 for a large scheme:
		// a ten-crore-rupee distribution against a million unit-days is
		// already past 2^63.
		num := new(big.Int).Mul(big.NewInt(int64(pool)), big.NewInt(weights[id]))
		q, r := new(big.Int).QuoRem(num, big.NewInt(total), new(big.Int))
		parts = append(parts, part{id: id, weighted: weights[id], base: q.Int64(), remainder: r})
		assigned += q.Int64()
	}

	// Largest remainder: hand the leftover paise out one at a time, biggest
	// fractional claim first, ties on investor ID.
	leftover := int64(pool) - assigned
	order := make([]int, len(parts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa, pb := parts[order[a]], parts[order[b]]
		if c := pb.remainder.Cmp(pa.remainder); c != 0 {
			return c < 0
		}
		return pa.id < pb.id
	})
	for i := int64(0); i < leftover; i++ {
		parts[order[i%int64(len(order))]].base++
	}

	ents := make([]dr.Entitlement, 0, len(parts))
	for _, p := range parts {
		gross := dr.Paise(p.base)
		rate := in.TDSRateBps[in.ClassOf[p.id]]
		tds := dr.Paise(mulDiv(int64(gross), int64(rate), 10000))
		ents = append(ents, dr.Entitlement{
			InvestorID:   p.id,
			WeightedDays: p.weighted,
			Gross:        gross,
			TDS:          tds,
			Net:          gross - tds,
		})
	}
	return ents, 0, nil
}

// Reconcile proves the distribution balances. It runs before any payment is
// released, and a failure is an incident rather than a warning.
//
// The identity: everything that came in was either spent, retained, paid to
// the manager, paid to holders, or withheld for tax. Nothing evaporates.
func Reconcile(d dr.Distribution) error {
	deductions := d.OperatingExpense + d.PropertyMgmt + d.DebtService + d.Reserves + d.CustodyFee + d.AssetMgmtFee
	expected := d.GrossRent - deductions
	if expected < 0 {
		expected = 0
	}
	if d.Distributable != expected {
		return fmt.Errorf("reconciliation failed: distributable %d does not equal gross %d less deductions %d",
			d.Distributable, d.GrossRent, deductions)
	}
	if d.ToHolders+d.Promote != d.Distributable {
		return fmt.Errorf("reconciliation failed: holders %d plus promote %d does not equal distributable %d",
			d.ToHolders, d.Promote, d.Distributable)
	}
	var allocated dr.Paise
	for _, e := range d.Entitlements {
		if e.Gross != e.TDS+e.Net {
			return fmt.Errorf("reconciliation failed: entitlement for %s does not split cleanly", e.InvestorID)
		}
		allocated += e.Gross
	}
	if allocated+d.Residual != d.ToHolders {
		return fmt.Errorf("reconciliation failed: allocated %d plus residual %d does not equal holder share %d",
			allocated, d.Residual, d.ToHolders)
	}
	return nil
}

// mulDiv computes a*b/c without overflowing int64 on the intermediate product.
func mulDiv(a, b, c int64) int64 {
	if c == 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	return new(big.Int).Quo(n, big.NewInt(c)).Int64()
}

func min64(a, b dr.Paise) dr.Paise {
	if a < b {
		return a
	}
	return b
}
