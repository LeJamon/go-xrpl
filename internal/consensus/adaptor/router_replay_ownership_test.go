package adaptor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

// A completed fetch is removed from the tracker before its replay is published.
// Recovery must continue to see its owner throughout that interval.
func TestReplayOwnershipSurvivesTransactionHandoff(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	c := r.catchupReplay
	parent := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, parent, 2)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	c.standardReplay.pivotReady = false
	completeStandardReplayTestLink(t, r, links[0])
	link := links[0]
	require.Nil(t, c.fetchTracker.Find(link.hash))
	entry := c.standardReplay.entries[link.seq]
	require.NotNil(t, entry)
	require.False(t, entry.readyAt.IsZero())

	require.True(t, c.isAcquiring(link.hash), "prepared replay lost ownership when its fetch completed")
	sender.mu.Lock()
	sender.peerSupportsReplay = true
	sender.mu.Unlock()
	before := len(sender.legacyCalls())
	require.True(t, c.startLedgerAcquisitionFromParent(link.seq, link.hash, 7, parent))
	require.Empty(t, sender.replayCalls(), "recovery duplicated a prepared replay")
	require.Len(t, sender.legacyCalls(), before)

	c.acquisitionMu.Lock()
	require.Error(t, c.startReplayDeltaAcquisition(link.seq, link.hash, 7, parent))
	il, created := c.startLedgerReplayAcquisitionLegacyLocked(link.seq, link.hash, 7)
	c.acquisitionMu.Unlock()
	require.False(t, created)
	require.Nil(t, il, "legacy fallback duplicated a prepared replay")
	require.False(t, c.replayer.Has(link.hash))
}

type legacyReplayProofFamily struct {
	replayPublicationBarrierFamily
	reads atomic.Uint64
}

func (f *legacyReplayProofFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.reads.Add(1)
	return f.Family.Fetch(ctx, hash)
}

func TestLegacyReplayReusesVerifiedParentProof(t *testing.T) {
	for _, delta := range []bool{false, true} {
		name := "standard"
		if delta {
			name = "delta"
		}
		t.Run(name, func(t *testing.T) {
			base := backend.NewMemory()
			t.Cleanup(func() { require.NoError(t, base.Close()) })
			family := &legacyReplayProofFamily{replayPublicationBarrierFamily: replayPublicationBarrierFamily{Family: base}}
			svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig(), SHAMapFamily: family})
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)
			a, _ := newRecordingAdaptor(t, svc)
			r := newTestRouter(nil, a, nil)
			state, err := shamap.NewBacked(shamap.TypeState, family)
			require.NoError(t, err)
			for i := range 4096 {
				key := [32]byte{byte(i), byte(i >> 8), 1}
				require.NoError(t, state.PutWithNodeType(key, []byte("untouched state"), shamap.NodeTypeAccountState))
			}
			root, err := state.Hash()
			require.NoError(t, err)
			h := header.LedgerHeader{LedgerIndex: 100, AccountHash: root, Drops: svc.GetClosedLedger().TotalDrops()}
			h.Hash = header.CalculateHash(h)
			prepared, err := svc.PrepareAcquiredLedger(t.Context(), &h, state, nil)
			require.NoError(t, err)
			defer prepared.Release()
			require.NoError(t, svc.StorePreparedLedger(t.Context(), prepared))
			parent, err := svc.GetLedgerByHash(h.Hash)
			require.NoError(t, err)
			response, child, hash, seq := buildSuccessorAgainstParent(t, parent)
			family.reads.Store(0)
			if delta {
				require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(seq, hash, 7, parent))
				r.handleReplayDeltaResponse(&peermanagement.InboundMessage{PeerID: 7, Type: message.TypeReplayDeltaResponse, Payload: encodePayload(t, response)})
			} else {
				childHeader := child.Header()
				il := inbound.New(hash, seq, 7, serveTestLogger(), inbound.WithTransactionOnly())
				require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: header.AddRaw(childHeader, false)}, {NodeData: []byte{1}}}))
				r.catchupReplay.fetchTracker.Track(il)
				r.catchupReplay.completeInboundLedgerReady(il)
			}
			stored, err := svc.GetLedgerByHash(hash)
			require.NoError(t, err)
			require.Equal(t, hash, stored.Hash())
			require.Less(t, family.reads.Load(), uint64(200), "legacy replay repeated the 4,096-leaf parent walk")
			require.False(t, svc.ReplayBlocked())
		})
	}
}

func TestReplayOwnershipDiscardsDuplicateCompletion(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	c := r.catchupReplay
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 2)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	c.standardReplay.pivotReady = false
	completeStandardReplayTestLink(t, r, links[0])
	link := links[0]
	entry := c.standardReplay.entries[link.seq]
	readyAt := entry.readyAt
	duplicate := inbound.New(link.hash, link.seq, 8, serveTestLogger(), inbound.WithTransactionOnly())
	h := link.ledger.Header()
	c.acquisitionMu.Lock()
	handled, start, retain := c.completeStandardReplayPipelineEntryLocked(duplicate, &h, nil, 8)
	c.acquisitionMu.Unlock()
	require.True(t, handled, "duplicate completion would enter synchronous legacy replay")
	require.False(t, start)
	require.False(t, retain)
	require.Equal(t, readyAt, entry.readyAt)
	require.Equal(t, uint64(1), r.FastSyncMetrics().ReplayPipelineReady)

	// Ownership also survives local-memory or peer-availability waits; the
	// pipeline's bounded retry scheduler remains responsible for this entry.
	entry.readyAt = time.Time{}
	entry.availabilityPending = true
	require.True(t, c.isAcquiring(link.hash))
}

