package adaptor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type blockedReplayRootFamily struct {
	shamap.Family
	root    [32]byte
	blocked atomic.Bool
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (f *blockedReplayRootFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if f.blocked.Load() && hash == f.root {
		f.once.Do(func() { close(f.entered) })
		// Keep the read alive after cancellation to check ownership until I/O returns.
		<-f.resume
	}
	return f.Family.Fetch(ctx, hash)
}

func TestReplayCancellationKeepsActiveDrainMemoryUntilItReturns(t *testing.T) {
	base := backend.NewMemory()
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	source := closedLedgerWithPayment(t, base)
	t.Cleanup(source.Stop)
	target := source.GetClosedLedger()
	hash := target.Hash()
	r, _, sender, svc := makeRouter(t, base)
	parent := shamap.NewAcquisitionBudget(2 << 30)
	require.NoError(t, r.SetAcquisitionMemoryBudget(2<<30, parent))
	budget := r.catchupReplay.acquisitionMemory
	family := &blockedReplayRootFamily{Family: base, root: target.Header().TxHash, entered: make(chan struct{}), resume: make(chan struct{})}
	release := sync.OnceFunc(func() { close(family.resume) })
	defer release()
	r.SetAcquisitionFamily(family)
	store := r.catchupReplay.acquisitionStore
	store.start(t.Context())
	t.Cleanup(store.stopDrain)
	head := standardReplayTestLink{seq: target.Sequence(), hash: hash, ledger: target,
		response: &message.ReplayDeltaResponse{LedgerHeader: header.AddRaw(target.Header(), false)}}
	tail := buildAlternativeReplaySuccessor(t, target, time.Second)
	c := r.catchupReplay
	c.recordSeqHash(head.seq, head.hash, target.ParentHash(), true)
	c.recordSeqHash(tail.seq, tail.hash, head.hash, true)
	sender.peerSupportsReplay = false
	trackCatchupPeer(r, 7, tail.seq, tail.hash)
	require.True(t, c.tryArmStandardReplayPipeline(svc, svc.GetClosedLedger(), tail.seq, tail.hash, 7))
	c.standardReplay.pivotReady = false
	completePaymentReplayTestLink(t, r, head, target)
	entry := c.standardReplay.entries[head.seq]
	require.True(t, entry.durable)
	family.blocked.Store(true)
	c.standardReplay.pivotReady = true
	done := make(chan struct{})
	go func() { c.drainStandardReplayPipeline(); close(done) }()
	select {
	case <-family.entered:
	case <-time.After(time.Second):
		t.Fatal("replay drain did not reach the cold transaction root")
	}
	c.acquisitionMu.Lock()
	identity := c.standardReplayIdentityLocked()
	c.acquisitionMu.Unlock()
	_, canceled := c.cancelStandardReplayPipelineIdentity(identity, "test_cancellation")
	require.True(t, canceled)
	require.GreaterOrEqual(t, budget.Snapshot().Used, uint64(standardReplayProcessingBytes+standardReplayEntryMetadataBytes),
		"the active reader must retain its entry and processing admission after cancellation")
	require.False(t, budget.Close(), "cancellation cannot close a budget still owned by the reader")
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled drain did not finish after its read returned")
	}
	require.False(t, svc.ReplayBlocked())
	r.StopAcquisitions()
	store.stopDrain()
	require.Zero(t, budget.Snapshot().Used)
	require.True(t, r.CloseAcquisitionMemoryBudget())
	require.Zero(t, parent.Snapshot().Used)
}
