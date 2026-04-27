package rcl

import (
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/ledgertrie"
)

// ValidationTracker tracks validations and determines ledger finality.
type ValidationTracker struct {
	mu sync.RWMutex

	// now returns the clock used for freshness checks in isCurrent.
	// Defaults to time.Now; the engine rewires it to adaptor.Now so
	// freshness comparisons honor the network-adjusted close offset —
	// mirrors rippled's app_.timeKeeper().closeTime() call in
	// Validations.h. Using wall time.Now against a validator's own
	// freshly-signed SignTime (which uses adaptor.Now) would reject
	// the self-add by exactly the accumulated offset.
	now func() time.Time

	// validations maps ledger ID to validations for that ledger
	validations map[consensus.LedgerID]map[consensus.NodeID]*consensus.Validation

	// byNode maps node ID to their latest validation
	byNode map[consensus.NodeID]*consensus.Validation

	// trusted is the set of trusted validators
	trusted map[consensus.NodeID]bool

	// negUNL is the set of validators disabled via the negative-UNL
	// mechanism. They contribute to the UNL size accounting but do NOT
	// count toward the full-validation quorum for a ledger — matching
	// rippled's LedgerMaster.cpp:886,952 where negative-UNL entries
	// are filtered before the quorum comparison.
	negUNL map[consensus.NodeID]bool

	// quorum is the number of validations needed for finality
	quorum int

	// freshness is how long validations are considered fresh
	freshness time.Duration

	// fired records ledgers we've already reported as fully validated,
	// so the callback fires exactly once per ledger even if more
	// validations keep arriving after the threshold is crossed.
	fired map[consensus.LedgerID]struct{}

	// minSeq is the sequence floor for accepting new validations.
	// Validations with LedgerSeq < minSeq are rejected in Add(). The
	// gate prevents a flood of far-stale validations from a broken peer
	// from inflating memory or tripping old-ledger quorum firings.
	// Caller (the engine) advances minSeq as ledgers accept.
	minSeq uint32

	// resolver translates a validator's signing public key to its
	// master public key via the manifest cache. Applied at Add() so
	// every internal map is keyed by master — matching rippled's
	// RCLValidations.cpp:165-186 which calls calcNodeID(masterKey ??
	// signingKey) before handing off to Validations::add. When the
	// signing key has no manifest mapping the resolver returns the
	// input unchanged, so non-validator peers still round-trip
	// correctly. Default is the identity function (tests that never
	// wire a cache keep working).
	resolver func(consensus.NodeID) consensus.NodeID

	// callbacks
	onFullyValidated func(ledgerID consensus.LedgerID, ledgerSeq uint32)

	// onStale is fired once per validation dropped by ExpireOld, after
	// the tracker's internal maps have been mutated but before returning
	// to the caller. Invoked outside vt.mu so callbacks (e.g. the archive
	// writer's channel send) may do I/O without risking lock-order
	// inversion. Nil means "no archive wired."
	onStale func(*consensus.Validation)

	// ancestry resolves LedgerID → ancestry for the trie. nil disables
	// the trie; the tracker then falls back to flat hash-count support.
	ancestry LedgerAncestryProvider

	// trie holds branchSupport for trusted-and-not-negUNL validators'
	// latest tips. nil when ancestry is unset.
	trie *ledgertrie.Trie

	// trieTips records each validator's current trie tip so a newer
	// validation can remove the old before inserting the new.
	trieTips map[consensus.NodeID]ledgertrie.Ledger
}

// NewValidationTracker creates a new validation tracker.
// The tracker's freshness clock defaults to time.Now; wire it to
// adaptor.Now via SetNow before use so isCurrent honors the network
// close-time offset.
func NewValidationTracker(quorum int, freshness time.Duration) *ValidationTracker {
	return &ValidationTracker{
		now:         time.Now,
		validations: make(map[consensus.LedgerID]map[consensus.NodeID]*consensus.Validation),
		byNode:      make(map[consensus.NodeID]*consensus.Validation),
		trusted:     make(map[consensus.NodeID]bool),
		negUNL:      make(map[consensus.NodeID]bool),
		quorum:      quorum,
		freshness:   freshness,
		fired:       make(map[consensus.LedgerID]struct{}),
		resolver:    func(n consensus.NodeID) consensus.NodeID { return n },
	}
}

