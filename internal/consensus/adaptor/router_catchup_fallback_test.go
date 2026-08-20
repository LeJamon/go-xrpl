package adaptor

import (
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func storeRecoveryLedger(t *testing.T, svc *service.Service, l *ledger.Ledger) {
	t.Helper()
	stateMap, err := l.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := l.TxMapSnapshot()
	require.NoError(t, err)
	h := l.Header()
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &h, stateMap, txMap))
}

func TestConsensusRecoveryLegacyFallbackPipelinesProvenSuccessors(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	_, anchor, anchorHash, anchorSeq := buildSuccessorAgainstParent(t, closed)
	_, child1, child1Hash, child1Seq := buildSuccessorAgainstParent(t, anchor)
	_, child2, child2Hash, child2Seq := buildSuccessorAgainstParent(t, child1)
	_, _, targetHash, targetSeq := buildSuccessorAgainstParent(t, child2)
	for _, link := range []struct {
		seq    uint32
		hash   [32]byte
		parent [32]byte
	}{
		{anchorSeq, anchorHash, closed.Hash()},
		{child1Seq, child1Hash, anchorHash},
		{child2Seq, child2Hash, child1Hash},
		{targetSeq, targetHash, child2Hash},
	} {
		r.recordSeqHash(link.seq, link.hash, link.parent, true)
	}
	storeRecoveryLedger(t, svc, anchor)
	trackCatchupPeer(r, 7, targetSeq)
	sender.mu.Lock()
	sender.peerSupportsReplay = false
	sender.mu.Unlock()

	r.consensusRecovery = consensusRecovery{targetHash: targetHash, stepHash: anchorHash}
	r.completeStoredConsensusRecovery(anchorSeq, anchorHash, closed.Hash(), false)

	require.Equal(t, consensusRecovery{
		targetHash: targetHash,
		stepHash:   child1Hash,
		anchorHash: anchorHash,
		anchorSeq:  anchorSeq,
	}, r.consensusRecovery)
	require.Equal(t, []legacyBaseCall{
		{peerID: 7, hash: child1Hash, seq: child1Seq},
		{peerID: 7, hash: child2Hash, seq: child2Seq},
		{peerID: 7, hash: targetHash, seq: targetSeq},
	}, sender.legacyCalls())
	require.Empty(t, sender.replayCalls())
	for _, hash := range [][32]byte{child1Hash, child2Hash, targetHash} {
		acquisition := r.fetchTracker.Find(hash)
		require.NotNil(t, acquisition)
		require.True(t, acquisition.TransactionOnly())
	}
}

func TestConsensusRecoveryReplayIssueFailureFallsBackToNextChild(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	_, anchor, anchorHash, anchorSeq := buildSuccessorAgainstParent(t, closed)
	_, child, childHash, childSeq := buildSuccessorAgainstParent(t, anchor)
	_, _, targetHash, targetSeq := buildSuccessorAgainstParent(t, child)
	r.recordSeqHash(anchorSeq, anchorHash, closed.Hash(), true)
	r.recordSeqHash(childSeq, childHash, anchorHash, true)
	r.recordSeqHash(targetSeq, targetHash, childHash, true)
	storeRecoveryLedger(t, svc, anchor)
	trackCatchupPeer(r, 7, targetSeq)
	sender.mu.Lock()
	sender.replayDeltaErr = errors.New("replay request failed")
	sender.mu.Unlock()
	r.consensusRecovery = consensusRecovery{
		targetHash: targetHash,
		anchorHash: anchorHash,
		anchorSeq:  anchorSeq,
	}

	require.True(t, r.armPendingConsensusLedger())
	require.Equal(t, []replayDeltaCall{{peerID: 7, hash: childHash}}, sender.replayCalls())
	require.Equal(t, []legacyBaseCall{{peerID: 7, hash: childHash, seq: childSeq}}, sender.legacyCalls())
	require.Equal(t, childHash, r.consensusRecovery.stepHash)
	require.Zero(t, r.replayer.Count())
	require.NotNil(t, r.fetchTracker.Find(childHash))
	require.True(t, r.fetchTracker.Find(childHash).TransactionOnly())
	require.Nil(t, r.fetchTracker.Find(targetHash))
}

