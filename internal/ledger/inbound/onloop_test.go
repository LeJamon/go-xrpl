package inbound

import (
	"bytes"
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

type notifyingBuffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	wrote chan struct{}
}

func newNotifyingBuffer() *notifyingBuffer {
	return &notifyingBuffer{wrote: make(chan struct{}, 1)}
}

func (b *notifyingBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.data.Write(p)
	b.mu.Unlock()
	select {
	case b.wrote <- struct{}{}:
	default:
	}
	return n, err
}

func (b *notifyingBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

// incompleteStateAcquisition builds an acquisition seeded with a header + state
// root for a tree with several branches, leaving it in StateWantState with
// outstanding missing state nodes — the shape the retry loop operates on.
func incompleteStateAcquisition(t *testing.T) *Ledger {
	t.Helper()
	il, _ := incompleteStateAcquisitionFixture(t)
	return il
}

func incompleteStateAcquisitionFixture(t *testing.T) (*Ledger, []message.LedgerNode) {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for branch := range byte(8) {
		for i := range byte(4) {
			var key [32]byte
			key[0] = (branch << 4) | i
			key[31] = 0xA5
			if err := source.Put(key, make([]byte, 12)); err != nil {
				t.Fatalf("put: %v", err)
			}
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}
	hdrBytes, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash})

	il := New(ledgerHash, 100, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	if il.State() != StateWantState {
		t.Fatalf("acquisition state = %v, want StateWantState", il.State())
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk wire nodes: %v", err)
	}
	nodes := make([]message.LedgerNode, 0, len(wireNodes)-1)
	for _, wire := range wireNodes {
		nodeID, err := shamap.ParseNodeID(wire.NodeID)
		if err != nil {
			t.Fatalf("parse node ID: %v", err)
		}
		if nodeID.IsRoot() {
			continue
		}
		nodes = append(nodes, message.LedgerNode{NodeID: wire.NodeID, NodeData: wire.Data})
	}
	sort.Slice(nodes, func(i, j int) bool {
		left, _ := shamap.ParseNodeID(nodes[i].NodeID)
		right, _ := shamap.ParseNodeID(nodes[j].NodeID)
		return left.Depth() < right.Depth()
	})
	return il, nodes
}

func TestLedger_OnTimer_FailsAfterSevenConsecutiveQuietIntervals(t *testing.T) {
	t.Parallel()
	il := New([32]byte{0xAB}, 42, 1, discardLogger())
	il.state = StateWantState
	base := time.Unix(1_700_000_000, 0)
	il.lastTimer = base

	for i := 1; i <= ledgerTimeoutRetriesMax; i++ {
		now := base.Add(time.Duration(i) * acquireTimerInterval)
		if got := il.OnTimer(now); got != TimerEscalate {
			t.Fatalf("fire %d: got %v, want TimerEscalate", i, got)
		}
		if il.State() == StateFailed {
			t.Fatalf("fire %d failed before the budget was exhausted", i)
		}
	}

	final := base.Add(time.Duration(ledgerTimeoutRetriesMax+1) * acquireTimerInterval)
	if got := il.OnTimer(final); got != TimerFailed {
		t.Fatalf("budget fire: got %v, want TimerFailed", got)
	}
	if il.State() != StateFailed {
		t.Fatalf("state = %v, want StateFailed", il.State())
	}
	if err := il.Err(); err == nil || !strings.Contains(err.Error(), "7 consecutive timeouts") {
		t.Fatalf("terminal error = %v, want seven consecutive timeouts", err)
	}
}

func TestLedger_OnTimer_ReportsIntervalStallWithLifetimeTotals(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	il := New([32]byte{0xAC}, 43, 1, logger)
	il.state = StateWantState
	il.stateRecv = 1365
	il.stateUseful = 1365
	base := time.Unix(1_700_000_000, 0)
	il.lastTimer = base

	if got := il.OnTimer(base.Add(acquireTimerInterval)); got != TimerEscalate {
		t.Fatalf("timer action = %v, want TimerEscalate", got)
	}
	logged := output.String()
	if !strings.Contains(logged, "no progress in current interval") {
		t.Fatalf("interval-progress diagnostic missing from %q", logged)
	}
	for _, lifetimeField := range []string{
		`"state_received_total":1365`,
		`"state_useful_total":1365`,
	} {
		if !strings.Contains(logged, lifetimeField) {
			t.Fatalf("lifetime progress %s missing from %q", lifetimeField, logged)
		}
	}
}

func TestLedger_StateDiscoveryProgressLogIncludesLiveCounters(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	il := New([32]byte{0xAC}, 43, 1, logger)
	il.stateUseful = 1365

	if !il.logStateDiscoveryProgress(time.Now().Add(-time.Minute)) {
		t.Fatal("incomplete state discovery did not request another heartbeat")
	}

	logged := output.String()
	for _, field := range []string{
		`"msg":"inbound ledger: state discovery in progress"`,
		`"nodes_examined":0`,
		`"equal_subtrees_skipped":0`,
		`"missing_nodes_found":0`,
		`"state_nodes_downloaded":1365`,
	} {
		if !strings.Contains(logged, field) {
			t.Fatalf("state discovery progress %s missing from %q", field, logged)
		}
	}

	output.Reset()
	il.haveState = true
	if il.logStateDiscoveryProgress(time.Now()) {
		t.Fatal("completed state discovery requested another heartbeat")
	}
	if output.Len() != 0 {
		t.Fatalf("completed state discovery logged %q", output.String())
	}
}

func TestLedger_StateDiscoveryProgressHeartbeatLifecycle(t *testing.T) {
	waitStopped := func(t *testing.T, stopped <-chan struct{}) {
		t.Helper()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("state discovery heartbeat did not stop")
		}
	}

	t.Run("stop is synchronous and idempotent", func(t *testing.T) {
		output := newNotifyingBuffer()
		ledger := New([32]byte{0xAD}, 44, 1, slog.New(slog.NewJSONHandler(output, nil)))
		ledger.stateUseful = 1365
		ticks := make(chan time.Time, 1)
		stopped := make(chan struct{})
		stop := ledger.startStateDiscoveryProgressWithTicks(
			t.Context(), time.Now().Add(-time.Minute), ticks, func() { close(stopped) },
		)

		ticks <- time.Now()
		select {
		case <-output.wrote:
		case <-time.After(time.Second):
			t.Fatal("state discovery heartbeat was not logged")
		}
		if logged := output.String(); !strings.Contains(logged, `"state_nodes_downloaded":1365`) {
			t.Fatalf("live state discovery counter missing from %q", logged)
		}

		stop()
		stop()
		waitStopped(t, stopped)
		logged := output.String()
		ticks <- time.Now()
		if after := output.String(); after != logged {
			t.Fatalf("heartbeat logged after stop: before %q, after %q", logged, after)
		}
	})

	t.Run("context cancellation stops without another log", func(t *testing.T) {
		output := newNotifyingBuffer()
		ledger := New([32]byte{0xAE}, 45, 1, slog.New(slog.NewJSONHandler(output, nil)))
		ticks := make(chan time.Time, 1)
		stopped := make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		stop := ledger.startStateDiscoveryProgressWithTicks(
			ctx, time.Now(), ticks, func() { close(stopped) },
		)

		cancel()
		waitStopped(t, stopped)
		stop()
		ticks <- time.Now()
		if logged := output.String(); logged != "" {
			t.Fatalf("heartbeat logged after context cancellation: %q", logged)
		}
	})

	t.Run("completed discovery stops on its next tick", func(t *testing.T) {
		output := newNotifyingBuffer()
		ledger := New([32]byte{0xAF}, 46, 1, slog.New(slog.NewJSONHandler(output, nil)))
		ledger.haveState = true
		ticks := make(chan time.Time, 1)
		stopped := make(chan struct{})
		stop := ledger.startStateDiscoveryProgressWithTicks(
			t.Context(), time.Now(), ticks, func() { close(stopped) },
		)

		ticks <- time.Now()
		waitStopped(t, stopped)
		stop()
		if logged := output.String(); logged != "" {
			t.Fatalf("completed state discovery logged a heartbeat: %q", logged)
		}
	})
}

