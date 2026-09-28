package adaptor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	standardReplayPipelineWindow = 8
	standardReplayPreparedLimit  = 2048
	standardReplayProgressWindow = time.Minute
	standardReplayStallWindows   = 2
	standardReplayApplyBatch     = 8
	standardReplayApplyBudget    = 25 * time.Millisecond
)

type standardReplayPipeline struct {
	generation        uint64
	active            bool
	applying          bool
	pivotReady        bool
	initialCandidate  bool
	pivotSeq          uint32
	pivotHash         [32]byte
	anchorSeq         uint32
	anchorHash        [32]byte
	collectSeq        uint32
	collectHash       [32]byte
	targetSeq         uint32
	targetHash        [32]byte
	entries           map[uint32]*standardReplayEntry
	headBlockedAt     time.Time
	pivotStartedAt    time.Time
	progressSampleAt  time.Time
	sampleAnchorSeq   uint32
	stalledSamples    uint8
	retargetAttemptAt time.Time
	backpressured     bool
	baseLedger        *inbound.Ledger
	baseRelease       func()
	// acquisitionMu protects the owner retained between tracker removal and installation.
	pivotHandoff *standardReplayPivotHandoff
}

type standardReplayIdentity struct {
	generation       uint64
	active           bool
	pivotReady       bool
	initialCandidate bool
	pivotSeq         uint32
	pivotHash        [32]byte
	anchorSeq        uint32
	anchorHash       [32]byte
	collectSeq       uint32
	collectHash      [32]byte
	targetSeq        uint32
	targetHash       [32]byte
	pivotHandoff     *standardReplayPivotHandoff
}

type standardReplayPivotHandoff struct {
	generation  uint64
	seq         uint32
	hash        [32]byte
	acquisition *inbound.Ledger
}

type standardReplayTarget struct {
	seq  uint32
	hash [32]byte
}

type standardReplayEntry struct {
	generation  uint64
	seq         uint32
	hash        [32]byte
	parentHash  [32]byte
	peerID      uint64
	requestedAt time.Time
	readyAt     time.Time
	header      header.LedgerHeader
	txMap       *shamap.SHAMap
	acquisition *inbound.Ledger
	durable     bool
	failed      bool
}

type standardReplayLink struct {
	seq        uint32
	hash       [32]byte
	parentHash [32]byte
}

type standardReplayLinkState uint8

const (
	standardReplayLinkUnknown standardReplayLinkState = iota
	standardReplayLinkReady
	standardReplayLinkConflict
)

func (c *catchupReplayCoordinator) standardReplayLinks(
	anchorSeq uint32,
	anchorHash [32]byte,
	targetSeq uint32,
	targetHash [32]byte,
) ([]standardReplayLink, standardReplayLinkState) {
	if targetSeq <= anchorSeq || anchorHash == ([32]byte{}) || targetHash == ([32]byte{}) {
		return nil, standardReplayLinkUnknown
	}

	links := make([]standardReplayLink, 0, standardReplayPipelineWindow)
	parentHash := anchorHash
	for seq := anchorSeq + 1; seq <= targetSeq && len(links) < standardReplayPipelineWindow; seq++ {
		entry, ok := c.lookupSeqHash(seq)
		if !ok || entry.hash == ([32]byte{}) || !entry.haveParent {
			if len(links) == 0 {
				return nil, standardReplayLinkUnknown
			}
			return links, standardReplayLinkReady
		}
		if entry.parentHash != parentHash {
			return nil, standardReplayLinkConflict
		}
		if seq == targetSeq && entry.hash != targetHash {
			return nil, standardReplayLinkConflict
		}
		links = append(links, standardReplayLink{
			seq:        seq,
			hash:       entry.hash,
			parentHash: parentHash,
		})
		parentHash = entry.hash
	}
	if len(links) == 0 {
		return nil, standardReplayLinkUnknown
	}
	return links, standardReplayLinkReady
}

func (c *catchupReplayCoordinator) standardReplayBase(
	svc *service.Service,
	fallback *ledger.Ledger,
	targetSeq uint32,
	targetHash [32]byte,
) (*ledger.Ledger, standardReplayIdentity, bool) {
	if svc == nil {
		return fallback, standardReplayIdentity{}, false
	}

	c.acquisitionMu.Lock()
	identity := c.standardReplayIdentityLocked()
	c.acquisitionMu.Unlock()

	base := fallback
	if identity.active {
		if !identity.pivotReady {
			return nil, identity, true
		}
		if anchor, err := svc.GetLedgerByHash(identity.anchorHash); err == nil && anchor != nil && anchor.Sequence() == identity.anchorSeq {
			base = anchor
		} else {
			var current bool
			identity, current = c.cancelStandardReplayPipelineIdentity(identity, "anchor_unavailable")
			if !current {
				return fallback, standardReplayIdentity{}, false
			}
		}
	}

	advanced := false
	var advancedLinks []standardReplayLink
	for base != nil && base.Sequence() < targetSeq {
		nextSeq := base.Sequence() + 1
		link, ok := c.lookupSeqHash(nextSeq)
		if !ok || !link.haveParent || link.parentHash != base.Hash() || link.hash == ([32]byte{}) {
			break
		}
		if nextSeq == targetSeq && link.hash != targetHash {
			break
		}
		next, err := svc.GetLedgerByHash(link.hash)
		if err != nil || next == nil || next.Sequence() != nextSeq || next.ParentHash() != base.Hash() {
			break
		}
		advancedLinks = append(advancedLinks, standardReplayLink{
			seq:        nextSeq,
			hash:       link.hash,
			parentHash: base.Hash(),
		})
		base = next
		advanced = true
	}
	if identity.active && advanced {
		updated, retirement, current := c.advanceStandardReplayAnchor(identity, advancedLinks, base)
		c.retireStandardReplay(retirement)
		if !current {
			return fallback, standardReplayIdentity{}, false
		}
		identity = updated
	}
	return base, identity, true
}

