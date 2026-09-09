package adaptor

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type runtimeRecoveryFamily struct {
	shamap.Family
	delay time.Duration
	reads atomic.Uint64
}

func (f *runtimeRecoveryFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.reads.Add(1)
	if f.delay != 0 {
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return f.Family.Fetch(ctx, hash)
}

func (f *runtimeRecoveryFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.Fetch(ctx, hash)
}

func TestRuntimeRecoveryRepairsThirtyLedgerOutage(t *testing.T) {
	for _, speculativeCloses := range []int{0, 4, 30, 40} {
		for _, delay := range []time.Duration{0, 20 * time.Millisecond} {
			t.Run(fmt.Sprintf("closes=%d/delay=%s", speculativeCloses, delay), func(t *testing.T) {
				svc, err := service.New(service.Config{GenesisConfig: genesis.DefaultConfig()})
				require.NoError(t, err)
				require.NoError(t, svc.Start())
				t.Cleanup(svc.Stop)
				base := svc.GetClosedLedger()
				svc.SetValidatedLedger(base.Sequence(), base.Hash())
				require.NoError(t, svc.SwitchToPreferredLedger(base))
				require.False(t, svc.NeedsInitialSync())
				require.False(t, svc.IsFastLoadProvisional())

				a, sender := newRecordingAdaptor(t, svc)
				sender.peerSupportsReplay = false
				engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
				engine.switchHook = func(id consensus.LedgerID) {
					selected, getErr := a.GetLedger(id)
					require.NoError(t, getErr)
					require.NoError(t, a.OnLedgerSwitched(selected))
				}
				r := newTestRouter(engine, a, nil)
				family := &runtimeRecoveryFamily{Family: backend.NewMemory(), delay: delay}
				r.acquisitionFamily = family
				a.SetOperatingMode(consensus.OpModeTracking)

				links := make([]standardReplayTestLink, 0, 30)
				parent := base
				for range 30 {
					link := buildAlternativeReplaySuccessor(t, parent, time.Second)
					links = append(links, link)
					parent = link.ledger
				}
				for range speculativeCloses {
					_, acceptErr := svc.AcceptConsensusResult(t.Context(), svc.GetClosedLedger(), nil, nil, time.Now(), true)
					require.NoError(t, acceptErr)
				}
				require.Equal(t, base.Hash(), svc.GetValidatedLedger().Hash())
				require.Equal(t, base.Sequence()+uint32(speculativeCloses), svc.GetClosedLedgerIndex())
				target := links[len(links)-1]
				require.False(t, r.recoveryAnchorReachesTarget(base.Sequence(), base.Hash(), target.hash))

				started := time.Now()
				r.handleStatusChange(statusChangeMessage(t, 7, target.seq, target.hash))
				require.Empty(t, sender.legacyCalls(), "peer status must not start an unnecessary full-state pivot")
				require.Equal(t, base.Hash(), svc.GetValidatedLedger().Hash())

				tracker := rcl.NewValidationTracker(2)
				trusted := []consensus.NodeID{{1}, {2}}
				tracker.SetNow(func() time.Time { return target.ledger.CloseTime() })
				tracker.SetTrustedAndQuorum(trusted, 2)
				a.SetValidationHistorian(tracker)
				for _, node := range trusted {
					require.True(t, tracker.Add(&consensus.Validation{
						LedgerID: consensus.LedgerID(target.hash), LedgerSeq: target.seq,
						NodeID: node, SignTime: target.ledger.CloseTime(), SeenTime: target.ledger.CloseTime(), Full: true,
					}))
				}
				r.onLedgerFullyValidated(target.seq, target.hash)
				r.armConsensusCatchup()
				require.NotNil(t, r.headerDiscovery)
				generation := r.headerDiscovery.generation
				deadline := r.headerDiscovery.deadline
				r.onLedgerBuilt(svc.GetClosedLedgerIndex(), svc.GetClosedLedger().Hash())
				require.NotNil(t, r.headerDiscovery)
				require.Equal(t, generation, r.headerDiscovery.generation)
				require.Equal(t, deadline, r.headerDiscovery.deadline)
				for i := len(links) - 1; i >= 0; i-- {
					link := links[i]
					requests := sender.headerRequests()
					require.NotEmpty(t, requests)
					require.Equal(t, link.hash, requests[len(requests)-1].hash)
					r.handleMessage(&peermanagement.InboundMessage{
						PeerID: 7, Type: message.TypeLedgerData,
						Payload: encodePayload(t, &message.LedgerData{
							LedgerHash: link.hash[:], LedgerSeq: link.seq, InfoType: message.LedgerInfoBase,
							Nodes: []message.LedgerNode{{NodeData: link.response.LedgerHeader}},
						}),
					})
				}
				require.Equal(t, base.Hash(), svc.GetValidatedLedger().Hash(), "headers alone cannot advance validation")
				for _, link := range links {
					r.armConsensusCatchup()
					acquisition := r.fetchTracker.Find(link.hash)
					require.NotNil(t, acquisition, "missing transaction acquisition at %d", link.seq)
					require.True(t, acquisition.TransactionOnly())
					r.handleMessage(&peermanagement.InboundMessage{
						PeerID: 7, Type: message.TypeLedgerData,
						Payload: encodePayload(t, &message.LedgerData{
							LedgerHash: link.hash[:], LedgerSeq: link.seq, InfoType: message.LedgerInfoBase,
							Nodes: []message.LedgerNode{{NodeData: link.response.LedgerHeader}, {NodeData: []byte{1}}},
						}),
					})
					select {
					case <-r.standardReplayDrainWake:
						r.drainStandardReplayPipeline()
					default:
					}
					stored, lookupErr := svc.GetLedgerByHash(link.hash)
					require.NoError(t, lookupErr)
					require.NotNil(t, stored)
					require.Equal(t, link.ledger.Header().AccountHash, stored.Header().AccountHash)
					require.Equal(t, link.ledger.Header().TxHash, stored.Header().TxHash)
					require.Equal(t, link.hash, stored.Hash())
				}
				require.Equal(t, target.hash, svc.GetValidatedLedger().Hash())
				r.checkBehind(target.seq, target.hash, 7)
				require.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
				require.Zero(t, family.reads.Load(), "recovery must avoid full-state discovery reads")
				t.Logf("gap=30 header_requests=%d transaction_requests=%d recovery_elapsed=%s state_reads=%d configured_read_delay=%s",
					len(sender.headerRequests()), len(sender.legacyCalls()), time.Since(started), family.reads.Load(), delay)
			})
		}
	}
}
