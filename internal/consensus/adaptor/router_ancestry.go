package adaptor

import (
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

const (
	// A header walk is useful only while the target is close enough that
	// replaying each successor is cheaper than rebuilding a full state tree.
	headerDiscoveryMaxRequests   = maxForwardDeltaGap * 2
	headerDiscoveryRetryInterval = 500 * time.Millisecond
	headerDiscoveryDeadline      = 10 * time.Second
)

var (
	errHeaderDiscoveryConflict    = errors.New("header ancestry conflict")
	errHeaderDiscoveryUnavailable = errors.New("header ancestry unavailable")
)

// ledgerHeaderNetwork is optional so existing acquisition senders keep their
// narrow interface. OverlaySender implements it using the same liBASE wire
// request as a normal ledger-base acquisition; keeping the semantic method
// separate prevents a header-only walk from creating an inbound full-state
// acquisition.
type ledgerHeaderNetwork interface {
	RequestLedgerHeaderFromPeer(peerID uint64, hash [32]byte, seq uint32, indirect bool) error
}

// headerDiscoverySession is a serial walk from a trusted target to the local
// replay anchor. Headers are retained until the walk reaches the anchor, so a
// peer cannot make a partial or speculative chain look acquired.
type headerDiscoverySession struct {
	generation    uint64
	baseSeq       uint32
	baseHash      [32]byte
	targetSeq     uint32
	targetHash    [32]byte
	targetSource  catchupTargetSource
	nextSeq       uint32
	nextHash      [32]byte
	peerID        uint64
	attempts      uint16
	pending       bool
	terminal      bool
	deadline      time.Time
	lastSentAt    time.Time
	excludedPeers map[uint64]struct{}
	headers       map[uint32]header.LedgerHeader
}

func (r *Router) startHeaderParentDiscovery(
	base *ledger.Ledger,
	targetSeq uint32,
	targetHash [32]byte,
	peerHint uint64,
	targetSource catchupTargetSource,
) bool {
	if r == nil || base == nil || targetHash == ([32]byte{}) ||
		!trustedHeaderDiscoverySource(targetSource) {
		return false
	}
	baseSeq := base.Sequence()
	if targetSeq <= baseSeq || targetSeq-baseSeq > maxForwardDeltaGap || base.Hash() == ([32]byte{}) {
		return false
	}
	if _, ok := r.acquisition.(ledgerHeaderNetwork); !ok {
		return false
	}
	// Recheck the frontier at admission. Callers snapshot it before doing
	// service work, so a newer peer-only or withdrawn target must not start a
	// walk for stale evidence.
	r.catchupMu.Lock()
	currentTarget := r.catchup
	r.catchupMu.Unlock()
	if !trustedHeaderDiscoverySource(currentTarget.source) ||
		currentTarget.seq != targetSeq || currentTarget.hash != targetHash {
		return false
	}

	r.headerDiscoveryMu.Lock()
	if current := r.headerDiscovery; current != nil {
		if current.terminal &&
			(baseSeq > current.baseSeq || current.baseHash != base.Hash()) {
			// A completed fallback or ledger switch moved the replay anchor.
			// Retire the old terminal session so a later outage gets a fresh
			// bounded budget; a moving target alone must not reset a failed walk.
			r.rememberHeaderRequestsLocked(current)
			r.headerDiscoveryGeneration++
			r.headerDiscovery = nil
		} else {
			// The admitted target and replay base are immutable for this walk.
			// A newer validation is re-armed after it completes, so it cannot
			// replace the anchor while replies are in flight.
			active := !current.terminal
			r.headerDiscoveryMu.Unlock()
			return active
		}
	}
	r.headerDiscoveryGeneration++
	now := time.Now()
	current := &headerDiscoverySession{
		generation:    r.headerDiscoveryGeneration,
		baseSeq:       baseSeq,
		baseHash:      base.Hash(),
		targetSeq:     targetSeq,
		targetHash:    targetHash,
		targetSource:  targetSource,
		nextSeq:       targetSeq,
		nextHash:      targetHash,
		peerID:        peerHint,
		deadline:      now.Add(headerDiscoveryDeadline),
		excludedPeers: make(map[uint64]struct{}),
		headers:       make(map[uint32]header.LedgerHeader, targetSeq-baseSeq),
	}
	r.headerDiscovery = current
	r.headerDiscoveryMu.Unlock()

	r.cancelFrozenPivotForHeaderDiscovery()
	if err := r.issueHeaderDiscoveryRequest(current.generation); err != nil {
		r.headerDiscoveryRequestFailed(current.generation, err)
		r.retryHeaderDiscovery(current.generation)
	}
	return true
}

func trustedHeaderDiscoverySource(source catchupTargetSource) bool {
	return source == catchupSourceQuorum
}

// headerDiscoveryNeeded reports whether the local sequence map proves a
// contiguous parent chain from base through target. A target hash by itself is
// insufficient: the walk is needed whenever a parent or intermediate hash is
// unknown. Existing parent links are also checked so a stale branch cannot
// suppress discovery merely because every sequence has an entry.
func (r *Router) headerDiscoveryNeeded(base *ledger.Ledger, targetSeq uint32, targetHash [32]byte) bool {
	if base == nil || targetHash == ([32]byte{}) || targetSeq <= base.Sequence() {
		return false
	}
	parentHash := base.Hash()
	for seq := base.Sequence() + 1; ; seq++ {
		entry, ok := r.lookupSeqHash(seq)
		if !ok || entry.hash == ([32]byte{}) || !entry.haveParent || entry.parentHash != parentHash {
			return true
		}
		if seq == targetSeq {
			return entry.hash != targetHash
		}
		parentHash = entry.hash
	}
}

// A peer-only pivot may already be collecting transactions when quorum
// validation supplies the real target. Retire that pivot before a header walk
// is admitted so its speculative anchor cannot win adoption while the walk is
// proving the target's ancestry.
func (r *Router) cancelFrozenPivotForHeaderDiscovery() {
	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	if !r.standardReplay.active {
		r.acquisitionMu.Unlock()
		r.replayCommitMu.Unlock()
		return
	}
	retired := r.cancelStandardReplayPipelineLocked()
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()
	r.retireStandardReplay(retired)
}

// maybeStartHeaderParentDiscovery is the common admission path used by
// catch-up arms and direct target acquisition. It keeps startup/provisional
// modes on their existing full-state path and only walks when the validated
// anchor's ancestry is actually unknown.
func (r *Router) maybeStartHeaderParentDiscovery(target catchupTarget, peerHint uint64) bool {
	if r == nil || r.adaptor == nil || target.seq == 0 ||
		!trustedHeaderDiscoverySource(target.source) {
		return false
	}
	svc := r.adaptor.LedgerService()
	if svc == nil || svc.NeedsInitialSync() || svc.IsFastLoadProvisional() {
		return false
	}
	if r.isBuildingLedger(target.seq) {
		return false
	}
	r.acquisitionMu.Lock()
	activePivotReachesTarget := r.standardReplay.active &&
		r.standardReplay.pivotSeq < target.seq &&
		r.recoveryAnchorReachesTarget(
			r.standardReplay.pivotSeq,
			r.standardReplay.pivotHash,
			target.hash,
		)
	r.acquisitionMu.Unlock()
	if activePivotReachesTarget {
		return false
	}
	base := r.catchupReplayBase(svc)
	if base == nil || target.seq <= base.Sequence() ||
		target.seq-base.Sequence() > maxForwardDeltaGap ||
		!r.headerDiscoveryNeeded(base, target.seq, target.hash) {
		return false
	}
	if r.invalidFutureLedgerSequence(target.seq) {
		return true
	}
	if peerHint == 0 {
		peerHint = target.peerID
	}
	if !r.startHeaderParentDiscovery(base, target.seq, target.hash, peerHint, target.source) {
		return false
	}
	return true
}

// Caller holds catchupMu.
func (r *Router) headerDiscoveryTargetStillTrustedLocked(current *headerDiscoverySession) bool {
	if current == nil || !trustedHeaderDiscoverySource(current.targetSource) {
		return false
	}
	target := r.catchup
	if !trustedHeaderDiscoverySource(target.source) || target.hash == ([32]byte{}) {
		return false
	}
	if target.seq < current.targetSeq {
		return false
	}
	if target.seq == current.targetSeq {
		return target.hash == current.targetHash
	}
	return true
}

func (r *Router) issueHeaderDiscoveryRequest(generation uint64) error {
	network, ok := r.acquisition.(ledgerHeaderNetwork)
	if !ok {
		return errHeaderDiscoveryUnavailable
	}

	r.headerDiscoveryMu.Lock()
	current := r.headerDiscovery
	if current == nil || current.generation != generation || current.terminal {
		r.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}
	now := time.Now()
	if !current.deadline.IsZero() && !now.Before(current.deadline) {
		current.terminal = true
		current.pending = false
		r.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}
	if current.attempts >= headerDiscoveryMaxRequests {
		current.terminal = true
		current.pending = false
		r.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}
	seq := current.nextSeq
	hash := current.nextHash
	peerHint := current.peerID
	excluded := make(map[uint64]struct{}, len(current.excludedPeers))
	for peerID := range current.excludedPeers {
		excluded[peerID] = struct{}{}
	}
	indirect := current.attempts > 0
	r.headerDiscoveryMu.Unlock()

	peerID, found := r.selectHeaderDiscoveryPeer(seq, peerHint, excluded)
	if !found {
		r.headerDiscoveryMu.Lock()
		if current = r.headerDiscovery; current != nil && current.generation == generation {
			current.attempts++
			current.pending = false
			current.lastSentAt = now
			if current.attempts >= headerDiscoveryMaxRequests {
				current.terminal = true
			}
		}
		r.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}

	r.headerDiscoveryMu.Lock()
	current = r.headerDiscovery
	now = time.Now()
	if current == nil || current.generation != generation || current.terminal ||
		current.nextSeq != seq || current.nextHash != hash {
		r.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}
	if !current.deadline.IsZero() && !now.Before(current.deadline) {
		current.terminal = true
		current.pending = false
		r.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}
	current.peerID = peerID
	current.attempts++
	current.pending = true
	current.lastSentAt = now
	r.headerDiscoveryMu.Unlock()

	return network.RequestLedgerHeaderFromPeer(peerID, hash, seq, indirect)
}

func (r *Router) selectHeaderDiscoveryPeer(
	seq uint32,
	preferred uint64,
	excluded map[uint64]struct{},
) (uint64, bool) {
	if preferred != 0 {
		if _, skip := excluded[preferred]; !skip &&
			(r.peerSessions == nil || r.peerSessions.IsPeerConnected(peermanagement.PeerID(preferred))) {
			return preferred, true
		}
	}
	return r.selectAcquisitionPeerExcluding(seq, excluded)
}

func (r *Router) headerDiscoveryRequestFailed(generation uint64, err error) {
	r.headerDiscoveryMu.Lock()
	current := r.headerDiscovery
	if current == nil || current.generation != generation {
		r.headerDiscoveryMu.Unlock()
		return
	}
	current.pending = false
	peerID := current.peerID
	seq := current.nextSeq
	hash := current.nextHash
	attempts := current.attempts
	if peerID != 0 {
		current.excludedPeers[peerID] = struct{}{}
	}
	if isAcquisitionDisconnectError(err) {
		current.peerID = 0
	}
	if current.attempts >= headerDiscoveryMaxRequests ||
		(!current.deadline.IsZero() && !time.Now().Before(current.deadline)) {
		current.terminal = true
	}
	r.headerDiscoveryMu.Unlock()
	r.logger.Debug("header ancestry request unavailable",
		"error", err,
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
		"attempts", attempts,
	)
}

func (r *Router) tickHeaderDiscovery(now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	r.headerDiscoveryMu.Lock()
	current := r.headerDiscovery
	if current == nil || current.terminal {
		r.headerDiscoveryMu.Unlock()
		return
	}
	if current.attempts >= headerDiscoveryMaxRequests {
		current.pending = false
		current.terminal = true
		r.headerDiscoveryMu.Unlock()
		return
	}
	if !current.deadline.IsZero() && !now.Before(current.deadline) {
		current.pending = false
		current.terminal = true
		r.headerDiscoveryMu.Unlock()
		return
	}
	if !current.lastSentAt.IsZero() && now.Sub(current.lastSentAt) < headerDiscoveryRetryInterval {
		r.headerDiscoveryMu.Unlock()
		return
	}
	if current.pending {
		if current.peerID != 0 {
			current.excludedPeers[current.peerID] = struct{}{}
		}
		current.peerID = 0
		current.pending = false
	}
	generation := current.generation
	r.headerDiscoveryMu.Unlock()

	if err := r.issueHeaderDiscoveryRequest(generation); err != nil {
		r.headerDiscoveryRequestFailed(generation, err)
	}
}

func (r *Router) cancelHeaderDiscovery() {
	r.headerDiscoveryMu.Lock()
	r.rememberHeaderRequestsLocked(r.headerDiscovery)
	r.headerDiscoveryGeneration++
	r.headerDiscovery = nil
	r.headerDiscoveryMu.Unlock()
}

func (r *Router) rememberHeaderRequestsLocked(current *headerDiscoverySession) {
	if current == nil {
		return
	}
	if r.retiredHeaderRequests == nil {
		r.retiredHeaderRequests = make(map[[32]byte]time.Time)
	}
	expires := time.Now().Add(time.Minute)
	r.retiredHeaderRequests[current.nextHash] = expires
	for _, h := range current.headers {
		r.retiredHeaderRequests[h.Hash] = expires
	}
	for len(r.retiredHeaderRequests) > headerDiscoveryMaxRequests {
		var oldest [32]byte
		oldestExpiry := expires
		for hash, expiry := range r.retiredHeaderRequests {
			if !expiry.After(oldestExpiry) {
				oldest, oldestExpiry = hash, expiry
			}
		}
		delete(r.retiredHeaderRequests, oldest)
	}
}

func (r *Router) retiredHeaderRequestLocked(hash [32]byte) bool {
	expires, known := r.retiredHeaderRequests[hash]
	if known && !time.Now().Before(expires) {
		delete(r.retiredHeaderRequests, hash)
		return false
	}
	return known
}

func (r *Router) headerDiscoveryPeerDisconnected(peerID uint64) {
	if peerID == 0 {
		return
	}
	r.headerDiscoveryMu.Lock()
	defer r.headerDiscoveryMu.Unlock()
	if current := r.headerDiscovery; current != nil && current.peerID == peerID {
		current.excludedPeers[peerID] = struct{}{}
		current.peerID = 0
		current.pending = false
		current.lastSentAt = time.Time{}
	}
}

func (current *headerDiscoverySession) headerHashSeen(hash [32]byte) bool {
	if current == nil || hash == ([32]byte{}) {
		return false
	}
	for _, h := range current.headers {
		if h.Hash == hash {
			return true
		}
	}
	return false
}

func (r *Router) handleHeaderDiscoveryReply(ld *message.LedgerData, peerID uint64) bool {
	if ld == nil || ld.HasRequestCookie() || ld.InfoType != message.LedgerInfoBase || len(ld.LedgerHash) != 32 {
		return false
	}
	var responseHash [32]byte
	copy(responseHash[:], ld.LedgerHash)
	activeAcquisition := r.fetchTracker.Find(responseHash) != nil

	r.headerDiscoveryMu.Lock()
	current := r.headerDiscovery
	if current == nil || current.terminal {
		known := r.retiredHeaderRequestLocked(responseHash) ||
			current != nil && (current.nextHash == responseHash || current.headerHashSeen(responseHash))
		r.headerDiscoveryMu.Unlock()
		return known && !activeAcquisition
	}
	expected := current.nextHash
	expectedPeer := current.peerID
	pending := current.pending
	expectedSeq := current.nextSeq
	generation := current.generation
	baseSeq := current.baseSeq
	baseHash := current.baseHash
	seen := current.headerHashSeen(responseHash) || responseHash != expected && r.retiredHeaderRequestLocked(responseHash) && !activeAcquisition
	expired := !current.deadline.IsZero() && !time.Now().Before(current.deadline)
	current.pending = current.pending && !expired
	if expired {
		current.terminal = true
	}
	r.headerDiscoveryMu.Unlock()
	if expired {
		r.failHeaderDiscovery(generation, errHeaderDiscoveryUnavailable, peerID, errors.New("header ancestry deadline expired"))
		return true
	}
	if seen {
		// Header replies have no request identifier. A duplicate from a prior
		// step can arrive after the walk has advanced; consume known buffered
		// hashes without penalizing a healthy peer.
		return true
	}
	if !pending || expectedPeer == 0 || expectedPeer != peerID {
		return responseHash == expected && !activeAcquisition
	}
	if responseHash != expected {
		return false
	}

	h, err := decodeHeaderDiscoveryHeader(ld)
	if err != nil {
		r.retryHeaderDiscoveryPeer(generation, peerID, err)
		return true
	}
	if ld.LedgerSeq != expectedSeq || header.CalculateHash(*h) != expected {
		r.retryHeaderDiscoveryPeer(generation, peerID,
			fmt.Errorf("header does not match requested sequence/hash: requested %d/%x, got %d/%x", expectedSeq, expected[:8], h.LedgerIndex, h.Hash[:8]))
		return true
	}
	if h.LedgerIndex != expectedSeq {
		r.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID,
			fmt.Errorf("header sequence %d conflicts with requested sequence %d", h.LedgerIndex, expectedSeq))
		return true
	}
	if h.ParentHash == ([32]byte{}) {
		r.retryHeaderDiscoveryPeer(generation, peerID,
			fmt.Errorf("header %d has no parent hash", expectedSeq))
		return true
	}
	if expectedSeq == baseSeq+1 && h.ParentHash != baseHash {
		r.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID,
			fmt.Errorf("header parent %x does not reach replay base %x", h.ParentHash[:8], baseHash[:8]))
		return true
	}
	if r.headerDiscoveryEntryConflicts(expectedSeq, expected, h.ParentHash) {
		r.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID,
			fmt.Errorf("header conflicts with trusted sequence evidence at %d", expectedSeq))
		return true
	}

	r.headerDiscoveryMu.Lock()
	current = r.headerDiscovery
	if current == nil || current.generation != generation || current.terminal || current.nextHash != expected {
		r.headerDiscoveryMu.Unlock()
		return true
	}
	h.Hash = expected
	current.headers[expectedSeq] = *h
	current.pending = false
	current.lastSentAt = time.Time{}
	complete := expectedSeq == current.baseSeq+1 && h.ParentHash == current.baseHash
	if !complete {
		if expectedSeq <= current.baseSeq+1 {
			r.headerDiscoveryMu.Unlock()
			r.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID, errors.New("header sequence walked below replay base"))
			return true
		}
		current.nextSeq = expectedSeq - 1
		current.nextHash = h.ParentHash
	}
	r.headerDiscoveryMu.Unlock()

	if complete {
		r.finishHeaderDiscovery(generation, peerID)
		return true
	}
	if err := r.issueHeaderDiscoveryRequest(generation); err != nil {
		r.headerDiscoveryRequestFailed(generation, err)
	}
	return true
}

