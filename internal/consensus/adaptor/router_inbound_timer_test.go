package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
)

func TestRetryInboundLedgerAcquisitionsDoesNotDeferForUnrelatedQueuedReply(t *testing.T) {
	t.Parallel()
	r, _, _, _ := makeRouter(t)
	il := inbound.New([32]byte{0x91}, 42, 7, serveTestLogger())
	base := time.Now()
	for i := 1; i <= 6; i++ {
		if got := il.OnTimer(base.Add(time.Duration(i) * 4 * time.Second)); got != inbound.TimerEscalate {
			t.Fatalf("fire %d: got %v, want TimerEscalate", i, got)
		}
	}
	r.fetchTracker.Track(il)

	replies := make(chan *peermanagement.InboundMessage, 1)
	replies <- nil
	r.SetAcqInbox(replies)
	deferredAt := base.Add(time.Minute)
	r.retryInboundLedgerAcquisitions(deferredAt)

	if got := il.Timeouts(); got != 7 {
		t.Fatalf("timeouts after retry = %d, want 7", got)
	}
	if got := r.fetchTracker.Find(il.Hash()); got != nil {
		t.Fatalf("failed acquisition still tracked: %p", got)
	}
}
