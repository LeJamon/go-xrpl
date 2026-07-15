package rcl

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
)

// safeTrieCall runs fn under a deferred recover so a ledgertrie
// invariant panic (LedgerTrie.h:553-style XRPL_ASSERT or our equivalent
// at trie.go:143/173/306) does not fail-stop the consensus goroutine.
// rippled responds to these with a process abort; in Go we'd rather
// log + continue and let the next operation rebuild the trie on its
// own. Returns true if the call panicked. Caller must already hold the
// lock that protects the trie state.
func safeTrieCall(fn string, op func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			slog.Error("ledgertrie panic recovered",
				"t", "consensus",
				"event", "trie-panic",
				"fn", fn,
				"err", r,
			)
		}
	}()
	op()
	return false
}

// ValStatus classifies the outcome of AddStatus, mirroring rippled's
// ValStatus (Validations.h:169-180).
type ValStatus int

const (
	// ValStatusCurrent — added; counts toward quorum and steers the trie.
	ValStatusCurrent ValStatus = iota
	// ValStatusStale — outside the freshness window, below the sequence
	// floor, or superseded by the node's tracked tip.
	ValStatusStale
	// ValStatusBadSeq — violates the increasing-seq requirement without
	// double-sign evidence.
	ValStatusBadSeq
	// ValStatusMultiple — same seq and ledger signed with different
	// cookies (likely two servers sharing a validator key).
	ValStatusMultiple
	// ValStatusConflicting — same seq signed for different ledgers, or
	// re-signed with a different sign time.
	ValStatusConflicting
)

func (s ValStatus) String() string {
	switch s {
	case ValStatusCurrent:
		return "current"
	case ValStatusStale:
		return "stale"
	case ValStatusBadSeq:
		return "badSeq"
	case ValStatusMultiple:
		return "multiple"
	case ValStatusConflicting:
		return "conflicting"
	default:
		return "unknown"
	}
}

// seqEnforcer tracks the largest validation seq a node issued within the
// validationSetExpires window; the floor resets after that long idle. A
// validation must exceed the floor to be considered monotonic.
type seqEnforcer struct {
	seq  uint32
	when time.Time
}

// advance reports whether seq exceeds the non-expired floor, bumping the
// floor when it does.
func (s *seqEnforcer) advance(now time.Time, seq uint32) bool {
	if now.Sub(s.when) > validationSetExpires {
		s.seq = 0
	}
	if seq <= s.seq {
		return false
	}
	s.seq = seq
	s.when = now
	return true
}

// seqValidations is one bySequence bucket: the validation each node
// signed at a given seq, plus the last-access time driving expiry.
type seqValidations struct {
	touched time.Time
	byNode  map[consensus.NodeID]*consensus.Validation
}

// ledgerValidations is one per-ledger validation set plus the
// access-age bookkeeping ExpireOld needs. lastAccess holds unix-nanos
// of the set's creation or last read touch — writes into an existing
// set do not refresh it, matching rippled's aged byLedger_ container,
// which stamps on insert and touches on read only. Stored atomically
// so query paths holding only vt.mu.RLock can refresh it. Aging
// deliberately follows the tracker clock (adaptor.Now in production)
// rather than rippled's monotonic steady_clock: touch and the
// ExpireOld cutoff share that clock — consistent with the engine's
// other validationSetExpires windows — so a close-offset adjustment
// shifts retention by at most the adjustment.
type ledgerValidations struct {
	vals       map[consensus.NodeID]*consensus.Validation
	lastAccess atomic.Int64
}

