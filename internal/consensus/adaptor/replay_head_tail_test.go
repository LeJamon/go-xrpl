package adaptor

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func TestReplayHeadTailRecoversFromSilentOrPartialPeer(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "silent"
		if partial {
			name = "partial"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, _, _, svc := makeRouter(t)
				t.Cleanup(svc.Stop)
				ledger, replies := newLedgerNodeChargeAcquisition(t, r, true)
				sender := &fixedLatencySender{
					acqRecordingSender: &acqRecordingSender{},
					latencies: map[uint64]time.Duration{
						7: time.Second, 8: 20 * time.Millisecond, 9: 100 * time.Millisecond,
					},
				}
				c := r.catchupReplay
				c.acquisition = sender
				ledger.AddPeer(8)
				ledger.AddPeer(9)
				c.standardReplay = standardReplayPipeline{
					active: true, pivotReady: true, anchorSeq: ledger.Seq() - 1,
					entries: map[uint32]*standardReplayEntry{
						ledger.Seq(): {seq: ledger.Seq(), hash: ledger.Hash(), acquisition: ledger, requestedAt: time.Now()},
					},
				}
				plan, err := ledger.PlanMissingReplyRequestsContext(t.Context(), []uint64{7})
				require.NoError(t, err)
				require.Len(t, plan.Requests, 1)
				require.False(t, c.sendMissingReplyRequest(ledger, plan.Requests[0]))
				if partial {
					_, err = ledger.ApplyMissingReplyContext(t.Context(), 7, true, replies[:1])
					require.NoError(t, err)
					plan, err = ledger.PlanMissingReplyRequestsContext(t.Context(), []uint64{7})
					require.NoError(t, err)
					require.Len(t, plan.Requests, 1)
					require.False(t, c.sendMissingReplyRequest(ledger, plan.Requests[0]))
					replies = replies[1:]
				}
				before := len(sender.txInd)
				c.retryReplayHeadTail(time.Now())
				require.Len(t, sender.txInd, before)
				time.Sleep(time.Second)
				c.retryInboundLedgerAcquisitions(time.Now())
				require.Len(t, sender.txInd, before, "the ordinary retry timer has not fired")
				c.standardReplay.pivotReady = false
				c.retryReplayHeadTail(time.Now())
				require.Len(t, sender.txInd, before, "a missing parent must not enable head hedging")
				c.standardReplay.pivotReady = true
				c.standardReplay.anchorSeq--
				c.retryReplayHeadTail(time.Now())
				require.Len(t, sender.txInd, before, "future entries must not hedge")
				c.standardReplay.anchorSeq++
				c.retryReplayHeadTail(time.Now())
				require.Len(t, sender.txInd, before+1)
				require.Equal(t, uint64(8), c.replayHeadDiagnostics.lastPeer)
				for range 10 {
					c.retryReplayHeadTail(time.Now())
				}
				require.Len(t, sender.txInd, before+1, "ticks must not amplify the hedge")
				result, err := ledger.ApplyMissingReplyContext(t.Context(), 8, true, replies)
				require.NoError(t, err)
				require.True(t, result.Complete)
				require.True(t, ledger.Snapshot().HaveState)
				require.True(t, ledger.Snapshot().HaveTransactions)
				require.True(t, ledger.TransactionOnly())
				late, err := ledger.ApplyMissingReplyContext(t.Context(), 7, true, replies)
				require.NoError(t, err)
				require.Zero(t, late.Useful)
				c.retryReplayHeadTail(time.Now())
				require.Len(t, sender.txInd, before+1)
			})
		})
	}
}

func TestReplayTailCandidatesPreferAvailableLowLatencyPeer(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	t.Cleanup(svc.Stop)
	ledger, _ := newLedgerNodeChargeAcquisition(t, r, true)
	for _, peerID := range []uint64{8, 9, 10} {
		ledger.AddPeer(peerID)
	}
	c := r.catchupReplay
	c.acquisition = &fixedLatencySender{
		acqRecordingSender: &acqRecordingSender{},
		latencies: map[uint64]time.Duration{
			7: time.Second, 8: 20 * time.Millisecond, 9: 100 * time.Millisecond,
		},
	}
	require.Equal(t, []uint64{8, 9, 10, 7}, c.replayTailCandidates(ledger, time.Now()))
	c.recordPeerAvailabilityMiss(8, ledger.Seq(), ledger.Hash(), message.ReplyErrorNoLedger, time.Now())
	require.Equal(t, []uint64{9, 10, 7}, c.replayTailCandidates(ledger, time.Now()))
}