// SetManifestResolver installs a function that translates a validator's
// signing public key to its master public key. Applied at Add() before
// the NodeID is used as a map key, so quorum / trusted-set / negUNL
// lookups operate on master keys even when the validator has rotated
// its ephemeral signing key. Pass nil to reset to the identity
// function (no translation). Safe to call at any time.
func (vt *ValidationTracker) SetManifestResolver(fn func(consensus.NodeID) consensus.NodeID) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	if fn == nil {
		vt.resolver = func(n consensus.NodeID) consensus.NodeID { return n }
		return
	}
	vt.resolver = fn
}

// SetNow replaces the clock used by isCurrent for freshness checks.
// Production use: wire to adaptor.Now so the freshness window honors
// the network-adjusted close offset (matches rippled's
// app_.timeKeeper().closeTime() usage in Validations.h). Tests pass
// fixed-time functions to get deterministic accept/reject behavior.
// Passing nil resets to time.Now.
func (vt *ValidationTracker) SetNow(fn func() time.Time) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	if fn == nil {
		vt.now = time.Now
		return
	}
	vt.now = fn
}

// SetTrusted updates the set of trusted validators and rebuilds the
// trie if wired so de-trusted validators stop contributing support.
func (vt *ValidationTracker) SetTrusted(nodes []consensus.NodeID) {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	vt.trusted = make(map[consensus.NodeID]bool)
	for _, node := range nodes {
		vt.trusted[node] = true
	}
	vt.rebuildTrieLocked()
}

// SetQuorum updates the quorum requirement.
func (vt *ValidationTracker) SetQuorum(quorum int) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.quorum = quorum
}

// SetNegativeUNL replaces the current negative-UNL set. Validators on
// the negative-UNL are still considered trusted for message acceptance
// but are excluded from the quorum count in checkFullValidation —
// matching rippled's behavior of disabling temporarily-offline
// validators without removing them from the config. Pass nil or an
// empty slice to clear the negUNL.
func (vt *ValidationTracker) SetNegativeUNL(nodes []consensus.NodeID) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.negUNL = make(map[consensus.NodeID]bool, len(nodes))
	for _, n := range nodes {
		vt.negUNL[n] = true
	}
	vt.rebuildTrieLocked()
}

// SetMinSeq advances the sequence floor below which incoming
// validations are rejected. Called by the engine after a ledger is
// accepted to discard far-stale validations without holding them in
// memory. Passing a value <= current minSeq is a no-op.
func (vt *ValidationTracker) SetMinSeq(seq uint32) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	if seq > vt.minSeq {
		vt.minSeq = seq
	}
}

// SetFullyValidatedCallback sets the callback for when a ledger is fully validated.
// Fired once per ledger the first time trusted-validation count crosses the quorum
// threshold. Seq is passed alongside the ledger ID so the callee can look up or
// stamp the ledger without a secondary map lookup.
func (vt *ValidationTracker) SetFullyValidatedCallback(fn func(ledgerID consensus.LedgerID, ledgerSeq uint32)) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.onFullyValidated = fn
}

// SetOnStale installs a callback invoked once per validation dropped by
// ExpireOld. Mirrors rippled's Validations<Adaptor>::onStale contract —
// consumed by the on-disk validation archive to persist stale validations
// before they leave memory. Callback runs outside the tracker's mutex so it
// may do blocking work (channel send to a batched writer); callers must
// ensure it does not call back into the tracker. Pass nil to disable.
func (vt *ValidationTracker) SetOnStale(fn func(*consensus.Validation)) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.onStale = fn
}

// Validation freshness windows mirror rippled's Validations.h:626
// isCurrent gate:
//   - validationCurrentWall: SignTime must be within this window of
//     wall-clock NOW (both past and future). A validation signed too
//     long ago is stale; one signed too far ahead is either clock-
//     skewed or forged. Rippled default: 5 minutes.
//   - validationCurrentLocal: SeenTime must be within this window of
//     wall-clock NOW (local-clock sanity). Rippled default: 3 minutes.
//   - validationCurrentEarly: negative — how far into the FUTURE a
//     SignTime may drift before we reject. Separately bounded because
//     the forward bound is normally tighter than the backward one.
//     Rippled default: 3 minutes.
const (
	validationCurrentWall  = 5 * time.Minute
	validationCurrentLocal = 3 * time.Minute
	validationCurrentEarly = 3 * time.Minute
)