type coldLegacyReplayFamily struct {
	replayPublicationBarrierFamily
	epoch atomic.Uint32
}

func (f *coldLegacyReplayFamily) AcquireDurableSnapshot(context.Context) ([32]byte, func(), error) {
	return [32]byte{byte(f.epoch.Load())}, func() {}, nil
}

func (f *coldLegacyReplayFamily) DurableFingerprint(context.Context) ([32]byte, error) {
	return [32]byte{byte(f.epoch.Load())}, nil
}

// A generation change invalidates the cached parent certificate. Verify that
// both legacy protocols keep control traffic responsive during the resulting
// cold read, and cancel or reject stale work without publishing its child.
func TestLegacyReplayColdParentDoesNotBlockRouter(t *testing.T) {
	for _, protocol := range []string{"standard", "delta"} {
		for _, outcome := range []string{"publish", "shutdown", "replaced"} {
			t.Run(protocol+"/"+outcome, func(t *testing.T) {
				base := backend.NewMemory()
				t.Cleanup(func() { require.NoError(t, base.Close()) })
				family := &coldLegacyReplayFamily{replayPublicationBarrierFamily: replayPublicationBarrierFamily{
					Family: base, entered: make(chan struct{}), resume: make(chan struct{}),
				}}
				family.epoch.Store(1)
				resume := sync.OnceFunc(func() { close(family.resume) })
				svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig(), SHAMapFamily: family})
				require.NoError(t, err)
				require.NoError(t, svc.Start())
				t.Cleanup(svc.Stop)
				a, _ := newRecordingAdaptor(t, svc)
				inbox := make(chan *peermanagement.InboundMessage, 4)
				router := newTestRouter(nil, a, inbox)
				c := router.catchupReplay
				state, err := shamap.NewBacked(shamap.TypeState, family)
				require.NoError(t, err)
				for i := range 128 {
					require.NoError(t, state.PutWithNodeType([32]byte{byte(i), 1}, []byte("parent state"), shamap.NodeTypeAccountState))
				}
				root, err := state.Hash()
				require.NoError(t, err)
				h := header.LedgerHeader{LedgerIndex: 100, AccountHash: root, Drops: svc.GetClosedLedger().TotalDrops()}
				h.Hash = header.CalculateHash(h)
				prepared, err := svc.PrepareAcquiredLedger(t.Context(), &h, state, nil)
				require.NoError(t, err)
				require.NoError(t, svc.StorePreparedLedger(t.Context(), prepared))
				prepared.Release()
				parent, err := svc.GetLedgerByHash(h.Hash)
				require.NoError(t, err)
				response, child, hash, seq := buildSuccessorAgainstParent(t, parent)
				family.root = root
				family.epoch.Store(2)
				family.blocked.Store(true)
				var il *inbound.Ledger
				if protocol == "standard" {
					il = inbound.New(hash, seq, 7, serveTestLogger(), inbound.WithTransactionOnly())
					require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: header.AddRaw(child.Header(), false)}, {NodeData: []byte{1}}}))
					c.fetchTracker.Track(il)
				} else {
					require.NoError(t, c.startReplayDeltaAcquisition(seq, hash, 7, parent))
					inbox <- &peermanagement.InboundMessage{PeerID: 7, Type: message.TypeReplayDeltaResponse, Payload: encodePayload(t, response)}
				}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() { router.Run(ctx); close(done) }()
				t.Cleanup(func() {
					cancel()
					resume()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("router did not join legacy completion")
					}
					router.StopAcquisitions()
				})
				select {
				case <-family.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("cold parent verification never started")
				}
				// This message must be processed while the parent read is blocked.
				closed := svc.GetClosedLedger()
				inbox <- statusChangeMessage(t, 17, closed.Sequence(), closed.Hash())
				require.Eventually(t, func() bool {
					c.peersMu.RLock()
					defer c.peersMu.RUnlock()
					return c.peerStates[17] != nil
				}, time.Second, time.Millisecond, "cold legacy replay blocked Router.Run")
				require.True(t, c.isAcquiring(hash), "worker lost acquisition ownership")
				if outcome == "replaced" {
					if il != nil {
						require.True(t, c.fetchTracker.DiscardExpected(il))
						c.fetchTracker.Track(inbound.New(hash, seq, 8, serveTestLogger(), inbound.WithTransactionOnly()))
					} else {
						c.replayer.Abandon(hash)
						_, err := c.replayer.Acquire(hash, 8, parent)
						require.NoError(t, err)
					}
				}
				if outcome != "shutdown" {
					resume()
					if outcome == "publish" {
						require.Eventually(t, func() bool { stored, err := svc.GetLedgerByHash(hash); return err == nil && stored != nil }, 3*time.Second, time.Millisecond)
					} else {
						require.Eventually(t, func() bool {
							c.completionMu.Lock()
							defer c.completionMu.Unlock()
							return c.completionTasks[1] == nil && c.legacyDeltaTask == nil
						}, 3*time.Second, time.Millisecond)
					}
				}
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("shutdown did not cancel the cold parent read")
				}
				if outcome != "publish" {
					_, err := svc.GetLedgerByHash(hash)
					require.Error(t, err, "canceled or replaced work published a child")
				}
				require.False(t, svc.ReplayBlocked(), "cancellation was recorded as an execution fault")
			})
		}
	}
}