func (lv *ledgerValidations) touch(now time.Time) {
	lv.lastAccess.Store(now.UnixNano())
}

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
	validations map[consensus.LedgerID]*ledgerValidations

	// byNode maps node ID to their latest validation
	byNode map[consensus.NodeID]*consensus.Validation

	// bySequence tracks, per ledger seq, the validation each node signed
	// at that seq — the evidence the Byzantine cross-check runs against
	// when a node submits a non-monotonic seq, so equivocation is caught
	// even at seqs the node has already superseded. Buckets age out after
	// validationSetExpires without access (FlushStale sweep).
	bySequence map[uint32]*seqValidations

	// seqEnforcers holds the per-remote-node monotonic-seq floor with
	// idle reset. Distinct from the engine's local enforcer, which gates
	// what WE sign; these gate what each remote node may submit.
	seqEnforcers map[consensus.NodeID]*seqEnforcer

	// trusted is the set of trusted validators
	trusted map[consensus.NodeID]bool

	// negUNL is the set of validators disabled via the negative-UNL
	// mechanism. They do NOT count toward the full-validation quorum for
	// a ledger, nor toward the quorum/peer-LCL support the engine reads
	// via GetTrustedSupport — matching rippled, which filters negative-UNL
	// entries before every quorum comparison. They DO still steer
	// preferred-ledger selection through the trie: rippled's updateTrie
	// gates only on trusted(), so a deemed-offline validator still
	// contributes branch support to GetPreferred / getNodesAfter.
	negUNL map[consensus.NodeID]bool

	// quorum is the number of validations needed for finality
	quorum int

	// quorumUnavailable is a live safety gate. It closes the brief window
	// between a publisher-status change and installation of the corresponding
	// trusted/quorum snapshot.
	quorumUnavailable func() bool

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

	// keepLow, keepHigh pin the sequence range [keepLow, keepHigh) against
	// ExpireOld: validations in it survive even below the retention floor and
	// past their access age. The negative-UNL vote sets it via SetSeqToKeep so
	// a fast-advancing tip (whose floor is anchored at the validated tip, not
	// the flag ledger) can't prune the flag-ledger scan window mid-vote.
	// keepLow == keepHigh disables the pin. Mirrors rippled's toKeep_.
	keepLow  uint32
	keepHigh uint32

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

	// trie holds branchSupport for every trusted validator's latest tip,
	// INCLUDING validators on the negUNL — mirroring rippled's
	// trusted()-only updateTrie so negUNL validators still steer
	// GetPreferred / ProposersFinished. The negUNL-excluded count the
	// engine's quorum/peer-LCL gates need is derived separately in
	// branchSupportExcludingNegUNLLocked. nil when ancestry is unset.
	trie *ledgertrie.Trie

	// trieTips records each validator's current trie tip so a newer
	// validation can remove the old before inserting the new.
	trieTips map[consensus.NodeID]ledgertrie.Ledger

	// acquiring parks trusted validations whose ledger isn't locally
	// resolvable yet, keyed by (seq, id) → waiting validators — rippled's
	// acquiring_ map. Entries drain via checkAcquiredLocked once the
	// ledger is acquired, and expire with the validations that reference
	// them (supersede, ExpireOld, FlushStale, trust rotation). nil when
	// the trie is disabled.
	acquiring map[acquiringKey]map[consensus.NodeID]struct{}
}

// NewValidationTracker creates a new validation tracker.
// The tracker's freshness clock defaults to time.Now; wire it to
// adaptor.Now via SetNow before use so isCurrent honors the network
// close-time offset.
func NewValidationTracker(quorum int, freshness time.Duration) *ValidationTracker {
	return &ValidationTracker{
		now:          time.Now,
		validations:  make(map[consensus.LedgerID]*ledgerValidations),
		byNode:       make(map[consensus.NodeID]*consensus.Validation),
		bySequence:   make(map[uint32]*seqValidations),
		seqEnforcers: make(map[consensus.NodeID]*seqEnforcer),
		trusted:      make(map[consensus.NodeID]bool),
		negUNL:       make(map[consensus.NodeID]bool),
		quorum:       quorum,
		freshness:    freshness,
		fired:        make(map[consensus.LedgerID]struct{}),
	}
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

	vt.setTrustedLocked(nodes)
}

// SetTrustedAndQuorum updates the trusted set and its quorum atomically.
func (vt *ValidationTracker) SetTrustedAndQuorum(nodes []consensus.NodeID, quorum int) {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	vt.setTrustedLocked(nodes)
	vt.quorum = quorum
}

func (vt *ValidationTracker) setTrustedLocked(nodes []consensus.NodeID) {
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

// SetQuorumUnavailableFunc installs the live finality safety gate.
func (vt *ValidationTracker) SetQuorumUnavailableFunc(fn func() bool) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.quorumUnavailable = fn
}

// SetSeqToKeep pins validations in [low, high) so ExpireOld will not drop
// them, even once the retention floor advances past them. The negative-UNL
// vote calls this before scanning the flag-ledger window so a fast-advancing
// tip can't prune its low end mid-scan. Unlike the seq floor — which is
// anchored at the validated tip — the pin is anchored at the flag ledger, so
// it holds regardless of how far the tip has raced ahead or how small the
// configured retention is. Mirrors rippled's setSeqToKeep; a range with
// high <= low clears the pin.
func (vt *ValidationTracker) SetSeqToKeep(low, high uint32) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	if high <= low {
		vt.keepLow, vt.keepHigh = 0, 0
		return
	}
	vt.keepLow, vt.keepHigh = low, high
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

