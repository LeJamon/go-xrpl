package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func newBlockedAcquisitionCompletion(t *testing.T, reason ...inbound.Reason) (*Router, *service.Service, *inbound.Ledger, *replayPublicationBarrierFamily, func()) {
	t.Helper()
	base := backend.NewMemory()
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	family := &replayPublicationBarrierFamily{Family: base, entered: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(family.resume) })
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig(), SHAMapFamily: family})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	adaptor, _ := newRecordingAdaptor(t, svc)
	router := newTestRouter(nil, adaptor, make(chan *peermanagement.InboundMessage, 1))
	router.SetAcquisitionFamily(family)
	router.SetAcquisitionMemoryLimit(512 << 20)
	store := router.catchupReplay.acquisitionStore
	store.start(t.Context())
	t.Cleanup(store.stopDrain)
	root, rootData, wire := buildSelfHealSourceState(t)
	h := header.LedgerHeader{LedgerIndex: 100, AccountHash: root}
	h.Hash = header.CalculateHash(h)
	newLedger := inbound.NewGeneric
	if len(reason) > 0 && reason[0] == inbound.ReasonConsensus {
		newLedger = inbound.New
	}
	il := newLedger(h.Hash, h.LedgerIndex, 7, serveTestLogger(), inbound.WithFamily(store.scope()), inbound.WithAcquisitionBudget(router.catchupReplay.acquisitionMemory))
	require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: header.AddRaw(h, false)}, {NodeData: rootData}}))
	il.CollectMissingRequest(true)
	require.NoError(t, il.GotStateNodes(wire))
	require.NoError(t, il.CheckpointMemory(t.Context()))
	il.CollectMissingRequest(false)
	require.True(t, il.IsComplete())
	router.catchupReplay.fetchTracker.Track(il)
	family.root = root
	family.blocked.Store(true)
	t.Cleanup(func() {
		resume()
		router.catchupReplay.stopTasks()
		router.StopAcquisitions()
	})
	return router, svc, il, family, resume
}

func TestFullAcquisitionCompletionReleasesRequestMemory(t *testing.T) {
	for _, mode := range []string{"generic", "consensus", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			reason := inbound.ReasonConsensus
			if mode == "generic" {
				reason = inbound.ReasonGeneric
			}
			router, svc, il, _, resume := newBlockedAcquisitionCompletion(t, reason)
			resume()
			c := router.catchupReplay
			if mode == "replacement" {
				parent := svc.GetClosedLedger()
				c.standardReplay = standardReplayPipeline{
					generation: 1, active: true, pivotReady: true,
					anchorSeq: parent.Sequence(), anchorHash: parent.Hash(),
					targetSeq: il.Seq(), targetHash: il.Hash(),
					replacement: &standardReplayReplacement{generation: 1, seq: il.Seq(), hash: il.Hash(), acquisition: il},
				}
			}
			require.Positive(t, c.acquisitionMemory.Snapshot().Used)
			c.completeInboundLedgerReady(il)
			stored, err := svc.GetLedgerByHash(il.Hash())
			require.NoError(t, err)
			require.NotNil(t, stored)
			require.Nil(t, c.fetchTracker.Find(il.Hash()))
			require.Zero(t, c.acquisitionMemory.Snapshot().Used, "completed full-state acquisition leaked its request/cursor reservation")
			require.True(t, c.closeAcquisitionMemoryBudget(), "leaked ownership would prevent a shutdown checkpoint")
			// Releasing acquisition accounting must not destroy the adopted tree.
			snapshot, err := stored.StateMapSnapshot()
			require.NoError(t, err)
			require.Equal(t, shamap.TypeState, snapshot.Type())
			require.NoError(t, svc.VerifyDetachedLedger(t.Context(), stored))
		})
	}
}

