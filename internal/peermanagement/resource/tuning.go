package resource

import "time"

// Peer reputation thresholds and decay tuning.
const (
	// WarningThreshold is the balance at which a Consumer should be
	// warned that load is high.
	WarningThreshold = 5000

	// DropThreshold is the balance at which a Consumer is dropped for
	// excess load.
	DropThreshold = 25000

	// DecayWindowSeconds is the exponential-decay window for the
	// per-Consumer balance.
	DecayWindowSeconds = 32

	// MinimumGossipBalance is the threshold at or above which a
	// Consumer is included in exported Gossip.
	MinimumGossipBalance = 1000
)

// SecondsUntilExpiration is how long an inactive Entry is retained
// before periodicActivity removes it. Persisting balance across short
// reconnects is what blacklists a freshly-dropped IP for a window.
const SecondsUntilExpiration = 300 * time.Second

// GossipExpiration is how long an imported gossip record stays
// effective before its contributions are subtracted.
const GossipExpiration = 30 * time.Second
