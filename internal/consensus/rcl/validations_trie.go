package rcl

import (
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/ledgertrie"
)

// LedgerAncestryProvider resolves a LedgerID to a ledgertrie.Ledger
// carrying its ancestry. Returns (nil, false) when the ledger's
// history is not locally known.
type LedgerAncestryProvider interface {
	LedgerByID(id consensus.LedgerID) (ledgertrie.Ledger, bool)
}

// SetLedgerAncestryProvider installs a provider and enables the trie.
// Passing nil disables the trie and reverts to flat-count support.
// The trie is rebuilt from the current byNode / trusted / negUNL state.
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

// rebuildTrieLocked resets the trie and reseeds it from byNode.
// Caller must hold vt.mu (write); no-op if ancestry is unset.
//
// Resolution runs under vt.mu — admin-only path (trust rotation,
// negUNL change, provider swap). Add() relies on the trie staying
// consistent with byNode while the lock is held.
func (vt *ValidationTracker) rebuildTrieLocked() {
	if vt.ancestry == nil {
		return
	}
	vt.trie = ledgertrie.New(genesisLedger{})
	vt.trieTips = make(map[consensus.NodeID]ledgertrie.Ledger)

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

// updateTrieLocked replaces nodeID's previous trie tip (if any) with
// newLedgerID's tip. Silent no-op if ancestry is unavailable.
//
// preResolved is the ledger Add() walked outside vt.mu to avoid
// serialising cold-LRU lookups; if nil or stale we resolve under lock.
//
// Precondition: caller holds vt.mu (write) and has verified nodeID is
// trusted and not on negUNL.
func (vt *ValidationTracker) updateTrieLocked(nodeID consensus.NodeID, newLedgerID consensus.LedgerID, preResolved ledgertrie.Ledger) {
	if vt.trie == nil || vt.ancestry == nil {
		return
	}

	lgr := preResolved
	if lgr == nil || lgr.ID() != newLedgerID {
		var ok bool
		lgr, ok = vt.ancestry.LedgerByID(newLedgerID)
		if !ok {
			return
		}
	}

	if prev, existed := vt.trieTips[nodeID]; existed {
		vt.trie.Remove(prev, 1)
	}
	vt.trie.Insert(lgr, 1)
	vt.trieTips[nodeID] = lgr
}

// genesisLedger is the trie's root placeholder. The trie only reads
// Ancestor(0) and Seq()==0 from it.
type genesisLedger struct{}

func (genesisLedger) ID() consensus.LedgerID               { return consensus.LedgerID{} }
func (genesisLedger) Seq() uint32                          { return 0 }
func (genesisLedger) MinSeq() uint32                       { return 0 }
func (genesisLedger) Ancestor(s uint32) consensus.LedgerID { return consensus.LedgerID{} }
