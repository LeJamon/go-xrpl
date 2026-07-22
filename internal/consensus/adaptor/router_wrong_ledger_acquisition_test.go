package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdaptorRequestLedgerTracksExactConsensusTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	target := [32]byte{0xA1}
	targetSeq := svc.GetClosedLedgerIndex() + 1
	trackCatchupPeer(r, 7, targetSeq)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(target)))
	il := r.fetchTracker.Find(target)
	require.NotNil(t, il)
	assert.Equal(t, uint32(0), il.Seq())
	assert.Equal(t, inbound.ReasonConsensus, il.Reason())
	require.Equal(t, []legacyBaseCall{{peerID: 7, hash: target, seq: 0}}, sender.legacyCalls())

	require.NoError(t, a.RequestLedger(consensus.LedgerID(target)))
	assert.Len(t, sender.legacyCalls(), 1)
	assert.Equal(t, 1, r.catchupInFlight())

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: 7,
		Type:   uint16(message.TypeLedgerData),
		Payload: encodePayload(t, &message.LedgerData{
			LedgerHash: target[:],
			LedgerSeq:  targetSeq,
			InfoType:   message.LedgerInfoBase,
			Nodes:      []message.LedgerNode{{NodeData: []byte{1, 2, 3}}},
		}),
	})
	assert.Nil(t, r.fetchTracker.Find(target), "matching reply must be consumed by the tracked acquisition")
}

func TestAdaptorRequestLedgerSupersedesSpeculativeConsensusTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	oldHash := [32]byte{0xB1}
	exactHash := [32]byte{0xB2}
	targetSeq := svc.GetClosedLedgerIndex() + 100
	trackCatchupPeer(r, 7, targetSeq)
	active := inbound.New(oldHash, targetSeq-1, 7, serveTestLogger())
	r.fetchTracker.Track(active)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(exactHash)))

	assert.Nil(t, r.fetchTracker.Find(oldHash))
	exact := r.fetchTracker.Find(exactHash)
	require.NotNil(t, exact)
	assert.Equal(t, uint32(0), exact.Seq())
	assert.Equal(t, exactHash, r.activeConsensusLedger)
	assert.Equal(t, [32]byte{}, r.pendingConsensusLedger)
	assert.Equal(t, 1, r.catchupInFlight())
	require.Len(t, sender.legacyCalls(), 1)
	assert.Equal(t, exactHash, sender.legacyCalls()[0].hash)
}

func TestAdaptorRequestLedgerCancelsSupersededTraversal(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	oldHash := [32]byte{0xD1}
	exactHash := [32]byte{0xD2}
	targetSeq := svc.GetClosedLedgerIndex() + 100
	trackCatchupPeer(r, 7, targetSeq)
	active := inbound.New(oldHash, targetSeq-1, 7, serveTestLogger())
	r.fetchTracker.Track(active)

	lane := newAcquisitionWorkLane(1)
	started := make(chan struct{})
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		close(started)
		<-ctx.Done()
		return acquisitionWorkResult{ledger: ledger, err: ctx.Err()}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lane.start(ctx)
	r.acquisitionWork = lane
	require.True(t, lane.submit(active, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-started

	require.NoError(t, a.RequestLedger(consensus.LedgerID(exactHash)))
	result := <-lane.results()
	assert.Same(t, active, result.ledger)
	assert.ErrorIs(t, result.err, context.Canceled)
	r.handleAcquisitionWorkResult(result)
	require.Eventually(t, func() bool { return !lane.has(active) }, time.Second, time.Millisecond)
	assert.Nil(t, r.fetchTracker.Find(oldHash))
	require.NotNil(t, r.fetchTracker.Find(exactHash))

	lane.stop()
}

func TestAdaptorRequestLedgerLetsExactAcquisitionFinish(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	firstHash := [32]byte{0xB3}
	intermediateHash := [32]byte{0xB8}
	latestHash := [32]byte{0xB4}
	trackCatchupPeer(r, 7, svc.GetClosedLedgerIndex()+100)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(firstHash)))
	require.NoError(t, a.RequestLedger(consensus.LedgerID(intermediateHash)))
	require.NoError(t, a.RequestLedger(consensus.LedgerID(latestHash)))

	require.NotNil(t, r.fetchTracker.Find(firstHash))
	assert.Nil(t, r.fetchTracker.Find(intermediateHash))
	assert.Nil(t, r.fetchTracker.Find(latestHash))
	assert.Equal(t, firstHash, r.activeConsensusLedger)
	assert.Equal(t, latestHash, r.pendingConsensusLedger)
	require.Len(t, sender.legacyCalls(), 1)
	assert.Equal(t, firstHash, sender.legacyCalls()[0].hash)

	first := r.fetchTracker.Find(firstHash)
	require.True(t, r.fetchTracker.DiscardExpected(first))
	r.armConsensusCatchup()

	require.NotNil(t, r.fetchTracker.Find(latestHash))
	assert.Equal(t, latestHash, r.activeConsensusLedger)
	require.Len(t, sender.legacyCalls(), 2)
	assert.Equal(t, latestHash, sender.legacyCalls()[1].hash)
}

