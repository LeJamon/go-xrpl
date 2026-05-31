package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// TestAdjustCloseTime_DampingMatchesRippled exercises the three damping
// branches of rippled's TimeKeeper::adjustCloseTime (TimeKeeper.h:88-116):
// by > 1s, by < -1s, and |by| <= 1s. Each step is a quarter-step toward
// the network's view; offsets within ±1s decay toward zero by ¼.
func TestAdjustCloseTime_DampingMatchesRippled(t *testing.T) {
	const second = int64(time.Second)

	// self is fixed; peer close times are constructed to produce the
	// target `by` (avg_secs - self_secs). With 1 peer the average is
	// the midpoint, so a single peer at self + 2*by yields the desired
	// `by`.
	self := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name       string
		initialNs  int64 // pre-existing closeOffsetNs
		bySecs     int64 // desired (avg - self) in seconds
		wantNewSec int64 // expected post-store offset, in seconds
	}{
		{
			name:       "by>1s, zero offset: +(by+3)/4",
			initialNs:  0,
			bySecs:     8,
			wantNewSec: (8 + 3) / 4, // = 2
		},
		{
			name:       "by>1s, with offset: accumulates",
			initialNs:  5 * second,
			bySecs:     8,
			wantNewSec: 5 + (8+3)/4, // = 7
		},
		{
			name:       "by<-1s, zero offset: +(by-3)/4 (negative)",
			initialNs:  0,
			bySecs:     -8,
			wantNewSec: (-8 - 3) / 4, // = -2 (Go truncates toward zero)
		},
		{
			name:       "by<-1s, with positive offset: pulls back",
			initialNs:  5 * second,
			bySecs:     -8,
			wantNewSec: 5 + (-8-3)/4, // = 3
		},
		{
			name:       "|by|<=1s with offset: decay by 1/4",
			initialNs:  8 * second,
			bySecs:     0,
			wantNewSec: 8 * 3 / 4, // = 6
		},
		{
			name:       "|by|=1s, with offset: still decays (small-by branch)",
			initialNs:  8 * second,
			bySecs:     1,
			wantNewSec: 8 * 3 / 4, // = 6
		},
		{
			name:       "|by|=1s, negative offset: decay toward zero",
			initialNs:  -8 * second,
			bySecs:     -1,
			wantNewSec: -8 * 3 / 4, // = -6 (toward zero)
		},
		{
			name:       "by==0 and offset==0: no-op",
			initialNs:  0,
			bySecs:     0,
			wantNewSec: 0,
		},
		{
			name:       "by==0 with offset: still decays",
			initialNs:  4 * second,
			bySecs:     0,
			wantNewSec: 4 * 3 / 4, // = 3
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestAdaptor(t)
			a.closeOffsetNs.Store(tc.initialNs)

			peer := self.Add(time.Duration(2*tc.bySecs) * time.Second)
			a.AdjustCloseTime(consensus.CloseTimes{
				Self:  self,
				Peers: map[time.Time]int{peer: 1},
			})

			gotNs := a.closeOffsetNs.Load()
			wantNs := tc.wantNewSec * second
			if gotNs != wantNs {
				t.Fatalf("closeOffsetNs = %dns (%ds), want %dns (%ds)",
					gotNs, gotNs/second, wantNs, tc.wantNewSec)
			}
		})
	}
}

// TestAdjustCloseTime_SelfZeroIsNoOp guards the early-return when the
// engine hasn't set our close time yet.
func TestAdjustCloseTime_SelfZeroIsNoOp(t *testing.T) {
	a := newTestAdaptor(t)
	a.closeOffsetNs.Store(int64(7 * time.Second))

	a.AdjustCloseTime(consensus.CloseTimes{}) // Self.IsZero()

	if got := a.closeOffsetNs.Load(); got != int64(7*time.Second) {
		t.Fatalf("closeOffsetNs changed on zero-Self input: got %d", got)
	}
}