// isCurrent reports whether a validation's sign-time and seen-time are
// close enough to now to be considered "current" in rippled's sense.
// Exact mirror of Validations.h:148-166 isCurrent:
//
//	signTime > (now - validationCURRENT_EARLY) &&
//	signTime < (now + validationCURRENT_WALL) &&
//	(seenTime == 0 || seenTime < (now + validationCURRENT_LOCAL))
//
// Note on constant names: rippled's EARLY bounds the PAST on signTime
// (not "early" in the usual sense of future-side); WALL bounds the
// FUTURE on signTime. The prior goXRPL implementation had the two
// swapped and used a past-bound on seenTime — three wire-parity bugs
// that would desync freshness decisions between Go and rippled peers
// under clock skew.
//
// `now` is the network-adjusted time from the adaptor so the freshness
// window honors the close-offset consensus has converged on.
func isCurrent(now, signTime, seenTime time.Time) bool {
	// Past bound on signTime (rippled uses EARLY=3m here, NOT WALL=5m):
	// a validation signed more than EARLY in the past is stale —
	// interoperating peers already moved on.
	if !signTime.After(now.Add(-validationCurrentEarly)) {
		return false
	}
	// Future bound on signTime (rippled uses WALL=5m, NOT EARLY=3m):
	// a validation signed beyond WALL in the future indicates clock
	// skew or forgery.
	if !signTime.Before(now.Add(validationCurrentWall)) {
		return false
	}
	// Future bound on seenTime (rippled uses LOCAL=3m): detects a peer
	// with a fast local clock queuing validations "from the future"
	// and dumping them on us. SeenTime == 0 for self-built validations
	// — skip the check since there's no delivery moment to bound.
	if !seenTime.IsZero() && !seenTime.Before(now.Add(validationCurrentLocal)) {
		return false
	}
	return true
}