func TestConsensusRecoveryReplayFailureKeepsChildOfMovingTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	_, anchor, anchorHash, anchorSeq := buildSuccessorAgainstParent(t, closed)
	_, child, childHash, childSeq := buildSuccessorAgainstParent(t, anchor)
	_, oldTarget, oldTargetHash, oldTargetSeq := buildSuccessorAgainstParent(t, child)
	_, _, newTargetHash, newTargetSeq := buildSuccessorAgainstParent(t, oldTarget)
	for _, link := range []struct {
		seq    uint32
		hash   [32]byte
		parent [32]byte
	}{
		{anchorSeq, anchorHash, closed.Hash()},
		{childSeq, childHash, anchorHash},
		{oldTargetSeq, oldTargetHash, childHash},
		{newTargetSeq, newTargetHash, oldTargetHash},
	} {
		r.recordSeqHash(link.seq, link.hash, link.parent, true)
	}
	storeRecoveryLedger(t, svc, anchor)
	trackCatchupPeer(r, 7, newTargetSeq)
	r.consensusRecovery = consensusRecovery{
		targetHash: oldTargetHash,
		anchorHash: anchorHash,
		anchorSeq:  anchorSeq,
	}
	require.True(t, r.armPendingConsensusLedger())
	require.Equal(t, childHash, r.consensusRecovery.stepHash)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(newTargetHash)))

	bad := &message.ReplayDeltaResponse{LedgerHash: childHash[:], Error: message.ReplyErrorNoLedger}
	payload, err := message.Encode(bad)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    message.TypeReplayDeltaResponse,
		Payload: payload,
	})

	require.Equal(t, []legacyBaseCall{{peerID: 7, hash: childHash, seq: childSeq}}, sender.legacyCalls())
	require.Equal(t, consensusRecovery{
		targetHash: newTargetHash,
		stepHash:   childHash,
		anchorHash: anchorHash,
		anchorSeq:  anchorSeq,
	}, r.consensusRecovery)
	require.Nil(t, r.fetchTracker.Find(newTargetHash))
}

func TestConsensusRecoveryLegacyFallbackRequiresCompleteAncestry(t *testing.T) {
	for _, tc := range []struct {
		name         string
		recordTarget bool
		parentHash   [32]byte
	}{
		{name: "missing ancestry"},
		{name: "broken linkage", recordTarget: true, parentHash: [32]byte{0xFF}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, sender, svc := makeRouter(t)
			_, err := svc.AcceptLedger(context.Background())
			require.NoError(t, err)
			closed := svc.GetClosedLedger()
			require.NotNil(t, closed)

			_, anchor, anchorHash, anchorSeq := buildSuccessorAgainstParent(t, closed)
			_, child, childHash, childSeq := buildSuccessorAgainstParent(t, anchor)
			_, _, targetHash, targetSeq := buildSuccessorAgainstParent(t, child)
			r.recordSeqHash(anchorSeq, anchorHash, closed.Hash(), true)
			r.recordSeqHash(childSeq, childHash, tc.parentHash, true)
			if tc.recordTarget {
				r.recordSeqHash(targetSeq, targetHash, childHash, true)
			}
			storeRecoveryLedger(t, svc, anchor)
			trackCatchupPeer(r, 7, targetSeq)
			sender.mu.Lock()
			sender.peerSupportsReplay = false
			sender.mu.Unlock()
			r.consensusRecovery = consensusRecovery{
				targetHash: targetHash,
				anchorHash: anchorHash,
				anchorSeq:  anchorSeq,
			}

			require.True(t, r.armPendingConsensusLedger())
			expectedSeq := uint32(0)
			expectedRecovery := consensusRecovery{
				targetHash: targetHash,
				stepHash:   targetHash,
				anchorHash: anchorHash,
				anchorSeq:  anchorSeq,
			}
			if tc.recordTarget {
				expectedSeq = targetSeq
				expectedRecovery.anchorHash = [32]byte{}
				expectedRecovery.anchorSeq = 0
			}
			require.Equal(t, []legacyBaseCall{{peerID: 7, hash: targetHash, seq: expectedSeq}}, sender.legacyCalls())
			require.Equal(t, expectedRecovery, r.consensusRecovery)
			require.Nil(t, r.fetchTracker.Find(childHash))
		})
	}
}