func TestLedger_OnTimer_AlternatingProgressAndQuietPreservesCumulativeTimeouts(t *testing.T) {
	t.Parallel()
	il := New([32]byte{0x01}, 7, 1, discardLogger())
	il.state = StateWantState
	base := time.Unix(1_700_000_000, 0)
	il.lastTimer = base

	now := base
	quietIntervals := ledgerTimeoutRetriesMax + 2
	for i := 1; i <= quietIntervals; i++ {
		now = now.Add(acquireTimerInterval)
		if got := il.OnTimer(now); got != TimerEscalate {
			t.Fatalf("quiet interval %d: got %v, want TimerEscalate", i, got)
		}
		il.RearmTimer(now)

		il.mu.Lock()
		il.markProgressLocked()
		il.mu.Unlock()
		now = now.Add(acquireTimerInterval)
		if got := il.OnTimer(now); got != TimerRefresh {
			t.Fatalf("progress interval %d: got %v, want TimerRefresh", i, got)
		}
		if il.State() == StateFailed {
			t.Fatalf("alternating progress failed after %d cumulative quiet intervals", i)
		}
	}
	if got := il.Timeouts(); got != quietIntervals {
		t.Fatalf("timeouts = %d, want cumulative total %d", got, quietIntervals)
	}
}