// advanceStandardReplayAnchor consumes successors that were stored by another
// recovery path while this pipeline was collecting them. The prepared suffix
// remains owned by the same generation, so the next arm can continue from the
// newly established anchor without reacquiring or rebuilding it.
func (c *catchupReplayCoordinator) advanceStandardReplayAnchor(
	identity standardReplayIdentity,
	links []standardReplayLink,
	base *ledger.Ledger,
) (standardReplayIdentity, standardReplayRetirement, bool) {
	if len(links) == 0 {
		return identity, standardReplayRetirement{}, true
	}

	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	if !c.standardReplayIdentityMatchesLocked(identity) {
		current := c.standardReplayIdentityLocked()
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return current, standardReplayRetirement{}, false
	}

	anchorSeq := c.standardReplay.anchorSeq
	anchorHash := c.standardReplay.anchorHash
	// Validate the complete transition before touching the tracker or the
	// prepared map. A concurrent handoff can make the snapshot stale; in that
	// case the caller will re-arm against the newer generation.
	for _, link := range links {
		if link.seq != anchorSeq+1 || link.parentHash != anchorHash || link.hash == ([32]byte{}) {
			current := c.standardReplayIdentityLocked()
			c.acquisitionMu.Unlock()
			c.replayCommitMu.Unlock()
			return current, standardReplayRetirement{}, false
		}
		if entry := c.standardReplay.entries[link.seq]; entry != nil &&
			(entry.hash != link.hash || entry.parentHash != link.parentHash) {
			current := c.standardReplayIdentityLocked()
			c.acquisitionMu.Unlock()
			c.replayCommitMu.Unlock()
			return current, standardReplayRetirement{}, false
		}
		anchorSeq = link.seq
		anchorHash = link.hash
	}
	if base == nil || base.Sequence() != anchorSeq || base.Hash() != anchorHash {
		current := c.standardReplayIdentityLocked()
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return current, standardReplayRetirement{}, false
	}

	anchorSeq = c.standardReplay.anchorSeq
	anchorHash = c.standardReplay.anchorHash
	var retirement standardReplayRetirement
	for _, link := range links {
		if entry := c.standardReplay.entries[link.seq]; entry != nil {
			if entry.acquisition != nil && c.discardInboundAcquisitionLocked(entry.acquisition) {
				retirement.ledgers = append(retirement.ledgers, entry.acquisition)
			}
			if c.consensusRecovery.stepHash == entry.hash {
				c.consensusRecovery.stepHash = [32]byte{}
			}
			delete(c.standardReplay.entries, link.seq)
		}
		anchorSeq = link.seq
		anchorHash = link.hash
	}

	c.standardReplay.anchorSeq = anchorSeq
	c.standardReplay.anchorHash = anchorHash
	// Recompute the prepared tail after consuming the stored prefix. The old
	// collector cursor may point into that prefix, while entries beyond it can
	// already be complete and ready to drain.
	c.recomputeStandardReplayCollectorLocked()
	startDrain := c.standardReplay.pivotReady && !c.standardReplay.applying
	if startDrain {
		head := c.standardReplay.entries[anchorSeq+1]
		startDrain = head != nil && (!head.readyAt.IsZero() || head.failed)
		if startDrain {
			c.standardReplay.applying = true
		}
	}
	c.updateStandardReplayHeadBlockLocked(time.Now())
	updated := c.standardReplayIdentityLocked()
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	if startDrain {
		c.scheduleStandardReplayDrain()
	}
	return updated, retirement, true
}

// standardReplayHeaderDiscoveryCompatibleLocked admits only a verified pivot
// whose accepted anchor is the walk's base. An unfinished full-state pivot has
// no durable chain proof and must be retired before header discovery starts.
// The target may move forward or backward while the walk is in flight; an
// exact target hash mismatch is the one contradiction that is already visible
// before the walk publishes its intermediate headers.
func (c *catchupReplayCoordinator) standardReplayHeaderDiscoveryCompatibleLocked(
	baseSeq uint32,
	baseHash [32]byte,
	targetSeq uint32,
	targetHash [32]byte,
) bool {
	if !c.standardReplay.active || !c.standardReplay.pivotReady ||
		baseSeq > c.standardReplay.anchorSeq ||
		targetSeq < baseSeq || targetHash == ([32]byte{}) {
		return false
	}
	if baseSeq == c.standardReplay.anchorSeq {
		if baseHash != c.standardReplay.anchorHash {
			return false
		}
	} else if !c.standardReplayChainReachesAnchorLocked(baseSeq, baseHash) {
		return false
	}
	if targetSeq == c.standardReplay.anchorSeq {
		return targetHash == c.standardReplay.anchorHash
	}
	if targetSeq == c.standardReplay.targetSeq && targetHash != c.standardReplay.targetHash {
		return false
	}
	if entry := c.standardReplay.entries[targetSeq]; entry != nil && entry.hash != targetHash {
		return false
	}
	return true
}

func (c *catchupReplayCoordinator) standardReplayChainReachesAnchorLocked(startSeq uint32, startHash [32]byte) bool {
	if startSeq > c.standardReplay.anchorSeq || startHash == ([32]byte{}) {
		return false
	}
	if startSeq == c.standardReplay.anchorSeq {
		return startHash == c.standardReplay.anchorHash
	}
	parentHash := startHash
	for seq := startSeq + 1; seq != 0; seq++ {
		entry, ok := c.lookupSeqHash(seq)
		if !ok || !entry.haveParent || entry.hash == ([32]byte{}) || entry.parentHash != parentHash {
			return false
		}
		parentHash = entry.hash
		if seq == c.standardReplay.anchorSeq {
			return parentHash == c.standardReplay.anchorHash
		}
	}
	return false
}

func (c *catchupReplayCoordinator) recomputeStandardReplayCollectorLocked() {
	collectSeq, collectHash := c.standardReplay.anchorSeq, c.standardReplay.anchorHash
	for seq := collectSeq + 1; seq != 0; seq++ {
		entry := c.standardReplay.entries[seq]
		if entry == nil || entry.hash == ([32]byte{}) || entry.parentHash != collectHash {
			break
		}
		collectSeq, collectHash = entry.seq, entry.hash
	}
	c.standardReplay.collectSeq = collectSeq
	c.standardReplay.collectHash = collectHash
}