// Add adds a validation to the tracker.
// Returns true if this is a new validation (not duplicate).
//
// Inbound filters match rippled's LedgerMaster.cpp:886,952 and
// Validations.h:626 isCurrent:
//   - Only Full validations count toward quorum. Partial validations
//     (Full=false) indicate a node that hasn't applied the ledger
//     yet and can't attest to its state root. We drop them entirely.
//   - Stale or clock-skewed validations (outside the wall/local
//     windows defined above) are rejected via isCurrent.
//   - Validations with seq below minSeq are rejected. Once a ledger
//     accepts, validations for seqs many rounds back are noise that
//     can never retroactively become quorum; keeping them in memory
//     wastes work on every checkFullValidation pass.
//   - Per-node newer-seq-only rule: a node's latest validation
//     supersedes any earlier one. Same as before.
//
// onFullyValidated is fired OUTSIDE vt.mu so the callback may take other
// locks (engine.mu, archive channel send) or call back into the tracker
// (e.g. ExpireOld) without deadlocking. Mirrors the lock-free callback
// dispatch ExpireOld already uses for onStale.
//
// Defer order is LIFO: vt.mu.Unlock runs first (released before the
// callback), then the captured fire-tuple is dispatched.
func (vt *ValidationTracker) Add(validation *consensus.Validation) bool {
	var (
		fireID     consensus.LedgerID
		fireSeq    uint32
		shouldFire bool
		cb         func(consensus.LedgerID, uint32)
	)
	defer func() {
		if shouldFire && cb != nil {
			cb(fireID, fireSeq)
		}
	}()

	// Pre-resolve ancestry outside vt.mu — cold-LRU walks would
	// otherwise serialise concurrent Add()s behind us.
	vt.mu.RLock()
	ancestrySnap := vt.ancestry
	trieEnabled := vt.trie != nil
	vt.mu.RUnlock()
	var preResolvedLedger ledgertrie.Ledger
	if trieEnabled && ancestrySnap != nil {
		if l, ok := ancestrySnap.LedgerByID(validation.LedgerID); ok {
			preResolvedLedger = l
		}
	}

	vt.mu.Lock()
	defer vt.mu.Unlock()

	// Reject partial validations — a node signaling Full=false has not
	// fully applied the ledger and its signature doesn't anchor the
	// state root. Rippled's LedgerMaster.cpp:886 filters the same way.
	if !validation.Full {
		return false
	}

	// Freshness window. Rejects validations signed too long ago or
	// too far in the future (clock-skewed / forged) and validations
	// delivered to us after too much local-clock drift. Without this
	// gate a peer can keep year-old validations alive as long as the
	// sequence number is in range — pointless memory + noise.
	//
	// Uses vt.now (wired to adaptor.Now by the engine) rather than
	// raw time.Now so the check honors the network-adjusted close
	// offset and doesn't reject our own just-signed validations on a
	// clock-skewed node.
	if !isCurrent(vt.now(), validation.SignTime, validation.SeenTime) {
		return false
	}

	// Reject far-stale validations below the sequence floor.
	if vt.minSeq > 0 && validation.LedgerSeq < vt.minSeq {
		return false
	}

	// Resolve ephemeral signing key to master key via the manifest
	// resolver so every map below is keyed by master. A validator that
	// has rotated its ephemeral key still counts as one participant in
	// trusted / negUNL / quorum arithmetic. Mirrors rippled's
	// RCLValidations.cpp:165-186 calcNodeID(masterKey ?? signingKey).
	resolvedID := vt.resolver(validation.NodeID)

	// Check if this is a newer validation from this node
	existing, hasExisting := vt.byNode[resolvedID]
	if hasExisting {
		if validation.LedgerSeq <= existing.LedgerSeq {
			return false // Not newer, ignore
		}
	}

	// Update by-node tracking
	vt.byNode[resolvedID] = validation

	// Add to ledger validations
	ledgerVals, exists := vt.validations[validation.LedgerID]
	if !exists {
		ledgerVals = make(map[consensus.NodeID]*consensus.Validation)
		vt.validations[validation.LedgerID] = ledgerVals
	}
	ledgerVals[resolvedID] = validation

	if vt.trusted[resolvedID] && !vt.negUNL[resolvedID] {
		vt.updateTrieLocked(resolvedID, validation.LedgerID, preResolvedLedger)
	}

	// Capture the fire-tuple under the lock; the deferred dispatcher
	// invokes onFullyValidated after vt.mu.Unlock has run.
	fireID, fireSeq, shouldFire = vt.checkFullValidationLocked(validation.LedgerID)
	cb = vt.onFullyValidated
	return true
}

// checkFullValidationLocked records that a ledger crossed the quorum
// threshold (via vt.fired) and returns the (id, seq, shouldFire) tuple
// the caller needs to invoke onFullyValidated outside the lock.
//
// Fires (well, requests-fire-by) exactly once per ledger — the first
// time trusted count crosses the quorum threshold. Subsequent adds for
// the same ledger are ignored to avoid repeatedly flipping
// server_info's validated_ledger on every late-arriving peer validation.
//
// Zero-quorum edge case (empty UNL): requires at least one tracked
// validation for the ledger before firing, so we don't spuriously
// promote a ledger hash we haven't seen any validator sign.
//
// Negative-UNL filter: a validator on the negUNL is trusted for
// message acceptance but excluded from the quorum count here, matching
// rippled's LedgerMaster.cpp:952. Same-quorum with a validator
// temporarily disabled shouldn't require one MORE validation to finalize.
//
// Caller MUST hold vt.mu.
func (vt *ValidationTracker) checkFullValidationLocked(ledgerID consensus.LedgerID) (consensus.LedgerID, uint32, bool) {
	if vt.onFullyValidated == nil {
		return ledgerID, 0, false
	}
	if _, done := vt.fired[ledgerID]; done {
		return ledgerID, 0, false
	}
	ledgerVals, exists := vt.validations[ledgerID]
	if !exists || len(ledgerVals) == 0 {
		return ledgerID, 0, false
	}

	var sampleSeq uint32
	for _, v := range ledgerVals {
		sampleSeq = v.LedgerSeq
		break
	}
	trustedCount := vt.countTrustedExcludingNegUNLLocked(ledgerVals)

	if trustedCount >= vt.quorum {
		vt.fired[ledgerID] = struct{}{}
		return ledgerID, sampleSeq, true
	}
	return ledgerID, 0, false
}

