// Package memstore is an in-memory Store.
//
// It exists so the receipt lifecycle can be exercised end to end without a
// network: the demo runs against it, the tests run against it, and a reviewer
// can see the rules behave before deciding whether to stand up peers.
//
// It is not a simulation of Drunix and it makes no claim to be. It implements
// the same Store interface the chaincode adapter implements, so the logic
// under test is the same logic that runs on the peer. What it does not
// reproduce is endorsement, ordering, MVCC read conflicts or private data
// dissemination, and no conclusion about those should be drawn from it.
package memstore

import (
	"fmt"
	"sort"
	"time"

	"bharat-rwa/chaincode/dr/internal/dr"
)

// Event is one emitted ledger event, retained so the demo can print the trail
// a supervisor's observer node would see.
type Event struct {
	Seq     int    `json:"seq"`
	Name    string `json:"name"`
	TxID    string `json:"txId"`
	Payload any    `json:"payload"`
}

// Store is an in-memory implementation of registry.Store.
type Store struct {
	schemes       map[string]*dr.Scheme
	investors     map[string]*dr.Investor
	lots          map[string]dr.Lot // keyed by scheme|lotID
	distributions []dr.Distribution
	Events        []Event

	clock time.Time
	txSeq int
}

// New returns a store whose clock starts at the given instant. The clock is
// injected rather than read from the host because chaincode must not call
// time.Now(): every endorsing peer would read a different value, the
// read-write sets would differ, and endorsement would fail. In the deployed
// adapter the timestamp comes from the transaction proposal, which every peer
// sees identically.
func New(start time.Time) *Store {
	return &Store{
		schemes:   map[string]*dr.Scheme{},
		investors: map[string]*dr.Investor{},
		lots:      map[string]dr.Lot{},
		clock:     start,
	}
}

// Advance moves the clock, which is how the demo shows a lockup expiring
// without waiting ninety days.
func (s *Store) Advance(d time.Duration) { s.clock = s.clock.Add(d) }

// Now returns the current ledger time.
func (s *Store) Now() time.Time { return s.clock }

// TxID returns a deterministic pseudo transaction id.
func (s *Store) TxID() string {
	s.txSeq++
	return fmt.Sprintf("tx%04d", s.txSeq)
}

func (s *Store) GetScheme(id string) (*dr.Scheme, error) {
	sc, ok := s.schemes[id]
	if !ok {
		return nil, fmt.Errorf("scheme %s not found", id)
	}
	cp := *sc
	return &cp, nil
}

func (s *Store) PutScheme(sc *dr.Scheme) error {
	cp := *sc
	s.schemes[sc.ID] = &cp
	return nil
}

func (s *Store) GetInvestor(id string) (*dr.Investor, error) {
	i, ok := s.investors[id]
	if !ok {
		return nil, fmt.Errorf("investor %s not found", id)
	}
	cp := *i
	return &cp, nil
}

func (s *Store) PutInvestor(i *dr.Investor) error {
	cp := *i
	s.investors[i.ID] = &cp
	return nil
}

func (s *Store) key(schemeID, lotID string) string { return schemeID + "|" + lotID }

func (s *Store) PutLot(l dr.Lot) error {
	s.lots[s.key(l.SchemeID, l.LotID)] = l
	return nil
}

func (s *Store) DeleteLot(schemeID, lotID string) error {
	delete(s.lots, s.key(schemeID, lotID))
	return nil
}

// LotsFor returns one holder's lots, oldest first.
func (s *Store) LotsFor(schemeID, investorID string) ([]dr.Lot, error) {
	var out []dr.Lot
	for _, l := range s.lots {
		if l.SchemeID == schemeID && l.InvestorID == investorID {
			out = append(out, l)
		}
	}
	sortLots(out)
	return out, nil
}

// AllLots returns every lot in a scheme in a stable order.
func (s *Store) AllLots(schemeID string) ([]dr.Lot, error) {
	var out []dr.Lot
	for _, l := range s.lots {
		if l.SchemeID == schemeID {
			out = append(out, l)
		}
	}
	sortLots(out)
	return out, nil
}

func (s *Store) HolderCount(schemeID string) (int, error) {
	seen := map[string]bool{}
	for _, l := range s.lots {
		if l.SchemeID == schemeID && l.Units > 0 {
			seen[l.InvestorID] = true
		}
	}
	return len(seen), nil
}

func (s *Store) PutDistribution(d dr.Distribution) error {
	s.distributions = append(s.distributions, d)
	return nil
}

func (s *Store) Emit(name string, payload any) error {
	s.Events = append(s.Events, Event{Seq: len(s.Events) + 1, Name: name, TxID: fmt.Sprintf("tx%04d", s.txSeq), Payload: payload})
	return nil
}

// Positions is a reporting helper for the demo.
func (s *Store) Positions(schemeID string) []dr.Position {
	by := map[string]*dr.Position{}
	for _, l := range s.lots {
		if l.SchemeID != schemeID {
			continue
		}
		p, ok := by[l.InvestorID]
		if !ok {
			p = &dr.Position{InvestorID: l.InvestorID, SchemeID: schemeID}
			by[l.InvestorID] = p
		}
		p.Units += l.Units
		p.Lots = append(p.Lots, l)
	}
	ids := make([]string, 0, len(by))
	for id := range by {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]dr.Position, 0, len(ids))
	for _, id := range ids {
		p := by[id]
		sortLots(p.Lots)
		out = append(out, *p)
	}
	return out
}

// sortLots orders lots oldest first, breaking ties on lot id so that the
// order is total and identical on every peer.
func sortLots(ls []dr.Lot) {
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].AcquiredAt.Equal(ls[j].AcquiredAt) {
			return ls[i].LotID < ls[j].LotID
		}
		return ls[i].AcquiredAt.Before(ls[j].AcquiredAt)
	})
}