// reconcileStandardReplayAfterHeaderDiscovery validates prepared entries
// against the chain just proven by the header walk. Entries on the compatible
// prefix remain in the same generation; a conflicting suffix is retired and
// can be re-collected from the newly published chain.
func (c *catchupReplayCoordinator) reconcileStandardReplayAfterHeaderDiscovery(
	baseSeq uint32,
	baseHash [32]byte,
	targetSeq uint32,
	targetHash [32]byte,
	chain []header.LedgerHeader,
) {
	if len(chain) == 0 {
		return
	}
	bySeq := make(map[uint32]header.LedgerHeader, len(chain))
	for _, h := range chain {
		bySeq[h.LedgerIndex] = h
	}

	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	if !c.standardReplay.active {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return
	}
	if !c.standardReplay.pivotReady || c.standardReplay.anchorSeq < baseSeq ||
		(c.standardReplay.anchorSeq == baseSeq && c.standardReplay.anchorHash != baseHash) ||
		targetHash == ([32]byte{}) {
		retired := c.cancelStandardReplayPipelineLocked("header_discovery_conflict")
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		c.retireStandardReplay(retired)
		return
	}
	if targetSeq < c.standardReplay.anchorSeq {
		if !c.standardReplayChainReachesAnchorLocked(targetSeq, targetHash) {
			retired := c.cancelStandardReplayPipelineLocked("header_discovery_conflict")
			c.acquisitionMu.Unlock()
			c.replayCommitMu.Unlock()
			c.retireStandardReplay(retired)
			return
		}
		// The replay already advanced beyond the frozen discovery target while
		// headers were in flight. The proven target is an ancestor, so it must
		// not lower the active pipeline's frontier or discard its prepared tail.
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return
	}
	if c.standardReplay.anchorSeq > baseSeq {
		anchor, ok := bySeq[c.standardReplay.anchorSeq]
		if !ok || anchor.Hash != c.standardReplay.anchorHash {
			retired := c.cancelStandardReplayPipelineLocked("header_discovery_conflict")
			c.acquisitionMu.Unlock()
			c.replayCommitMu.Unlock()
			c.retireStandardReplay(retired)
			return
		}
	}

	var firstDiscard uint32
	for seq, entry := range c.standardReplay.entries {
		if seq > targetSeq {
			continue
		}
		h, ok := bySeq[seq]
		if !ok || h.Hash != entry.hash || h.ParentHash != entry.parentHash {
			if firstDiscard == 0 || seq < firstDiscard {
				firstDiscard = seq
			}
		}
	}
	// A lower trusted target does not invalidate a prepared tail by itself. It
	// remains usable when its first successor attaches to the newly trusted
	// target and each following prepared entry attaches to the preceding one.
	// If that link is missing or contradictory, retire the unproven suffix.
	var nextHash = targetHash
	retainedTailSeq := targetSeq
	retainedTailHash := targetHash
	minAfterTarget := uint32(0)
	for seq := range c.standardReplay.entries {
		if seq > targetSeq && (minAfterTarget == 0 || seq < minAfterTarget) {
			minAfterTarget = seq
		}
	}
	if minAfterTarget != 0 {
		if targetSeq == ^uint32(0) || minAfterTarget != targetSeq+1 {
			if firstDiscard == 0 || minAfterTarget < firstDiscard {
				firstDiscard = minAfterTarget
			}
		} else {
			for seq := minAfterTarget; seq != 0; seq++ {
				entry, ok := c.standardReplay.entries[seq]
				if !ok {
					for later := range c.standardReplay.entries {
						if later > seq && (firstDiscard == 0 || later < firstDiscard) {
							firstDiscard = later
						}
					}
					break
				}
				if entry.parentHash != nextHash {
					if firstDiscard == 0 || seq < firstDiscard {
						firstDiscard = seq
					}
					break
				}
				nextHash = entry.hash
				retainedTailSeq = seq
				retainedTailHash = entry.hash
			}
		}
	}
	originalTargetSeq := c.standardReplay.targetSeq
	originalTargetHash := c.standardReplay.targetHash
	generation := c.standardReplay.generation
	anchorSeq := c.standardReplay.anchorSeq
	anchorHash := c.standardReplay.anchorHash
	preserveOriginalTarget := originalTargetSeq > targetSeq
	if preserveOriginalTarget {
		expected := targetHash
		for seq := targetSeq + 1; seq != 0; seq++ {
			entry, ok := c.standardReplay.entries[seq]
			if !ok || entry.parentHash != expected {
				preserveOriginalTarget = false
				break
			}
			expected = entry.hash
			if seq == originalTargetSeq {
				break
			}
		}
		if preserveOriginalTarget && expected != originalTargetHash {
			preserveOriginalTarget = false
		}
	}

	var retirement standardReplayRetirement
	discardedEntries := 0
	if firstDiscard != 0 {
		for seq, entry := range c.standardReplay.entries {
			if seq < firstDiscard {
				continue
			}
			if entry.acquisition != nil && c.discardInboundAcquisitionLocked(entry.acquisition) {
				retirement.ledgers = append(retirement.ledgers, entry.acquisition)
			}
			if c.consensusRecovery.stepHash == entry.hash {
				c.consensusRecovery.stepHash = [32]byte{}
			}
			delete(c.standardReplay.entries, seq)
			c.replayPipelineDiscarded.Add(1)
			discardedEntries++
		}
	}
	if preserveOriginalTarget {
		c.standardReplay.targetSeq = originalTargetSeq
		c.standardReplay.targetHash = originalTargetHash
	} else if firstDiscard == 0 && retainedTailSeq > targetSeq {
		// The old target may be beyond the resident window, so its complete
		// prepared path is unavailable for proof here. Keep the verified
		// contiguous suffix as the frontier instead of letting the lower walk
		// target clear it when the suffix drains.
		c.standardReplay.targetSeq = retainedTailSeq
		c.standardReplay.targetHash = retainedTailHash
	} else {
		c.standardReplay.targetSeq = targetSeq
		c.standardReplay.targetHash = targetHash
	}
	c.recomputeStandardReplayCollectorLocked()
	startDrain := c.standardReplay.pivotReady && !c.standardReplay.applying
	if startDrain {
		head := c.standardReplay.entries[c.standardReplay.anchorSeq+1]
		startDrain = head != nil && (!head.readyAt.IsZero() || head.failed)
		if startDrain {
			c.standardReplay.applying = true
		}
	}
	c.updateStandardReplayHeadBlockLocked(time.Now())
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	if discardedEntries > 0 {
		c.logStandardReplayCancellation(
			"header_discovery_conflict",
			generation,
			anchorSeq,
			anchorHash,
			originalTargetSeq,
			originalTargetHash,
			discardedEntries,
			len(retirement.ledgers),
		)
	}
	c.retireStandardReplay(retirement)
	if startDrain {
		c.scheduleStandardReplayDrain()
	}
}

func (c *catchupReplayCoordinator) reconcileStandardReplayTarget(targetSeq uint32, targetHash [32]byte) {
	// The consensus engine may request an exact ledger hash before peer status
	// or validation bookkeeping has associated that hash with a sequence. An
	// unknown sequence is not evidence that the requested ledger precedes (or
	// conflicts with) the frozen replay anchor. Treating seq=0 as an older
	// target cancels the frozen pivot acquisition and drops startup back onto
	// the moving-head full-state treadmill.
	if targetSeq == 0 {
		return
	}
	c.acquisitionMu.Lock()
	identity := c.standardReplayIdentityLocked()
	c.acquisitionMu.Unlock()
	if !identity.active {
		return
	}
	if targetSeq < identity.anchorSeq ||
		(targetSeq == identity.anchorSeq && targetHash != identity.anchorHash) ||
		(targetSeq == identity.targetSeq && targetHash != identity.targetHash) {
		c.cancelStandardReplayPipelineIdentity(identity, "target_conflict")
	}
}

