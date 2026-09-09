package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestAcquisitionWorkYieldWithoutProgressExhaustsTimeout(t *testing.T) {
	ledger := inbound.New([32]byte{0xe7}, 1876, 7, serveTestLogger())
	clock := time.Now().Add(-time.Minute)
	ledger.RearmTimer(clock)
	for range 6 {
		clock = clock.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, ledger.OnTimer(clock))
	}
	lane := newAcquisitionWorkLane(1)
	lane.process = func(context.Context, *inbound.Ledger, []acquisitionWorkEvent) acquisitionWorkResult {
		return acquisitionWorkResult{ledger: ledger, err: shamap.ErrTraversalBudget}
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	result := <-lane.results()
	require.True(t, result.yielded)
	require.False(t, result.rearmTimer)
	require.True(t, result.timerFailure)
	require.True(t, result.remove)
	require.True(t, result.snapshot.Failed)
	close(result.ack)
}
