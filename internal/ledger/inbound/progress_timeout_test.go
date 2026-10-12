package inbound

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

func TestLedger_OnTimer_DuplicateAndRejectedRepliesDoNotResetStreak(t *testing.T) {
	t.Parallel()
	il, nodes := incompleteStateAcquisitionFixture(t)
	base := time.Unix(1_700_000_000, 0)
	il.mu.Lock()
	il.lastTimer = base
	il.mu.Unlock()

	if got := il.OnTimer(base.Add(acquireTimerInterval)); got != TimerRefresh {
		t.Fatalf("interval after base reply: got %v, want TimerRefresh", got)
	}
	now := base.Add(2 * acquireTimerInterval)
	if got := il.OnTimer(now); got != TimerEscalate {
		t.Fatalf("initial quiet interval: got %v, want TimerEscalate", got)
	}
	if got := il.ConsecutiveTimeouts(); got != 1 {
		t.Fatalf("initial consecutive timeouts = %d, want 1", got)
	}

	useful, err := il.GotStateNodesUseful([]message.LedgerNode{nodes[0]})
	if err != nil {
		t.Fatalf("useful reply: %v", err)
	}
	if useful != 1 {
		t.Fatalf("useful reply added %d nodes, want 1", useful)
	}
	now = now.Add(acquireTimerInterval)
	if got := il.OnTimer(now); got != TimerRefresh {
		t.Fatalf("interval after useful reply: got %v, want TimerRefresh", got)
	}
	if got := il.ConsecutiveTimeouts(); got != 0 {
		t.Fatalf("consecutive timeouts after useful reply = %d, want 0", got)
	}

	duplicate, err := il.GotStateNodesUseful([]message.LedgerNode{nodes[0]})
	if err != nil {
		t.Fatalf("duplicate reply: %v", err)
	}
	if duplicate != 0 {
		t.Fatalf("duplicate reply added %d nodes, want 0", duplicate)
	}
	now = now.Add(acquireTimerInterval)
	if got := il.OnTimer(now); got != TimerEscalate {
		t.Fatalf("interval after duplicate reply: got %v, want TimerEscalate", got)
	}
	if got := il.ConsecutiveTimeouts(); got != 1 {
		t.Fatalf("duplicate reply reset consecutive timeouts to %d, want 1", got)
	}

	il.RearmTimer(now)
	bad := nodes[1]
	bad.NodeData = []byte{0}
	if useful, err := il.GotStateNodesUseful([]message.LedgerNode{bad}); useful != 0 || err == nil {
		t.Fatalf("rejected reply = (%d, %v), want no useful nodes and an error", useful, err)
	}
	now = now.Add(acquireTimerInterval)
	if got := il.OnTimer(now); got != TimerEscalate {
		t.Fatalf("interval after rejected reply: got %v, want TimerEscalate", got)
	}
	if got := il.ConsecutiveTimeouts(); got != 2 {
		t.Fatalf("rejected reply reset consecutive timeouts to %d, want 2", got)
	}
	if got := il.Timeouts(); got != 3 {
		t.Fatalf("cumulative timeouts = %d, want 3", got)
	}
}