func (c *catchupReplayCoordinator) tryArmStandardReplayPipeline(
	svc *service.Service,
	anchor *ledger.Ledger,
	targetSeq uint32,
	targetHash [32]byte,
	peerHint uint64,
) bool {
	if c.stoppedForShutdown() || c.replayFaultBlocked() {
		return false
	}
	// Retire superseded walkers only after releasing acquisitionMu: their
	// cancellation/cleanup may need locks owned by acquisition workers.
	var superseded []*inbound.Ledger
	defer func() { c.retireLegacyAcquisitions(superseded) }()
	anchor, identity, current := c.standardReplayBase(svc, anchor, targetSeq, targetHash)
	if !current {
		return false
	}
	if anchor != nil && anchor.Sequence() == targetSeq && anchor.Hash() == targetHash {
		if _, current = c.cancelStandardReplayPipelineIdentity(identity, "target_reached"); !current {
			return false
		}
		c.completeStoredConsensusRecovery(targetSeq, targetHash, anchor.ParentHash(), false)
		return true
	}
	anchorSeq := identity.collectSeq
	anchorHash := identity.collectHash
	if identity.active && anchorHash == ([32]byte{}) {
		anchorSeq = identity.anchorSeq
		anchorHash = identity.anchorHash
	}
	if !identity.active {
		if anchor == nil {
			return false
		}
		anchorSeq = anchor.Sequence()
		anchorHash = anchor.Hash()
	}
	links, linkState := c.standardReplayLinks(anchorSeq, anchorHash, targetSeq, targetHash)
	if linkState == standardReplayLinkConflict {
		c.cancelStandardReplayPipelineIdentity(identity, "link_conflict")
		return false
	}
	if linkState != standardReplayLinkReady {
		if identity.active {
			c.acquisitionMu.Lock()
			if c.standardReplayIdentityMatchesLocked(identity) && targetSeq > c.standardReplay.targetSeq {
				c.standardReplay.targetSeq = targetSeq
				c.standardReplay.targetHash = targetHash
			}
			c.acquisitionMu.Unlock()
			return true
		}
		return false
	}

	c.acquisitionMu.Lock()
	if c.stoppedForShutdown() || !c.standardReplayIdentityMatchesLocked(identity) {
		c.acquisitionMu.Unlock()
		return false
	}
	initial := !c.standardReplay.active
	if initial && len(links) < 2 {
		c.acquisitionMu.Unlock()
		return false
	}
	if !initial && len(links) == 0 {
		c.acquisitionMu.Unlock()
		return true
	}
	if !initial && anchor != nil &&
		(c.standardReplay.anchorSeq != anchor.Sequence() || c.standardReplay.anchorHash != anchor.Hash()) {
		c.acquisitionMu.Unlock()
		c.cancelStandardReplayPipelineIdentity(identity, "anchor_conflict")
		return false
	}
	if initial {
		c.standardReplay.generation++
		c.standardReplay.active = true
		c.standardReplay.applying = false
		c.standardReplay.pivotReady = true
		c.standardReplay.pivotSeq = anchor.Sequence()
		c.standardReplay.pivotHash = anchor.Hash()
		c.standardReplay.anchorSeq = anchor.Sequence()
		c.standardReplay.anchorHash = anchor.Hash()
		c.standardReplay.collectSeq = anchor.Sequence()
		c.standardReplay.collectHash = anchor.Hash()
		c.standardReplay.entries = make(map[uint32]*standardReplayEntry, standardReplayPreparedLimit)
		c.standardReplay.progressSampleAt = time.Now()
		c.standardReplay.sampleAnchorSeq = anchor.Sequence()
		c.standardReplay.stalledSamples = 0
		c.standardReplay.backpressured = false
	}
	if targetSeq > c.standardReplay.targetSeq ||
		(targetSeq == c.standardReplay.targetSeq && targetHash == c.standardReplay.targetHash) {
		c.standardReplay.targetSeq = targetSeq
		c.standardReplay.targetHash = targetHash
	}

	now := time.Now()
	for _, link := range links {
		if len(c.standardReplay.entries) >= standardReplayPreparedLimit ||
			c.standardReplayResidentCountLocked() >= standardReplayPipelineWindow {
			break
		}
		if existing := c.standardReplay.entries[link.seq]; existing != nil {
			if existing.hash != link.hash || existing.parentHash != link.parentHash {
				cancelIdentity := c.standardReplayIdentityLocked()
				c.acquisitionMu.Unlock()
				c.cancelStandardReplayPipelineIdentity(cancelIdentity, "entry_conflict")
				return false
			}
			c.standardReplay.collectSeq = link.seq
			c.standardReplay.collectHash = link.hash
			continue
		}
		peerID, ok := c.resolveAcquisitionPeer(link.seq, peerHint)
		if !ok {
			break
		}
		if c.replayNeedsFullStateLocked(link.hash) {
			c.startLedgerAcquisitionLegacyLocked(link.seq, link.hash, peerID)
			break
		}
		// Consensus may have started a full-state fetch before replay proved
		// this successor chain. The hash-keyed tracker would return that fetch
		// to the tx-only collector forever. Once the pivot is verified, replay
		// owns these proven successors; replace the redundant state walk.
		if c.standardReplay.pivotReady && link.seq > c.standardReplay.anchorSeq {
			if existing := c.fetchTracker.Find(link.hash); existing != nil && !existing.TransactionOnly() &&
				c.discardInboundAcquisitionLocked(existing) {
				superseded = append(superseded, existing)
				c.logger.Info("replacing full-state acquisition with verified-chain transaction replay",
					"seq", link.seq, "hash", fmt.Sprintf("%x", link.hash[:8]))
			}
		}
		il, created := c.startLedgerReplayAcquisitionLegacyLocked(link.seq, link.hash, peerID)
		if il == nil || !il.TransactionOnly() {
			break
		}
		c.standardReplay.entries[link.seq] = &standardReplayEntry{
			generation:  c.standardReplay.generation,
			seq:         link.seq,
			hash:        link.hash,
			parentHash:  link.parentHash,
			peerID:      peerID,
			requestedAt: now,
			acquisition: il,
		}
		c.standardReplay.collectSeq = link.seq
		c.standardReplay.collectHash = link.hash
		if created {
			c.replayPipelineRequested.Add(1)
		}
	}
	backpressureStarted := len(c.standardReplay.entries) >= standardReplayPreparedLimit &&
		c.standardReplay.collectSeq < c.standardReplay.targetSeq && !c.standardReplay.backpressured
	if backpressureStarted {
		c.standardReplay.backpressured = true
		c.replayPipelineBackpressureEvents.Add(1)
	} else if len(c.standardReplay.entries) < standardReplayPreparedLimit {
		c.standardReplay.backpressured = false
	}
	backpressurePivotSeq := c.standardReplay.pivotSeq
	backpressurePivotReady := c.standardReplay.pivotReady
	backpressureTailSeq := c.standardReplay.collectSeq
	backpressureTargetSeq := c.standardReplay.targetSeq
	backpressureOccupancy := len(c.standardReplay.entries)

	if c.consensusRecovery.targetHash == targetHash {
		if head := c.standardReplay.entries[c.standardReplay.anchorSeq+1]; head != nil {
			c.consensusRecovery.stepHash = head.hash
		}
	}
	armed := !initial || len(c.standardReplay.entries) > 0
	if !armed {
		cancelIdentity := c.standardReplayIdentityLocked()
		c.acquisitionMu.Unlock()
		c.cancelStandardReplayPipelineIdentity(cancelIdentity, "empty_pipeline")
		return false
	}
	c.acquisitionMu.Unlock()
	if backpressureStarted {
		c.logger.Info("standard replay collector paused at prepared capacity",
			"pivot_seq", backpressurePivotSeq,
			"pivot_ready", backpressurePivotReady,
			"prepared_tail_seq", backpressureTailSeq,
			"trusted_head_seq", backpressureTargetSeq,
			"prepared_occupancy", backpressureOccupancy,
			"prepared_limit", standardReplayPreparedLimit,
		)
	}
	return armed
}

