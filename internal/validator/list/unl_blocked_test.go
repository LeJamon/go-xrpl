package list

import (
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// TestAggregator_IsUNLBlocked drives the sticky UNL-blocked flag through the
// real Tick → recomputeAndEmitLocked path, asserting it mirrors rippled's
// NetworkOPs unlBlocked_ as maintained by ValidatorList::updateTrusted
// (ValidatorList.cpp:1929-2006, 2096-2101).
func TestAggregator_IsUNLBlocked(t *testing.T) {
	pk1 := PublisherKey{1}
	pk2 := PublisherKey{2}
	val := [][33]byte{{9}}

	newAgg := func(now *time.Time, pubs ...PublisherKey) *Aggregator {
		a := &Aggregator{
			publishers: map[PublisherKey]struct{}{},
			state:      map[PublisherKey]*PublisherState{},
			threshold:  1,
			clock:      func() time.Time { return *now },
		}
		for _, p := range pubs {
			a.publishers[p] = struct{}{}
			a.state[p] = &PublisherState{MasterKey: p, Status: StatusUnavailable}
		}
		return a
	}

	t.Run("no publishers configured -> not blocked", func(t *testing.T) {
		now := time.Unix(1000, 0)
		a := newAgg(&now)
		a.Tick()
		if a.IsUNLBlocked() {
			t.Fatal("a node with no configured publishers must never be blocked")
		}
	})

	t.Run("fresh start, configured but no list ingested -> blocked", func(t *testing.T) {
		now := time.Unix(1000, 0)
		a := newAgg(&now, pk1)
		a.Tick()
		if !a.IsUNLBlocked() {
			t.Fatal("rippled locks down when publishers are configured but the trusted union is empty (ValidatorList.cpp:2096-2101)")
		}
	})

	t.Run("static validators keep unavailable publishers from blocking", func(t *testing.T) {
		now := time.Unix(1000, 0)
		a := newAgg(&now, pk1)
		a.staticValidatorCount = 1
		a.Tick()
		if a.IsUNLBlocked() {
			t.Fatal("a non-empty static UNL must prevent empty-union lockdown")
		}
	})

	t.Run("already expired publisher does not block static validators", func(t *testing.T) {
		now := time.Unix(1000, 0)
		a := newAgg(&now, pk1)
		a.staticValidatorCount = 1
		a.state[pk1].Status = StatusExpired
		a.Tick()
		if a.IsUNLBlocked() {
			t.Fatal("a list received after expiry must not block a non-empty static UNL")
		}
	})

	t.Run("live publisher expiry blocks despite static validators", func(t *testing.T) {
		now := time.Unix(1000, 0)
		a := newAgg(&now, pk1)
		a.staticValidatorCount = 1
		a.state[pk1] = &PublisherState{
			MasterKey:  pk1,
			Status:     StatusAvailable,
			Validators: val,
			Expiration: now.Add(time.Hour),
		}
		a.Tick()
		now = now.Add(2 * time.Hour)
		a.Tick()
		if !a.IsUNLBlocked() {
			t.Fatal("an available list expiring must latch lockdown")
		}
	})

	t.Run("available publisher with non-empty union -> not blocked", func(t *testing.T) {
		now := time.Unix(1000, 0)
		a := newAgg(&now, pk1)
		s := a.state[pk1]
		s.Status = StatusAvailable
		s.Validators = val
		s.Expiration = now.Add(time.Hour)
		a.Tick()
		if a.IsUNLBlocked() {
			t.Fatal("a healthy publisher with a non-empty union must clear the block")
		}
	})

	t.Run("available publisher but empty union -> blocked", func(t *testing.T) {
		now := time.Unix(1000, 0)
		a := newAgg(&now, pk1)
		s := a.state[pk1]
		s.Status = StatusAvailable
		s.Validators = nil
		s.Expiration = now.Add(time.Hour)
		a.Tick()
		if !a.IsUNLBlocked() {
			t.Fatal("an available publisher yielding no trusted validators must lock down")
		}
	})

	t.Run("multi-publisher clock-expiry latches and stays blocked", func(t *testing.T) {
		now := time.Unix(1000, 0)
		a := newAgg(&now, pk1, pk2)
		for _, p := range []PublisherKey{pk1, pk2} {
			s := a.state[p]
			s.Status = StatusAvailable
			s.Validators = val
		}
		a.state[pk1].Expiration = now.Add(2 * time.Hour)
		a.state[pk2].Expiration = now.Add(time.Hour)
		a.Tick()
		if a.IsUNLBlocked() {
			t.Fatal("two healthy publishers must not be blocked")
		}

		// Advance past pk2's expiry; pk1 is still valid. rippled flips pk2 to
		// expired and sets the block (1996-2001); because good is now false it
		// never clears, even though pk1 still yields a non-empty union.
		now = now.Add(90 * time.Minute)
		a.Tick()
		if !a.IsUNLBlocked() {
			t.Fatal("a list expiring by clock must lock the node down even though pk1 is still healthy (ValidatorList.cpp:1996-2001, good=false)")
		}
		if a.state[pk2].Status != StatusExpired {
			t.Fatalf("expected pk2 flipped to StatusExpired, got %v", a.state[pk2].Status)
		}

		// Stays latched on a subsequent tick while pk2 is non-available.
		now = now.Add(time.Minute)
		a.Tick()
		if !a.IsUNLBlocked() {
			t.Fatal("block must stay latched while any publisher is non-available")
		}
	})

	t.Run("clears once every publisher available again", func(t *testing.T) {
		now := time.Unix(1000, 0)
		a := newAgg(&now, pk1)
		s := a.state[pk1]
		s.Status = StatusAvailable
		s.Validators = val
		s.Expiration = now.Add(time.Hour)
		a.Tick()

		now = now.Add(2 * time.Hour) // expire it
		a.Tick()
		if !a.IsUNLBlocked() {
			t.Fatal("expected blocked after the sole publisher's list expired")
		}

		s.Status = StatusAvailable // renewed list
		s.Validators = val
		s.Expiration = now.Add(time.Hour)
		a.Tick()
		if a.IsUNLBlocked() {
			t.Fatal("a renewed available list for every publisher must clear the block (ValidatorList.cpp:2002-2006)")
		}
	})

	t.Run("expired-then-revoked stays blocked", func(t *testing.T) {
		now := time.Unix(1000, 0)
		a := newAgg(&now, pk1, pk2)
		for _, p := range []PublisherKey{pk1, pk2} {
			s := a.state[p]
			s.Status = StatusAvailable
			s.Validators = val
			s.Expiration = now.Add(time.Hour)
		}
		a.Tick()
		if a.IsUNLBlocked() {
			t.Fatal("two healthy publishers must not be blocked")
		}

		// pk2 expires by clock; keep pk1 healthy with a future expiry.
		now = now.Add(2 * time.Hour)
		a.state[pk1].Status = StatusAvailable
		a.state[pk1].Expiration = now.Add(time.Hour)
		a.Tick()
		if !a.IsUNLBlocked() {
			t.Fatal("expected blocked after pk2 expiry")
		}

		// pk2's master key is now revoked — its status leaves Expired, so a
		// stateless snapshot would clear the block. rippled stays blocked
		// because clearing requires ALL publishers available (good=false).
		a.state[pk2].Status = StatusRevoked
		a.Tick()
		if !a.IsUNLBlocked() {
			t.Fatal("a revoked-after-expiry publisher must keep the node blocked while another stays healthy (ValidatorList.cpp:2002-2006)")
		}
	})
}

func TestAggregator_UNLBlockChangeEmitsWithoutTrustChange(t *testing.T) {
	now := time.Unix(1000, 0)
	pk1 := PublisherKey{1}
	pk2 := PublisherKey{2}
	validator := [][33]byte{{9}}
	a := &Aggregator{
		publishers: map[PublisherKey]struct{}{pk1: {}, pk2: {}},
		state: map[PublisherKey]*PublisherState{
			pk1: {MasterKey: pk1, Status: StatusAvailable, Validators: validator, Expiration: now.Add(2 * time.Hour)},
			pk2: {MasterKey: pk2, Status: StatusAvailable, Validators: validator, Expiration: now.Add(time.Hour)},
		},
		threshold: 1,
		clock:     func() time.Time { return now },
		logger:    slog.Default(),
	}

	changes := 0
	a.OnChange(func(_ []consensus.NodeID, _ [][33]byte) { changes++ })
	a.Tick()
	if changes != 1 {
		t.Fatalf("initial trust callback count: got %d want 1", changes)
	}

	now = now.Add(90 * time.Minute)
	a.Tick()
	if changes != 2 {
		t.Fatalf("expiry must emit even when overlapping publisher preserves trust: got %d callbacks want 2", changes)
	}
	if !a.IsUNLBlocked() {
		t.Fatal("expired overlapping publisher must block the UNL")
	}
}