// IsCurrent reports whether a validation's sign-time and seen-time are
// close enough to now to be considered "current" in rippled's sense.
// Exact mirror of Validations.h:148-166 isCurrent:
//
//	signTime > (now - validationCURRENT_EARLY) &&
//	signTime < (now + validationCURRENT_WALL) &&
//	(seenTime == 0 || seenTime < (now + validationCURRENT_LOCAL))
//
// Note on constant names: rippled's EARLY bounds the PAST on signTime
// (not "early" in the usual sense of future-side); WALL bounds the
// FUTURE on signTime. The prior go-xrpl implementation had the two
// swapped and used a past-bound on seenTime — three wire-parity bugs
// that would desync freshness decisions between Go and rippled peers
// under clock skew.
//
// `now` is the network-adjusted time from the adaptor so the freshness
// window honors the close-offset consensus has converged on.
func IsCurrent(now, signTime, seenTime time.Time) bool {
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
func (vt *ValidationTracker) Add(validation *consensus.Validation) bool {
	return vt.AddStatus(validation) == ValStatusCurrent
}

// AddStatus adds a validation and classifies the outcome. Only
// ValStatusCurrent validations enter the quorum/trie indexes; every
// non-stale one is recorded in the by-seq evidence index first, so a
// double-sign is detected even at a seq the node has already superseded.
//
// Inbound filters match rippled's Validations::add (Validations.h:
// 623-707) and isCurrent:
//   - Both full and partial validations are tracked. A trusted partial
//     (Full=false) — emitted by a recovering validator that has not
//     fully applied the ledger — still steers branch selection through
//     the trie, mirroring rippled where updateTrie runs for every
//     trusted validation regardless of full-ness. The Full filter lives
//     in the quorum counters (countTrustedExcludingNegUNLLocked,
//     ProposersValidated), not at the door — dropping partials here
//     blinds every peer's preferred-ledger steering during recovery.
//   - Stale or clock-skewed validations (outside the wall/local
//     windows defined above) are rejected via isCurrent.
//   - A non-monotonic seq (per-node enforcer floor, idle-reset after
//     validationSetExpires) is rejected: as conflicting when the node
//     already signed a different ledger — or the same ledger with a
//     different sign time — at that seq, as multiple on a cookie
//     mismatch, else as badSeq.
//   - Validations with seq below minSeq are rejected. Once a ledger
//     accepts, validations for seqs many rounds back are noise that
//     can never retroactively become quorum; keeping them in memory
//     wastes work on every checkFullValidation pass.
//   - Per-node newer-seq-only rule: a node's tip is superseded only by
//     a strictly higher seq; a same-or-lower seq is rejected.
//
// onFullyValidated is fired OUTSIDE vt.mu so the callback may call
// back into the tracker (e.g. ExpireOld) or take other locks that
// vt.mu doesn't shadow without deadlocking. Mirrors the lock-free
// callback dispatch ExpireOld already uses for onStale.
//
// IMPORTANT: the engine's callers (OnValidation, sendValidation) hold
// engine.e.mu.Lock when they call Add, so the callback runs under that
// write lock too. The callback MUST NOT take engine.e.mu (RLock or
// otherwise) — Go's RWMutex is non-recursive and would self-deadlock.
// Adding new locks the callback may need is fine as long as they are
// not already held by the caller chain.
//
// Defer order is LIFO: vt.mu.Unlock runs first (released before the
// callback), then the captured fire-tuple is dispatched.
func (vt *ValidationTracker) AddStatus(validation *consensus.Validation) ValStatus {
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
	now := vt.now()
	if !IsCurrent(now, validation.SignTime, validation.SeenTime) {
		return ValStatusStale
	}

	// validation.NodeID is already master-shaped on entry — the
	// consensus router resolved the ephemeral signing pubkey through
	// the manifest cache before calling OnValidation. We can use it
	// directly as the map key, matching rippled's RCLValidations.cpp:
	// 165-186 calcNodeID(masterKey ?? signingKey) (the resolution now
	// happens at the wire seam, not here).
	resolvedID := validation.NodeID

	// Byzantine detector (Validations.h:637-681). Record the validation
	// as by-seq evidence, then enforce monotonically increasing seqs per
	// node. On a non-monotonic seq, classify against the entry already
	// tracked at that exact seq — catching a double-sign even after the
	// node's tip moved past it. Runs before the minSeq gate: rippled
	// classifies anything inside the freshness window.
	tracked := vt.trackBySequenceLocked(resolvedID, validation, now)
	enf := vt.seqEnforcers[resolvedID]
	if enf == nil {
		enf = &seqEnforcer{}
		vt.seqEnforcers[resolvedID] = enf
	}
	if !enf.advance(now, validation.LedgerSeq) {
		if tracked.LedgerID != validation.LedgerID {
			return ValStatusConflicting
		}
		if !tracked.SignTime.Equal(validation.SignTime) {
			return ValStatusConflicting
		}
		if tracked.Cookie != validation.Cookie {
			return ValStatusMultiple
		}
		return ValStatusBadSeq
	}

	// Reject far-stale validations below the sequence floor.
	if vt.minSeq > 0 && validation.LedgerSeq < vt.minSeq {
		return ValStatusStale
	}

	// Supersede a node's tip only with a strictly higher seq; a
	// same-or-lower-seq re-sign is rejected so a stale or sideways
	// ledger can't skew ProposersFinished / PreferredFromValidations.
	existing, hasExisting := vt.byNode[resolvedID]
	if hasExisting {
		if validation.LedgerSeq <= existing.LedgerSeq {
			return ValStatusStale
		}
	}

	// Update by-node tracking
	vt.byNode[resolvedID] = validation

	// Add to ledger validations
	ledgerVals, exists := vt.validations[validation.LedgerID]
	if !exists {
		ledgerVals = &ledgerValidations{vals: make(map[consensus.NodeID]*consensus.Validation)}
		ledgerVals.touch(vt.now())
		vt.validations[validation.LedgerID] = ledgerVals
	}
	ledgerVals.vals[resolvedID] = validation

	// Steer the trie on trusted() alone — negUNL validators included —
	// mirroring rippled's updateTrie precondition. negUNL exclusion lives
	// on the quorum/support read paths, not on trie membership.
	if vt.trusted[resolvedID] {
		var prior *acquiringKey
		if hasExisting {
			prior = &acquiringKey{seq: existing.LedgerSeq, id: existing.LedgerID}
		}
		vt.updateTrieLocked(resolvedID, validation, preResolvedLedger, prior)
	}

	// Capture the fire-tuple under the lock; the deferred dispatcher
	// invokes onFullyValidated after vt.mu.Unlock has run.
	fireID, fireSeq, shouldFire = vt.checkFullValidationLocked(validation.LedgerID)
	cb = vt.onFullyValidated
	return ValStatusCurrent
}

// trackBySequenceLocked records validation in the by-seq evidence index
// and returns the entry tracked for (seq, node) — the prior validation
// when one exists, else validation itself. A prior entry signed more
// than validationCurrentWall before a newer one is disregarded and
// replaced, so an ancient double-sign no longer reads as conflicting
// (Validations.h:640-651). Caller must hold vt.mu.
func (vt *ValidationTracker) trackBySequenceLocked(
	nodeID consensus.NodeID,
	validation *consensus.Validation,
	now time.Time,
) *consensus.Validation {
	bucket := vt.bySequence[validation.LedgerSeq]
	if bucket == nil {
		bucket = &seqValidations{byNode: make(map[consensus.NodeID]*consensus.Validation)}
		vt.bySequence[validation.LedgerSeq] = bucket
	}
	bucket.touched = now
	tracked, ok := bucket.byNode[nodeID]
	if !ok || validation.SignTime.Sub(tracked.SignTime) > validationCurrentWall {
		bucket.byNode[nodeID] = validation
		return validation
	}
	return tracked
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
	if vt.quorumUnavailable != nil && vt.quorumUnavailable() {
		return ledgerID, 0, false
	}
	if _, done := vt.fired[ledgerID]; done {
		return ledgerID, 0, false
	}
	ledgerVals, exists := vt.validations[ledgerID]
	if !exists || len(ledgerVals.vals) == 0 {
		return ledgerID, 0, false
	}

	var sampleSeq uint32
	for _, v := range ledgerVals.vals {
		sampleSeq = v.LedgerSeq
		break
	}
	trustedCount := vt.countTrustedExcludingNegUNLLocked(ledgerVals.vals)

	if trustedCount >= vt.quorum {
		vt.fired[ledgerID] = struct{}{}
		return ledgerID, sampleSeq, true
	}
	return ledgerID, 0, false
}

// GetTrustedValidations returns trusted validations for a ledger. No
// seq argument is needed: entries are keyed by ledger hash, which
// uniquely determines the seq, so all entries under a ledgerID share it.
// Reading a ledger's set refreshes its access age (rippled's
// byLedger touch) so actively-queried ledgers survive ExpireOld.
func (vt *ValidationTracker) GetTrustedValidations(ledgerID consensus.LedgerID) []*consensus.Validation {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return nil
	}
	ledgerVals.touch(vt.now())

	var result []*consensus.Validation
	for nodeID, v := range ledgerVals.vals {
		if vt.trusted[nodeID] {
			result = append(result, v)
		}
	}
	return result
}

