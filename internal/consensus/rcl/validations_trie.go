package rcl

import (
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
)

// LedgerAncestryProvider resolves a LedgerID to a ledgertrie.Ledger
// carrying its ancestry. Returns (nil, false) when the ledger's
// history is not locally known.
type LedgerAncestryProvider interface {
	LedgerByID(id consensus.LedgerID) (ledgertrie.Ledger, bool)
}

// acquiringKey identifies a ledger referenced by trusted validations but
// not yet locally resolvable — the key of rippled's acquiring_ map.
type acquiringKey struct {
	seq uint32
	id  consensus.LedgerID
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
		vt.acquiring = nil
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
	vt.acquiring = make(map[acquiringKey]map[consensus.NodeID]struct{})

	for nodeID, v := range vt.byNode {
		// Seed on trusted() alone — negUNL validators included, mirroring
		// rippled's updateTrie. They steer GetPreferred; the negUNL
		// exclusion lives on the quorum/support read paths.
		if !vt.trusted[nodeID] {
			continue
		}
		lgr, ok := vt.ancestry.LedgerByID(v.LedgerID)
		if !ok {
			vt.parkLocked(acquiringKey{seq: v.LedgerSeq, id: v.LedgerID}, nodeID)
			continue
		}
		vt.insertTipLocked(nodeID, lgr)
	}
}

// updateTrieLocked places validation's ledger as nodeID's trie tip, or
// parks the validation until the ledger is acquired (rippled updateTrie,
// Validations.h:431-469). The node's previous tip keeps steering the trie
// while its latest validation is parked. Silent no-op if the trie is
// unavailable.
//
// preResolved is the ledger Add() walked outside vt.mu to avoid
// serialising cold-LRU lookups; if nil or stale we resolve under lock.
// prior is the (seq, id) of the node's superseded validation, cleared
// from any parked entry first.
//
// Precondition: caller holds vt.mu (write) and has verified nodeID is
// trusted. negUNL validators are intentionally inserted (they steer
// GetPreferred); exclusion happens on the quorum/support read paths.
func (vt *ValidationTracker) updateTrieLocked(nodeID consensus.NodeID, validation *consensus.Validation, preResolved ledgertrie.Ledger, prior *acquiringKey) {
	if vt.trie == nil || vt.ancestry == nil {
		return
	}

	if prior != nil {
		vt.unparkLocked(*prior, nodeID)
	}
	vt.checkAcquiredLocked()

	key := acquiringKey{seq: validation.LedgerSeq, id: validation.LedgerID}
	if parked, ok := vt.acquiring[key]; ok {
		parked[nodeID] = struct{}{}
		return
	}

	lgr := preResolved
	if lgr == nil || lgr.ID() != validation.LedgerID {
		var ok bool
		lgr, ok = vt.ancestry.LedgerByID(validation.LedgerID)
		if !ok {
			// Park until acquisition lands. The fetch itself is armed by
			// the router on every trusted current validation
			// (maybeAcquireFromValidation); replay happens on the next
			// checkAcquiredLocked poll.
			vt.parkLocked(key, nodeID)
			return
		}
	}
	vt.insertTipLocked(nodeID, lgr)
}

// checkAcquiredLocked replays parked validations whose ledger has become
// locally resolvable into the trie (rippled checkAcquired). Polled from
// updateTrieLocked on every trusted validation and from GetPreferred.
// Caller must hold vt.mu (write).
func (vt *ValidationTracker) checkAcquiredLocked() {
	for key, nodes := range vt.acquiring {
		lgr, ok := vt.ancestry.LedgerByID(key.id)
		if !ok {
			continue
		}
		for nodeID := range nodes {
			vt.insertTipLocked(nodeID, lgr)
		}
		delete(vt.acquiring, key)
	}
}

// parkLocked records nodeID as waiting on key's ledger acquisition.
// Caller must hold vt.mu (write).
func (vt *ValidationTracker) parkLocked(key acquiringKey, nodeID consensus.NodeID) {
	parked, ok := vt.acquiring[key]
	if !ok {
		parked = make(map[consensus.NodeID]struct{})
		vt.acquiring[key] = parked
	}
	parked[nodeID] = struct{}{}
}

// unparkLocked removes nodeID from key's parked set, dropping the entry
// when it empties. No-op if the entry or node is absent. Caller must
// hold vt.mu (write).
func (vt *ValidationTracker) unparkLocked(key acquiringKey, nodeID consensus.NodeID) {
	parked, ok := vt.acquiring[key]
	if !ok {
		return
	}
	delete(parked, nodeID)
	if len(parked) == 0 {
		delete(vt.acquiring, key)
	}
}

// insertTipLocked replaces nodeID's previous trie tip (if any) with lgr.
// Caller must hold vt.mu (write).
func (vt *ValidationTracker) insertTipLocked(nodeID consensus.NodeID, lgr ledgertrie.Ledger) {
	if prev, existed := vt.trieTips[nodeID]; existed {
		safeTrieCall("Remove", func() { vt.trie.Remove(prev, 1) })
	}
	safeTrieCall("Insert", func() { vt.trie.Insert(lgr, 1) })
	vt.trieTips[nodeID] = lgr
}

// genesisLedger is the trie's root placeholder. The trie only reads
// Ancestor(0) and Seq()==0 from it.
type genesisLedger struct{}

func (genesisLedger) ID() consensus.LedgerID               { return consensus.LedgerID{} }
func (genesisLedger) Seq() uint32                          { return 0 }
func (genesisLedger) MinSeq() uint32                       { return 0 }
func (genesisLedger) Ancestor(s uint32) consensus.LedgerID { return consensus.LedgerID{} }
