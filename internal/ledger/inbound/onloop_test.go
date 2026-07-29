package inbound

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

// incompleteStateAcquisition builds an acquisition seeded with a header + state
// root for a tree with several branches, leaving it in StateWantState with
// outstanding missing state nodes — the shape the retry loop operates on.
func incompleteStateAcquisition(t *testing.T) *Ledger {
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
	return il
}

// TestLedger_OnTimer_FailsCleanlyAfterBudget pins the core of issue #985: a
// non-progressing acquisition escalates for ledgerTimeoutRetriesMax no-progress
// intervals, then fails cleanly instead of re-arming the same stall forever.
func TestLedger_OnTimer_FailsCleanlyAfterBudget(t *testing.T) {
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
	if il.Err() == nil {
		t.Fatal("a failed acquisition must carry a terminal error")
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

// TestLedger_OnTimer_ProgressPreservesCumulativeFailureBudget confirms useful
// intervals do not count as timeouts but also do not erase prior no-progress
// strikes, matching rippled's TimeoutCounter.
func TestLedger_OnTimer_ProgressPreservesCumulativeFailureBudget(t *testing.T) {
	t.Parallel()
	il := New([32]byte{0x01}, 7, 1, discardLogger())
	il.state = StateWantState
	base := time.Unix(1_700_000_000, 0)
	il.lastTimer = base

	for i := 1; i <= 5; i++ {
		il.OnTimer(base.Add(time.Duration(i) * acquireTimerInterval))
	}
	if il.Timeouts() != 5 {
		t.Fatalf("timeouts = %d, want 5 after five no-progress fires", il.Timeouts())
	}

	il.mu.Lock()
	il.markProgressLocked()
	il.mu.Unlock()
	if got := il.OnTimer(base.Add(6 * acquireTimerInterval)); got != TimerRefresh {
		t.Fatalf("progress fire: got %v, want TimerRefresh", got)
	}
	if il.Timeouts() != 5 {
		t.Fatalf("a progressing interval must not count a timeout, got %d", il.Timeouts())
	}

	next := base.Add(7 * acquireTimerInterval)
	if got := il.OnTimer(next); got != TimerEscalate {
		t.Fatalf("sixth no-progress fire: got %v, want TimerEscalate", got)
	}
	il.RearmTimer(next)

	il.mu.Lock()
	il.markProgressLocked()
	il.mu.Unlock()
	if got := il.OnTimer(base.Add(8 * acquireTimerInterval)); got != TimerRefresh {
		t.Fatalf("second progress fire: got %v, want TimerRefresh", got)
	}
	if got := il.Timeouts(); got != ledgerTimeoutRetriesMax {
		t.Fatalf("progress erased the cumulative budget: got %d, want %d", got, ledgerTimeoutRetriesMax)
	}

	if got := il.OnTimer(base.Add(9 * acquireTimerInterval)); got != TimerFailed {
		t.Fatalf("seventh no-progress fire: got %v, want TimerFailed", got)
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