// RecheckFullyValidated returns the validations that currently count toward
// finality for ledgerID together with the quorum from the same tracker state.
// When the set no longer reaches quorum it removes the prior firing marker
// before unlocking, so a concurrent or later validation can notify again.
func (vt *ValidationTracker) RecheckFullyValidated(
	ledgerID consensus.LedgerID,
	seq uint32,
) ([]*consensus.Validation, int, bool) {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	quorum := vt.quorum
	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		delete(vt.fired, ledgerID)
		return nil, quorum, false
	}
	ledgerVals.touch(vt.now())

	result := make([]*consensus.Validation, 0, len(ledgerVals.vals))
	for nodeID, validation := range ledgerVals.vals {
		if validation == nil || !validation.Full || validation.LedgerSeq != seq ||
			validation.SignTime.IsZero() || !vt.trusted[nodeID] || vt.negUNL[nodeID] {
			continue
		}
		copy := *validation
		result = append(result, &copy)
	}
	accepted := len(result) > 0 && len(result) >= quorum
	if !accepted {
		delete(vt.fired, ledgerID)
	}
	return result, quorum, accepted
}

// TrustedValidationCount returns the count of trusted validations
// for a ledger, EXCLUDING validators currently on the negative UNL.
// Matches rippled's LedgerMaster.cpp:886,952,1120 where every trusted
// count flows through negativeUNLFilter before comparison — so any
// consumer of this method (quorum gate, server_info, future LedgerTrie
// port) sees consistent, filtered numbers.
func (vt *ValidationTracker) TrustedValidationCount(ledgerID consensus.LedgerID) int {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return 0
	}
	ledgerVals.touch(vt.now())
	return vt.countTrustedExcludingNegUNLLocked(ledgerVals.vals)
}

