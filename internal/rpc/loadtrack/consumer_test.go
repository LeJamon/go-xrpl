package loadtrack

import (
	"testing"
	"time"
)

func TestWarnConsumesFeeOncePerClockInstant(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })
	const key = "198.51.100.7"
	tr.Import("peer", Gossip{Items: []GossipItem{{Key: key, Balance: WarningThreshold}}})

	if !tr.Warn(key) {
		t.Fatal("expected the first warning at the threshold")
	}
	if got, want := tr.Balance(key), float64(WarningThreshold+ChargeWarning/uint32(decayWindowSeconds)); got != want {
		t.Fatalf("balance after warning = %v, want %v", got, want)
	}
	if tr.Warn(key) {
		t.Fatal("expected a second warning at the same clock instant to be suppressed")
	}
	if got, want := tr.Balance(key), float64(WarningThreshold+ChargeWarning/uint32(decayWindowSeconds)); got != want {
		t.Fatalf("balance after suppressed warning = %v, want %v", got, want)
	}

	now = now.Add(time.Second)
	before := tr.LocalBalance(key)
	if !tr.Warn(key) {
		t.Fatal("expected a warning at a new clock instant")
	}
	if got, want := tr.LocalBalance(key), before+float64(ChargeWarning/uint32(decayWindowSeconds)); got != want {
		t.Fatalf("new-clock warning balance = %v, want %v", got, want)
	}
	if tr.Warn(key) {
		t.Fatal("expected only one warning at the new clock instant")
	}
}

func TestWarnConsumesFeeOncePerSecond(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	now := base.Add(100 * time.Millisecond)
	tr := newWithClock(func() time.Time { return now })
	const key = "198.51.100.7"
	tr.Import("peer", Gossip{Items: []GossipItem{{Key: key, Balance: WarningThreshold}}})

	if !tr.Warn(key) {
		t.Fatal("expected the first warning in the second")
	}
	now = base.Add(900 * time.Millisecond)
	if tr.Warn(key) {
		t.Fatal("expected a second warning in the same second to be suppressed")
	}
}

func TestSubsecondOperationsDoNotPostponeDecay(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	now := base.Add(100 * time.Millisecond)
	tr := newWithClock(func() time.Time { return now })
	const key = "198.51.100.7"
	tr.Charge(key, LoadKind(3200))

	now = base.Add(900 * time.Millisecond)
	if got := tr.LocalBalance(key); got != 100 {
		t.Fatalf("subsecond balance = %v, want 100", got)
	}
	now = base.Add(1100 * time.Millisecond)
	if got := tr.LocalBalance(key); got != 96 {
		t.Fatalf("one-second decayed balance = %v, want 96", got)
	}
}

func TestWarnWhenChargeCrossesThreshold(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })
	const key = "198.51.100.7"

	tr.Import("peer", Gossip{Items: []GossipItem{{Key: key, Balance: WarningThreshold - 1}}})
	if got := tr.Charge(key, LoadKind(decayWindowSeconds)); got != OutcomeWarn {
		t.Fatalf("threshold-crossing charge outcome = %v, want %v", got, OutcomeWarn)
	}
	if !tr.Warn(key) {
		t.Fatal("expected warning after threshold crossing")
	}
	if got, want := tr.Balance(key), float64(WarningThreshold+ChargeWarning/uint32(decayWindowSeconds)); got != want {
		t.Fatalf("balance after threshold warning = %v, want %v", got, want)
	}
}

func TestDisconnectConsumesDropFee(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })
	const key = "198.51.100.7"
	tr.Import("peer", Gossip{Items: []GossipItem{{Key: key, Balance: DropThreshold}}})

	if !tr.Disconnect(key) {
		t.Fatal("expected disconnect at drop threshold")
	}
	if got, want := tr.Balance(key), float64(DropThreshold+ChargeDrop/uint32(decayWindowSeconds)); got != want {
		t.Fatalf("balance after disconnect = %v, want %v", got, want)
	}
	if !tr.Disconnect(key) {
		t.Fatal("expected repeated disconnect while still over threshold")
	}
	if got, want := tr.Balance(key), float64(DropThreshold+(2*ChargeDrop)/uint32(decayWindowSeconds)); got != want {
		t.Fatalf("balance after repeated disconnect = %v, want %v", got, want)
	}
}

func TestDisconnectRecoversAfterRemoteOnlyBalanceExpires(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })
	const key = "198.51.100.7"
	tr.Import("peer", Gossip{Items: []GossipItem{{Key: key, Balance: DropThreshold}}})

	if !tr.Disconnect(key) {
		t.Fatal("expected imported balance to trigger disconnect")
	}
	now = now.Add(GossipExpiration)
	if tr.Disconnect(key) {
		t.Fatal("remote-only threshold did not recover after gossip expiry")
	}
	if got := tr.Balance(key); got >= DropThreshold {
		t.Fatalf("post-expiry balance = %v, want below %d", got, DropThreshold)
	}
}

func TestConsumerOperationsIgnoreEmptyKey(t *testing.T) {
	tr := New()
	if tr.Warn("") {
		t.Fatal("empty key must not emit a warning")
	}
	if tr.Disconnect("") {
		t.Fatal("empty key must not disconnect")
	}
	if got := tr.Balance(""); got != 0 {
		t.Fatalf("empty-key balance = %v, want 0", got)
	}
}

func TestRemoteOnlyThresholdExpiresOnReadPaths(t *testing.T) {
	readers := []struct {
		name string
		read func(*Tracker, string)
	}{
		{name: "balance", read: func(tr *Tracker, key string) { tr.Balance(key) }},
		{name: "local balance", read: func(tr *Tracker, key string) { tr.LocalBalance(key) }},
		{name: "threshold", read: func(tr *Tracker, key string) { tr.OverDropThreshold(key) }},
		{name: "warning", read: func(tr *Tracker, key string) { tr.Warn(key) }},
		{name: "disconnect", read: func(tr *Tracker, key string) { tr.Disconnect(key) }},
		{name: "export", read: func(tr *Tracker, _ string) { tr.Export() }},
	}

	for _, tc := range readers {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1_000_000, 0)
			tr := newWithClock(func() time.Time { return now })
			const key = "198.51.100.7"
			tr.Import("peer", Gossip{Items: []GossipItem{
				{Key: key, Balance: DropThreshold / 2},
				{Key: key, Balance: DropThreshold / 2},
			}})
			if !tr.OverDropThreshold(key) {
				t.Fatal("expected imported balance to reach drop threshold")
			}

			now = now.Add(GossipExpiration)
			tc.read(tr, key)
			if got := tr.entries[key].remoteBalance; got != 0 {
				t.Fatalf("remote balance after expiry = %d, want 0", got)
			}
			if len(tr.imports) != 0 {
				t.Fatalf("expired imports retained: %d", len(tr.imports))
			}
			if tr.OverDropThreshold(key) {
				t.Fatal("remote-only consumer remained over drop threshold after expiry")
			}
			if got := tr.Balance(key); got != 0 {
				t.Fatalf("balance after remote-only expiry = %v, want 0", got)
			}
		})
	}
}
