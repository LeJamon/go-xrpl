package ledger

import (
	"bytes"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

func TestConsumeStateTransfersOwnershipAndPersists(t *testing.T) {
	family := newConstructorRecordingFamily()
	store, _ := executionTestStore(t, family)
	target := newOpenChildWithStore(t, store)

	staged, err := target.MutableSnapshotUnflushed()
	if err != nil {
		t.Fatalf("MutableSnapshotUnflushed: %v", err)
	}
	stateKey := mutAcct(0x91)
	stateData := mutData(0x11)
	if err := staged.Insert(stateKey, stateData); err != nil {
		t.Fatalf("stage state: %v", err)
	}
	var txID [32]byte
	txID[0] = 0xa1
	txData := bytes.Repeat([]byte{0x21}, 16)
	if err := staged.AddTransactionWithMeta(txID, txData); err != nil {
		t.Fatalf("stage transaction: %v", err)
	}
	if err := staged.AdjustDropsDestroyed(11); err != nil {
		t.Fatalf("stage destroyed drops: %v", err)
	}

	stagedExecution := staged.execution
	if err := target.ConsumeState(staged); err != nil {
		t.Fatalf("ConsumeState: %v", err)
	}
	if target.execution != stagedExecution {
		t.Fatal("ConsumeState did not transfer the staged execution view")
	}
	if staged.execution == nil || staged.execution == stagedExecution {
		t.Fatal("consumed source retained the adopted execution view")
	}
	if staged.writable || staged.dropsDestroyed != 0 {
		t.Fatalf("consumed source writable=%v drops=%d, want false/0", staged.writable, staged.dropsDestroyed)
	}
	if err := staged.Insert(mutAcct(0x92), mutData(0x31)); !errors.Is(err, ErrLedgerImmutable) {
		t.Fatalf("consumed source mutation error = %v, want ErrLedgerImmutable", err)
	}
	if got, err := staged.Read(stateKey); err != nil || got != nil {
		t.Fatalf("consumed source state = %x, %v; want empty", got, err)
	}
	if _, found, err := staged.GetTransaction(txID); err != nil || found {
		t.Fatalf("consumed source transaction found=%v, err=%v; want empty", found, err)
	}
	if err := target.ConsumeState(staged); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second ConsumeState error = %v, want ErrInvalidState", err)
	}
	if got, err := target.Read(stateKey); err != nil || !bytes.Equal(got, stateData) {
		t.Fatalf("adopted state = %x, %v; want %x", got, err, stateData)
	}
	if got, found, err := target.GetTransaction(txID); err != nil || !found || !bytes.Equal(got, txData) {
		t.Fatalf("adopted transaction = %x, %v, %v; want %x", got, found, err, txData)
	}
	if target.dropsDestroyed != drops.XRPAmount(11) {
		t.Fatalf("adopted destroyed drops = %d, want 11", target.dropsDestroyed)
	}

	stateRoot, err := target.StateMapHash()
	if err != nil {
		t.Fatalf("state root: %v", err)
	}
	txRoot, err := target.TxMapHash()
	if err != nil {
		t.Fatalf("transaction root: %v", err)
	}
	reloadedState, err := shamap.NewFromRootHash(shamap.TypeState, stateRoot, family)
	if err != nil {
		t.Fatalf("reload state map: %v", err)
	}
	item, found, err := reloadedState.Get(stateKey.Key)
	if err != nil || !found || !bytes.Equal(item.Data(), stateData) {
		t.Fatalf("reloaded state = %v, %v, %v", item, found, err)
	}
	reloadedTx, err := shamap.NewFromRootHash(shamap.TypeTransaction, txRoot, family)
	if err != nil {
		t.Fatalf("reload transaction map: %v", err)
	}
	item, found, err = reloadedTx.Get(txID)
	if err != nil || !found || !bytes.Equal(item.Data(), txData) {
		t.Fatalf("reloaded transaction = %v, %v, %v", item, found, err)
	}
}

func TestConsumeStateRejectedCommitPreservesSource(t *testing.T) {
	target := newOpenChild(t)
	staged, err := target.MutableSnapshotUnflushed()
	if err != nil {
		t.Fatalf("MutableSnapshotUnflushed: %v", err)
	}
	stateKey := mutAcct(0x93)
	if err := staged.Insert(stateKey, mutData(0x41)); err != nil {
		t.Fatalf("stage state: %v", err)
	}
	stagedExecution := staged.execution
	if err := target.Close(target.CloseTime(), 0); err != nil {
		t.Fatalf("close target: %v", err)
	}
	targetExecution := target.execution

	if err := target.ConsumeState(staged); !errors.Is(err, ErrLedgerImmutable) {
		t.Fatalf("ConsumeState error = %v, want ErrLedgerImmutable", err)
	}
	if target.execution != targetExecution {
		t.Fatal("rejected consume changed target execution view")
	}
	if staged.execution != stagedExecution || !staged.writable {
		t.Fatal("rejected consume changed source ownership")
	}
	if err := staged.Insert(mutAcct(0x94), mutData(0x51)); err != nil {
		t.Fatalf("source was not writable after rejected consume: %v", err)
	}
	selfExecution := staged.execution
	if err := staged.ConsumeState(staged); err == nil {
		t.Fatal("self ConsumeState succeeded")
	}
	if staged.execution != selfExecution || !staged.writable {
		t.Fatal("self ConsumeState changed source")
	}
	if err := staged.ConsumeState(nil); err == nil {
		t.Fatal("nil ConsumeState succeeded")
	}
	if staged.execution != selfExecution || !staged.writable {
		t.Fatal("nil ConsumeState changed target")
	}
}