// countTrustedExcludingNegUNLLocked counts validators in ledgerVals
// that are trusted, not on the negUNL, AND issued a FULL validation.
// The Full filter is the quorum-side gate that lets Add track trusted
// partials (for trie steering) without letting them cross the
// finality threshold — mirroring rippled's numTrustedForLedger
// (Validations.h:1037-1050), which counts full validations only.
// Caller must hold vt.mu.
func (vt *ValidationTracker) countTrustedExcludingNegUNLLocked(
	ledgerVals map[consensus.NodeID]*consensus.Validation,
) int {
	count := 0
	for nodeID, v := range ledgerVals {
		if v.Full && vt.trusted[nodeID] && !vt.negUNL[nodeID] {
			count++
		}
	}
	return count
}

// TrustedSupport returns the count of trusted-and-not-negUNL
// validators committing to this ledger or any descendant — the
// negUNL-excluded analogue of the trie's branchSupport. The trie itself
// includes negUNL validators (for GetPreferred steering), so the
// exclusion is applied here in branchSupportExcludingNegUNLLocked
// rather than at trie membership. Polls checkAcquired before reading,
// rippled's withTrie cadence. Falls back to the flat trusted count when
// the trie or ancestry is unavailable.
func (vt *ValidationTracker) TrustedSupport(ledgerID consensus.LedgerID) int {
	// Snapshot pointers, drop the lock for ancestry resolution, then
	// re-acquire for the cheap trie query.
	vt.mu.RLock()
	trie := vt.trie
	ancestry := vt.ancestry
	vt.mu.RUnlock()

	if trie == nil || ancestry == nil {
		return vt.TrustedValidationCount(ledgerID)
	}

	lgr, ok := ancestry.LedgerByID(ledgerID)
	if !ok {
		return vt.TrustedValidationCount(ledgerID)
	}

	vt.mu.Lock()
	defer vt.mu.Unlock()
	// Trie may have been swapped while we resolved ancestry.
	if vt.trie != trie {
		ledgerVals, exists := vt.validations[ledgerID]
		if !exists {
			return 0
		}
		ledgerVals.touch(vt.now())
		return vt.countTrustedExcludingNegUNLLocked(ledgerVals.vals)
	}
	vt.checkAcquiredLocked()
	return vt.branchSupportExcludingNegUNLLocked(lgr)
}

