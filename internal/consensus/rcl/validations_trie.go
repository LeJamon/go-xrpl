package rcl

import (
	"log/slog"
	"reflect"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
)

// LedgerAncestryProvider resolves a LedgerID to a ledgertrie.Ledger
// carrying its ancestry. Returns (nil, false) when the ledger's
// history is not locally known.
type LedgerAncestryProvider interface {
	LedgerByID(id consensus.LedgerID) (ledgertrie.Ledger, bool)
}

type retryableAncestryLedger interface {
	retryable() bool
}

// ancestryResolution is the provider result plus metadata captured while the
// provider boundary is outside ValidationTracker.mu. Callers holding the
// tracker lock must use these values instead of invoking methods on the
// provider-owned ledger again.
type ancestryResolution struct {
	ledger    ledgertrie.Ledger
	id        consensus.LedgerID
	seq       uint32
	retryable bool
}

// safeAncestryCall contains panics from provider code and custom ledger
// metadata methods. Provider and ledger objects are external to the tracker,
// so a malformed implementation must degrade to an unresolved result rather
// than unwind a consensus goroutine.
func safeAncestryCall(fn string, op func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			slog.Error("ancestry provider panic recovered",
				"t", "consensus",
				"event", "ancestry-provider-panic",
				"fn", fn,
				"err", r,
			)
		}
	}()
	op()
	return false
}

// resolveAncestry performs a provider lookup and captures retryable/ID/Seq
// metadata without holding ValidationTracker.mu. A result is usable only when
// the provider returned the requested ID and, when expectedSeq is non-nil, the
// exact requested sequence. Nil, typed-nil, panicking, and mismatched results
// are all treated as unresolved so callers park or use their flat fallback.
func resolveAncestry(
	provider LedgerAncestryProvider,
	expectedID consensus.LedgerID,
	expectedSeq *uint32,
) (resolved ancestryResolution, ok bool) {
	if isNilInterface(provider) {
		return ancestryResolution{}, false
	}
	var ledger ledgertrie.Ledger
	var found bool
	if safeAncestryCall("LedgerByID", func() {
		ledger, found = provider.LedgerByID(expectedID)
	}) || !found || !isValidAncestryLedger(ledger) {
		return ancestryResolution{}, false
	}
	resolved.ledger = ledger
	if safeAncestryCall("LedgerMetadata", func() {
		resolved.id = ledger.ID()
		resolved.seq = ledger.Seq()
		if partial, hasRetry := ledger.(retryableAncestryLedger); hasRetry {
			resolved.retryable = partial.retryable()
		}
	}) {
		return ancestryResolution{}, false
	}
	if resolved.id != expectedID || expectedSeq != nil && resolved.seq != *expectedSeq {
		return ancestryResolution{}, false
	}
	return resolved, true
}

// isNilInterface catches both a nil interface and an interface containing a
// typed nil pointer. Providers are external to the tracker and must not be
// able to make an ok=true result panic a read path.
func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func isValidAncestryLedger(lgr ledgertrie.Ledger) bool {
	return !isNilInterface(lgr)
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
	if isNilInterface(p) {
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
	preResolved ancestryResolution,
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
	if preResolved.ledger != nil && !preResolved.retryable &&
		preResolved.id == validation.LedgerID &&
		preResolved.seq == validation.LedgerSeq && vt.trie == preResolvedTrie {
		if parked, ok := vt.acquiring[key]; ok {
			parked[nodeID] = struct{}{}
			for parkedNode := range parked {
				current := vt.byNode[parkedNode]
				if current == nil || current.LedgerSeq != key.seq || current.LedgerID != key.id || !vt.trusted[parkedNode] {
					continue
				}
				if !vt.insertTipLocked(parkedNode, preResolved.ledger) {
					return
				}
			}
			delete(vt.acquiring, key)
			return
		}
		vt.insertTipLocked(nodeID, preResolved.ledger)
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

	resolved := make(map[acquiringKey]ancestryResolution, len(keys))
	for _, key := range keys {
		if lgr, ok := resolveAncestry(ancestry, key.id, &key.seq); ok && !lgr.retryable {
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
			if !vt.insertTipLocked(nodeID, lgr.ledger) {
				return
			}
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
func (vt *ValidationTracker) insertTipLocked(nodeID consensus.NodeID, lgr ledgertrie.Ledger) bool {
	if vt.trie == nil || lgr == nil {
		return false
	}
	if prev, existed := vt.trieTips[nodeID]; existed {
		if safeTrieCall("Remove", func() { vt.trie.Remove(prev, 1) }) {
			vt.resetTrieLocked()
			return false
		}
		delete(vt.trieTips, nodeID)
	}
	if safeTrieCall("Insert", func() { vt.trie.Insert(lgr, 1) }) {
		vt.resetTrieLocked()
		return false
	}
	vt.trieTips[nodeID] = lgr
	return true
}

// resetTrieLocked discards all derived trie state after a mutation panic.
// The tracked validation maps are authoritative, so rebuildTrieLocked can
// safely re-park every trusted latest validation. The next checkAcquired poll
// resolves available ledgers and repopulates the replacement trie.
// Caller must hold vt.mu.
func (vt *ValidationTracker) resetTrieLocked() {
	if vt.ancestry == nil {
		if vt.trie != nil {
			vt.trie = ledgertrie.New(genesisLedger{})
		}
		vt.trieTips = make(map[consensus.NodeID]ledgertrie.Ledger)
		vt.acquiring = make(map[acquiringKey]map[consensus.NodeID]struct{})
		return
	}
	vt.rebuildTrieLocked()
}

// removeTipLocked removes a validator's derived trie tip while keeping the
// trieTips index coupled to the trie. A panic can leave ledgertrie partially
// mutated, so discard and rebuild the entire derived state instead of
// continuing against a corrupted instance.
// Caller must hold vt.mu.
func (vt *ValidationTracker) removeTipLocked(nodeID consensus.NodeID) bool {
	if vt.trie == nil {
		delete(vt.trieTips, nodeID)
		return true
	}
	prev, ok := vt.trieTips[nodeID]
	if !ok {
		return true
	}
	if safeTrieCall("Remove", func() { vt.trie.Remove(prev, 1) }) {
		vt.resetTrieLocked()
		return false
	}
	delete(vt.trieTips, nodeID)
	return true
}

// GetJSONTrie returns a JSON-serializable snapshot of the ancestry trie's
// support state for diagnosing preferred-ledger divergence. Returns nil when
// the trie is disabled (no ancestry provider wired) or a serialization panic
// is trapped. Guarded by vt.mu.
func (vt *ValidationTracker) GetJSONTrie() map[string]any {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	if vt.trie == nil {
		return nil
	}
	var res map[string]any
	if safeTrieCall("GetJSON", func() { res = vt.trie.GetJSON() }) {
		vt.resetTrieLocked()
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
