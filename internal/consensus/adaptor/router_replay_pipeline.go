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
	standardReplayPipelineWindow    = 8
	standardReplayPreparedLimit     = 2048
	standardReplayRetargetThreshold = standardReplayPreparedLimit * 7 / 8
	standardReplayProgressWindow    = time.Minute
	standardReplayStallWindows      = 2
	standardReplayApplyBatch        = 8
	standardReplayApplyBudget       = 25 * time.Millisecond
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

func (r *Router) standardReplayLinks(
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
		entry, ok := r.lookupSeqHash(seq)
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

func (r *Router) standardReplayBase(
	svc *service.Service,
	fallback *ledger.Ledger,
	targetSeq uint32,
	targetHash [32]byte,
) (*ledger.Ledger, standardReplayIdentity, bool) {
	if svc == nil {
		return fallback, standardReplayIdentity{}, false
	}

	r.acquisitionMu.Lock()
	identity := r.standardReplayIdentityLocked()
	r.acquisitionMu.Unlock()

	base := fallback
	if identity.active {
		if !identity.pivotReady {
			return nil, identity, true
		}
		if anchor, err := svc.GetLedgerByHash(identity.anchorHash); err == nil && anchor != nil && anchor.Sequence() == identity.anchorSeq {
			base = anchor
		} else {
			var current bool
			identity, current = r.cancelStandardReplayPipelineIdentity(identity)
			if !current {
				return fallback, standardReplayIdentity{}, false
			}
		}
	}

	advanced := false
	for base != nil && base.Sequence() < targetSeq {
		nextSeq := base.Sequence() + 1
		link, ok := r.lookupSeqHash(nextSeq)
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
		base = next
		advanced = true
	}
	if identity.active && advanced {
		var current bool
		identity, current = r.cancelStandardReplayPipelineIdentity(identity)
		if !current {
			return fallback, standardReplayIdentity{}, false
		}
	}
	return base, identity, true
}

func (r *Router) reconcileStandardReplayTarget(targetSeq uint32, targetHash [32]byte) {
	// The consensus engine may request an exact ledger hash before peer status
	// or validation bookkeeping has associated that hash with a sequence. An
	// unknown sequence is not evidence that the requested ledger precedes (or
	// conflicts with) the frozen replay anchor. Treating seq=0 as an older
	// target cancels the frozen pivot acquisition and drops startup back onto
	// the moving-head full-state treadmill.
	if targetSeq == 0 {
		return
	}
	r.acquisitionMu.Lock()
	identity := r.standardReplayIdentityLocked()
	r.acquisitionMu.Unlock()
	if !identity.active {
		return
	}
	if targetSeq < identity.anchorSeq ||
		(targetSeq == identity.anchorSeq && targetHash != identity.anchorHash) ||
		(targetSeq == identity.targetSeq && targetHash != identity.targetHash) {
		r.cancelStandardReplayPipelineIdentity(identity)
	}
}

func (r *Router) tryArmStandardReplayPipeline(
	svc *service.Service,
	anchor *ledger.Ledger,
	targetSeq uint32,
	targetHash [32]byte,
	peerHint uint64,
) bool {
	anchor, identity, current := r.standardReplayBase(svc, anchor, targetSeq, targetHash)
	if !current {
		return false
	}
	if anchor != nil && anchor.Sequence() == targetSeq && anchor.Hash() == targetHash {
		if _, current = r.cancelStandardReplayPipelineIdentity(identity); !current {
			return false
		}
		r.completeStoredConsensusRecovery(targetSeq, targetHash, anchor.ParentHash(), false)
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
	links, linkState := r.standardReplayLinks(anchorSeq, anchorHash, targetSeq, targetHash)
	if linkState == standardReplayLinkConflict {
		r.cancelStandardReplayPipelineIdentity(identity)
		return false
	}
	if linkState != standardReplayLinkReady {
		if identity.active {
			r.acquisitionMu.Lock()
			if r.standardReplayIdentityMatchesLocked(identity) && targetSeq > r.standardReplay.targetSeq {
				r.standardReplay.targetSeq = targetSeq
				r.standardReplay.targetHash = targetHash
			}
			r.acquisitionMu.Unlock()
			return true
		}
		return false
	}

	r.acquisitionMu.Lock()
	if !r.standardReplayIdentityMatchesLocked(identity) {
		r.acquisitionMu.Unlock()
		return false
	}
	initial := !r.standardReplay.active
	if initial && len(links) < 2 {
		r.acquisitionMu.Unlock()
		return false
	}
	if !initial && len(links) == 0 {
		r.acquisitionMu.Unlock()
		return true
	}
	if !initial && anchor != nil &&
		(r.standardReplay.anchorSeq != anchor.Sequence() || r.standardReplay.anchorHash != anchor.Hash()) {
		r.acquisitionMu.Unlock()
		r.cancelStandardReplayPipelineIdentity(identity)
		return false
	}
	if initial {
		r.standardReplay.generation++
		r.standardReplay.active = true
		r.standardReplay.pivotReady = true
		r.standardReplay.pivotSeq = anchor.Sequence()
		r.standardReplay.pivotHash = anchor.Hash()
		r.standardReplay.anchorSeq = anchor.Sequence()
		r.standardReplay.anchorHash = anchor.Hash()
		r.standardReplay.collectSeq = anchor.Sequence()
		r.standardReplay.collectHash = anchor.Hash()
		r.standardReplay.entries = make(map[uint32]*standardReplayEntry, standardReplayPreparedLimit)
		r.standardReplay.progressSampleAt = time.Now()
		r.standardReplay.sampleAnchorSeq = anchor.Sequence()
		r.standardReplay.stalledSamples = 0
	}
	if targetSeq > r.standardReplay.targetSeq ||
		(targetSeq == r.standardReplay.targetSeq && targetHash == r.standardReplay.targetHash) {
		r.standardReplay.targetSeq = targetSeq
		r.standardReplay.targetHash = targetHash
	}

	now := time.Now()
	for _, link := range links {
		if len(r.standardReplay.entries) >= standardReplayPreparedLimit ||
			r.standardReplayResidentCountLocked() >= standardReplayPipelineWindow {
			break
		}
		if existing := r.standardReplay.entries[link.seq]; existing != nil {
			if existing.hash != link.hash || existing.parentHash != link.parentHash {
				cancelIdentity := r.standardReplayIdentityLocked()
				r.acquisitionMu.Unlock()
				r.cancelStandardReplayPipelineIdentity(cancelIdentity)
				return false
			}
			r.standardReplay.collectSeq = link.seq
			r.standardReplay.collectHash = link.hash
			continue
		}
		peerID, ok := r.resolveAcquisitionPeer(link.seq, peerHint)
		if !ok {
			break
		}
		il, created := r.startLedgerReplayAcquisitionLegacyLocked(link.seq, link.hash, peerID)
		if il == nil || !il.TransactionOnly() {
			break
		}
		r.standardReplay.entries[link.seq] = &standardReplayEntry{
			generation:  r.standardReplay.generation,
			seq:         link.seq,
			hash:        link.hash,
			parentHash:  link.parentHash,
			peerID:      peerID,
			requestedAt: now,
			acquisition: il,
		}
		r.standardReplay.collectSeq = link.seq
		r.standardReplay.collectHash = link.hash
		if created {
			r.replayPipelineRequested.Add(1)
		}
	}

	if r.consensusRecovery.targetHash == targetHash {
		if head := r.standardReplay.entries[r.standardReplay.anchorSeq+1]; head != nil {
			r.consensusRecovery.stepHash = head.hash
		}
	}
	armed := !initial || len(r.standardReplay.entries) > 0
	if !armed {
		cancelIdentity := r.standardReplayIdentityLocked()
		r.acquisitionMu.Unlock()
		r.cancelStandardReplayPipelineIdentity(cancelIdentity)
		return false
	}
	r.acquisitionMu.Unlock()
	return armed
}

func (r *Router) standardReplayResidentCountLocked() int {
	count := 0
	for _, entry := range r.standardReplay.entries {
		if entry.acquisition != nil || (!entry.readyAt.IsZero() && !entry.durable) {
			count++
		}
	}
	return count
}

func (r *Router) standardReplayIdentityLocked() standardReplayIdentity {
	return standardReplayIdentity{
		generation:       r.standardReplay.generation,
		active:           r.standardReplay.active,
		pivotReady:       r.standardReplay.pivotReady,
		initialCandidate: r.standardReplay.initialCandidate,
		pivotSeq:         r.standardReplay.pivotSeq,
		pivotHash:        r.standardReplay.pivotHash,
		anchorSeq:        r.standardReplay.anchorSeq,
		anchorHash:       r.standardReplay.anchorHash,
		collectSeq:       r.standardReplay.collectSeq,
		collectHash:      r.standardReplay.collectHash,
		targetSeq:        r.standardReplay.targetSeq,
		targetHash:       r.standardReplay.targetHash,
	}
}

func (r *Router) standardReplayIdentityMatchesLocked(identity standardReplayIdentity) bool {
	return r.standardReplayIdentityLocked() == identity
}

func (r *Router) cancelStandardReplayPipelineIdentity(identity standardReplayIdentity) (standardReplayIdentity, bool) {
	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	if !r.standardReplayIdentityMatchesLocked(identity) {
		current := r.standardReplayIdentityLocked()
		r.acquisitionMu.Unlock()
		r.replayCommitMu.Unlock()
		return current, false
	}
	retired := r.cancelStandardReplayPipelineLocked()
	current := r.standardReplayIdentityLocked()
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()
	r.retireLegacyAcquisitions(retired)
	return current, true
}

func (r *Router) cancelStandardReplayPipelineLocked() []*inbound.Ledger {
	if !r.standardReplay.active && len(r.standardReplay.entries) == 0 {
		return nil
	}
	var retired []*inbound.Ledger
	if !r.standardReplay.pivotReady {
		if pivot := r.fetchTracker.Find(r.standardReplay.pivotHash); pivot != nil &&
			!pivot.TransactionOnly() && r.fetchTracker.DiscardExpected(pivot) {
			retired = append(retired, pivot)
		}
	}
	for _, entry := range r.standardReplay.entries {
		if entry.acquisition != nil && r.fetchTracker.DiscardExpected(entry.acquisition) {
			retired = append(retired, entry.acquisition)
		}
		if r.consensusRecovery.stepHash == entry.hash {
			r.consensusRecovery.stepHash = [32]byte{}
		}
		r.replayPipelineDiscarded.Add(1)
	}
	r.standardReplay.generation++
	r.standardReplay.active = false
	r.standardReplay.pivotReady = false
	r.standardReplay.initialCandidate = false
	r.standardReplay.pivotSeq = 0
	r.standardReplay.pivotHash = [32]byte{}
	r.standardReplay.anchorSeq = 0
	r.standardReplay.anchorHash = [32]byte{}
	r.standardReplay.collectSeq = 0
	r.standardReplay.collectHash = [32]byte{}
	r.standardReplay.targetSeq = 0
	r.standardReplay.targetHash = [32]byte{}
	r.standardReplay.entries = nil
	r.standardReplay.headBlockedAt = time.Time{}
	r.standardReplay.pivotStartedAt = time.Time{}
	r.standardReplay.progressSampleAt = time.Time{}
	r.standardReplay.sampleAnchorSeq = 0
	r.standardReplay.stalledSamples = 0
	r.standardReplay.retargetAttemptAt = time.Time{}
	return retired
}

func (r *Router) discardSupersededProvisionalFullStateLocked(keepHash [32]byte) []*inbound.Ledger {
	if r.adaptor == nil {
		return nil
	}
	svc := r.adaptor.LedgerService()
	if svc == nil || !svc.IsFastLoadProvisional() {
		return nil
	}

	var retired []*inbound.Ledger
	for _, candidate := range r.fetchTracker.Active() {
		if candidate.Hash() == keepHash || candidate.Reason() != inbound.ReasonConsensus || candidate.TransactionOnly() {
			continue
		}
		if r.fetchTracker.DiscardExpected(candidate) {
			retired = append(retired, candidate)
		}
	}
	return retired
}

func (r *Router) waitStandardReplayCommit() {
	r.replayCommitMu.Lock()
	r.replayCommitMu.Unlock()
}

func (r *Router) standardReplayOwnsLocked(hash [32]byte) bool {
	if r.standardReplay.active &&
		(hash == r.standardReplay.pivotHash || hash == r.standardReplay.targetHash) {
		return true
	}
	for _, entry := range r.standardReplay.entries {
		if entry.hash == hash {
			return true
		}
	}
	return false
}

func (r *Router) completeStandardReplayPipelineEntryLocked(
	il *inbound.Ledger,
	h *header.LedgerHeader,
	txMap *shamap.SHAMap,
	peerID uint64,
) (bool, bool) {
	if il == nil || h == nil {
		return false, false
	}
	now := time.Now()
	entry := r.standardReplay.entries[h.LedgerIndex]
	if !r.standardReplay.active || entry == nil || entry.hash != h.Hash ||
		entry.generation != r.standardReplay.generation || entry.acquisition != il {
		return false, false
	}
	entry.header = *h
	entry.txMap = txMap
	if r.acquisitionStore != nil && r.acquisitionFamily != nil {
		entry.durable = true
		entry.txMap = nil
	}
	entry.peerID = peerID
	entry.readyAt = now
	entry.acquisition = nil
	r.replayPipelineReady.Add(1)
	r.replayPipelineRetried.Add(uint64(il.Timeouts()))
	r.replayPipelineAcquireUs.Add(durationMicros(now.Sub(entry.requestedAt)))
	startDrain := r.standardReplay.pivotReady && !r.standardReplay.applying
	if startDrain {
		r.standardReplay.applying = true
	}
	r.updateStandardReplayHeadBlockLocked(now)
	return true, startDrain
}

func (r *Router) refillStandardReplayCollector(peerHint uint64) bool {
	r.acquisitionMu.Lock()
	active := r.standardReplay.active
	targetSeq := r.standardReplay.targetSeq
	targetHash := r.standardReplay.targetHash
	r.acquisitionMu.Unlock()
	if !active || targetSeq == 0 || targetHash == ([32]byte{}) || r.adaptor == nil {
		return false
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return false
	}
	return r.tryArmStandardReplayPipeline(svc, nil, targetSeq, targetHash, peerHint)
}

func (r *Router) failStandardReplayPipelineEntry(il *inbound.Ledger) bool {
	if il == nil {
		return false
	}
	now := time.Now()
	r.acquisitionMu.Lock()
	entry := r.standardReplay.entries[il.Seq()]
	if !r.standardReplay.active || entry == nil || entry.hash != il.Hash() || entry.acquisition != il {
		r.acquisitionMu.Unlock()
		return false
	}
	entry.failed = true
	entry.acquisition = nil
	entry.peerID = il.PeerID()
	r.replayPipelineRetried.Add(uint64(il.Timeouts()))
	// A failed entry may be far ahead of a frozen pivot that is still being
	// acquired. Do not let that failure wake the drain before the pivot is
	// installed: the prepared head has no locally available parent until then,
	// so applying it would cancel the replay pipeline and incorrectly fall back
	// to another full-state acquisition.
	startDrain := r.standardReplay.pivotReady && !r.standardReplay.applying
	if startDrain {
		r.standardReplay.applying = true
	}
	r.updateStandardReplayHeadBlockLocked(now)
	r.acquisitionMu.Unlock()
	if startDrain {
		r.drainStandardReplayPipeline()
	}
	return true
}

func (r *Router) updateStandardReplayHeadBlockLocked(now time.Time) {
	if !r.standardReplay.active {
		r.standardReplay.headBlockedAt = time.Time{}
		return
	}
	head := r.standardReplay.entries[r.standardReplay.anchorSeq+1]
	if head != nil && (!head.readyAt.IsZero() || head.failed) {
		r.standardReplay.headBlockedAt = time.Time{}
		return
	}
	for seq, entry := range r.standardReplay.entries {
		if seq > r.standardReplay.anchorSeq+1 && (!entry.readyAt.IsZero() || entry.failed) {
			if r.standardReplay.headBlockedAt.IsZero() {
				r.standardReplay.headBlockedAt = now
			}
			return
		}
	}
	r.standardReplay.headBlockedAt = time.Time{}
}

func (r *Router) scheduleStandardReplayDrain() {
	select {
	case r.standardReplayDrainWake <- struct{}{}:
	default:
	}
}

func (r *Router) drainStandardReplayPipeline() {
	batchStarted := time.Now()
	applied := 0
	for {
		r.acquisitionMu.Lock()
		if !r.standardReplay.active {
			r.standardReplay.applying = false
			r.acquisitionMu.Unlock()
			return
		}
		entry := r.standardReplay.entries[r.standardReplay.anchorSeq+1]
		if entry == nil || (entry.readyAt.IsZero() && !entry.failed) {
			r.standardReplay.applying = false
			r.updateStandardReplayHeadBlockLocked(time.Now())
			r.acquisitionMu.Unlock()
			return
		}
		generation := r.standardReplay.generation
		if entry.failed {
			retired, target, current := r.discardStandardReplayHeadLocked(entry, generation)
			r.acquisitionMu.Unlock()
			r.waitStandardReplayCommit()
			r.retireLegacyAcquisitions(retired)
			if current {
				r.replayPipelineFallbacks.Add(1)
				r.fallbackStandardReplayAcquisition(entry.seq, entry.hash, entry.peerID, target)
			}
			return
		}
		copyEntry := *entry
		r.acquisitionMu.Unlock()

		applyStarted := time.Now()
		hdr, initialCandidate, persistDuration, releaseCommit, err := r.applyStandardReplayEntry(&copyEntry, entry, generation)
		applyDuration := time.Since(applyStarted) - persistDuration
		if applyDuration < 0 {
			applyDuration = 0
		}
		if err != nil {
			r.acquisitionMu.Lock()
			retired, target, current := r.discardStandardReplayHeadLocked(entry, generation)
			continueDrain := !current && r.standardReplay.active
			r.acquisitionMu.Unlock()
			r.waitStandardReplayCommit()
			r.retireLegacyAcquisitions(retired)
			if current {
				r.replayPipelineFallbacks.Add(1)
				r.logger.Error("standard transaction replay pipeline apply failed; falling back to full-state acquisition",
					"seq", entry.seq,
					"hash", fmt.Sprintf("%x", entry.hash[:8]),
					"error", err,
				)
				r.fallbackStandardReplayAcquisition(entry.seq, entry.hash, entry.peerID, target)
			}
			if continueDrain {
				continue
			}
			return
		}

		r.acquisitionMu.Lock()
		current := r.standardReplay.active && r.standardReplay.generation == generation &&
			r.standardReplay.entries[entry.seq] == entry && r.standardReplay.anchorSeq+1 == entry.seq
		if !current {
			if r.standardReplay.active {
				r.acquisitionMu.Unlock()
				releaseCommit()
				continue
			}
			r.standardReplay.applying = false
			r.acquisitionMu.Unlock()
			releaseCommit()
			return
		}
		delete(r.standardReplay.entries, entry.seq)
		r.standardReplay.anchorSeq = entry.seq
		r.standardReplay.anchorHash = entry.hash
		r.replayPipelineApplied.Add(1)
		r.replayPipelineApplyUs.Add(durationMicros(applyDuration))
		r.replayPipelinePersistUs.Add(durationMicros(persistDuration))
		r.replayPipelineReadyWaitUs.Add(durationMicros(applyStarted.Sub(entry.readyAt)))
		reachedTarget := entry.seq == r.standardReplay.targetSeq && entry.hash == r.standardReplay.targetHash
		if reachedTarget {
			initialCandidate = initialCandidate || r.standardReplay.initialCandidate
		}
		r.acquisitionMu.Unlock()
		releaseCommit()

		r.logger.Info("applied standard transaction replay pipeline entry",
			"seq", entry.seq,
			"hash", fmt.Sprintf("%x", entry.hash[:8]),
			"acquire_us", durationMicros(entry.readyAt.Sub(entry.requestedAt)),
			"ready_wait_us", durationMicros(applyStarted.Sub(entry.readyAt)),
			"apply_us", durationMicros(applyDuration),
			"persist_us", durationMicros(persistDuration),
		)
		r.completeStoredConsensusRecovery(hdr.LedgerIndex, hdr.Hash, hdr.ParentHash, initialCandidate)

		r.acquisitionMu.Lock()
		current = r.standardReplay.active && r.standardReplay.generation == generation &&
			r.standardReplay.anchorSeq == entry.seq && r.standardReplay.anchorHash == entry.hash
		if current && reachedTarget && r.standardReplay.targetSeq == entry.seq && r.standardReplay.targetHash == entry.hash {
			r.standardReplay.active = false
			r.standardReplay.applying = false
			r.standardReplay.entries = nil
			r.standardReplay.headBlockedAt = time.Time{}
		}
		active := r.standardReplay.active
		r.acquisitionMu.Unlock()
		if !current {
			return
		}
		applied++
		if active && (applied >= standardReplayApplyBatch || time.Since(batchStarted) >= standardReplayApplyBudget) {
			// Keep applying set while the edge-triggered wake is pending. A
			// completion that lands before the wake is consumed joins this same
			// drain instead of starting a concurrent applier.
			r.scheduleStandardReplayDrain()
			return
		}
	}
}

func (r *Router) discardStandardReplayHeadLocked(
	entry *standardReplayEntry,
	generation uint64,
) ([]*inbound.Ledger, standardReplayTarget, bool) {
	target := standardReplayTarget{
		seq:  r.standardReplay.targetSeq,
		hash: r.standardReplay.targetHash,
	}
	if !r.standardReplay.active || r.standardReplay.generation != generation ||
		r.standardReplay.entries[entry.seq] != entry || r.standardReplay.anchorSeq+1 != entry.seq {
		if !r.standardReplay.active {
			r.standardReplay.applying = false
		}
		return nil, standardReplayTarget{}, false
	}
	retired := r.cancelStandardReplayPipelineLocked()
	if r.consensusRecovery.targetHash != ([32]byte{}) {
		r.consensusRecovery.stepHash = entry.hash
	}
	r.standardReplay.applying = false
	return retired, target, true
}

func (r *Router) applyStandardReplayEntry(
	entry, activeEntry *standardReplayEntry,
	generation uint64,
) (header.LedgerHeader, bool, time.Duration, func(), error) {
	if entry == nil {
		return header.LedgerHeader{}, false, 0, nil, errors.New("nil standard replay pipeline entry")
	}
	h := entry.header
	if h.Hash != entry.hash {
		return header.LedgerHeader{}, false, 0, nil, errors.New("prepared ledger hash changed")
	}
	if h.LedgerIndex != entry.seq {
		return header.LedgerHeader{}, false, 0, nil, fmt.Errorf("prepared ledger sequence %d does not match expected %d", h.LedgerIndex, entry.seq)
	}
	if h.ParentHash != entry.parentHash {
		return header.LedgerHeader{}, false, 0, nil, errors.New("prepared ledger no longer attaches to the accepted predecessor")
	}

	svc := r.adaptor.LedgerService()
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
	txMap, err := r.loadStandardReplayTransactionMap(r.lifecycleContext(), entry)
	if err != nil {
		return header.LedgerHeader{}, false, 0, nil, err
	}

	target, err := ledger.NewFromHeader(h, stateMap, txMap, parent.Fees())
	if err != nil {
		return header.LedgerHeader{}, false, 0, nil, fmt.Errorf("construct transaction-only replay target: %w", err)
	}
	replay, err := inbound.NewStoredLedgerReplay(parent, target, r.logger)
	if err != nil {
		return header.LedgerHeader{}, false, 0, nil, fmt.Errorf("prepare transaction-only replay: %w", err)
	}
	derived, err := replay.Apply(r.adaptor.EngineConfigForReplay(parent))
	if err != nil {
		return header.LedgerHeader{}, false, 0, nil, err
	}

	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	current := r.standardReplay.active && r.standardReplay.generation == generation &&
		r.standardReplay.entries[activeEntry.seq] == activeEntry && r.standardReplay.anchorSeq+1 == activeEntry.seq
	if !current {
		r.acquisitionMu.Unlock()
		r.replayCommitMu.Unlock()
		return header.LedgerHeader{}, false, 0, nil, errors.New("standard replay pipeline entry was superseded")
	}
	r.acquisitionMu.Unlock()
	persistStarted := time.Now()
	storedHeader, initialCandidate, err := r.storeVerifiedLedger(derived)
	persistDuration := time.Since(persistStarted)
	if err != nil {
		r.replayCommitMu.Unlock()
		return header.LedgerHeader{}, false, persistDuration, nil, err
	}
	return storedHeader, initialCandidate, persistDuration, r.replayCommitMu.Unlock, nil
}

func (r *Router) loadStandardReplayTransactionMap(ctx context.Context, entry *standardReplayEntry) (*shamap.SHAMap, error) {
	if entry == nil {
		return nil, errors.New("nil standard replay pipeline entry")
	}
	txMap := entry.txMap
	if txMap == nil {
		if entry.header.TxHash == ([32]byte{}) {
			return shamap.New(shamap.TypeTransaction), nil
		}
		if !entry.durable || r.acquisitionFamily == nil {
			return nil, errors.New("missing transaction map for non-empty transaction root")
		}
		var err error
		txMap, err = shamap.NewFromRootHashContext(
			ctx, shamap.TypeTransaction, entry.header.TxHash, r.acquisitionFamily,
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