// branchSupportExcludingNegUNLLocked counts the trusted validators whose
// current trie tip is lgr or one of lgr's descendants, EXCLUDING any on
// the negUNL. It is the negUNL-aware analogue of trie.BranchSupport: the
// trie now mirrors rippled's trusted()-only updateTrie and therefore
// includes negUNL validators for preferred-ledger steering, but the
// engine's quorum / peer-LCL gates must not credit a deemed-offline
// validator toward branch support. A tip supports lgr iff lgr lies on
// the tip's ancestry chain at lgr's sequence — exactly the membership
// trie.BranchSupport sums, since every validator inserts weight 1.
// Caller must hold vt.mu.
func (vt *ValidationTracker) branchSupportExcludingNegUNLLocked(lgr ledgertrie.Ledger) int {
	targetSeq := lgr.Seq()
	targetID := lgr.ID()
	count := 0
	for nodeID, tip := range vt.trieTips {
		if vt.negUNL[nodeID] {
			continue
		}
		if tip.Seq() >= targetSeq && tip.Ancestor(targetSeq) == targetID {
			count++
		}
	}
	return count
}

// GetPreferred returns the network-preferred ledger ID and sequence as
// decided by the ancestry trie. Parked validations whose ledger has been
// acquired since the last poll are replayed into the trie first, so the
// trie decides unconditionally (rippled getPreferred via withTrie,
// Validations.h:849-879). When the trie yields no tip at all, falls back
// to the majority over still-acquiring ledgers; ok is false only when
// the trie is unwired or both sources are empty. largestIssued is the
// highest sequence this node has validated; it seeds uncommitted support
// from earlier seqs.
func (vt *ValidationTracker) GetPreferred(largestIssued uint32) (consensus.LedgerID, uint32, bool) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	if vt.trie == nil {
		return consensus.LedgerID{}, 0, false
	}
	vt.checkAcquiredLocked()

	var (
		tip ledgertrie.SpanTip
		ok  bool
	)
	if safeTrieCall("GetPreferred", func() {
		tip, ok = vt.trie.GetPreferred(largestIssued)
	}) {
		return consensus.LedgerID{}, 0, false
	}
	if !ok {
		return vt.acquiringMajorityLocked()
	}
	return tip.ID, tip.Seq, true
}

// acquiringMajorityLocked is GetPreferred's fallback when the trie holds
// no tip: the still-acquiring ledger backed by the most trusted
// validators, ties broken by greater ledger ID (Validations.h:857-878).
// Caller must hold vt.mu.
func (vt *ValidationTracker) acquiringMajorityLocked() (consensus.LedgerID, uint32, bool) {
	var (
		bestKey acquiringKey
		bestN   int
	)
	for key, parked := range vt.acquiring {
		n := len(parked)
		if n > bestN || (n == bestN && lexLessLgrID(bestKey.id, key.id)) {
			bestKey = key
			bestN = n
		}
	}
	if bestN == 0 {
		return consensus.LedgerID{}, 0, false
	}
	return bestKey.id, bestKey.seq, true
}

// ProposersValidated returns the count of trusted validators whose
// MOST RECENT (highest-seq) full validation points at ledgerID. This
// is the peer-pressure signal rippled uses in shouldCloseLedger via
// adaptor_.proposersValidated(prevLedgerID_) at RCLConsensus.cpp:281.
//
// Mirrors numTrustedForLedger at Validations.h:1037-1050 — filters on
// trusted && full and intentionally does NOT filter negUNL (negUNL
// adjusts quorum, not the count).
func (vt *ValidationTracker) ProposersValidated(ledgerID consensus.LedgerID) int {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	// Per-ledger map, not byNode — byNode overwrites once a validator
	// advances, but shouldCloseLedger's peer-pressure short-circuit needs
	// the historical count. Mirrors rippled's byLedger in RCLValidations.
	perLedger, ok := vt.validations[ledgerID]
	if !ok {
		return 0
	}
	perLedger.touch(vt.now())
	count := 0
	for nodeID, v := range perLedger.vals {
		if !vt.trusted[nodeID] {
			continue
		}
		if !v.Full {
			continue
		}
		count++
	}
	return count
}

