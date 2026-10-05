package adaptor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestAcquisitionDiscoveryContinuesAfterPartialRequests(t *testing.T) {
	lane := newAcquisitionWorkLane(2)
	ledger := inbound.New([32]byte{1}, 1, 1, serveTestLogger())
	other := inbound.New([32]byte{2}, 2, 2, serveTestLogger())
	resumed := make(chan []acquisitionWorkEvent, 1)
	calls := 0
	lane.process = func(_ context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		if current != ledger {
			return acquisitionWorkResult{ledger: current}
		}
		calls++
		if calls == 1 {
			return acquisitionWorkResult{
				ledger:           current,
				continueRequests: true,
				requests: []inbound.MissingRequest{{
					PeerID: 1, NodeIDs: [][]byte{{1}},
				}},
			}
		}
		resumed <- append([]acquisitionWorkEvent(nil), events...)
		return acquisitionWorkResult{ledger: current}
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkTimer}))
	first := <-lane.results()
	require.Len(t, first.requests, 1)
	require.True(t, first.yielded)
	require.True(t, lane.submit(other, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	reply := &message.LedgerData{InfoType: message.LedgerInfoAsNode}
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkData, data: reply}))
	close(first.ack)

	second := <-lane.results()
	require.Same(t, other, second.ledger, "discovery must yield to another ledger")
	close(second.ack)
	third := <-lane.results()
	require.Same(t, ledger, third.ledger)
	require.Empty(t, third.requests, "the earlier request must not be emitted again")
	close(third.ack)
	events := <-resumed
	require.Len(t, events, 2)
	require.Equal(t, acquisitionWorkData, events[0].kind)
	require.Same(t, reply, events[0].data)
	require.Equal(t, acquisitionWorkContinue, events[1].kind)
	require.Nil(t, events[1].data)
	require.Eventually(t, func() bool { return !lane.has(ledger) }, time.Second, time.Millisecond)
}

func TestAcquisitionDiscoveryYieldPreservesPeerControl(t *testing.T) {
	lane := newAcquisitionWorkLane(1)
	ledger := inbound.New([32]byte{4}, 4, 4, serveTestLogger())
	resumed := make(chan []acquisitionWorkEvent, 1)
	calls := 0
	lane.process = func(_ context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		calls++
		if calls == 1 {
			return acquisitionWorkResult{ledger: current, continueRequests: true}
		}
		resumed <- append([]acquisitionWorkEvent(nil), events...)
		return acquisitionWorkResult{ledger: current}
	}
	lane.ctx = t.Context()
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkData}))
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkAdded, peerID: 5}))
	retryIDs := [][]byte{{6}}
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{
		kind: acquisitionWorkRetarget, peers: []uint64{6}, stateIDs: retryIDs,
	}))
	lane.start(t.Context())
	t.Cleanup(lane.stop)
	first := <-lane.results()
	require.True(t, first.yielded)
	require.Empty(t, first.requests)
	close(first.ack)
	second := <-lane.results()
	close(second.ack)
	events := <-resumed
	require.Len(t, events, 3)
	require.Equal(t, acquisitionWorkAdded, events[0].kind)
	require.Equal(t, uint64(5), events[0].peerID)
	require.Equal(t, acquisitionWorkRetarget, events[1].kind)
	require.Equal(t, retryIDs, events[1].stateIDs)
	require.Equal(t, acquisitionWorkContinue, events[2].kind)
}

func TestAcquisitionDiscoveryWaitResumesAfterCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome inbound.MissingRequestOutcome
		held    uint64
	}{
		{"persistence_without_reclaimed_bytes", inbound.MissingRequestWaitPersistence, 0},
		{"memory_with_partial_requests", inbound.MissingRequestWaitMemory, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget := shamap.NewAcquisitionBudget(8 << 20)
			family := &releasingCheckpointFamily{Family: backend.NewMemory(), budget: budget, held: tc.held}
			require.True(t, budget.TryReserve(tc.held))
			ledger := inbound.New([32]byte{8}, 8, 8, serveTestLogger(), inbound.WithFamily(family), inbound.WithAcquisitionBudget(budget))
			lane := newAcquisitionWorkLane(1)
			resumed := make(chan []acquisitionWorkEvent, 1)
			calls := 0
			lane.process = func(_ context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
				calls++
				if calls == 1 {
					return acquisitionWorkResult{ledger: current, memoryWait: true, requestWait: tc.outcome,
						requests: []inbound.MissingRequest{{PeerID: 8, NodeIDs: [][]byte{{1}}}}}
				}
				resumed <- append([]acquisitionWorkEvent(nil), events...)
				return acquisitionWorkResult{ledger: current}
			}
			lane.start(t.Context())
			t.Cleanup(lane.stop)
			require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkContinue}))
			first := <-lane.results()
			require.NoError(t, first.persistenceErr)
			require.True(t, first.yielded, "a completed checkpoint must resume eligible discovery")
			require.Len(t, first.requests, 1)
			close(first.ack)
			second := <-lane.results()
			require.NoError(t, second.err)
			require.Empty(t, second.requests, "earlier requests must not be emitted again")
			close(second.ack)
			events := <-resumed
			require.Len(t, events, 1)
			require.Equal(t, acquisitionWorkContinue, events[0].kind)
			require.Eventually(t, func() bool { return !lane.has(ledger) }, time.Second, time.Millisecond)
		})
	}
}