func (r *Router) retryHeaderDiscoveryPeer(generation, peerID uint64, detail error) {
	r.headerDiscoveryRequestFailed(generation, detail)
	r.acquisition.IncPeerBadData(peerID, "ledger-header-ancestry")
	r.retryHeaderDiscovery(generation)
}

func (r *Router) retryHeaderDiscovery(generation uint64) {
	r.headerDiscoveryMu.Lock()
	current := r.headerDiscovery
	active := current != nil && current.generation == generation && !current.terminal
	r.headerDiscoveryMu.Unlock()
	if !active {
		return
	}
	if err := r.issueHeaderDiscoveryRequest(generation); err != nil {
		r.headerDiscoveryRequestFailed(generation, err)
	}
}

func decodeHeaderDiscoveryHeader(ld *message.LedgerData) (*header.LedgerHeader, error) {
	if len(ld.Nodes) == 0 || len(ld.Nodes[0].NodeData) == 0 {
		return nil, errors.New("header reply has no header node")
	}
	data := ld.Nodes[0].NodeData
	if len(data) < header.SizeBase {
		return nil, fmt.Errorf("invalid header node size: %d", len(data))
	}
	h, err := header.DeserializeHeader(data[:header.SizeBase], false)
	if err != nil {
		return nil, err
	}
	h.Hash = header.CalculateHash(*h)
	return h, nil
}

