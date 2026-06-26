package peermanagement

import "time"

// Reduce-relay constants. These mirror rippled's
// `src/xrpld/overlay/ReduceRelayCommon.h` (namespace `reduce_relay`).
//
// A peer's squelch is bounded in time to:
//
//	rand{MinUnsquelchExpire, max_squelch}
//
// where:
//
//	max_squelch = min(max(MaxUnsquelchExpireDefault, SquelchPerPeer * num_peers),
//	                  MaxUnsquelchExpirePeers)
//
// See: https://xrpl.org/blog/2021/message-routing-optimizations-pt-1-proposal-validation-relaying.html
const (
	// MinUnsquelchExpire is the minimum squelch duration (5 minutes).
	MinUnsquelchExpire = 300 * time.Second

	// MaxUnsquelchExpireDefault is the default upper bound for the squelch
	// duration when there are few peers (10 minutes).
	MaxUnsquelchExpireDefault = 600 * time.Second

	// SquelchPerPeer is the per-peer contribution to the upper squelch bound.
	SquelchPerPeer = 10 * time.Second

	// MaxUnsquelchExpirePeers is the absolute upper bound for any squelch
	// duration (1 hour).
	MaxUnsquelchExpirePeers = 3600 * time.Second

	// Idled is the no-message-received threshold before a peer is treated
	// as idle for selection purposes.
	Idled = 8 * time.Second

	// MinMessageThreshold is the per-peer message count needed before a
	// peer becomes a selection candidate.
	MinMessageThreshold = 19

	// MaxMessageThreshold is the per-peer message count that, when reached
	// by MaxSelectedPeers peers, triggers selection.
	MaxMessageThreshold = 20

	// MaxSelectedPeers is the maximum number of peers chosen as the source
	// of validator messages per slot.
	MaxSelectedPeers = 5

	// WaitOnBootup is the grace period after start-up before reduce-relay
	// engages, to let the node establish its peer connections.
	WaitOnBootup = 10 * time.Minute
)