func TestConsumeStateRejectsImmutableSourceWithoutChangingTarget(t *testing.T) {
	target := newOpenChild(t)
	source, err := target.MutableSnapshotUnflushed()
	if err != nil {
		t.Fatalf("MutableSnapshotUnflushed: %v", err)
	}
	if err := source.Insert(mutAcct(0x97), mutData(0x81)); err != nil {
		t.Fatalf("stage state: %v", err)
	}
	if err := source.Close(source.CloseTime(), 0); err != nil {
		t.Fatalf("close source: %v", err)
	}
	targetExecution := target.execution
	targetDrops := target.dropsDestroyed
	sourceExecution := source.execution

	if err := target.ConsumeState(source); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("ConsumeState error = %v, want ErrInvalidState", err)
	}
	if target.execution != targetExecution || target.dropsDestroyed != targetDrops {
		t.Fatal("immutable-source rejection changed target")
	}
	if source.execution != sourceExecution || !source.IsImmutable() {
		t.Fatal("immutable-source rejection changed source")
	}
}

func TestApplyAtomicallyInvalidatesEscapedStage(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			target := newOpenChild(t)
			var escaped *Ledger
			injected := errors.New("injected failure")
			err := target.ApplyAtomically(func(view Writer) error {
				escaped = view.(*Ledger)
				if err := view.Insert(mutAcct(0x95), mutData(0x61)); err != nil {
					return err
				}
				if fail {
					return injected
				}
				return nil
			})
			if fail && !errors.Is(err, injected) {
				t.Fatalf("ApplyAtomically error = %v, want %v", err, injected)
			}
			if !fail && err != nil {
				t.Fatalf("ApplyAtomically: %v", err)
			}
			if escaped == nil || escaped.writable {
				t.Fatal("escaped atomic stage was not invalidated")
			}
			if err := escaped.Insert(mutAcct(0x96), mutData(0x71)); !errors.Is(err, ErrLedgerImmutable) {
				t.Fatalf("escaped stage mutation error = %v, want ErrLedgerImmutable", err)
			}
		})
	}
}

func TestConsumeStateAllocationsDoNotScaleWithResidentTree(t *testing.T) {
	small := consumeStateAllocs(t, 1)
	large := consumeStateAllocs(t, 4096)
	if large > small+1 {
		t.Fatalf("ConsumeState allocations scaled with resident tree: small %.0f, large %.0f", small, large)
	}
}

func consumeStateAllocs(t *testing.T, entries int) float64 {
	t.Helper()
	target := newResidentOpenLedger(t, entries)
	const runs = 100
	sources := make([]*Ledger, runs+1)
	for i := range sources {
		var err error
		sources[i], err = target.MutableSnapshotUnflushed()
		if err != nil {
			t.Fatalf("snapshot source %d: %v", i, err)
		}
	}
	next := 0
	var consumeErr error
	allocs := testing.AllocsPerRun(runs, func() {
		consumeErr = target.ConsumeState(sources[next])
		next++
	})
	if consumeErr != nil {
		t.Fatalf("ConsumeState: %v", consumeErr)
	}
	return allocs
}

func newResidentOpenLedger(tb testing.TB, entries int) *Ledger {
	tb.Helper()
	pool := membudget.New(32 << 20)
	workspace, err := pool.NewAccount("workspace", 16<<20, 16<<20)
	if err != nil {
		tb.Fatalf("workspace: %v", err)
	}
	memory, err := pool.NewAccount("overlay", 0, 1<<20)
	if err != nil {
		tb.Fatalf("overlay: %v", err)
	}
	store, err := NewExecutionStore(ExecutionConfig{Family: backend.NewMemory(), Workspace: workspace,
		Overlay: overlay.Config{MemoryBudget: memory, MaxMemoryBytes: 128 << 10, MaxDiskBytes: 16 << 20, SpillDir: tb.TempDir()}})
	if err != nil {
		tb.Fatalf("execution store: %v", err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	baseState := &executionTree{store: store, kind: shamap.TypeState}
	state, err := baseState.newMutable(0)
	if err != nil {
		tb.Fatalf("mutable state: %v", err)
	}
	baseTxs := &executionTree{store: store, kind: shamap.TypeTransaction, countKnown: true}
	txs, err := baseTxs.newMutable(0)
	if err != nil {
		tb.Fatalf("mutable transactions: %v", err)
	}
	for i := range entries {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i))
		key := sha512.Sum512_256(seed[:])
		if err := state.Put(key, bytes.Repeat([]byte{byte(i)}, 12)); err != nil {
			tb.Fatalf("seed state %d: %v", i, err)
		}
	}
	return &Ledger{
		execution: &executionView{state: state, transactions: txs},
		state:     StateOpen,
		writable:  true,
	}
}

func BenchmarkConsumeState(b *testing.B) {
	for _, entries := range []int{1, 4096} {
		b.Run(map[int]string{1: "one", 4096: "4096"}[entries], func(b *testing.B) {
			target := newResidentOpenLedger(b, entries)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				staged, err := target.MutableSnapshotUnflushed()
				if err != nil {
					b.Fatal(err)
				}
				if err := target.ConsumeState(staged); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
