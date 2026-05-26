package list

import "testing"

func TestAggregator_IsUNLBlocked(t *testing.T) {
	pk1 := PublisherKey{1}
	pk2 := PublisherKey{2}
	nonEmptyUnion := [][33]byte{{9}}

	cases := []struct {
		name        string
		state       map[PublisherKey]*PublisherState
		lastEmitted [][33]byte
		want        bool
	}{
		{
			name:  "no publishers configured",
			state: map[PublisherKey]*PublisherState{},
			want:  false,
		},
		{
			name:  "configured but no list ingested yet",
			state: map[PublisherKey]*PublisherState{pk1: {Status: StatusUnavailable}},
			want:  false,
		},
		{
			name:        "available list with non-empty union",
			state:       map[PublisherKey]*PublisherState{pk1: {Status: StatusAvailable}},
			lastEmitted: nonEmptyUnion,
			want:        false,
		},
		{
			name:  "available list but empty union",
			state: map[PublisherKey]*PublisherState{pk1: {Status: StatusAvailable}},
			want:  true,
		},
		{
			name:        "expired list blocks even with a non-empty union",
			state:       map[PublisherKey]*PublisherState{pk1: {Status: StatusAvailable}, pk2: {Status: StatusExpired}},
			lastEmitted: nonEmptyUnion,
			want:        true,
		},
		{
			name:  "revoked sole publisher with empty union",
			state: map[PublisherKey]*PublisherState{pk1: {Status: StatusRevoked}},
			want:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Aggregator{state: tc.state, lastEmitted: tc.lastEmitted}
			if got := a.IsUNLBlocked(); got != tc.want {
				t.Errorf("IsUNLBlocked() = %v, want %v", got, tc.want)
			}
		})
	}
}
