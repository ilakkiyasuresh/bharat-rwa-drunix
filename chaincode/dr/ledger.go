package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"

	"bharat-rwa/chaincode/dr/internal/dr"
)

// Object type prefixes for composite keys.
const (
	typeScheme       = "scheme"
	typeInvestor     = "investor"
	typeLot          = "lot"
	typeDistribution = "distribution"
)

// collectionHolders is the private data collection carrying holder-level
// detail: which investor holds which lots, at what cost basis.
//
// It is private because a distribution partner has no business seeing another
// partner's book, and because holdings are the most commercially sensitive
// data in the system. The collection policy in network/collections_config.json
// admits the depositary, the registrar, the trustee and the supervisor; a
// distributor sees only what it is a member of.
//
// Drunix matters here specifically. Stock Fabric disseminates private data
// peer to peer through gossip, and the call volume grows with the number of
// collection members, which is precisely the pattern a distribution network
// creates. Drunix replaces the per-peer transient store with a shared one, so
// adding the twelfth distribution partner does not cost what it does upstream.
const collectionHolders = "collectionHolders"

// ledgerStore implements registry.Store against a Drunix transaction context.
//
// It is the only file in the chaincode that knows Fabric exists. Everything it
// calls lives under internal/, imports no ledger type, and can be run in a
// unit test or on another infrastructure unchanged.
type ledgerStore struct {
	ctx contractapi.TransactionContextInterface

	// eventName and eventPayload buffer the single event this transaction
	// will emit. Fabric permits one SetEvent per transaction and silently
	// keeps the last, so buffering and flushing once is the difference
	// between an event trail a supervisor can rely on and one that loses
	// whichever event happened not to be last.
	eventName    string
	eventPayload []byte
}

func (l *ledgerStore) stub() shim.ChaincodeStubInterface { return l.ctx.GetStub() }

// Now returns the transaction timestamp from the proposal.
//
// Not time.Now(). A chaincode that reads the host clock produces a different
// value on every endorsing peer, the read-write sets diverge, and the
// transaction fails validation for reasons that look like anything but a
// clock. The proposal timestamp is the one value every peer agrees on.
func (l *ledgerStore) Now() time.Time {
	ts, err := l.stub().GetTxTimestamp()
	if err != nil {
		return time.Unix(0, 0).UTC()
	}
	return ts.AsTime().UTC()
}

func (l *ledgerStore) TxID() string { return l.stub().GetTxID() }

func (l *ledgerStore) key(objectType string, attrs ...string) (string, error) {
	return l.stub().CreateCompositeKey(objectType, attrs)
}

// --- schemes and investors: world state on the issuance channel ---

func (l *ledgerStore) GetScheme(id string) (*dr.Scheme, error) {
	k, err := l.key(typeScheme, id)
	if err != nil {
		return nil, err
	}
	b, err := l.stub().GetState(k)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("scheme %s not found", id)
	}
	var s dr.Scheme
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (l *ledgerStore) PutScheme(s *dr.Scheme) error {
	k, err := l.key(typeScheme, s.ID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return l.stub().PutState(k, b)
}

// GetInvestor reads the compliance record.
//
// This record carries no personal data: a hash pointer into the off-chain
// vault, a class, a jurisdiction, an expiry and two flags. That is everything
// the transfer predicates need and nothing a data subject could ask to have
// erased from an immutable ledger.
func (l *ledgerStore) GetInvestor(id string) (*dr.Investor, error) {
	k, err := l.key(typeInvestor, id)
	if err != nil {
		return nil, err
	}
	b, err := l.stub().GetState(k)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("investor %s not found", id)
	}
	var i dr.Investor
	if err := json.Unmarshal(b, &i); err != nil {
		return nil, err
	}
	return &i, nil
}

func (l *ledgerStore) PutInvestor(i *dr.Investor) error {
	k, err := l.key(typeInvestor, i.ID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(i)
	if err != nil {
		return err
	}
	return l.stub().PutState(k, b)
}

// --- lots: private data ---

func (l *ledgerStore) PutLot(lot dr.Lot) error {
	k, err := l.key(typeLot, lot.SchemeID, lot.InvestorID, lot.LotID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(lot)
	if err != nil {
		return err
	}
	return l.stub().PutPrivateData(collectionHolders, k, b)
}

func (l *ledgerStore) DeleteLot(schemeID, lotID string) error {
	// The composite key carries the investor, so the lot is located by range
	// rather than addressed directly.
	lots, err := l.AllLots(schemeID)
	if err != nil {
		return err
	}
	for _, lot := range lots {
		if lot.LotID == lotID {
			k, err := l.key(typeLot, lot.SchemeID, lot.InvestorID, lot.LotID)
			if err != nil {
				return err
			}
			return l.stub().DelPrivateData(collectionHolders, k)
		}
	}
	return fmt.Errorf("lot %s not found in scheme %s", lotID, schemeID)
}

func (l *ledgerStore) LotsFor(schemeID, investorID string) ([]dr.Lot, error) {
	return l.queryLots(schemeID, investorID)
}

func (l *ledgerStore) AllLots(schemeID string) ([]dr.Lot, error) {
	return l.queryLots(schemeID)
}

func (l *ledgerStore) queryLots(attrs ...string) ([]dr.Lot, error) {
	it, err := l.stub().GetPrivateDataByPartialCompositeKey(collectionHolders, typeLot, attrs)
	if err != nil {
		return nil, err
	}
	defer it.Close()

	var out []dr.Lot
	for it.HasNext() {
		kv, err := it.Next()
		if err != nil {
			return nil, err
		}
		var lot dr.Lot
		if err := json.Unmarshal(kv.Value, &lot); err != nil {
			return nil, err
		}
		out = append(out, lot)
	}
	// Range query order is not guaranteed to be the order the business logic
	// needs, so the ordering is imposed here and is total.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AcquiredAt.Equal(out[j].AcquiredAt) {
			return out[i].LotID < out[j].LotID
		}
		return out[i].AcquiredAt.Before(out[j].AcquiredAt)
	})
	return out, nil
}

func (l *ledgerStore) HolderCount(schemeID string) (int, error) {
	lots, err := l.AllLots(schemeID)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	for _, lot := range lots {
		if lot.Units > 0 {
			seen[lot.InvestorID] = true
		}
	}
	return len(seen), nil
}

// PutDistribution records the computed distribution.
//
// The workings are stored, not just the totals: the weighted days, the gross,
// the withholding and the net for every holder. "Why is my payment this
// number" has to have an answer that does not depend on rerunning the job.
func (l *ledgerStore) PutDistribution(d dr.Distribution) error {
	k, err := l.key(typeDistribution, d.SchemeID, d.ID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return l.stub().PutPrivateData(collectionHolders, k, b)
}

// Emit buffers the transaction's event. See the note on ledgerStore.
func (l *ledgerStore) Emit(name string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	l.eventName, l.eventPayload = name, b
	return nil
}

// flush writes the buffered event. Called once by the contract wrapper after
// the domain call returns.
func (l *ledgerStore) flush() error {
	if l.eventName == "" {
		return nil
	}
	return l.stub().SetEvent(l.eventName, l.eventPayload)
}
