package batch

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type boundedBatchBase struct {
	data      map[[32]byte][]byte
	cfg       overlay.Config
	commitErr error
	drops     drops.XRPAmount
}

func newBoundedBatchBase(cfg overlay.Config) *boundedBatchBase {
	return &boundedBatchBase{data: make(map[[32]byte][]byte), cfg: cfg}
}

func (v *boundedBatchBase) ExecutionJournalConfig() overlay.Config {
	return v.cfg
}

func (v *boundedBatchBase) Read(k keylet.Keylet) ([]byte, error) {
	return bytes.Clone(v.data[k.Key]), nil
}

func (v *boundedBatchBase) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.data[k.Key]
	return ok, nil
}

func (v *boundedBatchBase) Rules() *amendment.Rules {
	return amendment.AllSupportedRules()
}

func (v *boundedBatchBase) ForEach(fn func([32]byte, []byte) bool) error {
	keys := make([][32]byte, 0, len(v.data))
	for key := range v.data {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	for _, key := range keys {
		if !fn(key, v.data[key]) {
			break
		}
	}
	return nil
}

func (v *boundedBatchBase) Succ(after [32]byte) ([32]byte, []byte, bool, error) {
	var best [32]byte
	found := false
	for key := range v.data {
		if bytes.Compare(key[:], after[:]) <= 0 {
			continue
		}
		if !found || bytes.Compare(key[:], best[:]) < 0 {
			best = key
			found = true
		}
	}
	if !found {
		return [32]byte{}, nil, false, nil
	}
	return best, bytes.Clone(v.data[best]), true, nil
}

func (v *boundedBatchBase) TxExists([32]byte) (bool, error) { return false, nil }

func (v *boundedBatchBase) LedgerSeq() uint32 { return 1 }

func (v *boundedBatchBase) Insert(k keylet.Keylet, data []byte) error {
	if _, exists := v.data[k.Key]; exists {
		return errors.New("entry already exists")
	}
	v.data[k.Key] = bytes.Clone(data)
	return nil
}

func (v *boundedBatchBase) Update(k keylet.Keylet, data []byte) error {
	if _, exists := v.data[k.Key]; !exists {
		return errors.New("entry not found")
	}
	v.data[k.Key] = bytes.Clone(data)
	return nil
}

func (v *boundedBatchBase) Erase(k keylet.Keylet) error {
	if _, exists := v.data[k.Key]; !exists {
		return errors.New("entry not found")
	}
	delete(v.data, k.Key)
	return nil
}

func (v *boundedBatchBase) AdjustDropsDestroyed(amount drops.XRPAmount) error {
	v.drops = v.drops.Add(amount)
	return nil
}

func (v *boundedBatchBase) ApplyAtomically(apply func(ledgercore.Writer) error) error {
	candidate := newBoundedBatchBase(v.cfg)
	for key, data := range v.data {
		candidate.data[key] = bytes.Clone(data)
	}
	candidate.drops = v.drops
	if err := apply(candidate); err != nil {
		return err
	}
	if v.commitErr != nil {
		return v.commitErr
	}
	v.data = candidate.data
	v.drops = candidate.drops
	return nil
}

type boundedBatchInnerEngine struct {
	mode          boundedBatchInnerMode
	key           keylet.Keylet
	value         []byte
	account       *membudget.Account
	observedInUse bool
}

type boundedBatchInnerMode uint8

const (
	boundedBatchInnerSuccess boundedBatchInnerMode = iota
	boundedBatchInnerFailure
	boundedBatchInnerTEC
)

func (e *boundedBatchInnerEngine) ApplyInnerTransaction(
	_ context.Context,
	view txcore.LedgerView,
	innerTx txcore.Transaction,
	parentBatchID [32]byte,
	transactionIndex uint32,
) txcore.ApplyResult {
	return e.apply(view, innerTx, parentBatchID, transactionIndex)
}

func (e *boundedBatchInnerEngine) apply(
	view txcore.LedgerView,
	innerTx txcore.Transaction,
	parentBatchID [32]byte,
	transactionIndex uint32,
) txcore.ApplyResult {
	atomicView, ok := view.(applystate.AtomicLedgerView)
	if !ok {
		return txcore.ApplyResult{Result: ter.TefINTERNAL}
	}
	inner := applystate.NewApplyStateTable(atomicView, parentBatchID, transactionIndex, amendment.AllSupportedRules())
	defer func() { _ = inner.Close() }()
	_ = inner.ExecutionJournalConfig()
	if err := inner.Insert(e.key, e.value); err != nil {
		return txcore.ApplyResult{Result: ter.TefEXCEPTION, Cause: err}
	}
	e.observedInUse = e.account.Snapshot().Used > 0
	switch e.mode {
	case boundedBatchInnerFailure:
		return txcore.ApplyResult{Result: ter.TefEXCEPTION}
	case boundedBatchInnerTEC:
		return txcore.ApplyResult{Result: ter.TecUNFUNDED_PAYMENT}
	default:
		if err := inner.ApplyUnthreaded(); err != nil {
			return txcore.ApplyResult{Result: ter.TefEXCEPTION, Cause: err}
		}
		return txcore.ApplyResult{Result: ter.TesSUCCESS, Applied: true, Metadata: &txcore.Metadata{}}
	}
}

var _ txcore.InnerTransactionEngine = (*boundedBatchInnerEngine)(nil)

func boundedBatchTestOffer(t *testing.T) (keylet.Keylet, []byte) {
	t.Helper()
	accountID, err := state.DecodeAccountID(testOuter)
	if err != nil {
		t.Fatal(err)
	}
	key := keylet.Offer(accountID, 2)
	data, err := state.SerializeLedgerOffer(&state.LedgerOffer{
		Account:       testOuter,
		Sequence:      2,
		TakerPays:     state.NewXRPAmountFromInt(100),
		TakerGets:     state.NewXRPAmountFromInt(50),
		BookDirectory: [32]byte{1},
		BookNode:      1,
		OwnerNode:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return key, data
}

func TestAllOrNothingBoundedBatchReleasesJournalOnEveryExit(t *testing.T) {
	tests := []struct {
		name       string
		mode       boundedBatchInnerMode
		commitErr  error
		wantResult ter.Result
		wantStored bool
	}{
		{name: "success and adoption", mode: boundedBatchInnerSuccess, wantResult: ter.TesSUCCESS, wantStored: true},
		{name: "inner failure", mode: boundedBatchInnerFailure, wantResult: ter.TesSUCCESS},
		{name: "inner tec", mode: boundedBatchInnerTEC, wantResult: ter.TesSUCCESS},
		{name: "adoption failure", mode: boundedBatchInnerSuccess, commitErr: errors.New("commit failed"), wantResult: ter.TefINTERNAL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool := membudget.New(1 << 20)
			account, err := pool.NewAccount("batch-test", 0, 1<<20)
			requireNoError(t, err)
			base := newBoundedBatchBase(overlay.Config{MemoryBudget: account, MaxMemoryBytes: 64 << 10})
			base.commitErr = tc.commitErr
			key, value := boundedBatchTestOffer(t)
			innerEngine := &boundedBatchInnerEngine{mode: tc.mode, key: key, value: value, account: account}
			ctx := &txcore.ApplyContext{
				View:                   base,
				TxHash:                 [32]byte{1},
				Config:                 txcore.EngineConfig{LedgerSequence: 10, Rules: amendment.AllSupportedRules()},
				InnerTransactionEngine: innerEngine,
				Ctx:                    context.Background(),
			}
			innerTx := txcore.NewBaseTx(txcore.TypePayment, testOuter)
			result, applied := (&Batch{}).applyAllOrNothing(ctx, []txcore.Transaction{innerTx})
			if result != tc.wantResult {
				t.Fatalf("result=%s, want %s", result, tc.wantResult)
			}
			if !innerEngine.observedInUse {
				t.Fatal("nested bounded journal did not reserve shared memory")
			}
			if got := account.Snapshot().Used; got != 0 {
				t.Fatalf("memory budget retained %d bytes after batch return", got)
			}
			_, stored := base.data[key.Key]
			if stored != tc.wantStored {
				t.Fatalf("stored=%v, want %v; applied=%d", stored, tc.wantStored, len(applied))
			}
		})
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var _ txcore.LedgerView = (*boundedBatchBase)(nil)
var _ ledgercore.AtomicWriter = (*boundedBatchBase)(nil)
