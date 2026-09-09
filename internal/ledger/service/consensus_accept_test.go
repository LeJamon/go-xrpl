package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type acceptanceBlockingFamily struct {
	shamap.Family
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	fetches atomic.Int64
}

func (f *acceptanceBlockingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.fetches.Add(1)
	if f.armed.CompareAndSwap(true, false) {
		close(f.entered)
		<-f.release
	}
	return f.Family.Fetch(ctx, hash)
}

func (f *acceptanceBlockingFamily) unblock() { f.once.Do(func() { close(f.release) }) }

func coldAcceptanceParent(t testing.TB, parent *ledger.Ledger) (*ledger.Ledger, *acceptanceBlockingFamily) {
	t.Helper()
	child, err := ledger.NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	require.NoError(t, err)
	require.NoError(t, child.Close(parent.CloseTime().Add(10*time.Second), 0))
	parent = child
	memory := backend.NewMemory()
	state, err := parent.StateMapSnapshot()
	require.NoError(t, err)
	require.NoError(t, state.StoreDirty(func(entries []shamap.FlushEntry) error { return memory.StoreBatch(t.Context(), entries) }))
	root, err := state.Hash()
	require.NoError(t, err)
	family := &acceptanceBlockingFamily{Family: memory, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(family.unblock)
	cold, err := shamap.NewFromRootHash(shamap.TypeState, root, family)
	require.NoError(t, err)
	txMap := shamap.New(shamap.TypeTransaction)
	require.Zero(t, parent.TxCount())
	loaded, err := ledger.NewFromHeader(parent.Header(), cold, txMap, parent.Fees())
	require.NoError(t, err)
	return loaded, family
}

func TestConsensusAcceptanceDetachedBuildRejectsReplacedParent(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	warm := svc.GetClosedLedger()
	preferred, err := ledger.NewOpen(warm, warm.CloseTime().Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, preferred.Close(warm.CloseTime().Add(time.Second), 0))
	cold, family := coldAcceptanceParent(t, warm)
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	blob, _ := startupPaymentBlob(t, "detached-build", 1)
	family.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(t.Context(), cold, [][]byte{blob}, nil, cold.CloseTime().Add(10*time.Second), true)
		done <- err
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptance did not reach a cold read")
	}
	reads := make(chan struct{})
	go func() { svc.GetClosedLedger(); svc.GetValidatedLedger(); close(reads) }()
	select {
	case <-reads:
	case <-time.After(time.Second):
		t.Fatal("detached build blocked closed-ledger readers")
	}
	switched := make(chan error, 1)
	go func() { switched <- svc.SwitchToPreferredLedger(preferred) }()
	select {
	case err := <-switched:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("detached build blocked preferred-ledger switch")
	}
	family.unblock()
	require.ErrorIs(t, <-done, ErrConsensusParentMismatch)
	require.Same(t, preferred, svc.GetClosedLedger())
	require.Equal(t, preferred.Hash(), svc.openLedgerView.Current().ParentHash())
}

func TestConsensusAcceptanceStopDrainsDetachedBuild(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	cold, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	blob, _ := startupPaymentBlob(t, "detached-stop", 1)
	family.armed.Store(true)
	accepted := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(t.Context(), cold, [][]byte{blob}, nil, cold.CloseTime().Add(10*time.Second), true)
		accepted <- err
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptance did not reach a cold read")
	}
	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	require.Eventually(t, func() bool {
		svc.lifecycleMu.Lock()
		defer svc.lifecycleMu.Unlock()
		return svc.lifecycleState == serviceStopping
	}, time.Second, time.Millisecond)
	select {
	case <-stopped:
		t.Fatal("Stop returned with a detached build reading storage")
	default:
	}
	family.unblock()
	require.ErrorIs(t, <-accepted, errServiceNotRunning)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain detached build")
	}
	require.Same(t, cold, svc.GetClosedLedger())
	fetches := family.fetches.Load()
	_, err = svc.AcceptConsensusResult(t.Context(), cold, [][]byte{blob}, nil, time.Now(), true)
	require.ErrorIs(t, err, errServiceNotRunning)
	require.Equal(t, fetches, family.fetches.Load(), "stopped acceptance accessed storage")
}