// GetValidations returns all validations for a ledger.
func (vt *ValidationTracker) GetValidations(ledgerID consensus.LedgerID) []*consensus.Validation {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return nil
	}

	result := make([]*consensus.Validation, 0, len(ledgerVals))
	for _, v := range ledgerVals {
		result = append(result, v)
	}
	return result
}

// GetTrustedValidations returns trusted validations for a ledger.
func (vt *ValidationTracker) GetTrustedValidations(ledgerID consensus.LedgerID) []*consensus.Validation {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return nil
	}

	var result []*consensus.Validation
	for nodeID, v := range ledgerVals {
		if vt.trusted[nodeID] {
			result = append(result, v)
		}
	}
	return result
}

// GetValidationCount returns the count of validations for a ledger.
func (vt *ValidationTracker) GetValidationCount(ledgerID consensus.LedgerID) int {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return 0
	}
	return len(ledgerVals)
}

// GetTrustedValidationCount returns the count of trusted validations
// for a ledger, EXCLUDING validators currently on the negative UNL.
// Matches rippled's LedgerMaster.cpp:886,952,1120 where every trusted
// count flows through negativeUNLFilter before comparison — so any
// consumer of this method (quorum gate, server_info, future LedgerTrie
// port) sees consistent, filtered numbers.
func (vt *ValidationTracker) GetTrustedValidationCount(ledgerID consensus.LedgerID) int {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return 0
	}
	return vt.countTrustedExcludingNegUNLLocked(ledgerVals)
}

// countTrustedExcludingNegUNLLocked counts validators in ledgerVals
// that are trusted AND not on the negUNL. Caller must hold vt.mu.
func (vt *ValidationTracker) countTrustedExcludingNegUNLLocked(
	ledgerVals map[consensus.NodeID]*consensus.Validation,
) int {
	count := 0
	for nodeID := range ledgerVals {
		if vt.trusted[nodeID] && !vt.negUNL[nodeID] {
			count++
		}
	}
	return count
}

// GetTrustedSupport returns the trie's branchSupport for ledgerID —
// the count of trusted-and-not-negUNL validators committing to this
// ledger or any descendant. Falls back to the flat trusted count when
// the trie or ancestry is unavailable.
func (vt *ValidationTracker) GetTrustedSupport(ledgerID consensus.LedgerID) int {
	// Snapshot pointers, drop the lock for ancestry resolution, then
	// re-acquire for the cheap trie query.
	vt.mu.RLock()
	trie := vt.trie
	ancestry := vt.ancestry
	vt.mu.RUnlock()

	if trie == nil || ancestry == nil {
		return vt.GetTrustedValidationCount(ledgerID)
	}

	lgr, ok := ancestry.LedgerByID(ledgerID)
	if !ok {
		return vt.GetTrustedValidationCount(ledgerID)
	}

	vt.mu.RLock()
	defer vt.mu.RUnlock()
	// Trie may have been swapped while we resolved ancestry.
	if vt.trie != trie {
		ledgerVals, exists := vt.validations[ledgerID]
		if !exists {
			return 0
		}
		return vt.countTrustedExcludingNegUNLLocked(ledgerVals)
	}
	return int(trie.BranchSupport(lgr))
}

// GetPreferred returns the network-preferred ledger ID and sequence
// as decided by the ancestry trie. ok is false when the trie is not
// wired or empty. largestIssued is the highest sequence this node has
// already validated; it seeds uncommitted support from earlier seqs.
func (vt *ValidationTracker) GetPreferred(largestIssued uint32) (consensus.LedgerID, uint32, bool) {
	vt.mu.RLock()
	defer vt.mu.RUnlock()
	if vt.trie == nil {
		return consensus.LedgerID{}, 0, false
	}
	tip, ok := vt.trie.GetPreferred(largestIssued)
	if !ok {
		return consensus.LedgerID{}, 0, false
	}
	return tip.ID, tip.Seq, true
}

// IsFullyValidated returns true if the ledger has reached full
// validation. Uses the negUNL-filtered trusted count, so a ledger
// reaches full validation with the same quorum whether or not a
// validator happens to be temporarily disabled.
func (vt *ValidationTracker) IsFullyValidated(ledgerID consensus.LedgerID) bool {
	return vt.GetTrustedValidationCount(ledgerID) >= vt.quorum
}

