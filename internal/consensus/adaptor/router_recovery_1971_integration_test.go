package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func TestRecoveryHeaderDiscoveryAndAvailabilityPreservePreparedSuccessors(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	completeStandardReplayTestLink(t, r, links[2])
	completeStandardReplayTestLink(t, r, links[0])
	svc.PromoteStoredValidatedLedgerAt(links[0].seq, links[0].hash, links[0].ledger.CloseTime())
	require.Equal(t, links[0].hash, svc.GetValidatedLedger().Hash())

	generation := r.standardReplay.generation
	prepared := r.standardReplay.entries[links[2].seq]
	require.NotNil(t, prepared)
	require.False(t, prepared.readyAt.IsZero())
	requests := len(sender.legacyCalls())
	r.recordValidationCatchupTarget(links[2].seq, links[2].hash, 7, catchupSourceQuorum)
	require.True(t, r.startHeaderParentDiscovery(links[0].ledger, links[2].seq, links[2].hash, 7, catchupSourceQuorum))
	for i := 2; i >= 1; i-- {
		sendTestHeaderReply(t, r, 7, links[i])
	}
	require.Equal(t, generation, r.standardReplay.generation)
	require.Same(t, prepared, r.standardReplay.entries[links[2].seq])

	_, err = r.replayer.Acquire(links[1].hash, 7, links[0].ledger)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: 7,
		Type:   message.TypeReplayDeltaResponse,
		Payload: encodePayload(t, &message.ReplayDeltaResponse{
			LedgerHash: links[1].hash[:],
			Error:      message.ReplyErrorNoNode,
		}),
	})
	require.Zero(t, r.replayer.Count())
	require.Len(t, sender.legacyCalls(), requests)
	acquisition := r.fetchTracker.Find(links[1].hash)
	require.NotNil(t, acquisition)
	require.True(t, acquisition.TransactionOnly())
	completeStandardReplayTestLink(t, r, links[1])
	for _, link := range links {
		stored, err := svc.GetLedgerByHash(link.hash)
		require.NoError(t, err)
		require.NotNil(t, stored)
		require.Equal(t, link.hash, stored.Hash())
	}
	require.Zero(t, r.FastSyncMetrics().ReplayPipelineFallbacks)
	require.Zero(t, r.FastSyncMetrics().ReplayPipelineDiscarded)
}
