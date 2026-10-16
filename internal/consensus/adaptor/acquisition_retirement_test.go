package adaptor

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type issue2072LogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *issue2072LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *issue2072LogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

type issue2072PausedScope struct {
	*acquisitionStoreScope
	entered    chan struct{}
	resume     chan struct{}
	returned   chan error
	storedMu   sync.Mutex
	stored     []shamap.FlushEntry
	once       sync.Once
	resumeOnce sync.Once
	retired    atomic.Uint32
}

func (f *issue2072PausedScope) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	f.once.Do(func() { close(f.entered) })
	<-f.resume
	cloned := make([]shamap.FlushEntry, len(entries))
	copy(cloned, entries)
	for i := range cloned {
		cloned[i].Data = bytes.Clone(cloned[i].Data)
	}
	f.storedMu.Lock()
	f.stored = append(f.stored, cloned...)
	f.storedMu.Unlock()
	err := f.acquisitionStoreScope.StoreBatch(ctx, entries)
	f.returned <- err
	return err
}

func (f *issue2072PausedScope) Retire(ctx context.Context) error {
	f.retired.Add(1)
	return f.acquisitionStoreScope.Retire(ctx)
}

func (f *issue2072PausedScope) release() {
	f.resumeOnce.Do(func() { close(f.resume) })
}

func (f *issue2072PausedScope) storedEntries() []shamap.FlushEntry {
	f.storedMu.Lock()
	defer f.storedMu.Unlock()
	entries := make([]shamap.FlushEntry, len(f.stored))
	copy(entries, f.stored)
	for i := range entries {
		entries[i].Data = bytes.Clone(entries[i].Data)
	}
	return entries
}

type issue2072TxOnlyFixture struct {
	headerData []byte
	txRootData []byte
	hash       [32]byte
	seq        uint32
	txRoot     [32]byte
}

func newIssue2072TxOnlyFixture(t *testing.T) issue2072TxOnlyFixture {
	t.Helper()
	built := closedLedgerWithPayment(t)
	t.Cleanup(built.Stop)
	target := built.GetClosedLedger()
	require.NotNil(t, target)
	require.True(t, target.IsClosed())
	h := target.Header()
	require.Equal(t, target.Hash(), header.CalculateHash(h))
	txMap, err := target.TxMapSnapshot()
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	require.NotZero(t, txRoot)
	require.Equal(t, h.TxHash, txRoot)
	wire, err := txMap.WalkWireNodes()
	require.NoError(t, err)
	require.NotEmpty(t, wire)
	rootID, err := shamap.ParseNodeID(wire[0].NodeID)
	require.NoError(t, err)
	require.True(t, rootID.IsRoot())

	return issue2072TxOnlyFixture{
		headerData: header.AddRaw(h, false),
		txRootData: wire[0].Data,
		hash:       target.Hash(),
		seq:        target.Sequence(),
		txRoot:     txRoot,
	}
}

func issue2072LedgerData(f issue2072TxOnlyFixture) *message.LedgerData {
	return &message.LedgerData{
		LedgerHash: f.hash[:],
		LedgerSeq:  f.seq,
		InfoType:   message.LedgerInfoBase,
		Nodes: []message.LedgerNode{
			{NodeData: f.headerData},
			{NodeData: nil},
			{NodeData: f.txRootData},
		},
	}
}

func issue2072AwaitWorkResult(t *testing.T, work *acquisitionWorkLane) acquisitionWorkResult {
	t.Helper()
	select {
	case result := <-work.results():
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("acquisition work did not return a result")
		return acquisitionWorkResult{}
	}
}

func issue2072NewRouter(t *testing.T, base shamap.Family, memoryLimit uint64) (*Router, *Adaptor, *service.Service, *acquisitionWorkLane, func()) {
	t.Helper()
	svc := newTestLedgerService(t)
	t.Cleanup(svc.Stop)
	a, _ := newRecordingAdaptor(t, svc)
	r := newTestRouter(nil, a, nil)
	r.SetAcquisitionFamily(base)
	r.SetAcquisitionMemoryLimit(memoryLimit)
	work, stop := r.catchupReplay.startAcquisitionWork(t.Context())
	return r, a, svc, work, stop
}