func (c *catchupReplayCoordinator) standardReplayResidentCountLocked() int {
	count := 0
	for _, entry := range c.standardReplay.entries {
		if entry.acquisition != nil || (!entry.readyAt.IsZero() && !entry.durable) {
			count++
		}
	}
	return count
}

func (c *catchupReplayCoordinator) standardReplayIdentityLocked() standardReplayIdentity {
	return standardReplayIdentity{
		generation:       c.standardReplay.generation,
		active:           c.standardReplay.active,
		pivotReady:       c.standardReplay.pivotReady,
		initialCandidate: c.standardReplay.initialCandidate,
		pivotSeq:         c.standardReplay.pivotSeq,
		pivotHash:        c.standardReplay.pivotHash,
		anchorSeq:        c.standardReplay.anchorSeq,
		anchorHash:       c.standardReplay.anchorHash,
		collectSeq:       c.standardReplay.collectSeq,
		collectHash:      c.standardReplay.collectHash,
		targetSeq:        c.standardReplay.targetSeq,
		targetHash:       c.standardReplay.targetHash,
		pivotHandoff:     c.standardReplay.pivotHandoff,
	}
}

func (c *catchupReplayCoordinator) standardReplayIdentityMatchesLocked(identity standardReplayIdentity) bool {
	return c.standardReplayIdentityLocked() == identity
}

func (c *catchupReplayCoordinator) ownsFrozenPivotAcquisitionLocked(il *inbound.Ledger) bool {
	return il != nil && !il.TransactionOnly() && c.standardReplay.active &&
		!c.standardReplay.pivotReady && c.standardReplay.pivotHandoff == nil &&
		c.standardReplay.pivotHash == il.Hash() && c.standardReplay.pivotSeq == il.Seq()
}

func (c *catchupReplayCoordinator) claimStandardReplayPivotHandoffLocked(il *inbound.Ledger) (standardReplayPivotHandoff, bool) {
	if !c.ownsFrozenPivotAcquisitionLocked(il) {
		return standardReplayPivotHandoff{}, false
	}
	handoff := standardReplayPivotHandoff{
		generation:  c.standardReplay.generation,
		seq:         c.standardReplay.pivotSeq,
		hash:        c.standardReplay.pivotHash,
		acquisition: il,
	}
	c.standardReplay.pivotHandoff = &handoff
	return handoff, true
}

func (c *catchupReplayCoordinator) standardReplayPivotHandoffMatchesLocked(handoff standardReplayPivotHandoff) bool {
	return handoff.acquisition != nil && c.standardReplay.active &&
		c.standardReplay.pivotHandoff != nil &&
		c.standardReplay.pivotHandoff.generation == handoff.generation &&
		c.standardReplay.pivotHandoff.acquisition == handoff.acquisition &&
		c.standardReplay.generation == handoff.generation &&
		c.standardReplay.pivotSeq == handoff.seq &&
		c.standardReplay.pivotHash == handoff.hash
}

func (c *catchupReplayCoordinator) clearStandardReplayPivotHandoffLocked(handoff standardReplayPivotHandoff) bool {
	if !c.standardReplayPivotHandoffMatchesLocked(handoff) {
		return false
	}
	c.standardReplay.pivotHandoff = nil
	return true
}

func (c *catchupReplayCoordinator) cancelStandardReplayPipelineIdentity(
	identity standardReplayIdentity,
	reason string,
) (standardReplayIdentity, bool) {
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	if !c.standardReplayIdentityMatchesLocked(identity) {
		current := c.standardReplayIdentityLocked()
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return current, false
	}
	retired := c.cancelStandardReplayPipelineLocked(reason)
	current := c.standardReplayIdentityLocked()
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	c.retireStandardReplay(retired)
	return current, true
}

type standardReplayRetirement struct {
	ledgers    []*inbound.Ledger
	baseLedger *inbound.Ledger
	release    func()
}

func (c *catchupReplayCoordinator) logStandardReplayCancellation(
	reason string,
	generation uint64,
	anchorSeq uint32,
	anchorHash [32]byte,
	targetSeq uint32,
	targetHash [32]byte,
	discardedEntries int,
	discardedAcquisitions int,
) {
	if c.logger == nil {
		return
	}
	c.logger.Info("canceled standard transaction replay pipeline",
		"reason", reason,
		"generation", generation,
		"anchor_seq", anchorSeq,
		"anchor_hash", fmt.Sprintf("%x", anchorHash[:8]),
		"target_seq", targetSeq,
		"target_hash", fmt.Sprintf("%x", targetHash[:8]),
		"discarded_entries", discardedEntries,
		"discarded_acquisitions", discardedAcquisitions,
	)
}