func TestPendingConsensusTargetPrecedesRecordedCatchupTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	activeHash := [32]byte{0xB5}
	pendingHash := [32]byte{0xB6}
	recordedHash := [32]byte{0xB7}
	trackCatchupPeer(r, 7, svc.GetClosedLedgerIndex()+100)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(activeHash)))
	require.NoError(t, a.RequestLedger(consensus.LedgerID(pendingHash)))
	r.recordCatchupTarget(svc.GetClosedLedgerIndex()+100, recordedHash, 7)
	active := r.fetchTracker.Find(activeHash)
	require.True(t, r.fetchTracker.DiscardExpected(active))

	r.armConsensusCatchup()

	require.NotNil(t, r.fetchTracker.Find(pendingHash))
	assert.Nil(t, r.fetchTracker.Find(recordedHash))
	require.Len(t, sender.legacyCalls(), 2)
	assert.Equal(t, pendingHash, sender.legacyCalls()[1].hash)
}

func TestHistoryBackfillWaitsForConsensusCatchup(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	historyHash := [32]byte{0xC1}
	catchupHash := [32]byte{0xC2}
	trackCatchupPeer(r, 7, closed+10)
	r.startHistoryBackfill(closed-1, historyHash, 7, 0)
	r.recordCatchupTarget(closed+10, catchupHash, 7)

	r.armHistoryBackfill()
	assert.Nil(t, r.fetchTracker.Find(historyHash))
	assert.Empty(t, sender.legacyCalls())

	r.catchupMu.Lock()
	r.catchup = catchupTarget{}
	r.catchupMu.Unlock()
	r.armHistoryBackfill()
	require.NotNil(t, r.fetchTracker.Find(historyHash))
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestBehindPeerCannotPromoteWhileNetworkTargetIsAhead(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeTracking)
	closed := svc.GetClosedLedgerIndex()
	targetHash := [32]byte{0xC2}
	r.recordCatchupTarget(closed+10, targetHash, 7)

	lowHash := [32]byte{0xC3}
	r.checkBehind(1, lowHash, 8)

	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
	assert.Empty(t, sender.legacyCalls())
	assert.Nil(t, r.fetchTracker.Find(lowHash))
	seq, hash, peerID := r.bestCatchupTarget()
	assert.Equal(t, closed+10, seq)
	assert.Equal(t, targetHash, hash)
	assert.Equal(t, uint64(7), peerID)
}

func TestAheadByMoreThanDoesNotWrap(t *testing.T) {
	assert.False(t, aheadByMoreThan(1, 19_263_641, 1))
	assert.False(t, aheadByMoreThan(19_263_641, 19_263_641, 1))
	assert.False(t, aheadByMoreThan(19_263_642, 19_263_641, 1))
	assert.True(t, aheadByMoreThan(19_263_643, 19_263_641, 1))
	assert.False(t, aheadByMoreThan(0, ^uint32(0), 1))
}

type ledgerRequestRecorder struct {
	noopSender
	mu    sync.Mutex
	calls []consensus.LedgerID
}

func (s *ledgerRequestRecorder) RequestLedger(id consensus.LedgerID) error {
	s.mu.Lock()
	s.calls = append(s.calls, id)
	s.mu.Unlock()
	return nil
}

func TestAdaptorRequestLedgerFallsBackWithoutRouter(t *testing.T) {
	sender := &ledgerRequestRecorder{}
	a := New(Config{LedgerService: newTestLedgerService(t), Sender: sender})
	target := consensus.LedgerID{0xD1}

	require.NoError(t, a.RequestLedger(target))
	require.NoError(t, a.RequestLedger(target))

	sender.mu.Lock()
	defer sender.mu.Unlock()
	assert.Equal(t, []consensus.LedgerID{target}, sender.calls)
}