func TestAcquisitionCompletionKeepsRouterAndSuccessorCollectionResponsive(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "publish"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			router, svc, il, family, resume := newBlockedAcquisitionCompletion(t)
			c := router.catchupReplay
			control := make(chan *peermanagement.InboundMessage, 1)
			acquisition := make(chan *peermanagement.InboundMessage, 1)
			router.SetConsensusControlInbox(control)
			router.SetAcqInbox(acquisition)
			links := buildStandardReplayTestChain(t, router, svc.GetClosedLedger(), 2)
			armStandardReplayTestPipeline(t, router, router.adaptor, router.adaptor.sender.(*recordingSender), links)
			c.standardReplay.pivotReady = false
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { router.Run(ctx); close(done) }()
			t.Cleanup(func() {
				cancel()
				resume()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("router did not join completion workers")
				}
			})
			select {
			case <-family.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("completion did not reach durable verification")
			}
			require.Eventually(t, func() bool {
				if !c.replayCommitMu.TryLock() {
					return false
				}
				c.replayCommitMu.Unlock()
				return true
			}, time.Second, time.Millisecond, "full-state walk held publication lock")
			for _, link := range links {
				acquisition <- &peermanagement.InboundMessage{PeerID: 7, Type: message.TypeLedgerData,
					Payload: encodePayload(t, &message.LedgerData{LedgerHash: link.hash[:], LedgerSeq: link.seq,
						InfoType: message.LedgerInfoBase, Nodes: []message.LedgerNode{{NodeData: link.response.LedgerHeader}, {NodeData: []byte{1}}}})}
			}
			require.Eventually(t, func() bool { return router.FastSyncMetrics().ReplayPipelineReady == 2 }, 3*time.Second, time.Millisecond,
				"transaction-only completions stalled behind full-state verification")
			parent := svc.GetClosedLedger()
			control <- statusChangeMessage(t, 17, parent.Sequence(), parent.Hash())
			require.Eventually(t, func() bool {
				c.peersMu.RLock()
				defer c.peersMu.RUnlock()
				return c.peerStates[17] != nil && c.peerStates[17].LedgerHash == parent.Hash()
			}, time.Second, time.Millisecond, "router stopped processing control messages")
			if !shutdown {
				resume()
				require.Eventually(t, func() bool { return c.fetchTracker.Find(il.Hash()) == nil }, 3*time.Second, time.Millisecond)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown waited for blocked verification instead of canceling it")
			}
			stored, err := svc.GetLedgerByHash(il.Hash())
			if shutdown {
				require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
			} else {
				require.NoError(t, err)
				require.Equal(t, il.Hash(), stored.Hash())
			}
			require.Equal(t, parent.Hash(), svc.GetClosedLedger().Hash(), "generic completion moved consensus")
		})
	}
}

func TestAcquisitionCompletionRejectsReplacedIdentityAndReleasesSlot(t *testing.T) {
	router, svc, il, family, resume := newBlockedAcquisitionCompletion(t)
	c := router.catchupReplay
	c.startTasks(t.Context())
	c.completeInboundLedgerReady(il)
	select {
	case <-family.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("verification did not start")
	}
	c.completionMu.Lock()
	owner := c.completionTasks[0]
	c.completionMu.Unlock()
	for range 20 {
		c.completeInboundLedgerReady(il)
	}
	c.completionMu.Lock()
	require.Same(t, owner, c.completionTasks[0])
	require.Nil(t, c.completionTasks[1])
	c.completionMu.Unlock()
	resume()
	var result *acquisitionCompletionResult
	select {
	case result = <-c.completionResults:
	case <-time.After(3 * time.Second):
		t.Fatal("verification did not finish")
	}
	require.NoError(t, result.err)
	require.True(t, c.fetchTracker.DiscardExpected(il))
	replacement := inbound.New(il.Hash(), il.Seq(), 8, serveTestLogger())
	c.fetchTracker.Track(replacement)
	c.handleAcquisitionCompletion(result)
	c.stopTasks()
	require.Same(t, replacement, c.fetchTracker.Find(il.Hash()))
	_, err := svc.GetLedgerByHash(il.Hash())
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
	c.completionMu.Lock()
	require.Nil(t, c.completionTasks[0])
	c.completionMu.Unlock()
}
