package adaptor

import (
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound/inboundtest"
	"github.com/stretchr/testify/require"
)

func TestRouterMaintenancePrunesInboundHistory(t *testing.T) {
	router := newTestRouter(nil, New(Config{}), nil)
	clock := inboundtest.NewFakeClock(time.Unix(1_700_000_000, 0))
	router.fetchTracker = inbound.NewTrackerWithClock(clock)
	hash := [32]byte{0xA3}
	ledger := inbound.New(hash, 1828, 1, slog.Default())
	router.fetchTracker.Track(ledger)
	require.True(t, router.fetchTracker.RemoveExpectedWithSnapshot(
		ledger,
		inbound.Snapshot{Hash: hash, Seq: 1828},
		true,
	))
	require.Contains(t, router.FetchInfo(), "1828")

	clock.Advance(time.Minute + time.Nanosecond)
	router.maintenanceTick()
	require.Empty(t, router.FetchInfo())
}

func TestRouterMaintenanceUsesConfiguredInboundSweepInterval(t *testing.T) {
	clock := inboundtest.NewFakeClock(time.Unix(1_700_000_000, 0))
	router := newRouter(nil, New(Config{}), nil, routerNetworkConfig{
		inboundClock:         clock,
		inboundSweepInterval: 90 * time.Second,
	})
	hash := [32]byte{0xA4}
	ledger := inbound.New(hash, 1829, 1, slog.Default())
	router.fetchTracker.Track(ledger)
	require.True(t, router.fetchTracker.RemoveExpectedWithSnapshot(
		ledger,
		inbound.Snapshot{Hash: hash, Seq: 1829},
		true,
	))

	clock.Advance(70 * time.Second)
	router.maintenanceTick()
	require.Contains(t, router.FetchInfo(), "1829")

	clock.Advance(20 * time.Second)
	router.maintenanceTick()
	require.Empty(t, router.FetchInfo())
}