func TestRouterIssue2072LedgerBuiltRetiresPausedTransactionOnlyAcquisition(t *testing.T) {
	base := backend.NewMemory()
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	var logs issue2072LogBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	r, a, svc, work, stop := issue2072NewRouter(t, base, 512<<20)
	t.Cleanup(stop)
	r.logger = logger
	r.catchupReplay.logger = logger
	r.catchupReplay.acquisitionStore.logger = logger
	fixture := newIssue2072TxOnlyFixture(t)
	scope := r.catchupReplay.acquisitionStore.scope().(*acquisitionStoreScope)
	paused := &issue2072PausedScope{
		acquisitionStoreScope: scope,
		entered:               make(chan struct{}),
		resume:                make(chan struct{}),
		returned:              make(chan error, 1),
	}
	t.Cleanup(paused.release)
	acquisition := inbound.New(fixture.hash, fixture.seq, 7, logger,
		inbound.WithFamily(paused),
		inbound.WithTransactionOnly(),
		inbound.WithAcquisitionBudget(r.catchupReplay.acquisitionMemory),
	)
	r.catchupReplay.fetchTracker.Track(acquisition)

	require.True(t, r.handleInboundLedgerData(acquisition, issue2072LedgerData(fixture), 7))
	select {
	case <-paused.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("verified-node persistence did not reach the paused family")
	}
	snapshot := acquisition.Snapshot()
	require.Equal(t, fixture.hash, snapshot.Hash)
	require.True(t, snapshot.HaveHeader)

	// Exercise the local-ledger-built retirement callback while persistence is paused.
	require.NotNil(t, a.onLedgerBuilt)
	a.onLedgerBuilt(fixture.seq, fixture.hash)
	replacement := inbound.New(fixture.hash, fixture.seq, 8, logger)
	r.catchupReplay.fetchTracker.Track(replacement)
	defer func() {
		r.catchupReplay.fetchTracker.DiscardExpected(replacement)
	}()
	paused.release()
	select {
	case err := <-paused.returned:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("late StoreBatch did not return after scope retirement")
	}
	stored := paused.storedEntries()
	require.Len(t, stored, 1)
	require.Equal(t, fixture.txRoot, stored[0].Hash)
	require.Equal(t, shamap.TypeTransaction, stored[0].MapType)
	storedNode, err := shamap.DeserializeFromPrefix(stored[0].Data)
	require.NoError(t, err)
	require.Equal(t, fixture.txRoot, storedNode.Hash())
	result := issue2072AwaitWorkResult(t, work)
	require.ErrorIs(t, result.err, context.Canceled)
	require.NotErrorIs(t, result.err, inbound.ErrPersistence)
	require.NoError(t, result.persistenceErr)
	require.False(t, result.complete)
	c := r.catchupReplay
	c.handleAcquisitionWorkResult(result)

	require.Equal(t, uint32(1), paused.retired.Load(), "retirement must run once")
	require.Same(t, replacement, c.fetchTracker.Find(fixture.hash), "late old result revived or removed the replacement")
	require.Zero(t, c.replayPipelineApplied.Load(), "retired acquisition must not install a ledger")
	_, err = svc.GetLedgerByHash(fixture.hash)
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
	require.False(t, replacement.IsComplete(), "retirement must not complete the replacement")
	require.Zero(t, c.acquisitionMemory.Snapshot().Used, "retired acquisition leaked request/tree credits")
	require.Zero(t, c.acquisitionStore.bytes.Load(), "retired acquisition leaked persistence credits")
	c.acquisitionStore.pendingMu.RLock()
	pendingLen := len(c.acquisitionStore.pending)
	pendingReleased := c.acquisitionStore.pendingReleased
	c.acquisitionStore.pendingMu.RUnlock()
	require.Zero(t, pendingLen)
	require.Zero(t, pendingReleased)
	require.NotContains(t, logs.String(), "failed to persist")
}

func TestRouterIssue2072ActiveStoreFailureRemainsPersistenceError(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	base.failAll = true
	r, _, svc, work, stop := issue2072NewRouter(t, base, 512<<20)
	t.Cleanup(stop)
	// Force the real scope down its synchronous borrowed-write path. The
	// acquisition map keeps a separate, ample budget so only persistence fails.
	r.catchupReplay.acquisitionStore.memory = shamap.NewAcquisitionBudget(1)
	fixture := newIssue2072TxOnlyFixture(t)
	scope := r.catchupReplay.acquisitionStore.scope().(*acquisitionStoreScope)
	acquisition := inbound.New(fixture.hash, fixture.seq, 7, serveTestLogger(),
		inbound.WithFamily(scope),
		inbound.WithTransactionOnly(),
		inbound.WithAcquisitionBudget(r.catchupReplay.acquisitionMemory),
	)
	r.catchupReplay.fetchTracker.Track(acquisition)
	require.True(t, r.handleInboundLedgerData(acquisition, issue2072LedgerData(fixture), 7))
	result := issue2072AwaitWorkResult(t, work)
	require.ErrorIs(t, result.persistenceErr, inbound.ErrPersistence)
	require.NotErrorIs(t, result.err, context.Canceled)
	require.False(t, result.complete)
	r.catchupReplay.handleAcquisitionWorkResult(result)
	require.Nil(t, r.catchupReplay.fetchTracker.Find(fixture.hash))
	_, err := svc.GetLedgerByHash(fixture.hash)
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
}

func TestRouterIssue2072ScopeOnlyRetirementRemainsPersistenceError(t *testing.T) {
	base := backend.NewMemory()
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	r, _, svc, work, stop := issue2072NewRouter(t, base, 512<<20)
	t.Cleanup(stop)
	fixture := newIssue2072TxOnlyFixture(t)
	scope := r.catchupReplay.acquisitionStore.scope().(*acquisitionStoreScope)
	require.NoError(t, scope.Retire(t.Context()))
	acquisition := inbound.New(fixture.hash, fixture.seq, 7, serveTestLogger(),
		inbound.WithFamily(scope),
		inbound.WithTransactionOnly(),
		inbound.WithAcquisitionBudget(r.catchupReplay.acquisitionMemory),
	)
	r.catchupReplay.fetchTracker.Track(acquisition)
	require.True(t, r.handleInboundLedgerData(acquisition, issue2072LedgerData(fixture), 7))
	result := issue2072AwaitWorkResult(t, work)
	require.ErrorIs(t, result.persistenceErr, inbound.ErrPersistence)
	require.NotErrorIs(t, result.err, context.Canceled)
	require.False(t, result.complete)
	r.catchupReplay.handleAcquisitionWorkResult(result)
	require.Nil(t, r.catchupReplay.fetchTracker.Find(fixture.hash))
	_, err := svc.GetLedgerByHash(fixture.hash)
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
}