func (r *Router) headerDiscoveryEntryConflicts(seq uint32, hash, parentHash [32]byte) bool {
	entry, ok := r.lookupSeqHash(seq)
	if ok && entry.hash != ([32]byte{}) && entry.hash != hash && entry.source >= seqHashSourceValidation {
		return true
	}
	if ok && entry.haveParent && entry.parentHash != parentHash && entry.parentFrom >= seqHashSourceValidation {
		return true
	}
	return false
}

func (r *Router) failHeaderDiscovery(generation uint64, kind error, peerID uint64, detail error) {
	r.headerDiscoveryMu.Lock()
	current := r.headerDiscovery
	if current == nil || current.generation != generation {
		r.headerDiscoveryMu.Unlock()
		return
	}
	current.pending = false
	current.terminal = true
	seq := current.nextSeq
	hash := current.nextHash
	r.headerDiscoveryMu.Unlock()

	r.logger.Warn("header ancestry discovery failed",
		"kind", kind,
		"error", detail,
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)
	r.armCatchupTowardTargetWithPeer(peerID)
}

func (r *Router) finishHeaderDiscovery(generation uint64, peerID uint64) {
	if r == nil || r.adaptor == nil {
		return
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		r.failHeaderDiscovery(generation, errHeaderDiscoveryUnavailable, peerID, errors.New("ledger service is unavailable"))
		return
	}

	r.headerDiscoveryMu.Lock()
	current := r.headerDiscovery
	if current == nil || current.generation != generation || current.terminal {
		r.headerDiscoveryMu.Unlock()
		return
	}
	if !current.deadline.IsZero() && !time.Now().Before(current.deadline) {
		current.pending = false
		current.terminal = true
		r.headerDiscoveryMu.Unlock()
		r.failHeaderDiscovery(generation, errHeaderDiscoveryUnavailable, peerID, errors.New("header ancestry deadline expired"))
		return
	}
	base, err := svc.GetLedgerByHash(current.baseHash)
	if err != nil || base == nil || base.Sequence() != current.baseSeq || base.Hash() != current.baseHash {
		current.pending = false
		current.terminal = true
		r.headerDiscoveryMu.Unlock()
		r.failHeaderDiscovery(generation, errHeaderDiscoveryUnavailable, peerID, errors.New("validated replay base is no longer available"))
		return
	}

	// Validate the entire buffered chain before publishing any sequence hash.
	// This catches a late branch conflict without leaving a prefix visible to
	// replay policy.
	chain := make([]header.LedgerHeader, 0, current.targetSeq-current.baseSeq)
	parentHash := current.baseHash
	for seq := current.baseSeq + 1; ; seq++ {
		h, ok := current.headers[seq]
		if !ok || h.LedgerIndex != seq || h.Hash == ([32]byte{}) ||
			header.CalculateHash(h) != h.Hash || h.ParentHash != parentHash {
			current.pending = false
			current.terminal = true
			r.headerDiscoveryMu.Unlock()
			r.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID, errors.New("header walk is not contiguous"))
			return
		}
		if r.headerDiscoveryEntryConflicts(seq, h.Hash, h.ParentHash) {
			current.pending = false
			current.terminal = true
			r.headerDiscoveryMu.Unlock()
			r.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID, fmt.Errorf("header walk conflicts at %d", seq))
			return
		}
		chain = append(chain, h)
		parentHash = h.Hash
		if seq == current.targetSeq {
			break
		}
	}

	// Keep the target trust check and sequence-map transaction under their
	// respective locks. Once publication starts, target movement is handled by
	// the next arm rather than reported as a failed partially-published walk.
	localAnchor := r.localSeqHashAnchor()
	r.catchupMu.Lock()
	trusted := r.headerDiscoveryTargetStillTrustedLocked(current) &&
		(current.deadline.IsZero() || time.Now().Before(current.deadline))
	committed := false
	if trusted {
		r.seqHashMu.Lock()
		committed = r.recordAcquiredSeqHashChainLocked(chain, localAnchor)
		r.seqHashMu.Unlock()
	}
	r.catchupMu.Unlock()
	if !trusted || !committed {
		current.pending = false
		current.terminal = true
		r.headerDiscoveryMu.Unlock()
		if !trusted {
			r.failHeaderDiscovery(generation, errHeaderDiscoveryUnavailable, peerID, errors.New("trusted catch-up target changed during commit"))
		} else {
			r.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID, errors.New("sequence evidence changed during commit"))
		}
		return
	}

	baseSeq := current.baseSeq
	targetSeq := current.targetSeq
	r.rememberHeaderRequestsLocked(current)
	r.headerDiscovery = nil
	r.headerDiscoveryMu.Unlock()

	r.logger.Info("header ancestry discovery complete",
		"base_seq", baseSeq,
		"target_seq", targetSeq,
		"peer", peerID,
	)
	r.armCatchupTowardTargetWithPeer(peerID)
}