func (c *catchupReplayCoordinator) cancelStandardReplayPipelineLocked(reason string) standardReplayRetirement {
	if !c.standardReplay.active && len(c.standardReplay.entries) == 0 && c.standardReplay.baseRelease == nil {
		return standardReplayRetirement{}
	}
	generation := c.standardReplay.generation
	anchorSeq := c.standardReplay.anchorSeq
	anchorHash := c.standardReplay.anchorHash
	targetSeq := c.standardReplay.targetSeq
	targetHash := c.standardReplay.targetHash
	discardedEntries := len(c.standardReplay.entries)
	var retired []*inbound.Ledger
	if !c.standardReplay.pivotReady {
		if pivot := c.fetchTracker.Find(c.standardReplay.pivotHash); pivot != nil &&
			!pivot.TransactionOnly() && c.discardInboundAcquisitionLocked(pivot) {
			retired = append(retired, pivot)
		}
	}
	for _, entry := range c.standardReplay.entries {
		if entry.failed {
			c.requireReplayFullStateLocked(entry.seq, entry.hash)
		}
		if entry.acquisition != nil && c.discardInboundAcquisitionLocked(entry.acquisition) {
			retired = append(retired, entry.acquisition)
		}
		if c.consensusRecovery.stepHash == entry.hash {
			c.consensusRecovery.stepHash = [32]byte{}
		}
		c.replayPipelineDiscarded.Add(1)
	}
	if c.consensusRecovery.stepHash == c.standardReplay.pivotHash {
		c.consensusRecovery.stepHash = [32]byte{}
	}
	c.standardReplay.generation++
	c.standardReplay.active = false
	c.standardReplay.applying = false
	c.standardReplay.pivotReady = false
	c.standardReplay.initialCandidate = false
	c.standardReplay.pivotSeq = 0
	c.standardReplay.pivotHash = [32]byte{}
	c.standardReplay.anchorSeq = 0
	c.standardReplay.anchorHash = [32]byte{}
	c.standardReplay.collectSeq = 0
	c.standardReplay.collectHash = [32]byte{}
	c.standardReplay.targetSeq = 0
	c.standardReplay.targetHash = [32]byte{}
	c.standardReplay.entries = nil
	c.standardReplay.headBlockedAt = time.Time{}
	c.standardReplay.pivotStartedAt = time.Time{}
	c.standardReplay.progressSampleAt = time.Time{}
	c.standardReplay.sampleAnchorSeq = 0
	c.standardReplay.stalledSamples = 0
	c.standardReplay.retargetAttemptAt = time.Time{}
	c.standardReplay.backpressured = false
	c.standardReplay.pivotHandoff = nil
	baseLedger := c.standardReplay.baseLedger
	c.standardReplay.baseLedger = nil
	release := c.standardReplay.baseRelease
	c.standardReplay.baseRelease = nil
	c.logStandardReplayCancellation(
		reason,
		generation,
		anchorSeq,
		anchorHash,
		targetSeq,
		targetHash,
		discardedEntries,
		len(retired),
	)
	return standardReplayRetirement{ledgers: retired, baseLedger: baseLedger, release: release}
}

func (c *catchupReplayCoordinator) retireStandardReplay(retirement standardReplayRetirement) <-chan struct{} {
	c.retireLegacyAcquisitions(retirement.ledgers)
	if retirement.release == nil {
		return nil
	}
	if retirement.baseLedger == nil {
		retirement.release()
		return nil
	}
	done := make(chan struct{})
	go func() {
		retirement.baseLedger.WaitForWork()
		retirement.release()
		close(done)
	}()
	return done
}

func (c *catchupReplayCoordinator) releaseStandardReplayBaseLocked() {
	c.standardReplay.baseLedger = nil
	if release := c.standardReplay.baseRelease; release != nil {
		c.standardReplay.baseRelease = nil
		release()
	}
}

func (c *catchupReplayCoordinator) discardSupersededProvisionalFullStateLocked(keepHash [32]byte) []*inbound.Ledger {
	if c.adaptor == nil {
		return nil
	}
	svc := c.adaptor.LedgerService()
	if svc == nil || !svc.IsFastLoadProvisional() {
		return nil
	}

	var retired []*inbound.Ledger
	for _, candidate := range c.fetchTracker.Active() {
		if candidate.Hash() == keepHash || candidate.Reason() != inbound.ReasonConsensus || candidate.TransactionOnly() {
			continue
		}
		if c.discardInboundAcquisitionLocked(candidate) {
			retired = append(retired, candidate)
		}
	}
	return retired
}

func (c *catchupReplayCoordinator) waitStandardReplayCommit() {
	c.replayCommitMu.Lock()
	c.replayCommitMu.Unlock()
}

func (c *catchupReplayCoordinator) standardReplayOwnsLocked(hash [32]byte) bool {
	if c.standardReplay.active &&
		(hash == c.standardReplay.pivotHash || hash == c.standardReplay.targetHash) {
		return true
	}
	for _, entry := range c.standardReplay.entries {
		if entry.hash == hash {
			return true
		}
	}
	return false
}

func (c *catchupReplayCoordinator) completeStandardReplayPipelineEntryLocked(
	il *inbound.Ledger,
	h *header.LedgerHeader,
	txMap *shamap.SHAMap,
	peerID uint64,
) (bool, bool) {
	if il == nil || h == nil {
		return false, false
	}
	now := time.Now()
	entry := c.standardReplay.entries[h.LedgerIndex]
	if !c.standardReplay.active || entry == nil || entry.hash != h.Hash ||
		entry.generation != c.standardReplay.generation || entry.acquisition != il {
		return false, false
	}
	entry.header = *h
	entry.txMap = txMap
	if c.acquisitionStore != nil && c.acquisitionFamily != nil {
		entry.durable = true
		entry.txMap = nil
	}
	entry.peerID = peerID
	entry.readyAt = now
	entry.acquisition = nil
	c.replayPipelineReady.Add(1)
	c.replayPipelineRetried.Add(uint64(il.Timeouts()))
	c.replayPipelineAcquireUs.Add(durationMicros(now.Sub(entry.requestedAt)))
	startDrain := c.standardReplay.pivotReady && !c.standardReplay.applying
	if startDrain {
		c.standardReplay.applying = true
	}
	c.updateStandardReplayHeadBlockLocked(now)
	return true, startDrain
}

func (c *catchupReplayCoordinator) refillStandardReplayCollector(peerHint uint64) bool {
	c.acquisitionMu.Lock()
	active := c.standardReplay.active
	targetSeq := c.standardReplay.targetSeq
	targetHash := c.standardReplay.targetHash
	c.acquisitionMu.Unlock()
	if !active || targetSeq == 0 || targetHash == ([32]byte{}) || c.adaptor == nil {
		return false
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return false
	}
	return c.tryArmStandardReplayPipeline(svc, nil, targetSeq, targetHash, peerHint)
}

func (c *catchupReplayCoordinator) failStandardReplayPipelineEntry(il *inbound.Ledger) bool {
	if il == nil {
		return false
	}
	now := time.Now()
	c.acquisitionMu.Lock()
	entry := c.standardReplay.entries[il.Seq()]
	if !c.standardReplay.active || entry == nil || entry.hash != il.Hash() || entry.acquisition != il {
		c.acquisitionMu.Unlock()
		return false
	}
	entry.failed = true
	entry.acquisition = nil
	entry.peerID = il.PeerID()
	c.replayPipelineRetried.Add(uint64(il.Timeouts()))
	// A failed entry may be far ahead of a frozen pivot that is still being
	// acquired. Do not let that failure wake the drain before the pivot is
	// installed: the prepared head has no locally available parent until then,
	// so applying it would cancel the replay pipeline and incorrectly fall back
	// to another full-state acquisition.
	startDrain := c.standardReplay.pivotReady && !c.standardReplay.applying
	if startDrain {
		c.standardReplay.applying = true
	}
	c.updateStandardReplayHeadBlockLocked(now)
	c.acquisitionMu.Unlock()
	if startDrain {
		c.drainStandardReplayPipeline()
	}
	return true
}

