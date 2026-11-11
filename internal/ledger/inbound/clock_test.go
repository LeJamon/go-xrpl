package inbound

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound/inboundtest"
	"github.com/stretchr/testify/require"
)

func TestNew_WithClockDrivesTimeoutsToFailure(t *testing.T) {
	t.Parallel()
	clock := inboundtest.NewFakeClock(time.Unix(1_700_000_000, 0))
	il := New([32]byte{1}, 5, 0, nil, WithClock(clock))

	require.False(t, il.TimerDue(clock.Now()))
	require.Equal(t, TimerNone, il.OnTimer(clock.Now()))
	clock.Advance(acquireTimerInterval - time.Nanosecond)
	require.False(t, il.TimerDue(clock.Now()))

	for i := 1; i <= ledgerTimeoutRetriesMax; i++ {
		clock.Advance(acquireTimerInterval)
		require.True(t, il.TimerDue(clock.Now()))
		require.Equal(t, TimerEscalate, il.OnTimer(clock.Now()))
		require.Equal(t, i, il.ConsecutiveTimeouts())
	}
	clock.Advance(acquireTimerInterval)
	require.Equal(t, TimerFailed, il.OnTimer(clock.Now()))
	require.Equal(t, StateFailed, il.State())
}

func TestWithClock_NilKeepsSystemClock(t *testing.T) {
	t.Parallel()
	il := New([32]byte{1}, 5, 0, nil, WithClock(nil))
	require.Equal(t, SystemClock, il.clock)
}
