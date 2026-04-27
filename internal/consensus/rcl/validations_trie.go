package rcl

import (
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/ledgertrie"
)

// LedgerAncestryProvider resolves a LedgerID to a ledgertrie.Ledger
// carrying its full ancestry. Returns (nil, false) when the ledger's
// history is not locally known — e.g. a validation arrived for a
// ledger we haven't acquired yet. Callers (the ValidationTracker)
// silently skip trie insertion in that case and fall back to the
// flat-count approximation via the byNode map.
type LedgerAncestryProvider interface {
	LedgerByID(id consensus.LedgerID) (ledgertrie.Ledger, bool)
}

// SetLedgerAncestryProvider installs a provider and enables the trie.
// Passing nil disables the trie and discards any tip support currently
// tracked — the ValidationTracker reverts to flat-count support.
//
// Safe to call at any time: the trie is rebuilt from the current
// byNode / trusted / negUNL state so a late-bound provider still
// reflects everything the tracker has already accepted.
func (vt *ValidationTracker) SetLedgerAncestryProvider(p LedgerAncestryProvider) {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	if p == nil {
		vt.ancestry = nil
		vt.trie = nil
		vt.trieTips = nil
		return
	}
	vt.ancestry = p
	vt.rebuildTrieLocked()
}

// rebuildTrieLocked resets the trie and reseeds it from the current
// byNode / trusted / negUNL state. Called when the trust set, negUNL,
// or ancestry provider change — any of those can alter which
// validators contribute which ledgers. Caller must hold vt.mu (write).
// No-op when the ancestry provider is not set.
func (vt *ValidationTracker) rebuildTrieLocked() {
	if vt.ancestry == nil {
		return
	}
	vt.trie = ledgertrie.New(genesisLedger{})
	vt.trieTips = make(map[consensus.NodeID]ledgertrie.Ledger)

	// Seed the trie with current byNode state. Mirrors rippled's
	// Validations::updateTrie which walks lastValidations on
	// reconfigure (Validations.h:415-470).
	for nodeID, v := range vt.byNode {
		if !vt.trusted[nodeID] || vt.negUNL[nodeID] {
			continue
		}
		lgr, ok := vt.ancestry.LedgerByID(v.LedgerID)
		if !ok {
			continue
		}
		vt.trie.Insert(lgr, 1)
		vt.trieTips[nodeID] = lgr
	}
}

// updateTrieLocked applies a trusted validator's latest validation to
// the trie: removes the validator's previous tip (if any) and inserts
// the new one. Silent no-op when the trie is not wired or ancestry
// for newLedgerID is unavailable.
//
// preResolved is an optional pre-walked ancestry chain captured by the
// caller before taking vt.mu — Add() resolves outside the lock so a
// cold-LRU walk does not serialise concurrent inserts. When nil, this
// function falls back to ancestry.LedgerByID under the lock (the rare
// race path where the provider was swapped between resolve and lock).
//
// Mirrors rippled's Validations::updateTrie (Validations.h:415-470)
// but keyed off the pre-computed trieTips map rather than re-reading
// lastValidations.
//
// Caller must hold vt.mu (write).
func (vt *ValidationTracker) updateTrieLocked(nodeID consensus.NodeID, newLedgerID consensus.LedgerID, preResolved ledgertrie.Ledger) {
	if vt.trie == nil || vt.ancestry == nil {
		return
	}
	// Validator is trusted and not on negUNL — those checks live at
	// the Add() call site. We still guard defensively.
	if !vt.trusted[nodeID] || vt.negUNL[nodeID] {
		// Validator lost trust or moved onto negUNL between the
		// snapshot and here — remove its prior tip if any.
		if prev, ok := vt.trieTips[nodeID]; ok {
			vt.trie.Remove(prev, 1)
			delete(vt.trieTips, nodeID)
		}
		return
	}

	lgr := preResolved
	if lgr == nil || lgr.ID() != newLedgerID {
		// Either the caller didn't pre-resolve (rebuildTrieLocked path)
		// or the provider was swapped between resolve and lock. Resolve
		// fresh — accepting the contention cost in this rare path.
		var ok bool
		lgr, ok = vt.ancestry.LedgerByID(newLedgerID)
		if !ok {
			// We don't know the new ledger's ancestry. Leave any existing
			// trie entry for this validator in place; flat-count semantics
			// will at least still reflect the prior tip.
			return
		}
	}

	if prev, existed := vt.trieTips[nodeID]; existed {
		vt.trie.Remove(prev, 1)
	}
	vt.trie.Insert(lgr, 1)
	vt.trieTips[nodeID] = lgr
}

// genesisLedger is the placeholder ledger used as the root of the
// trie. The trie only reads Ancestor(0) from it (via Span.startID for
// the root) and Seq()==0 for the MakeGenesis invariant check.
type genesisLedger struct{}

func (genesisLedger) ID() consensus.LedgerID               { return consensus.LedgerID{} }
func (genesisLedger) Seq() uint32                          { return 0 }
func (genesisLedger) MinSeq() uint32                       { return 0 }
func (genesisLedger) Ancestor(s uint32) consensus.LedgerID { return consensus.LedgerID{} }