func TestLedger_OnTimer_UsefulProgressAfterRepeatedStallsCompletes(t *testing.T) {
	t.Parallel()
	il, nodes := incompleteStateAcquisitionFixture(t)
	progressIntervals := ledgerTimeoutRetriesMax + 2
	if len(nodes) <= progressIntervals {
		t.Fatalf("fixture has %d nodes, need more than %d", len(nodes), progressIntervals)
	}

	base := time.Unix(1_700_000_000, 0)
	il.mu.Lock()
	il.lastTimer = base
	il.mu.Unlock()
	now := base.Add(acquireTimerInterval)
	if got := il.OnTimer(now); got != TimerRefresh {
		t.Fatalf("base progress: got %v, want TimerRefresh", got)
	}

	for i := range progressIntervals {
		now = now.Add(acquireTimerInterval)
		if got := il.OnTimer(now); got != TimerEscalate {
			t.Fatalf("quiet interval %d: got %v, want TimerEscalate", i+1, got)
		}
		il.RearmTimer(now)

		useful, err := il.GotStateNodesUseful([]message.LedgerNode{nodes[i]})
		if err != nil {
			t.Fatalf("useful interval %d: %v", i+1, err)
		}
		if useful != 1 {
			t.Fatalf("useful interval %d added %d nodes, want 1", i+1, useful)
		}
		now = now.Add(acquireTimerInterval)
		if got := il.OnTimer(now); got != TimerRefresh {
			t.Fatalf("progress interval %d: got %v, want TimerRefresh", i+1, got)
		}
	}

	if err := il.GotStateNodes(nodes[progressIntervals:]); err != nil {
		t.Fatalf("complete state tree: %v", err)
	}
	il.CollectMissingRequest(false)
	if il.State() != StateComplete {
		t.Fatalf("state = %v, want StateComplete", il.State())
	}
}

func TestLedger_OnTimer_ProgressResetsOnlyConsecutiveFailureBudget(t *testing.T) {
	t.Parallel()
	il := New([32]byte{0xAD}, 44, 1, discardLogger())
	il.state = StateWantState
	now := time.Unix(1_700_000_000, 0)
	il.lastTimer = now

	fireQuiet := func(count int) {
		t.Helper()
		for range count {
			now = now.Add(acquireTimerInterval)
			if got := il.OnTimer(now); got != TimerEscalate {
				t.Fatalf("quiet timeout %d: got %v, want TimerEscalate", il.Timeouts(), got)
			}
			il.RearmTimer(now)
		}
	}

	fireQuiet(ledgerTimeoutRetriesMax)
	il.mu.Lock()
	il.markProgressLocked()
	il.mu.Unlock()
	now = now.Add(acquireTimerInterval)
	if got := il.OnTimer(now); got != TimerRefresh {
		t.Fatalf("progress reset: got %v, want TimerRefresh", got)
	}
	fireQuiet(ledgerTimeoutRetriesMax)

	now = now.Add(acquireTimerInterval)
	if got := il.OnTimer(now); got != TimerFailed {
		t.Fatalf("seventh consecutive timeout: got %v, want TimerFailed", got)
	}
	if got := il.Timeouts(); got != 2*ledgerTimeoutRetriesMax+1 {
		t.Fatalf("cumulative timeouts = %d, want %d", got, 2*ledgerTimeoutRetriesMax+1)
	}
}

func TestLedger_OnTimer_BaseReplyCountsAsProgress(t *testing.T) {
	t.Parallel()
	il := incompleteStateAcquisition(t)
	base := time.Unix(1_700_000_000, 0)
	il.mu.Lock()
	il.lastTimer = base
	il.mu.Unlock()

	if got := il.OnTimer(base.Add(acquireTimerInterval)); got != TimerRefresh {
		t.Fatalf("timer action = %v, want TimerRefresh after useful base reply", got)
	}
	if got := il.Timeouts(); got != 0 {
		t.Fatalf("timeouts = %d, want 0 after useful base reply", got)
	}
}