func (c *catchupReplayCoordinator) updateStandardReplayHeadBlockLocked(now time.Time) {
	if !c.standardReplay.active {
		c.standardReplay.headBlockedAt = time.Time{}
		return
	}
	head := c.standardReplay.entries[c.standardReplay.anchorSeq+1]
	if head != nil && (!head.readyAt.IsZero() || head.failed) {
		c.standardReplay.headBlockedAt = time.Time{}
		return
	}
	for seq, entry := range c.standardReplay.entries {
		if seq > c.standardReplay.anchorSeq+1 && (!entry.readyAt.IsZero() || entry.failed) {
			if c.standardReplay.headBlockedAt.IsZero() {
				c.standardReplay.headBlockedAt = now
			}
			return
		}
	}
	c.standardReplay.headBlockedAt = time.Time{}
}

func (c *catchupReplayCoordinator) scheduleStandardReplayDrain() {
	if c.stoppedForShutdown() {
		return
	}
	select {
	case c.standardReplayDrainWake <- struct{}{}:
	default:
	}
}

func (c *catchupReplayCoordinator) drainStandardReplayPipeline() {
	owner := c.beginStandardReplayDrain()
	if owner == nil {
		return
	}
	defer c.finishStandardReplayDrain(owner)
	batchStarted := time.Now()
	applied := 0
	for {
		if c.stoppedForShutdown() {
			return
		}
		c.acquisitionMu.Lock()
		if c.standardReplay.generation != owner.generation {
			c.acquisitionMu.Unlock()
			return
		}
		if !c.standardReplay.active {
			c.standardReplay.applying = false
			c.acquisitionMu.Unlock()
			return
		}
		entry := c.standardReplay.entries[c.standardReplay.anchorSeq+1]
		if entry == nil || (entry.readyAt.IsZero() && !entry.failed) {
			c.standardReplay.applying = false
			c.updateStandardReplayHeadBlockLocked(time.Now())
			c.acquisitionMu.Unlock()
			return
		}
		generation := c.standardReplay.generation
		if entry.failed {
			retired, target, current := c.discardStandardReplayHeadLocked(entry, generation)
			c.acquisitionMu.Unlock()
			c.waitStandardReplayCommit()
			c.retireStandardReplay(retired)
			if current {
				c.replayPipelineFallbacks.Add(1)
				c.fallbackStandardReplayAcquisition(entry.seq, entry.hash, entry.peerID, target)
			}
			return
		}
		copyEntry := *entry
		c.acquisitionMu.Unlock()

		applyStarted := time.Now()
		hdr, initialCandidate, persistDuration, releaseCommit, err := c.applyStandardReplayEntry(&copyEntry, entry, generation)
		applyDuration := time.Since(applyStarted) - persistDuration
		if applyDuration < 0 {
			applyDuration = 0
		}
		if err != nil {
			if c.stoppedForShutdown() {
				return
			}
			c.acquisitionMu.Lock()
			retired, target, current := c.discardStandardReplayHeadLocked(entry, generation)
			continueDrain := !current && c.standardReplay.active && c.standardReplay.generation == owner.generation
			c.acquisitionMu.Unlock()
			c.waitStandardReplayCommit()
			c.retireStandardReplay(retired)
			if current {
				c.replayPipelineFallbacks.Add(1)
				c.logger.Error("standard transaction replay pipeline apply failed; recovery gated",
					"seq", entry.seq,
					"hash", fmt.Sprintf("%x", entry.hash[:8]),
					"error", err,
				)
				c.fallbackStandardReplayAcquisition(entry.seq, entry.hash, entry.peerID, target)
			}
			if continueDrain {
				continue
			}
			return
		}

		c.acquisitionMu.Lock()
		current := c.standardReplay.active && c.standardReplay.generation == generation &&
			c.standardReplay.entries[entry.seq] == entry && c.standardReplay.anchorSeq+1 == entry.seq
		if !current {
			if c.standardReplay.active && c.standardReplay.generation == owner.generation {
				c.acquisitionMu.Unlock()
				releaseCommit()
				continue
			}
			c.acquisitionMu.Unlock()
			releaseCommit()
			return
		}
		delete(c.standardReplay.entries, entry.seq)
		c.standardReplay.anchorSeq = entry.seq
		c.standardReplay.anchorHash = entry.hash
		c.replayPipelineApplied.Add(1)
		c.replayPipelineApplyUs.Add(durationMicros(applyDuration))
		c.replayPipelinePersistUs.Add(durationMicros(persistDuration))
		c.replayPipelineReadyWaitUs.Add(durationMicros(applyStarted.Sub(entry.readyAt)))
		reachedTarget := entry.seq == c.standardReplay.targetSeq && entry.hash == c.standardReplay.targetHash
		if reachedTarget {
			initialCandidate = initialCandidate || c.standardReplay.initialCandidate
		}
		c.acquisitionMu.Unlock()
		releaseCommit()
		if c.stoppedForShutdown() {
			return
		}

		c.logger.Info("applied standard transaction replay pipeline entry",
			"seq", entry.seq,
			"hash", fmt.Sprintf("%x", entry.hash[:8]),
			"acquire_us", durationMicros(entry.readyAt.Sub(entry.requestedAt)),
			"ready_wait_us", durationMicros(applyStarted.Sub(entry.readyAt)),
			"apply_us", durationMicros(applyDuration),
			"persist_us", durationMicros(persistDuration),
		)
		c.completeStoredConsensusRecovery(hdr.LedgerIndex, hdr.Hash, hdr.ParentHash, initialCandidate)

		c.acquisitionMu.Lock()
		current = c.standardReplay.active && c.standardReplay.generation == generation &&
			c.standardReplay.anchorSeq == entry.seq && c.standardReplay.anchorHash == entry.hash
		if current && reachedTarget && c.standardReplay.targetSeq == entry.seq && c.standardReplay.targetHash == entry.hash {
			c.standardReplay.active = false
			c.standardReplay.applying = false
			c.standardReplay.entries = nil
			c.standardReplay.headBlockedAt = time.Time{}
			c.standardReplay.backpressured = false
			c.releaseStandardReplayBaseLocked()
		}
		active := c.standardReplay.active
		c.acquisitionMu.Unlock()
		if !current {
			return
		}
		if active {
			c.refillStandardReplayCollector(entry.peerID)
		}
		applied++
		if active && (applied >= standardReplayApplyBatch || time.Since(batchStarted) >= standardReplayApplyBudget) {
			// Release actual execution ownership before the next router-loop
			// batch. A replacement generation is re-armed only if ready.
			owner.yielded = true
			return
		}
	}
}

