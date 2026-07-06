package rcl

import "github.com/LeJamon/go-xrpl/internal/consensus"

// censorshipWarnInterval is the ledger cadence at which a tracked tx that
// keeps being proposed but never included triggers a censorship warning.
const censorshipWarnInterval = 15

// censorshipDetector tracks the transactions this node proposes across
// consensus rounds and warns when an eligible tx is persistently excluded
// from ledgers. It is purely observational: it never affects the proposed
// set, ledger content, or validations.
//
// Every entry point runs on the single consensus goroutine under Engine.mu,
// so no internal locking is needed. The zero value is ready to use.
type censorshipDetector struct {
	// tracker maps a still-uncommitted proposed txid to the sequence of the
	// ledger at which we began tracking it, so the wait grows across rounds.
	tracker map[consensus.TxID]uint32
}

// propose records the txs proposed for the round building ledger seq. A tx
// carried over from an earlier round keeps its original tracking seq; txs we
// no longer propose are dropped.
func (c *censorshipDetector) propose(proposed []consensus.TxID, seq uint32) {
	next := make(map[consensus.TxID]uint32, len(proposed))
	for _, id := range proposed {
		if prev, ok := c.tracker[id]; ok {
			next[id] = prev
		} else {
			next[id] = seq
		}
	}
	c.tracker = next
}

// check drops every tracked tx that made it into the accepted set, then
// invokes pred for each remaining tx so it can warn about persistent
// exclusion. pred returns true for entries to drop without warning (txs we
// no longer vote for), false to keep tracking.
func (c *censorshipDetector) check(accepted []consensus.TxID, pred func(id consensus.TxID, seq uint32) bool) {
	acc := make(map[consensus.TxID]struct{}, len(accepted))
	for _, id := range accepted {
		acc[id] = struct{}{}
	}
	for id, seq := range c.tracker {
		if _, ok := acc[id]; ok {
			delete(c.tracker, id)
			continue
		}
		if pred(id, seq) {
			delete(c.tracker, id)
		}
	}
}

// reset clears all tracking, e.g. after leaving proposing/observing mode.
func (c *censorshipDetector) reset() {
	c.tracker = nil
}
