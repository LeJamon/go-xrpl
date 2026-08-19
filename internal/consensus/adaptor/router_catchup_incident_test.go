package adaptor

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newIssue1663BackedAcquisition(t *testing.T, seq uint32, peerID uint64) *inbound.Ledger {
	t.Helper()
	source := newWideWorkSource(t, 16)
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	hdr := header.LedgerHeader{LedgerIndex: seq, AccountHash: rootHash}
	headerData := header.AddRaw(hdr, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)

	family := backend.NewMemory()
	pack, err := source.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	entries := make([]shamap.FlushEntry, 0, len(pack))
	for _, node := range pack {
		entries = append(entries, shamap.FlushEntry{Hash: node.Hash, Data: node.Data})
	}
	require.NoError(t, family.StoreBatch(t.Context(), entries))

	il := inbound.New(ledgerHash, seq, peerID, serveTestLogger(), inbound.WithFamily(family))
	require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}))
	return il
}

func TestRouter_Issue1663CatchupCascadeRecoversToFull(t *testing.T) {
	svc := newTestLedgerService(t)
	a, sender := newRecordingAdaptor(t, svc)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r := newTestRouter(engine, a, nil)
	r.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	engine.switchHook = func(id consensus.LedgerID) {
		selected, err := a.GetLedger(id)
		require.NoError(t, err)
		require.NoError(t, a.OnLedgerSwitched(selected))
	}
	a.SetOperatingMode(consensus.OpModeTracking)

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	nextHash := [32]byte{0x31}
	next2Hash := [32]byte{0x32}
	r.handleStatusChange(statusChangeMessageWithParent(
		t, 7, closed.Sequence()+1, nextHash, closed.Hash(), true,
	))
	r.handleStatusChange(statusChangeMessageWithParent(
		t, 7, closed.Sequence()+2, next2Hash, nextHash, true,
	))

	const outlierBase = uint32(106_400_000)
	for offset := uint32(0); offset < seqHashRetain+10; offset++ {
		r.handleStatusChange(statusChangeMessageWithParent(
			t, 9, outlierBase+offset, [32]byte{0xee}, [32]byte{0xed}, true,
		))
	}
	entry, ok := r.lookupSeqHash(closed.Sequence() + 1)
	require.True(t, ok)
	require.Equal(t, nextHash, entry.hash)
	require.Equal(t, closed.Hash(), entry.parentHash)

	stale1 := newIssue1663BackedAcquisition(t, closed.Sequence()+1, 7)
	stale2 := newIssue1663BackedAcquisition(t, closed.Sequence()+2, 7)
	r.fetchTracker.Track(stale1)
	r.fetchTracker.Track(stale2)
	require.Equal(t, maxConcurrentSpeculativeCatchup, r.protectedCatchupInFlight())

	base := time.Unix(1_700_000_000, 0)
	stale1.RearmTimer(base)
	require.Equal(t, inbound.TimerRefresh, stale1.OnTimer(base.Add(3900*time.Millisecond)))
	lane := newAcquisitionWorkLane(1)
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		return processAcquisitionWorkWithBudget(ctx, ledger, events, 1)
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	defer func() {
		cancel()
		lane.stop()
	}()
	r.acquisitionWork = lane

	now := base.Add(3900 * time.Millisecond)
	for range 2 {
		require.True(t, lane.submit(stale1, acquisitionWorkEvent{
			kind:  acquisitionWorkTimer,
			fetch: func([32]byte) ([]byte, bool) { return nil, false },
		}))
		yielded := <-lane.results()
		require.True(t, yielded.yielded)

		now = now.Add(3900 * time.Millisecond)
		require.Equal(t, inbound.TimerRefresh, stale1.OnTimer(now))
		now = now.Add(3900 * time.Millisecond)
		require.True(t, lane.submit(stale1, acquisitionWorkEvent{
			kind: acquisitionWorkTimerCheck,
			at:   now,
		}))
		close(yielded.ack)

		escalated := <-lane.results()
		require.True(t, escalated.timerEscalate)
		require.False(t, escalated.yielded)
		close(escalated.ack)
		require.Eventually(t, func() bool { return !lane.has(stale1) }, time.Second, time.Millisecond)
	}
	now = now.Add(3900 * time.Millisecond)
	require.Equal(t, inbound.TimerEscalate, stale1.OnTimer(now))
	require.Equal(t, 2, stale1.ConsecutiveTimeouts())

	stale2.RearmTimer(base)
	require.Equal(t, inbound.TimerRefresh, stale2.OnTimer(base.Add(3900*time.Millisecond)))
	require.Equal(t, inbound.TimerEscalate, stale2.OnTimer(base.Add(7800*time.Millisecond)))
	require.Equal(t, inbound.TimerEscalate, stale2.OnTimer(base.Add(11700*time.Millisecond)))
	require.Equal(t, 2, stale2.ConsecutiveTimeouts())

	rootHash, rootData, wire := buildSelfHealSourceState(t)
	targetSeq := closed.Sequence() + maxForwardDeltaGap + 2
	hdr := header.LedgerHeader{
		LedgerIndex: targetSeq,
		ParentHash:  [32]byte{0x41},
		AccountHash: rootHash,
		CloseTime:   time.Unix(1_700_000_100, 0),
	}
	headerData := header.AddRaw(hdr, false)
	targetHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	r.handleStatusChange(statusChangeMessage(t, 8, targetSeq, targetHash))

	validationTracker := rcl.NewValidationTracker(2)
	trusted := []consensus.NodeID{{1}, {2}}
	validationTracker.SetNow(func() time.Time { return hdr.CloseTime })
	validationTracker.SetTrustedAndQuorum(trusted, 2)
	a.SetValidationHistorian(validationTracker)
	for _, nodeID := range trusted {
		require.True(t, validationTracker.Add(&consensus.Validation{
			LedgerID:  consensus.LedgerID(targetHash),
			LedgerSeq: targetSeq,
			NodeID:    nodeID,
			SignTime:  hdr.CloseTime,
			SeenTime:  hdr.CloseTime,
			Full:      true,
		}))
	}

	r.onLedgerFullyValidated(targetSeq, targetHash)
	require.Nil(t, r.fetchTracker.Find(stale1.Hash()))
	require.Same(t, stale2, r.fetchTracker.Find(stale2.Hash()))
	target := r.fetchTracker.Find(targetHash)
	require.NotNil(t, target)
	require.LessOrEqual(t, r.protectedCatchupInFlight(), maxConcurrentSpeculativeCatchup)
	require.GreaterOrEqual(t, acquireCount(sender), 1)

	require.NoError(t, target.GotBase([]message.LedgerNode{
		{NodeData: headerData},
		{NodeData: rootData},
	}))
	require.NoError(t, target.GotStateNodes(wire))
	target.CollectMissingRequest(false)
	require.True(t, target.IsComplete())
	r.completeInboundLedger(target)

	require.Eventually(t, func() bool {
		return svc.GetClosedLedgerIndex() == targetSeq && svc.GetValidatedLedgerIndex() == targetSeq
	}, time.Second, time.Millisecond)
	r.checkBehind(targetSeq, targetHash, 8)
	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
	assert.Equal(t, "proposing", consensusServerState(a.GetOperatingMode(), consensus.ModeProposing, true))
	assert.LessOrEqual(t, r.protectedCatchupInFlight(), maxConcurrentSpeculativeCatchup)
}
