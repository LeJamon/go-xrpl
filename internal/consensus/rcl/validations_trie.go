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
	if p == nil {
		vt.ancestry = nil
		vt.trie = nil
		vt.trieTips = nil
		vt.acquiring = nil
		vt.mu.Unlock()
		return
	}
	vt.ancestry = p
	vt.rebuildTrieLocked()
	vt.mu.Unlock()
	vt.checkAcquired()
}

// rebuildTrieLocked resets the trie and reseeds it from byNode.
// Caller must hold vt.mu (write); no-op if ancestry is unset.
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
		vt.parkLocked(acquiringKey{seq: v.LedgerSeq, id: v.LedgerID}, nodeID)
	}
}

// updateTrieLocked places validation's ledger as nodeID's trie tip, or
// parks the validation until the ledger is acquired (rippled updateTrie,
// Validations.h:431-469). The node's previous tip keeps steering the trie
// while its latest validation is parked. Silent no-op if the trie is
// unavailable.
//
// preResolved is the ledger Add() walked outside vt.mu. preResolvedTrie
// identifies the trie that was current during that lookup.
// prior is the (seq, id) of the node's superseded validation, cleared
// from any parked entry first.
//
// Precondition: caller holds vt.mu (write) and has verified nodeID is
// trusted. negUNL validators are intentionally inserted (they steer
// GetPreferred); exclusion happens on the quorum/support read paths.
func (vt *ValidationTracker) updateTrieLocked(
	nodeID consensus.NodeID,
	validation *consensus.Validation,
	preResolved ledgertrie.Ledger,
	preResolvedTrie *ledgertrie.Trie,
	prior *acquiringKey,
) {
	if vt.trie == nil || vt.ancestry == nil {
		return
	}

	if prior != nil {
		vt.unparkLocked(*prior, nodeID)
	}
	key := acquiringKey{seq: validation.LedgerSeq, id: validation.LedgerID}
	if preResolved != nil && preResolved.ID() == validation.LedgerID &&
		preResolved.Seq() == validation.LedgerSeq && vt.trie == preResolvedTrie {
		if parked, ok := vt.acquiring[key]; ok {
			parked[nodeID] = struct{}{}
			for parkedNode := range parked {
				current := vt.byNode[parkedNode]
				if current == nil || current.LedgerSeq != key.seq || current.LedgerID != key.id || !vt.trusted[parkedNode] {
					continue
				}
				vt.insertTipLocked(parkedNode, preResolved)
			}
			delete(vt.acquiring, key)
			return
		}
		vt.insertTipLocked(nodeID, preResolved)
		return
	}
	vt.parkLocked(key, nodeID)
}

// checkAcquired replays parked validations whose ledgers are now locally
// available. Resolution runs outside vt.mu because the production ancestry
// provider enters the ledger service.
func (vt *ValidationTracker) checkAcquired() {
	vt.mu.RLock()
	if vt.trie == nil || vt.ancestry == nil || len(vt.acquiring) == 0 {
		vt.mu.RUnlock()
		return
	}
	trie := vt.trie
	ancestry := vt.ancestry
	keys := make([]acquiringKey, 0, len(vt.acquiring))
	for key := range vt.acquiring {
		keys = append(keys, key)
	}
	vt.mu.RUnlock()

	resolved := make(map[acquiringKey]ledgertrie.Ledger, len(keys))
	for _, key := range keys {
		if lgr, ok := ancestry.LedgerByID(key.id); ok && lgr.ID() == key.id && lgr.Seq() == key.seq {
			resolved[key] = lgr
		}
	}
	if len(resolved) == 0 {
		return
	}

	vt.mu.Lock()
	defer vt.mu.Unlock()
	if vt.trie != trie {
		return
	}
	for key, lgr := range resolved {
		nodes, ok := vt.acquiring[key]
		if !ok {
			continue
		}
		for nodeID := range nodes {
			current := vt.byNode[nodeID]
			if current == nil || current.LedgerSeq != key.seq || current.LedgerID != key.id || !vt.trusted[nodeID] {
				continue
			}
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
		if safeTrieCall("Remove", func() { vt.trie.Remove(prev, 1) }) {
			return
		}
		delete(vt.trieTips, nodeID)
	}
	if safeTrieCall("Insert", func() { vt.trie.Insert(lgr, 1) }) {
		return
	}
	vt.trieTips[nodeID] = lgr
}

// GetJSONTrie returns a JSON-serializable snapshot of the ancestry trie's
// support state for diagnosing preferred-ledger divergence. Returns nil when
// the trie is disabled (no ancestry provider wired) or a serialization panic
// is trapped. Guarded by vt.mu.
func (vt *ValidationTracker) GetJSONTrie() map[string]any {
	vt.mu.RLock()
	defer vt.mu.RUnlock()
	if vt.trie == nil {
		return nil
	}
	var res map[string]any
	if safeTrieCall("GetJSON", func() { res = vt.trie.GetJSON() }) {
		return nil
	}
	return res
}

// genesisLedger is the trie's root placeholder. The trie only reads
// Ancestor(0) and Seq()==0 from it.
type genesisLedger struct{}

func (genesisLedger) ID() consensus.LedgerID               { return consensus.LedgerID{} }
func (genesisLedger) Seq() uint32                          { return 0 }
func (genesisLedger) MinSeq() uint32                       { return 0 }
func (genesisLedger) Ancestor(s uint32) consensus.LedgerID { return consensus.LedgerID{} }