// ProposersFinished counts trusted validators whose latest validation is
// strictly past prev. Equivalent to rippled's proposersFinished →
// getNodesAfter used by checkConsensus to return MovedOn. Like
// getNodesAfter it reads the trie, so negUNL validators ARE counted here
// (they steer just like any trusted validator); negUNL only adjusts the
// quorum threshold, not this "have the peers moved on" signal.
func (vt *ValidationTracker) ProposersFinished(prev consensus.Ledger) int {
	if prev == nil {
		return 0
	}

	// Trie fast path — getNodesAfter at Validations.h:973-993:
	// branchSupport(ledger) - tipSupport(ledger).
	vt.mu.RLock()
	trie := vt.trie
	ancestry := vt.ancestry
	vt.mu.RUnlock()
	if trie != nil && ancestry != nil {
		if lgr, ok := ancestry.LedgerByID(prev.ID()); ok {
			vt.mu.Lock()
			current := vt.trie == trie
			var branch, tip uint32
			if current {
				vt.checkAcquiredLocked()
				branch = trie.BranchSupport(lgr)
				tip = trie.TipSupport(lgr)
			}
			vt.mu.Unlock()
			if current {
				if branch <= tip {
					return 0
				}
				return int(branch - tip)
			}
		}
	}

	// Seq-only fallback when trie/ancestry isn't wired for prev (boot or
	// post-switch). Mirrors getNodesAfter's trie semantics, which count
	// every trusted tip past prev regardless of full-ness OR negUNL
	// membership — so partials and negUNL validators are included here
	// too, matching the trie fast-path above. Can over-count fork
	// validations — caller already gates on roundTime > LedgerMinConsensus.
	vt.mu.RLock()
	defer vt.mu.RUnlock()
	count := 0
	prevSeq := prev.Seq()
	for nodeID, v := range vt.byNode {
		if !vt.trusted[nodeID] {
			continue
		}
		if v.LedgerSeq > prevSeq {
			count++
		}
	}
	return count
}

// PreferredFromValidations returns the most-popular trusted-validator
// tip at seq >= minSeq, ignoring local ancestry — the no-trie fallback
// for GetPreferred during deep catch-up. negUNL validators are counted,
// matching the trie-backed GetPreferred (rippled steers on trusted()
// alone). Ties resolved by higher seq then lexicographic ID.
func (vt *ValidationTracker) PreferredFromValidations(minSeq uint32) (consensus.LedgerID, uint32, bool) {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	type tally struct {
		count int
		seq   uint32
	}
	tips := make(map[consensus.LedgerID]tally)
	for nodeID, v := range vt.byNode {
		if !vt.trusted[nodeID] {
			continue
		}
		if v.LedgerSeq < minSeq {
			continue
		}
		t := tips[v.LedgerID]
		t.count++
		t.seq = v.LedgerSeq
		tips[v.LedgerID] = t
	}
	if len(tips) == 0 {
		return consensus.LedgerID{}, 0, false
	}
	var bestID consensus.LedgerID
	var best tally
	first := true
	for id, t := range tips {
		better := first ||
			t.count > best.count ||
			(t.count == best.count && t.seq > best.seq) ||
			(t.count == best.count && t.seq == best.seq && lexLessLgrID(id, bestID))
		if better {
			bestID = id
			best = t
			first = false
		}
	}
	return bestID, best.seq, true
}

