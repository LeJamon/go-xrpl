package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound/inboundtest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/require"
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
	r.catchupReplay.fetchTracker.Track(il)

	replies := make(chan *peermanagement.InboundMessage, 1)
	replies <- nil
	r.SetAcqInbox(replies)
	deferredAt := base.Add(time.Minute)
	r.catchupReplay.retryInboundLedgerAcquisitions(deferredAt)

	if got := il.Timeouts(); got != 7 {
		t.Fatalf("timeouts after retry = %d, want 7", got)
	}
	if got := r.catchupReplay.fetchTracker.Find(il.Hash()); got != nil {
		t.Fatalf("failed acquisition still tracked: %p", got)
	}
}

func TestMaintenanceRetriesOnConfiguredAcquisitionClock(t *testing.T) {
	clock := inboundtest.NewFakeClock(time.Unix(1_700_000_000, 0))
	sender := &recordingSender{acquisitionPeers: []uint64{7}}
	router := newRouter(nil, New(Config{}), nil, routerNetworkConfig{acquisition: sender, inboundClock: clock}, routerDeps{})
	hash := [32]byte{0x92}
	trackCatchupPeer(router, 7, 42, hash)
	_, started, _ := router.RequestLedger(hash, 42)
	require.True(t, started)
	acquisition := router.catchupReplay.fetchTracker.Find(hash)
	require.NotNil(t, acquisition)

	for retry := 0; retry < 2; retry++ {
		clock.Advance(3*time.Second - time.Nanosecond)
		router.maintenanceTick()
		require.Equal(t, retry, acquisition.Timeouts())
		clock.Advance(time.Nanosecond)
		router.maintenanceTick()
		require.Equal(t, retry+1, acquisition.Timeouts())
		require.Len(t, sender.legacyCalls(), retry+2)
	}
}
