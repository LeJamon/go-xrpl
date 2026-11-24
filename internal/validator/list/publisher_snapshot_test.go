package list

import (
	"testing"
	"time"
)

func TestPublisherSnapshotStates(t *testing.T) {
	key := PublisherKey{0xed, 1}
	agg, err := New(Config{PublisherKeys: []PublisherKey{key}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []struct {
		name       string
		status     PublisherStatus
		pending    bool
		remainingN int
	}{
		{name: "unavailable", status: StatusUnavailable},
		{name: "available", status: StatusAvailable},
		{name: "expired", status: StatusExpired},
		{name: "revoked", status: StatusRevoked},
		{name: "pending", status: StatusUnavailable, pending: true, remainingN: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg.mu.Lock()
			state := agg.state[key]
			*state = publisherState{MasterKey: key, Status: tc.status}
			if tc.pending {
				state.Remaining = map[uint32]*pendingList{9: {
					Sequence: 9, Effective: time.Now().Add(time.Hour), EffectiveSet: true,
				}}
			}
			agg.mu.Unlock()

			got := agg.PublisherSnapshot()
			if len(got) != 1 || got[0].MasterKey != key || got[0].Status != tc.status {
				t.Fatalf("snapshot: got %+v", got)
			}
			if len(got[0].Remaining) != tc.remainingN {
				t.Fatalf("remaining: got %d want %d", len(got[0].Remaining), tc.remainingN)
			}
		})
	}
}