// TestLedger_OnTimer_NotDueIsNoop confirms OnTimer is a no-op before the timer
// interval elapses, so polling it from the maintenance tick is cheap.
func TestLedger_OnTimer_NotDueIsNoop(t *testing.T) {
	t.Parallel()
	il := New([32]byte{0x02}, 7, 1, discardLogger())
	il.state = StateWantState
	base := time.Unix(1_700_000_000, 0)
	il.lastTimer = base
	if got := il.OnTimer(base.Add(acquireTimerInterval / 2)); got != TimerNone {
		t.Fatalf("sub-interval fire: got %v, want TimerNone", got)
	}
	if il.Timeouts() != 0 {
		t.Fatalf("a not-due fire must not count a timeout, got %d", il.Timeouts())
	}
}

func TestLedger_OnTimer_EscalationStartsNextInterval(t *testing.T) {
	t.Parallel()
	il := New([32]byte{0x04}, 7, 1, discardLogger())
	il.state = StateWantState
	base := time.Unix(1_700_000_000, 0)
	il.lastTimer = base

	first := base.Add(acquireTimerInterval)
	if got := il.OnTimer(first); got != TimerEscalate {
		t.Fatalf("first fire: got %v, want TimerEscalate", got)
	}
	if got := il.OnTimer(first.Add(100 * time.Millisecond)); got != TimerNone {
		t.Fatalf("early retry: got %v, want TimerNone", got)
	}
	if got := il.Timeouts(); got != 1 {
		t.Fatalf("timeouts after early retry = %d, want 1", got)
	}
	if got := il.OnTimer(first.Add(acquireTimerInterval)); got != TimerEscalate {
		t.Fatalf("next interval: got %v, want TimerEscalate", got)
	}
}

func TestLedger_RearmTimerStartsIntervalAfterEscalation(t *testing.T) {
	t.Parallel()
	il := New([32]byte{0x03}, 7, 1, discardLogger())
	il.state = StateWantState
	base := time.Unix(1_700_000_000, 0)
	il.lastTimer = base

	if got := il.OnTimer(base.Add(acquireTimerInterval)); got != TimerEscalate {
		t.Fatalf("first fire: got %v, want TimerEscalate", got)
	}
	finished := base.Add(time.Minute)
	il.RearmTimer(finished)
	if got := il.OnTimer(finished.Add(acquireTimerInterval - time.Millisecond)); got != TimerNone {
		t.Fatalf("pre-deadline fire: got %v, want TimerNone", got)
	}
	if got := il.OnTimer(finished.Add(acquireTimerInterval)); got != TimerEscalate {
		t.Fatalf("rearmed fire: got %v, want TimerEscalate", got)
	}
}

// TestLedger_AddPeer_DedupsAndCaps pins the broadened source-peer set: the
// original peer is the primary, duplicates are rejected, and the set is bounded.
func TestLedger_AddPeer_DedupsAndCaps(t *testing.T) {
	t.Parallel()
	il := New([32]byte{}, 1, 7, discardLogger())
	if il.PeerID() != 7 {
		t.Fatalf("PeerID() = %d, want the original peer 7", il.PeerID())
	}
	if !il.AddPeer(8) {
		t.Fatal("a fresh peer must be added")
	}
	if il.AddPeer(7) || il.AddPeer(8) {
		t.Fatal("duplicate peers must not be added")
	}
	for i := uint64(9); i <= 20; i++ {
		if !il.AddPeer(i) {
			t.Fatalf("fresh peer %d must be added", i)
		}
	}
	if got := len(il.Peers()); got != 14 {
		t.Fatalf("peer set size = %d, want 14", got)
	}
}

// TestLedger_TakeByHashRequest_AggressiveGate confirms the by-hash escalation
// only arms once the acquisition has gone aggressive, returns the outstanding
// content hashes, and consumes the latch until the next no-progress tick.
func TestLedger_TakeByHashRequest_AggressiveGate(t *testing.T) {
	t.Parallel()
	il := incompleteStateAcquisition(t)

	// Armed (byHash) but not yet aggressive: no by-hash request.
	il.mu.Lock()
	il.byHash = true
	il.timeouts = ledgerBecomeAggressiveThreshold
	il.mu.Unlock()
	if state, tx := il.TakeByHashRequest(16); state != nil || tx != nil {
		t.Fatalf("by-hash must not arm at the threshold, got state=%d tx=%d", len(state), len(tx))
	}

	// Past the threshold: it returns the outstanding state hashes and consumes
	// the latch so it fires once per no-progress interval.
	il.mu.Lock()
	il.byHash = true
	il.timeouts = ledgerBecomeAggressiveThreshold + 1
	il.mu.Unlock()
	state, _ := il.TakeByHashRequest(16)
	if len(state) == 0 {
		t.Fatal("aggressive by-hash request must return outstanding state node hashes")
	}
	if again, _ := il.TakeByHashRequest(16); again != nil {
		t.Fatal("the by-hash latch must be consumed until the next no-progress tick")
	}
}
