package adaptor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type replayPublicationBarrierFamily struct {
	shamap.Family
	root    [32]byte
	blocked atomic.Bool
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (f *replayPublicationBarrierFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if f.blocked.Load() && hash == f.root {
		f.once.Do(func() { close(f.entered) })
		select {
		case <-f.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.Family.Fetch(ctx, hash)
}

func (*replayPublicationBarrierFamily) AcquireDurableSnapshot(context.Context) ([32]byte, func(), error) {
	return [32]byte{1}, func() {}, nil
}

func (*replayPublicationBarrierFamily) DurableFingerprint(context.Context) ([32]byte, error) {
	return [32]byte{1}, nil
}

func TestRouterReplayPublicationKeepsMessageLanesResponsive(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "publish"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			base := backend.NewMemory()
			t.Cleanup(func() { require.NoError(t, base.Close()) })
			family := &replayPublicationBarrierFamily{
				Family: base, entered: make(chan struct{}), resume: make(chan struct{}),
			}
			resume := sync.OnceFunc(func() { close(family.resume) })
			t.Cleanup(resume)
			svc, err := service.New(service.Config{
				Standalone: true, GenesisConfig: genesis.DefaultConfig(), SHAMapFamily: family,
			})
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)
			adaptor, sender := newRecordingAdaptor(t, svc)
			inbox := make(chan *peermanagement.InboundMessage, 1)
			control := make(chan *peermanagement.InboundMessage, 1)
			acquisition := make(chan *peermanagement.InboundMessage, 1)
			router := newTestRouter(nil, adaptor, inbox)
			router.SetConsensusControlInbox(control)
			router.SetAcqInbox(acquisition)
			router.SetAcquisitionMemoryLimit(2 << 30)
			c := router.catchupReplay
			links := buildStandardReplayTestChain(t, router, svc.GetClosedLedger(), 2)
			armStandardReplayTestPipeline(t, router, adaptor, sender, links)
			c.standardReplay.pivotReady = false
			completeStandardReplayTestLink(t, router, links[0])
			family.root = links[0].ledger.Header().AccountHash
			require.NotEqual(t, svc.GetClosedLedger().Header().AccountHash, family.root)
			family.blocked.Store(true)
			c.standardReplay.pivotReady = true
			c.scheduleStandardReplayDrain()

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { router.Run(ctx); close(done) }()
			t.Cleanup(func() {
				cancel()
				resume()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("router did not stop after replay cancellation")
				}
				router.StopAcquisitions()
			})
			select {
			case <-family.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("replay did not reach post-apply durable state verification")
			}
			require.True(t, c.replayCommitMu.TryLock(), "cold verification held the replay publication mutex")
			c.replayCommitMu.Unlock()

			tail := links[1]
			acquisition <- &peermanagement.InboundMessage{
				PeerID: 7, Type: message.TypeLedgerData,
				Payload: encodePayload(t, &message.LedgerData{
					LedgerHash: tail.hash[:], LedgerSeq: tail.seq, InfoType: message.LedgerInfoBase,
					Nodes: []message.LedgerNode{{NodeData: tail.response.LedgerHeader}, {NodeData: []byte{1}}},
				}),
			}
			require.Eventually(t, func() bool {
				return router.FastSyncMetrics().ReplayPipelineReady == 2
			}, 3*time.Second, time.Millisecond, "acquisition completion stalled behind replay verification")

			parent := svc.GetClosedLedger()
			for id := peermanagement.PeerID(10); id < 18; id++ {
				select {
				case control <- statusChangeMessage(t, id, parent.Sequence(), parent.Hash()):
				case <-time.After(time.Second):
					t.Fatal("control lane stopped draining behind replay verification")
				}
			}
			require.Eventually(t, func() bool {
				c.peersMu.RLock()
				defer c.peersMu.RUnlock()
				for id := peermanagement.PeerID(10); id < 18; id++ {
					if state := c.peerStates[id]; state == nil || state.LedgerHash != parent.Hash() {
						return false
					}
				}
				return true
			}, time.Second, time.Millisecond)
			router.queuePeerDisconnect(17)
			require.Eventually(t, func() bool {
				c.peersMu.RLock()
				defer c.peersMu.RUnlock()
				return c.peerStates[17] == nil
			}, time.Second, time.Millisecond, "peer lifecycle cleanup stalled")

			if !shutdown {
				resume()
				require.Eventually(t, func() bool {
					return router.FastSyncMetrics().ReplayPipelineApplied == 2
				}, 3*time.Second, time.Millisecond)
				stored, err := svc.GetLedgerByHash(tail.hash)
				require.NoError(t, err)
				require.NotNil(t, stored)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown waited for the blocked durable reader instead of canceling it")
			}
			require.False(t, svc.ReplayBlocked())
			if shutdown {
				stored, err := svc.GetLedgerByHash(links[0].hash)
				require.NoError(t, err)
				require.Nil(t, stored, "canceled replay published its prepared ledger")
			}
		})
	}
}