func TestConsensusAcceptancePublishesOpenAndClosedTogether(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	parent := svc.GetClosedLedger()
	blob, hash := startupPaymentBlob(t, "acceptance-publication", 1)
	outcome, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
	require.NoError(t, err)
	require.True(t, outcome.Applied)
	replaying := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	svc.SetTxRelay(func([]byte) { close(replaying); <-release })
	accepted := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(t.Context(), parent, nil, nil, parent.CloseTime().Add(10*time.Second), true)
		accepted <- err
	}()
	select {
	case <-replaying:
	case <-time.After(5 * time.Second):
		t.Fatal("next-open replay did not relay the local transaction")
	}
	type frontier struct {
		closed     *ledger.Ledger
		openParent [32]byte
	}
	readDone := make(chan frontier, 1)
	go func() {
		svc.mu.RLock()
		result := frontier{svc.closedLedger, svc.openLedgerView.Current().ParentHash()}
		svc.mu.RUnlock()
		readDone <- result
	}()
	select {
	case got := <-readDone:
		require.Same(t, parent, got.closed)
		require.Equal(t, parent.Hash(), got.openParent)
	case <-time.After(time.Second):
		t.Fatal("next-open replay blocked closed-ledger reads")
	}

	historyDone := make(chan struct{})
	go func() { svc.historyComponent.mu.RLock(); svc.historyComponent.mu.RUnlock(); close(historyDone) }()
	select {
	case <-historyDone:
	case <-time.After(time.Second):
		t.Fatal("next-open replay blocked history reads")
	}
	unblock()
	require.NoError(t, <-accepted)
	closed := svc.GetClosedLedger()
	require.Equal(t, parent.Sequence()+1, closed.Sequence())
	require.Equal(t, [32]byte{}, closed.Header().TxHash, "speculative transaction leaked into agreed empty set")
	require.Equal(t, closed.Hash(), svc.openLedgerView.Current().ParentHash())
	exists, err := svc.openLedgerView.Current().TxExists(hash)
	require.NoError(t, err)
	require.True(t, exists, "next-open replay dropped local transaction")
}

func TestConsensusAcceptanceCarriesRPCIngressDuringDetachedBuild(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	cold, family := coldAcceptanceParent(t, svc.GetClosedLedger())
	require.NoError(t, svc.SwitchToPreferredLedger(cold))
	agreed, _ := startupPaymentBlob(t, "detached-rpc-agreed", 1)
	ingress, ingressHash := startupPaymentBlob(t, "detached-rpc-ingress", 2)
	parsed, err := tx.ParseFromBinary(ingress)
	require.NoError(t, err)
	family.armed.Store(true)
	accepted := make(chan error, 1)
	go func() {
		_, err := svc.AcceptConsensusResult(t.Context(), cold, [][]byte{agreed}, nil, cold.CloseTime().Add(10*time.Second), true)
		accepted <- err
	}()
	select {
	case <-family.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("build did not reach cold state read")
	}
	submitted := make(chan error, 1)
	go func() { _, err := svc.SubmitTransaction(parsed, ingress, false); submitted <- err }()
	select {
	case err := <-submitted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("detached build blocked RPC ingress")
	}
	family.unblock()
	require.NoError(t, <-accepted)
	exists, err := svc.openLedgerView.Current().TxExists(ingressHash)
	require.NoError(t, err)
	require.True(t, exists, "RPC transaction accepted during build was not carried into the next open view")
	exists, err = svc.GetClosedLedger().TxExists(ingressHash)
	require.NoError(t, err)
	require.False(t, exists, "RPC ingress leaked into the agreed set")
}
