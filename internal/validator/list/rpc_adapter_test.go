package list

import (
	"testing"
	"time"
)

func TestRPCReaderPublisherProjectionStates(t *testing.T) {
	key := PublisherKey{0xed, 1}
	agg, err := New(Config{PublisherKeys: []PublisherKey{key}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []struct {
		name       string
		status     PublisherStatus
		pending    bool
		available  bool
		remainingN int
	}{
		{name: "unavailable", status: StatusUnavailable},
		{name: "available", status: StatusAvailable, available: true},
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

			got := NewRPCReader(agg).Publishers()
			if len(got) != 1 || got[0].Available != tc.available || got[0].Status != tc.status.String() {
				t.Fatalf("projection: got %+v", got)
			}
			if len(got[0].Remaining) != tc.remainingN {
				t.Fatalf("remaining: got %d want %d", len(got[0].Remaining), tc.remainingN)
			}
		})
	}
}

func TestRPCReaderNilIsSafe(t *testing.T) {
	var reader *RPCReader
	if reader.HasConfiguredPublishers() || reader.PublisherCount() != 0 || reader.Threshold() != 0 || reader.IsUNLBlocked() {
		t.Fatal("nil reader returned configured state")
	}
	if reader.Publishers() != nil || reader.Sites() != nil || reader.TrustedMasterKeys() != nil || reader.ListedValidators() != nil {
		t.Fatal("nil reader returned non-empty projection")
	}
}