func lexLessLgrID(a, b consensus.LedgerID) bool {
	for i := range len(a) {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// LatestValidation returns the latest validation from a node.
func (vt *ValidationTracker) LatestValidation(nodeID consensus.NodeID) *consensus.Validation {
	vt.mu.RLock()
	defer vt.mu.RUnlock()
	return vt.byNode[nodeID]
}

// CurrentNodeIDs returns the node IDs of every validator whose latest
// tracked validation still passes the IsCurrent freshness gate — the set
// observed actively validating right now, partial or full, trusted or not.
// The gate matches Add()'s admission check, so a node appears iff its most
// recent validation is neither stale nor clock-skewed against the
// network-adjusted clock. Enumeration only; FlushStale does the paired
// eviction that rippled folds into its current() sweep. Mirrors rippled's
// Validations::getCurrentNodeIDs — the live-participation set gathered when
// the engine refreshes the trusted set and quorum each round.
func (vt *ValidationTracker) CurrentNodeIDs() []consensus.NodeID {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	now := vt.now()
	ids := make([]consensus.NodeID, 0, len(vt.byNode))
	for nodeID, v := range vt.byNode {
		if IsCurrent(now, v.SignTime, v.SeenTime) {
			ids = append(ids, nodeID)
		}
	}
	return ids
}

// FlushStale drops non-current validations from the steering indexes (byNode
// + trie tips), mirroring rippled's current() sweep inside withTrie
// (Validations.h:509-533): a silent validator must stop steering
// preferred-ledger selection once its last validation ages past the isCurrent
// window. Driven from the engine heartbeat because ExpireOld only runs on
// full-validation progress — during a stall it never fires. Per-ledger
// history (vt.validations) still ages via ExpireOld.
func (vt *ValidationTracker) FlushStale() {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	now := vt.now()
	for nodeID, v := range vt.byNode {
		if IsCurrent(now, v.SignTime, v.SeenTime) {
			continue
		}
		delete(vt.byNode, nodeID)
		vt.unparkLocked(acquiringKey{seq: v.LedgerSeq, id: v.LedgerID}, nodeID)
		if prev, ok := vt.trieTips[nodeID]; ok {
			safeTrieCall("Remove", func() { vt.trie.Remove(prev, 1) })
			delete(vt.trieTips, nodeID)
		}
	}

	// Age out by-seq evidence and idle enforcer floors — rippled's
	// beast::expire(bySequence_) sweep. An enforcer idle past the window
	// would self-reset on next use, so deleting it is equivalent.
	for seq, bucket := range vt.bySequence {
		if now.Sub(bucket.touched) > validationSetExpires {
			delete(vt.bySequence, seq)
		}
	}
	for nodeID, enf := range vt.seqEnforcers {
		if now.Sub(enf.when) > validationSetExpires {
			delete(vt.seqEnforcers, nodeID)
		}
	}
}

// ExpireOld drops validations below minSeq from every index and fires
// onStale outside the mutex. Trie tips for dropped validators are also
// removed so phantom branchSupport doesn't linger on stale ancestors.
//
// A set created or read within validationSetExpires is retained even
// below the sequence floor — rippled's access-age expiry
// (validationSET_EXPIRES with byLedger touch) — so hot ledgers stay
// queryable for RPC and late peers. Memory stays bounded: Add rejects
// below the engine's minSeq gate, so a below-floor set cannot reheat
// from the network, and it drops on the first ExpireOld after going
// cold. The seq floor coarsely protects the recent (negative-UNL voting)
// window, but is anchored at the validated tip; SetSeqToKeep pins the exact
// flag-ledger window so a fast-advancing tip can't outrun the vote's read.
func (vt *ValidationTracker) ExpireOld(minSeq uint32) {
	vt.mu.Lock()

	onStale := vt.onStale
	var stale []*consensus.Validation

	cutoff := vt.now().Add(-validationSetExpires).UnixNano()

	for ledgerID, ledgerVals := range vt.validations {
		var sample *consensus.Validation
		for _, v := range ledgerVals.vals {
			sample = v
			break
		}
		if sample == nil || sample.LedgerSeq >= minSeq {
			continue
		}
		if vt.keepLow < vt.keepHigh &&
			sample.LedgerSeq >= vt.keepLow && sample.LedgerSeq < vt.keepHigh {
			continue
		}
		if ledgerVals.lastAccess.Load() > cutoff {
			continue
		}
		for nodeID, v := range ledgerVals.vals {
			stale = append(stale, v)
			if latest, ok := vt.byNode[nodeID]; ok && latest == v {
				delete(vt.byNode, nodeID)
				vt.unparkLocked(acquiringKey{seq: v.LedgerSeq, id: v.LedgerID}, nodeID)
				if vt.trie != nil {
					if prev, ok := vt.trieTips[nodeID]; ok {
						safeTrieCall("Remove", func() {
							vt.trie.Remove(prev, 1)
						})
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

// Flush discards all accumulated validation state — the latest
// validation per node, the per-ledger maps, the fully-validated firing
// set, and the trie — while preserving configuration (trusted set,
// negUNL, quorum, freshness, clock, callbacks, ancestry, and sequence
// floor). Called on orderly shutdown and to reset for a clean
// restart-in-process. It does not fire onStale: the state is dropped,
// not archived.
func (vt *ValidationTracker) Flush() {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	vt.validations = make(map[consensus.LedgerID]*ledgerValidations)
	vt.byNode = make(map[consensus.NodeID]*consensus.Validation)
	vt.bySequence = make(map[uint32]*seqValidations)
	vt.seqEnforcers = make(map[consensus.NodeID]*seqEnforcer)
	vt.fired = make(map[consensus.LedgerID]struct{})
	vt.rebuildTrieLocked()
}
