package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/require"
)

func TestFinalityDeferralBlocksConcurrentDrain(t *testing.T) {
	now := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	nodeID := consensus.NodeID{0x51}
	tracker := NewValidationTracker(1)
	tracker.SetNow(func() time.Time { return now })
	tracker.SetTrustedAndQuorum([]consensus.NodeID{nodeID}, 1)
	fired := make(chan struct{}, 1)
	tracker.SetFullyValidatedCallback(func(consensus.LedgerID, uint32) {
		fired <- struct{}{}
	})

	tracker.beginFinalityDeferral()
	status := tracker.addStatus(&consensus.Validation{
		LedgerID:  consensus.LedgerID{0xA5},
		LedgerSeq: 100,
		NodeID:    nodeID,
		SignTime:  now,
		SeenTime:  now,
		Full:      true,
	}, false)
	require.Equal(t, ValStatusCurrent, status)

	drainDone := make(chan struct{})
	go func() {
		tracker.drainFinality()
		close(drainDone)
	}()
	select {
	case <-drainDone:
	case <-time.After(time.Second):
		t.Fatal("concurrent finality drain did not return")
	}
	select {
	case <-fired:
		t.Fatal("finality callback ran while dispatch was deferred")
	default:
	}

	tracker.endFinalityDeferral()
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("finality callback did not run after deferral ended")
	}
}
