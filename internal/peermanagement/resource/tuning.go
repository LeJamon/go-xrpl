package resource

import "time"

// Tuning constants mirror rippled's Resource::detail::Tuning enum
// (rippled/include/xrpl/resource/detail/Tuning.h). Keeping the same
// numeric values means an operator familiar with rippled's reputation
// behavior sees identical thresholds here.
const (
	// WarningThreshold is the balance at which a Consumer should be
	// warned that load is high.
	WarningThreshold = 5000

	// DropThreshold is the balance at which a Consumer is dropped for
	// excess load.
	DropThreshold = 25000

	// DecayWindowSeconds is the exponential-decay window for the
	// per-Consumer balance. A power of two matches rippled's choice.
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
