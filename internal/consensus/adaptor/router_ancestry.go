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
	headerDiscoveryRepairBackoff = 5 * time.Second
	headerDiscoveryMaxRepairs    = 3
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
	repairAfter   time.Time
	repairRound   uint8
	deadline      time.Time
	lastSentAt    time.Time
	excludedPeers map[uint64]struct{}
	headers       map[uint32]header.LedgerHeader
}

func (c *catchupReplayCoordinator) startHeaderParentDiscovery(
	base *ledger.Ledger,
	targetSeq uint32,
	targetHash [32]byte,
	peerHint uint64,
	targetSource catchupTargetSource,
) bool {
	if c == nil || c.stoppedForShutdown() || base == nil || targetHash == ([32]byte{}) ||
		!trustedHeaderDiscoverySource(targetSource) {
		return false
	}
	baseSeq := base.Sequence()
	if targetSeq <= baseSeq || targetSeq-baseSeq > maxForwardDeltaGap || base.Hash() == ([32]byte{}) {
		return false
	}
	if _, ok := c.acquisition.(ledgerHeaderNetwork); !ok {
		return false
	}
	// Recheck the frontier at admission. Callers snapshot it before doing
	// service work, so a newer peer-only or withdrawn target must not start a
	// walk for stale evidence.
	c.catchupMu.Lock()
	currentTarget := c.catchup
	c.catchupMu.Unlock()
	if !trustedHeaderDiscoverySource(currentTarget.source) ||
		currentTarget.seq != targetSeq || currentTarget.hash != targetHash {
		return false
	}

	c.headerDiscoveryMu.Lock()
	if c.stoppedForShutdown() {
		c.headerDiscoveryMu.Unlock()
		return false
	}
	var repairRound uint8
	if current := c.headerDiscovery; current != nil {
		if current.terminal &&
			(baseSeq > current.baseSeq || current.baseHash != base.Hash()) {
			// A completed fallback or ledger switch moved the replay anchor.
			// Retire the old terminal session so a later outage gets a fresh
			// bounded budget; a moving target alone must not reset a failed walk.
			c.rememberHeaderRequestsLocked(current)
			c.headerDiscoveryGeneration++
			c.headerDiscovery = nil
		} else if current.terminal && !current.repairAfter.IsZero() {
			// A failed walk can prevent its own anchor from advancing. Retry
			// the frozen trusted target after backoff, not the moving head, with
			// a bounded number of fresh request/deadline budgets per anchor.
			if time.Now().Before(current.repairAfter) {
				c.headerDiscoveryMu.Unlock()
				return true
			}
			c.catchupMu.Lock()
			trusted := c.headerDiscoveryTargetStillTrustedLocked(current)
			c.catchupMu.Unlock()
			if !trusted {
				current.repairAfter = time.Time{}
				c.headerDiscoveryMu.Unlock()
				return false
			}
			targetSeq, targetHash, targetSource = current.targetSeq, current.targetHash, current.targetSource
			repairRound = current.repairRound + 1
			peerHint = 0
			c.rememberHeaderRequestsLocked(current)
		} else {
			// The admitted target and replay base are immutable for this walk.
			// A newer validation is re-armed after it completes, so it cannot
			// replace the anchor while replies are in flight.
			active := !current.terminal
			c.headerDiscoveryMu.Unlock()
			return active
		}
	}
	c.headerDiscoveryGeneration++
	now := time.Now()
	current := &headerDiscoverySession{
		generation:    c.headerDiscoveryGeneration,
		baseSeq:       baseSeq,
		baseHash:      base.Hash(),
		targetSeq:     targetSeq,
		targetHash:    targetHash,
		targetSource:  targetSource,
		nextSeq:       targetSeq,
		nextHash:      targetHash,
		peerID:        peerHint,
		repairRound:   repairRound,
		deadline:      now.Add(headerDiscoveryDeadline),
		excludedPeers: make(map[uint64]struct{}),
		headers:       make(map[uint32]header.LedgerHeader, targetSeq-baseSeq),
	}
	c.headerDiscovery = current
	c.headerDiscoveryMu.Unlock()
	if repairRound > 0 {
		c.logger.Info("retrying header ancestry from preserved replay base",
			"base_seq", baseSeq, "target_seq", targetSeq, "repair_round", repairRound)
	}

	c.cancelFrozenPivotForHeaderDiscovery(baseSeq, base.Hash(), targetSeq, targetHash)
	if err := c.issueHeaderDiscoveryRequest(current.generation); err != nil {
		c.headerDiscoveryRequestFailed(current.generation, err)
		c.retryHeaderDiscovery(current.generation)
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
func (c *catchupReplayCoordinator) headerDiscoveryNeeded(base *ledger.Ledger, targetSeq uint32, targetHash [32]byte) bool {
	if base == nil || targetHash == ([32]byte{}) || targetSeq <= base.Sequence() {
		return false
	}
	parentHash := base.Hash()
	for seq := base.Sequence() + 1; ; seq++ {
		entry, ok := c.lookupSeqHash(seq)
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
// validation supplies the real target. Retire an unverified pivot before a
// header walk is admitted; a verified pivot may keep its prepared successors
// when the walk starts from the same accepted anchor and target branch.
func (c *catchupReplayCoordinator) cancelFrozenPivotForHeaderDiscovery(
	baseSeq uint32,
	baseHash [32]byte,
	targetSeq uint32,
	targetHash [32]byte,
) {
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	if !c.standardReplay.active {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return
	}
	if c.standardReplay.pivotReady &&
		c.standardReplayHeaderDiscoveryCompatibleLocked(baseSeq, baseHash, targetSeq, targetHash) {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return
	}
	reason := "header_discovery_speculative"
	if c.standardReplay.pivotReady {
		reason = "header_discovery_conflict"
	}
	retired := c.cancelStandardReplayPipelineLocked(reason)
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	c.retireStandardReplay(retired)
}

// maybeStartHeaderParentDiscovery is the common admission path used by
// catch-up arms and direct target acquisition. It keeps startup/provisional
// modes on their existing full-state path and only walks when the validated
// anchor's ancestry is actually unknown.
func (c *catchupReplayCoordinator) maybeStartHeaderParentDiscovery(target catchupTarget, peerHint uint64) bool {
	if c == nil || c.adaptor == nil || target.seq == 0 ||
		!trustedHeaderDiscoverySource(target.source) {
		return false
	}
	svc := c.adaptor.LedgerService()
	if svc == nil || svc.NeedsInitialSync() || svc.IsFastLoadProvisional() {
		return false
	}
	if c.isBuildingLedger(target.seq) {
		return false
	}
	c.acquisitionMu.Lock()
	activePivotReachesTarget := c.standardReplay.active &&
		c.standardReplay.pivotSeq < target.seq &&
		c.recoveryAnchorReachesTarget(
			c.standardReplay.pivotSeq,
			c.standardReplay.pivotHash,
			target.hash,
		)
	c.acquisitionMu.Unlock()
	if activePivotReachesTarget {
		return false
	}
	base := c.catchupReplayBase(svc)
	if base == nil || target.seq <= base.Sequence() ||
		target.seq-base.Sequence() > maxForwardDeltaGap ||
		!c.headerDiscoveryNeeded(base, target.seq, target.hash) {
		return false
	}
	if c.invalidFutureLedgerSequence(target.seq) {
		return true
	}
	if peerHint == 0 {
		peerHint = target.peerID
	}
	if !c.startHeaderParentDiscovery(base, target.seq, target.hash, peerHint, target.source) {
		return false
	}
	return true
}

// Caller holds catchupMu.
func (c *catchupReplayCoordinator) headerDiscoveryTargetStillTrustedLocked(current *headerDiscoverySession) bool {
	if current == nil || !trustedHeaderDiscoverySource(current.targetSource) {
		return false
	}
	target := c.catchup
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

func (c *catchupReplayCoordinator) issueHeaderDiscoveryRequest(generation uint64) error {
	network, ok := c.acquisition.(ledgerHeaderNetwork)
	if !ok {
		return errHeaderDiscoveryUnavailable
	}

	c.headerDiscoveryMu.Lock()
	current := c.headerDiscovery
	if current == nil || current.generation != generation || current.terminal {
		c.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}
	now := time.Now()
	if !current.deadline.IsZero() && !now.Before(current.deadline) {
		current.markUnavailable(now)
		c.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}
	if current.attempts >= headerDiscoveryMaxRequests {
		current.markUnavailable(now)
		c.headerDiscoveryMu.Unlock()
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
	c.headerDiscoveryMu.Unlock()

	peerID, found := c.selectHeaderDiscoveryPeer(seq, peerHint, excluded)
	if !found {
		c.headerDiscoveryMu.Lock()
		if current = c.headerDiscovery; current != nil && current.generation == generation {
			current.attempts++
			current.pending = false
			current.lastSentAt = now
			if current.attempts >= headerDiscoveryMaxRequests {
				current.markUnavailable(now)
			}
		}
		c.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}

	c.headerDiscoveryMu.Lock()
	current = c.headerDiscovery
	now = time.Now()
	if current == nil || current.generation != generation || current.terminal ||
		current.nextSeq != seq || current.nextHash != hash {
		c.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}
	if !current.deadline.IsZero() && !now.Before(current.deadline) {
		current.markUnavailable(now)
		c.headerDiscoveryMu.Unlock()
		return errHeaderDiscoveryUnavailable
	}
	current.peerID = peerID
	current.attempts++
	current.pending = true
	current.lastSentAt = now
	c.headerDiscoveryMu.Unlock()

	return network.RequestLedgerHeaderFromPeer(peerID, hash, seq, indirect)
}

func (c *catchupReplayCoordinator) selectHeaderDiscoveryPeer(
	seq uint32,
	preferred uint64,
	excluded map[uint64]struct{},
) (uint64, bool) {
	if preferred != 0 {
		if _, skip := excluded[preferred]; !skip &&
			(c.peerSessions == nil || c.peerSessions.IsPeerConnected(peermanagement.PeerID(preferred))) {
			return preferred, true
		}
	}
	return c.selectAcquisitionPeerExcluding(seq, excluded)
}

func (c *catchupReplayCoordinator) headerDiscoveryRequestFailed(generation uint64, err error) {
	c.headerDiscoveryMu.Lock()
	current := c.headerDiscovery
	if current == nil || current.generation != generation {
		c.headerDiscoveryMu.Unlock()
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
		current.markUnavailable(time.Now())
	}
	c.headerDiscoveryMu.Unlock()
	c.logger.Debug("header ancestry request unavailable",
		"error", err,
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
		"attempts", attempts,
	)
}

func (c *catchupReplayCoordinator) tickHeaderDiscovery(now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	c.headerDiscoveryMu.Lock()
	current := c.headerDiscovery
	if current == nil || current.terminal {
		repair := current != nil && !current.repairAfter.IsZero() && !now.Before(current.repairAfter)
		c.headerDiscoveryMu.Unlock()
		if repair {
			c.armCatchupTowardTarget()
		}
		return
	}
	if current.attempts >= headerDiscoveryMaxRequests {
		current.markUnavailable(now)
		c.headerDiscoveryMu.Unlock()
		return
	}
	if !current.deadline.IsZero() && !now.Before(current.deadline) {
		current.markUnavailable(now)
		c.headerDiscoveryMu.Unlock()
		return
	}
	if !current.lastSentAt.IsZero() && now.Sub(current.lastSentAt) < headerDiscoveryRetryInterval {
		c.headerDiscoveryMu.Unlock()
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
	c.headerDiscoveryMu.Unlock()

	if err := c.issueHeaderDiscoveryRequest(generation); err != nil {
		c.headerDiscoveryRequestFailed(generation, err)
	}
}

func (c *catchupReplayCoordinator) cancelHeaderDiscovery() {
	c.headerDiscoveryMu.Lock()
	c.rememberHeaderRequestsLocked(c.headerDiscovery)
	c.headerDiscoveryGeneration++
	c.headerDiscovery = nil
	c.headerDiscoveryMu.Unlock()
}

func (c *catchupReplayCoordinator) rememberHeaderRequestsLocked(current *headerDiscoverySession) {
	if current == nil {
		return
	}
	if c.retiredHeaderRequests == nil {
		c.retiredHeaderRequests = make(map[[32]byte]time.Time)
	}
	expires := time.Now().Add(time.Minute)
	c.retiredHeaderRequests[current.nextHash] = expires
	for _, h := range current.headers {
		c.retiredHeaderRequests[h.Hash] = expires
	}
	for len(c.retiredHeaderRequests) > headerDiscoveryMaxRequests {
		var oldest [32]byte
		oldestExpiry := expires
		for hash, expiry := range c.retiredHeaderRequests {
			if !expiry.After(oldestExpiry) {
				oldest, oldestExpiry = hash, expiry
			}
		}
		delete(c.retiredHeaderRequests, oldest)
	}
}

func (c *catchupReplayCoordinator) retiredHeaderRequestLocked(hash [32]byte) bool {
	expires, known := c.retiredHeaderRequests[hash]
	if known && !time.Now().Before(expires) {
		delete(c.retiredHeaderRequests, hash)
		return false
	}
	return known
}

func (c *catchupReplayCoordinator) headerDiscoveryPeerDisconnected(peerID uint64) {
	if peerID == 0 {
		return
	}
	c.headerDiscoveryMu.Lock()
	defer c.headerDiscoveryMu.Unlock()
	if current := c.headerDiscovery; current != nil && current.peerID == peerID {
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

func (c *catchupReplayCoordinator) handleHeaderDiscoveryReply(ld *message.LedgerData, peerID uint64) bool {
	if ld == nil || ld.HasRequestCookie() || ld.InfoType != message.LedgerInfoBase || len(ld.LedgerHash) != 32 {
		return false
	}
	var responseHash [32]byte
	copy(responseHash[:], ld.LedgerHash)
	activeAcquisition := c.fetchTracker.Find(responseHash) != nil

	c.headerDiscoveryMu.Lock()
	current := c.headerDiscovery
	if current == nil || current.terminal {
		known := c.retiredHeaderRequestLocked(responseHash) ||
			current != nil && (current.nextHash == responseHash || current.headerHashSeen(responseHash))
		c.headerDiscoveryMu.Unlock()
		return known && !activeAcquisition
	}
	expected := current.nextHash
	expectedPeer := current.peerID
	pending := current.pending
	expectedSeq := current.nextSeq
	generation := current.generation
	baseSeq := current.baseSeq
	baseHash := current.baseHash
	seen := current.headerHashSeen(responseHash) || responseHash != expected && c.retiredHeaderRequestLocked(responseHash) && !activeAcquisition
	expired := !current.deadline.IsZero() && !time.Now().Before(current.deadline)
	current.pending = current.pending && !expired
	if expired {
		current.terminal = true
	}
	c.headerDiscoveryMu.Unlock()
	if expired {
		c.failHeaderDiscovery(generation, errHeaderDiscoveryUnavailable, peerID, errors.New("header ancestry deadline expired"))
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
		c.retryHeaderDiscoveryPeer(generation, peerID, err)
		return true
	}
	if ld.LedgerSeq != expectedSeq || header.CalculateHash(*h) != expected {
		c.retryHeaderDiscoveryPeer(generation, peerID,
			fmt.Errorf("header does not match requested sequence/hash: requested %d/%x, got %d/%x", expectedSeq, expected[:8], h.LedgerIndex, h.Hash[:8]))
		return true
	}
	if h.LedgerIndex != expectedSeq {
		c.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID,
			fmt.Errorf("header sequence %d conflicts with requested sequence %d", h.LedgerIndex, expectedSeq))
		return true
	}
	if h.ParentHash == ([32]byte{}) {
		c.retryHeaderDiscoveryPeer(generation, peerID,
			fmt.Errorf("header %d has no parent hash", expectedSeq))
		return true
	}
	if expectedSeq == baseSeq+1 && h.ParentHash != baseHash {
		c.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID,
			fmt.Errorf("header parent %x does not reach replay base %x", h.ParentHash[:8], baseHash[:8]))
		return true
	}
	if c.headerDiscoveryEntryConflicts(expectedSeq, expected, h.ParentHash) {
		c.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID,
			fmt.Errorf("header conflicts with trusted sequence evidence at %d", expectedSeq))
		return true
	}

	c.headerDiscoveryMu.Lock()
	current = c.headerDiscovery
	if current == nil || current.generation != generation || current.terminal || current.nextHash != expected {
		c.headerDiscoveryMu.Unlock()
		return true
	}
	h.Hash = expected
	current.headers[expectedSeq] = *h
	current.pending = false
	current.lastSentAt = time.Time{}
	complete := expectedSeq == current.baseSeq+1 && h.ParentHash == current.baseHash
	if !complete {
		if expectedSeq <= current.baseSeq+1 {
			c.headerDiscoveryMu.Unlock()
			c.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID, errors.New("header sequence walked below replay base"))
			return true
		}
		current.nextSeq = expectedSeq - 1
		current.nextHash = h.ParentHash
	}
	c.headerDiscoveryMu.Unlock()

	if complete {
		c.finishHeaderDiscovery(generation, peerID)
		return true
	}
	if err := c.issueHeaderDiscoveryRequest(generation); err != nil {
		c.headerDiscoveryRequestFailed(generation, err)
	}
	return true
}

func (c *catchupReplayCoordinator) retryHeaderDiscoveryPeer(generation, peerID uint64, detail error) {
	c.headerDiscoveryRequestFailed(generation, detail)
	c.acquisition.IncPeerBadData(peerID, "ledger-header-ancestry")
	c.retryHeaderDiscovery(generation)
}

func (c *catchupReplayCoordinator) retryHeaderDiscovery(generation uint64) {
	c.headerDiscoveryMu.Lock()
	current := c.headerDiscovery
	active := current != nil && current.generation == generation && !current.terminal
	c.headerDiscoveryMu.Unlock()
	if !active {
		return
	}
	if err := c.issueHeaderDiscoveryRequest(generation); err != nil {
		c.headerDiscoveryRequestFailed(generation, err)
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

func (c *catchupReplayCoordinator) headerDiscoveryEntryConflicts(seq uint32, hash, parentHash [32]byte) bool {
	entry, ok := c.lookupSeqHash(seq)
	if ok && entry.hash != ([32]byte{}) && entry.hash != hash && entry.source >= seqHashSourceValidation {
		return true
	}
	if ok && entry.haveParent && entry.parentHash != parentHash && entry.parentFrom >= seqHashSourceValidation {
		return true
	}
	return false
}

func (c *catchupReplayCoordinator) failHeaderDiscovery(generation uint64, kind error, peerID uint64, detail error) {
	c.headerDiscoveryMu.Lock()
	current := c.headerDiscovery
	if current == nil || current.generation != generation {
		c.headerDiscoveryMu.Unlock()
		return
	}
	current.pending = false
	current.terminal = true
	if kind == errHeaderDiscoveryUnavailable {
		current.markUnavailable(time.Now())
	} else {
		current.repairAfter = time.Time{}
	}
	baseSeq := current.baseSeq
	baseHash := current.baseHash
	targetSeq := current.targetSeq
	targetHash := current.targetHash
	seq := current.nextSeq
	hash := current.nextHash
	repairRound, repairAfter := current.repairRound, current.repairAfter
	c.headerDiscoveryMu.Unlock()

	c.logger.Warn("header ancestry discovery failed",
		"kind", kind,
		"error", detail,
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
		"base_seq", baseSeq, "target_seq", targetSeq,
		"repair_round", repairRound, "retry_at", repairAfter,
	)
	if kind == errHeaderDiscoveryConflict {
		c.cancelFrozenPivotForHeaderDiscovery(baseSeq, baseHash, targetSeq, targetHash)
	}
	c.armCatchupTowardTargetWithPeer(peerID)
}

func (c *catchupReplayCoordinator) finishHeaderDiscovery(generation uint64, peerID uint64) {
	if c == nil || c.adaptor == nil {
		return
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		c.failHeaderDiscovery(generation, errHeaderDiscoveryUnavailable, peerID, errors.New("ledger service is unavailable"))
		return
	}

	c.headerDiscoveryMu.Lock()
	current := c.headerDiscovery
	if current == nil || current.generation != generation || current.terminal {
		c.headerDiscoveryMu.Unlock()
		return
	}
	if !current.deadline.IsZero() && !time.Now().Before(current.deadline) {
		current.pending = false
		current.terminal = true
		c.headerDiscoveryMu.Unlock()
		c.failHeaderDiscovery(generation, errHeaderDiscoveryUnavailable, peerID, errors.New("header ancestry deadline expired"))
		return
	}
	base, err := svc.GetLedgerByHash(current.baseHash)
	if err != nil || base == nil || base.Sequence() != current.baseSeq || base.Hash() != current.baseHash {
		current.pending = false
		current.terminal = true
		c.headerDiscoveryMu.Unlock()
		c.failHeaderDiscovery(generation, errHeaderDiscoveryUnavailable, peerID, errors.New("validated replay base is no longer available"))
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
			c.headerDiscoveryMu.Unlock()
			c.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID, errors.New("header walk is not contiguous"))
			return
		}
		if c.headerDiscoveryEntryConflicts(seq, h.Hash, h.ParentHash) {
			current.pending = false
			current.terminal = true
			c.headerDiscoveryMu.Unlock()
			c.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID, fmt.Errorf("header walk conflicts at %d", seq))
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
	localAnchor := c.localSeqHashAnchor()
	c.catchupMu.Lock()
	trusted := !c.stoppedForShutdown() && c.headerDiscoveryTargetStillTrustedLocked(current) &&
		(current.deadline.IsZero() || time.Now().Before(current.deadline))
	committed := false
	if trusted {
		c.seqHashMu.Lock()
		committed = c.recordAcquiredSeqHashChainLocked(chain, localAnchor)
		c.seqHashMu.Unlock()
	}
	c.catchupMu.Unlock()
	if !trusted || !committed {
		current.pending = false
		current.terminal = true
		c.headerDiscoveryMu.Unlock()
		if !trusted {
			c.failHeaderDiscovery(generation, errHeaderDiscoveryUnavailable, peerID, errors.New("trusted catch-up target changed during commit"))
		} else {
			c.failHeaderDiscovery(generation, errHeaderDiscoveryConflict, peerID, errors.New("sequence evidence changed during commit"))
		}
		return
	}

	baseSeq := current.baseSeq
	baseHash := current.baseHash
	targetSeq := current.targetSeq
	targetHash := current.targetHash
	c.rememberHeaderRequestsLocked(current)
	c.headerDiscovery = nil
	c.headerDiscoveryMu.Unlock()

	c.logger.Info("header ancestry discovery complete",
		"base_seq", baseSeq,
		"target_seq", targetSeq,
		"peer", peerID,
	)
	c.reconcileStandardReplayAfterHeaderDiscovery(baseSeq, baseHash, targetSeq, targetHash, chain)
	// Consume the proven prefix before another moving-head header walk can
	// take over the catch-up arm and starve an otherwise ready replay queue.
	c.refillStandardReplayCollector(peerID)
	c.armCatchupTowardTargetWithPeer(peerID)
}