func (c *catchupReplayCoordinator) discardStandardReplayHeadLocked(
	entry *standardReplayEntry,
	generation uint64,
) (standardReplayRetirement, standardReplayTarget, bool) {
	target := standardReplayTarget{
		seq:  c.standardReplay.targetSeq,
		hash: c.standardReplay.targetHash,
	}
	if !c.standardReplay.active || c.standardReplay.generation != generation ||
		c.standardReplay.entries[entry.seq] != entry || c.standardReplay.anchorSeq+1 != entry.seq {
		if !c.standardReplay.active {
			c.standardReplay.applying = false
		}
		return standardReplayRetirement{}, standardReplayTarget{}, false
	}
	c.requireReplayFullStateLocked(entry.seq, entry.hash)
	retired := c.cancelStandardReplayPipelineLocked("head_failure")
	if c.consensusRecovery.targetHash != ([32]byte{}) {
		c.consensusRecovery.stepHash = entry.hash
	}
	c.standardReplay.applying = false
	return retired, target, true
}

func (c *catchupReplayCoordinator) applyStandardReplayEntry(
	entry, activeEntry *standardReplayEntry,
	generation uint64,
) (out header.LedgerHeader, initial bool, duration time.Duration, release func(), retErr error) {
	if c.stoppedForShutdown() {
		return header.LedgerHeader{}, false, 0, nil, context.Canceled
	}
	if entry == nil {
		return header.LedgerHeader{}, false, 0, nil, errors.New("nil standard replay pipeline entry")
	}
	h := entry.header
	defer func() {
		if retErr == nil || errors.Is(retErr, context.Canceled) || c.replayFaultBlocked() {
			return
		}
		c.acquisitionMu.Lock()
		current := c.standardReplay.active && c.standardReplay.generation == generation && c.standardReplay.entries[entry.seq] == activeEntry
		c.acquisitionMu.Unlock()
		if !current {
			return
		}
		svc := c.adaptor.LedgerService()
		if svc == nil {
			return
		}
		parent, _ := svc.GetLedgerByHash(h.ParentHash)
		txMap, _ := c.loadStandardReplayTransactionMap(c.lifecycleContext(), entry)
		svc.RecordReplayPreparationFailure(c.lifecycleContext(), h, txMap, parent, c.replayTargetAuthenticated(h), retErr)
	}()
	if h.Hash != entry.hash {
		return header.LedgerHeader{}, false, 0, nil, errors.New("prepared ledger hash changed")
	}
	if h.LedgerIndex != entry.seq {
		return header.LedgerHeader{}, false, 0, nil, fmt.Errorf("prepared ledger sequence %d does not match expected %d", h.LedgerIndex, entry.seq)
	}
	if h.ParentHash != entry.parentHash {
		return header.LedgerHeader{}, false, 0, nil, errors.New("prepared ledger no longer attaches to the accepted predecessor")
	}

	svc := c.adaptor.LedgerService()
	if svc == nil {
		return header.LedgerHeader{}, false, 0, nil, errors.New("no ledger service")
	}
	parent, err := svc.GetLedgerByHash(entry.parentHash)
	if err != nil || parent == nil {
		if err == nil {
			err = errors.New("accepted replay predecessor is unavailable")
		}
		return header.LedgerHeader{}, false, 0, nil, err
	}
	if parent.Sequence()+1 != entry.seq {
		return header.LedgerHeader{}, false, 0, nil, fmt.Errorf("replay predecessor sequence %d is not before %d", parent.Sequence(), entry.seq)
	}

	stateMap, err := parent.StateMapSnapshot()
	if err != nil {
		return header.LedgerHeader{}, false, 0, nil, fmt.Errorf("snapshot replay predecessor state: %w", err)
	}
	txMap, err := c.loadStandardReplayTransactionMap(c.lifecycleContext(), entry)
	if err != nil {
		return header.LedgerHeader{}, false, 0, nil, err
	}

	target, err := ledger.NewFromHeader(h, stateMap, txMap, parent.Fees())
	if err != nil {
		return header.LedgerHeader{}, false, 0, nil, fmt.Errorf("construct transaction-only replay target: %w", err)
	}
	replay, err := inbound.NewStoredLedgerReplay(parent, target, c.logger)
	if err != nil {
		return header.LedgerHeader{}, false, 0, nil, fmt.Errorf("prepare transaction-only replay: %w", err)
	}
	derived, err := svc.ApplyReplay(c.lifecycleContext(), replay, c.adaptor.EngineConfigForReplay(parent), c.replayTargetAuthenticated(h))
	if err != nil {
		return header.LedgerHeader{}, false, 0, nil, err
	}

	c.replayCommitMu.Lock()
	if c.stoppedForShutdown() {
		c.replayCommitMu.Unlock()
		return header.LedgerHeader{}, false, 0, nil, context.Canceled
	}
	c.acquisitionMu.Lock()
	current := c.standardReplay.active && c.standardReplay.generation == generation &&
		c.standardReplay.entries[activeEntry.seq] == activeEntry && c.standardReplay.anchorSeq+1 == activeEntry.seq
	if !current {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return header.LedgerHeader{}, false, 0, nil, errors.New("standard replay pipeline entry was superseded")
	}
	c.acquisitionMu.Unlock()
	persistStarted := time.Now()
	storedHeader, initialCandidate, err := c.storeVerifiedLedgerLocked(derived)
	persistDuration := time.Since(persistStarted)
	if err != nil {
		c.replayCommitMu.Unlock()
		return header.LedgerHeader{}, false, persistDuration, nil, err
	}
	return storedHeader, initialCandidate, persistDuration, c.replayCommitMu.Unlock, nil
}

func (c *catchupReplayCoordinator) loadStandardReplayTransactionMap(ctx context.Context, entry *standardReplayEntry) (*shamap.SHAMap, error) {
	if entry == nil {
		return nil, errors.New("nil standard replay pipeline entry")
	}
	txMap := entry.txMap
	if txMap == nil {
		if entry.header.TxHash == ([32]byte{}) {
			return shamap.New(shamap.TypeTransaction), nil
		}
		if !entry.durable || c.acquisitionFamily == nil {
			return nil, errors.New("missing transaction map for non-empty transaction root")
		}
		var err error
		txMap, err = shamap.NewFromRootHashContext(
			ctx, shamap.TypeTransaction, entry.header.TxHash, c.acquisitionFamily,
		)
		if err != nil {
			return nil, fmt.Errorf("reload prepared transaction map: %w", err)
		}
	}
	txHash, err := txMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("hash prepared transaction map: %w", err)
	}
	if txHash != entry.header.TxHash {
		return nil, errors.New("prepared transaction map root changed")
	}
	return txMap, nil
}

func durationMicros(d time.Duration) uint64 {
	if d <= 0 {
		return 0
	}
	return uint64(d.Microseconds())
}