func TestAcquisitionReplyResumesAfterAutomaticCheckpoint(t *testing.T) {
	budget := shamap.NewAcquisitionBudget(8 << 20)
	family := &releasingCheckpointFamily{Family: backend.NewMemory(), budget: budget, held: 1024}
	require.True(t, budget.TryReserve(family.held))
	ledger := inbound.New([32]byte{9}, 9, 9, serveTestLogger(),
		inbound.WithFamily(family), inbound.WithAcquisitionBudget(budget))
	require.NoError(t, ledger.CheckpointPersistence(t.Context(), 32*1024-1))
	require.Equal(t, uint64(1024), budget.Snapshot().Used)
	lane := newAcquisitionWorkLane(1)
	calls := 0
	lane.process = func(_ context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		calls++
		if calls == 1 {
			events[0].consumed = 1
			return acquisitionWorkResult{ledger: current, pendingReply: true, err: shamap.ErrAcquisitionMemory,
				replies: []acquisitionReplyStat{{useful: 1}}}
		}
		return acquisitionWorkResult{ledger: current}
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkData}))
	first := <-lane.results()
	require.NoError(t, first.persistenceErr)
	require.True(t, first.yielded, "credits reclaimed by the automatic checkpoint must wake the remaining reply")
	require.Zero(t, budget.Snapshot().Used)
	close(first.ack)
	select {
	case next := <-lane.results():
		require.NoError(t, next.err)
		close(next.ack)
	case <-time.After(time.Second):
		t.Fatal("remaining reply was parked despite reclaimed memory")
	}
}

type failingAcquisitionWriteFamily struct {
	shamap.Family
	err error
}

func (f *failingAcquisitionWriteFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	if f.err != nil {
		return f.err
	}
	return f.Family.StoreBatch(ctx, entries)
}

func TestAcquisitionWriteFailureIsLocalFailure(t *testing.T) {
	for _, baseReply := range []bool{false, true} {
		name := "state"
		if baseReply {
			name = "base"
		}
		t.Run(name, func(t *testing.T) {
			source := newWideWorkSource(t, 1)
			fixture, base := newWantBaseWorkLedger(t, source, []uint64{7})
			family := &failingAcquisitionWriteFamily{Family: backend.NewMemory()}
			ledger := inbound.New(fixture.Hash(), fixture.Seq(), 7, serveTestLogger(), inbound.WithFamily(family))
			data := &message.LedgerData{InfoType: message.LedgerInfoBase, Nodes: base}
			if !baseReply {
				require.NoError(t, ledger.GotBase(base))
				wire, err := source.WalkWireNodes()
				require.NoError(t, err)
				data = &message.LedgerData{InfoType: message.LedgerInfoAsNode}
				for _, node := range wire {
					if node.NodeID[32] == 1 {
						data.Nodes = append(data.Nodes, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
					}
				}
				require.NotEmpty(t, data.Nodes)
			}
			family.err = errors.New("write failed")
			result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
				kind: acquisitionWorkData, peerID: 7, data: data,
			}})
			require.True(t, result.remove)
			require.ErrorIs(t, result.persistenceErr, family.err)
			require.ErrorIs(t, result.persistenceErr, inbound.ErrPersistence)
			require.Equal(t, standardReplayFailurePersistence, result.failureClass)
			require.Empty(t, result.badData)
			require.False(t, result.complete)
			require.False(t, ledger.IsComplete())
		})
	}
}

func TestAcquisitionRetainsPressuredReplyUntilWake(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		name := "reply"
		if cancel {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			lane := newAcquisitionWorkLane(1)
			ledger := inbound.New([32]byte{3}, 3, 3, serveTestLogger())
			owner := &peermanagement.InboundMessage{}
			data := &message.LedgerData{InfoType: message.LedgerInfoAsNode}
			resumed := make(chan []acquisitionWorkEvent, 1)
			calls := 0
			lane.process = func(ctx context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
				calls++
				if calls == 1 {
					events[0].consumed = 7
					return acquisitionWorkResult{ledger: current, memoryWait: true, pendingReply: true}
				}
				resumed <- append([]acquisitionWorkEvent(nil), events...)
				return acquisitionWorkResult{ledger: current, err: ctx.Err()}
			}
			lane.start(t.Context())
			t.Cleanup(lane.stop)
			require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkData, data: data, owner: owner}))
			first := <-lane.results()
			require.True(t, first.memoryWait)
			require.False(t, first.yielded)
			close(first.ack)
			require.Eventually(t, func() bool {
				lane.mu.Lock()
				defer lane.mu.Unlock()
				batch := lane.pending[ledger]
				return batch != nil && batch.waiting
			}, time.Second, time.Millisecond)
			if cancel {
				require.True(t, lane.cancelLedger(ledger))
			} else {
				require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkTimerCheck, at: time.Now()}))
			}
			next := <-lane.results()
			if cancel {
				require.ErrorIs(t, next.err, context.Canceled)
			} else {
				require.NoError(t, next.err)
			}
			close(next.ack)
			events := <-resumed
			require.Equal(t, 7, events[0].consumed)
			require.Same(t, data, events[0].data)
			require.Same(t, owner, events[0].owner)
			require.Eventually(t, func() bool { return !lane.has(ledger) }, time.Second, time.Millisecond)
		})
	}
}