// ProposersValidated returns the count of trusted validators whose
// MOST RECENT (highest-seq) full validation points at ledgerID. This
// is the peer-pressure signal rippled uses in shouldCloseLedger via
// adaptor_.proposersValidated(prevLedgerID_) at RCLConsensus.cpp:281.
//
// Reads the persistent byNode map (not the round-scoped validations
// map on the engine), so the signal is available from the moment a
// new round begins — before any current-round validations have
// arrived. negUNL'd validators are excluded, matching the quorum
// semantics at Validations.h:849-899.
func (vt *ValidationTracker) ProposersValidated(ledgerID consensus.LedgerID) int {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	count := 0
	for nodeID, v := range vt.byNode {
		if !vt.trusted[nodeID] {
			continue
		}
		if vt.negUNL[nodeID] {
			continue
		}
		if !v.Full {
			continue
		}
		if v.LedgerID == ledgerID {
			count++
		}
	}
	return count
}

// GetLatestValidation returns the latest validation from a node.
func (vt *ValidationTracker) GetLatestValidation(nodeID consensus.NodeID) *consensus.Validation {
	vt.mu.RLock()
	defer vt.mu.RUnlock()
	return vt.byNode[nodeID]
}

// GetCurrentValidators returns nodes that have recently validated.
// Uses the injected clock (vt.now) so tests and production share the
// same network-adjusted time source that Add() uses for its isCurrent
// check — keeps "recent" consistent across both reads and writes.
func (vt *ValidationTracker) GetCurrentValidators() []consensus.NodeID {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	cutoff := vt.now().Add(-vt.freshness)
	var result []consensus.NodeID

	for nodeID, v := range vt.byNode {
		if v.SignTime.After(cutoff) {
			result = append(result, nodeID)
		}
	}
	return result
}

// ExpireOld drops validations below minSeq from every index and fires
// onStale outside the mutex. Trie tips for dropped validators are also
// removed so phantom branchSupport doesn't linger on stale ancestors.
func (vt *ValidationTracker) ExpireOld(minSeq uint32) {
	vt.mu.Lock()

	onStale := vt.onStale
	var stale []*consensus.Validation

	for ledgerID, ledgerVals := range vt.validations {
		var sample *consensus.Validation
		for _, v := range ledgerVals {
			sample = v
			break
		}
		if sample == nil || sample.LedgerSeq >= minSeq {
			continue
		}
		for nodeID, v := range ledgerVals {
			stale = append(stale, v)
			if latest, ok := vt.byNode[nodeID]; ok && latest == v {
				delete(vt.byNode, nodeID)
				if vt.trie != nil {
					if prev, ok := vt.trieTips[nodeID]; ok {
						vt.trie.Remove(prev, 1)
						delete(vt.trieTips, nodeID)
					}
				}
			}
		}
		delete(vt.validations, ledgerID)
		delete(vt.fired, ledgerID)
	}

	vt.mu.Unlock()

	if onStale == nil {
		return
	}
	for _, v := range stale {
		onStale(v)
	}
}

// Clear removes all tracked validations.
func (vt *ValidationTracker) Clear() {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	vt.validations = make(map[consensus.LedgerID]map[consensus.NodeID]*consensus.Validation)
	vt.byNode = make(map[consensus.NodeID]*consensus.Validation)
	vt.fired = make(map[consensus.LedgerID]struct{})
}

// Stats returns statistics about tracked validations.
type ValidationStats struct {
	TotalValidations   int
	TrustedValidations int
	ValidatorsActive   int
	LedgersTracked     int
}

// GetStats returns current validation statistics.
func (vt *ValidationTracker) GetStats() ValidationStats {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	totalValidations := 0
	trustedValidations := 0

	for _, ledgerVals := range vt.validations {
		for nodeID := range ledgerVals {
			totalValidations++
			if vt.trusted[nodeID] {
				trustedValidations++
			}
		}
	}

	return ValidationStats{
		TotalValidations:   totalValidations,
		TrustedValidations: trustedValidations,
		ValidatorsActive:   len(vt.byNode),
		LedgersTracked:     len(vt.validations),
	}
}
