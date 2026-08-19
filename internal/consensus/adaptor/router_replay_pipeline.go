package adaptor

import (
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/shamap"
)

const standardReplayPipelineWindow = 8

type standardReplayPipeline struct {
	generation    uint64
	active        bool
	applying      bool
	anchorSeq     uint32
	anchorHash    [32]byte
	targetSeq     uint32
	targetHash    [32]byte
	entries       map[uint32]*standardReplayEntry
	headBlockedAt time.Time
}

type standardReplayIdentity struct {
	generation uint64
	active     bool
	anchorSeq  uint32
	anchorHash [32]byte
	targetSeq  uint32
	targetHash [32]byte
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
	failed      bool
}

type standardReplayLink struct {
	seq        uint32
	hash       [32]byte
	parentHash [32]byte
}

func (r *Router) standardReplayLinks(
	anchor *ledger.Ledger,
	targetSeq uint32,
	targetHash [32]byte,
) ([]standardReplayLink, bool) {
	if anchor == nil || targetSeq <= anchor.Sequence() || targetHash == ([32]byte{}) {
		return nil, false
	}

	links := make([]standardReplayLink, 0, standardReplayPipelineWindow)
	parentHash := anchor.Hash()
	for seq := anchor.Sequence() + 1; seq <= targetSeq; seq++ {
		entry, ok := r.lookupSeqHash(seq)
		if !ok || entry.hash == ([32]byte{}) || !entry.haveParent || entry.parentHash != parentHash {
			return nil, false
		}
		if seq == targetSeq && entry.hash != targetHash {
			return nil, false
		}
		if len(links) < standardReplayPipelineWindow {
			links = append(links, standardReplayLink{
				seq:        seq,
				hash:       entry.hash,
				parentHash: parentHash,
			})
		}
		parentHash = entry.hash
	}
	return links, len(links) > 0
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
		compatible := targetSeq == identity.targetSeq && targetHash == identity.targetHash
		if !compatible && targetSeq > identity.anchorSeq {
			compatible = r.recoveryAnchorReachesTarget(identity.anchorSeq, identity.anchorHash, targetHash)
		}
		if !compatible {
			var current bool
			identity, current = r.cancelStandardReplayPipelineIdentity(identity)
			if !current {
				return fallback, standardReplayIdentity{}, false
			}
		} else if anchor, err := svc.GetLedgerByHash(identity.anchorHash); err == nil && anchor != nil && anchor.Sequence() == identity.anchorSeq {
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
	r.acquisitionMu.Lock()
	identity := r.standardReplayIdentityLocked()
	r.acquisitionMu.Unlock()
	if !identity.active {
		return
	}
	compatible := targetSeq == identity.targetSeq && targetHash == identity.targetHash
	if !compatible && targetSeq > identity.anchorSeq {
		compatible = r.recoveryAnchorReachesTarget(identity.anchorSeq, identity.anchorHash, targetHash)
	}
	if !compatible {
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
	links, proven := r.standardReplayLinks(anchor, targetSeq, targetHash)
	if !proven {
		r.cancelStandardReplayPipelineIdentity(identity)
		return false
	}
	for _, link := range links {
		if r.isBuildingLedger(link.seq) {
			r.cancelStandardReplayPipelineIdentity(identity)
			return false
		}
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
	if !initial && (r.standardReplay.anchorSeq != anchor.Sequence() || r.standardReplay.anchorHash != anchor.Hash()) {
		retired := r.cancelStandardReplayPipelineLocked()
		r.acquisitionMu.Unlock()
		r.retireLegacyAcquisitions(retired)
		return false
	}
	if initial {
		r.standardReplay.generation++
		r.standardReplay.active = true
		r.standardReplay.anchorSeq = anchor.Sequence()
		r.standardReplay.anchorHash = anchor.Hash()
		r.standardReplay.entries = make(map[uint32]*standardReplayEntry, standardReplayPipelineWindow)
	}
	r.standardReplay.targetSeq = targetSeq
	r.standardReplay.targetHash = targetHash

	desired := make(map[uint32]standardReplayLink, len(links))
	for _, link := range links {
		desired[link.seq] = link
	}
	retired := r.pruneStandardReplayEntriesLocked(desired, targetSeq)

	now := time.Now()
	for _, link := range links {
		if existing := r.standardReplay.entries[link.seq]; existing != nil {
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
		if created {
			r.replayPipelineRequested.Add(1)
		}
	}

	if r.consensusRecovery.targetHash == targetHash {
		if head := r.standardReplay.entries[r.standardReplay.anchorSeq+1]; head != nil {
			r.consensusRecovery.stepHash = head.hash
		}
	}
	armed := len(r.standardReplay.entries) > 0
	if !armed {
		retired = append(retired, r.cancelStandardReplayPipelineLocked()...)
	}
	r.acquisitionMu.Unlock()
	r.retireLegacyAcquisitions(retired)
	return armed
}

func (r *Router) pruneStandardReplayEntriesLocked(
	desired map[uint32]standardReplayLink,
	targetSeq uint32,
) []*inbound.Ledger {
	var retired []*inbound.Ledger
	for seq, entry := range r.standardReplay.entries {
		link, keep := desired[seq]
		keep = keep && seq <= targetSeq && entry.hash == link.hash && entry.parentHash == link.parentHash
		if keep {
			continue
		}
		if entry.acquisition != nil && r.fetchTracker.DiscardExpected(entry.acquisition) {
			retired = append(retired, entry.acquisition)
		}
		delete(r.standardReplay.entries, seq)
		r.replayPipelineDiscarded.Add(1)
		if r.consensusRecovery.stepHash == entry.hash {
			r.consensusRecovery.stepHash = [32]byte{}
		}
	}
	return retired
}

func (r *Router) cancelStandardReplayPipeline() {
	r.acquisitionMu.Lock()
	retired := r.cancelStandardReplayPipelineLocked()
	r.acquisitionMu.Unlock()
	r.retireLegacyAcquisitions(retired)
}

func (r *Router) standardReplayIdentityLocked() standardReplayIdentity {
	return standardReplayIdentity{
		generation: r.standardReplay.generation,
		active:     r.standardReplay.active,
		anchorSeq:  r.standardReplay.anchorSeq,
		anchorHash: r.standardReplay.anchorHash,
		targetSeq:  r.standardReplay.targetSeq,
		targetHash: r.standardReplay.targetHash,
	}
}

func (r *Router) standardReplayIdentityMatchesLocked(identity standardReplayIdentity) bool {
	return r.standardReplayIdentityLocked() == identity
}

func (r *Router) cancelStandardReplayPipelineIdentity(identity standardReplayIdentity) (standardReplayIdentity, bool) {
	r.acquisitionMu.Lock()
	if !r.standardReplayIdentityMatchesLocked(identity) {
		current := r.standardReplayIdentityLocked()
		r.acquisitionMu.Unlock()
		return current, false
	}
	retired := r.cancelStandardReplayPipelineLocked()
	current := r.standardReplayIdentityLocked()
	r.acquisitionMu.Unlock()
	r.retireLegacyAcquisitions(retired)
	return current, true
}

func (r *Router) cancelStandardReplayPipelineLocked() []*inbound.Ledger {
	if !r.standardReplay.active && len(r.standardReplay.entries) == 0 {
		return nil
	}
	var retired []*inbound.Ledger
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
	r.standardReplay.anchorSeq = 0
	r.standardReplay.anchorHash = [32]byte{}
	r.standardReplay.targetSeq = 0
	r.standardReplay.targetHash = [32]byte{}
	r.standardReplay.entries = nil
	r.standardReplay.headBlockedAt = time.Time{}
	return retired
}

func (r *Router) standardReplayOwnsLocked(hash [32]byte) bool {
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
	entry.peerID = peerID
	entry.readyAt = now
	entry.acquisition = nil
	r.replayPipelineReady.Add(1)
	r.replayPipelineRetried.Add(uint64(il.Timeouts()))
	r.replayPipelineAcquireUs.Add(durationMicros(now.Sub(entry.requestedAt)))
	startDrain := !r.standardReplay.applying
	if startDrain {
		r.standardReplay.applying = true
	}
	r.updateStandardReplayHeadBlockLocked(now)
	return true, startDrain
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
	startDrain := !r.standardReplay.applying
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

func (r *Router) drainStandardReplayPipeline() {
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
		hdr, initialCandidate, persistDuration, err := r.applyStandardReplayEntry(&copyEntry, entry, generation)
		applyDuration := time.Since(applyStarted) - persistDuration
		if applyDuration < 0 {
			applyDuration = 0
		}
		if err != nil {
			r.acquisitionMu.Lock()
			retired, target, current := r.discardStandardReplayHeadLocked(entry, generation)
			continueDrain := !current && r.standardReplay.active
			r.acquisitionMu.Unlock()
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
				continue
			}
			r.standardReplay.applying = false
			r.acquisitionMu.Unlock()
			return
		}
		delete(r.standardReplay.entries, entry.seq)
		r.standardReplay.anchorSeq = entry.seq
		r.standardReplay.anchorHash = entry.hash
		r.replayPipelineApplied.Add(1)
		r.replayPipelineApplyUs.Add(durationMicros(applyDuration))
		r.replayPipelinePersistUs.Add(durationMicros(persistDuration))
		r.replayPipelineReadyWaitUs.Add(durationMicros(applyStarted.Sub(entry.readyAt)))
		if entry.seq == r.standardReplay.targetSeq && entry.hash == r.standardReplay.targetHash {
			r.standardReplay.active = false
			r.standardReplay.entries = nil
			r.standardReplay.headBlockedAt = time.Time{}
		}
		r.acquisitionMu.Unlock()

		r.logger.Info("applied standard transaction replay pipeline entry",
			"seq", entry.seq,
			"hash", fmt.Sprintf("%x", entry.hash[:8]),
			"acquire_us", durationMicros(entry.readyAt.Sub(entry.requestedAt)),
			"ready_wait_us", durationMicros(applyStarted.Sub(entry.readyAt)),
			"apply_us", durationMicros(applyDuration),
			"persist_us", durationMicros(persistDuration),
		)
		r.completeStoredConsensusRecovery(hdr.LedgerIndex, hdr.Hash, hdr.ParentHash, initialCandidate)
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
) (header.LedgerHeader, bool, time.Duration, error) {
	if entry == nil {
		return header.LedgerHeader{}, false, 0, errors.New("nil standard replay pipeline entry")
	}
	h := entry.header
	if h.Hash != entry.hash {
		return header.LedgerHeader{}, false, 0, errors.New("prepared ledger hash changed")
	}
	if h.LedgerIndex != entry.seq {
		return header.LedgerHeader{}, false, 0, fmt.Errorf("prepared ledger sequence %d does not match expected %d", h.LedgerIndex, entry.seq)
	}
	if h.ParentHash != entry.parentHash {
		return header.LedgerHeader{}, false, 0, errors.New("prepared ledger no longer attaches to the accepted predecessor")
	}

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return header.LedgerHeader{}, false, 0, errors.New("no ledger service")
	}
	parent, err := svc.GetLedgerByHash(entry.parentHash)
	if err != nil || parent == nil {
		if err == nil {
			err = errors.New("accepted replay predecessor is unavailable")
		}
		return header.LedgerHeader{}, false, 0, err
	}
	if parent.Sequence()+1 != entry.seq {
		return header.LedgerHeader{}, false, 0, fmt.Errorf("replay predecessor sequence %d is not before %d", parent.Sequence(), entry.seq)
	}

	stateMap, err := parent.StateMapSnapshot()
	if err != nil {
		return header.LedgerHeader{}, false, 0, fmt.Errorf("snapshot replay predecessor state: %w", err)
	}
	txMap := entry.txMap
	if txMap == nil {
		if h.TxHash != ([32]byte{}) {
			return header.LedgerHeader{}, false, 0, errors.New("missing transaction map for non-empty transaction root")
		}
		txMap = shamap.New(shamap.TypeTransaction)
	} else {
		txHash, hashErr := txMap.Hash()
		if hashErr != nil {
			return header.LedgerHeader{}, false, 0, fmt.Errorf("hash prepared transaction map: %w", hashErr)
		}
		if txHash != h.TxHash {
			return header.LedgerHeader{}, false, 0, errors.New("prepared transaction map root changed")
		}
	}

	target, err := ledger.NewFromHeader(h, stateMap, txMap, parent.Fees())
	if err != nil {
		return header.LedgerHeader{}, false, 0, fmt.Errorf("construct transaction-only replay target: %w", err)
	}
	replay, err := inbound.NewStoredLedgerReplay(parent, target, r.logger)
	if err != nil {
		return header.LedgerHeader{}, false, 0, fmt.Errorf("prepare transaction-only replay: %w", err)
	}
	derived, err := replay.Apply(r.adaptor.EngineConfigForReplay(parent))
	if err != nil {
		return header.LedgerHeader{}, false, 0, err
	}

	r.acquisitionMu.Lock()
	current := r.standardReplay.active && r.standardReplay.generation == generation &&
		r.standardReplay.entries[activeEntry.seq] == activeEntry && r.standardReplay.anchorSeq+1 == activeEntry.seq
	if !current {
		r.acquisitionMu.Unlock()
		return header.LedgerHeader{}, false, 0, errors.New("standard replay pipeline entry was superseded")
	}
	persistStarted := time.Now()
	storedHeader, initialCandidate, err := r.storeVerifiedLedger(derived)
	persistDuration := time.Since(persistStarted)
	r.acquisitionMu.Unlock()
	if err != nil {
		return header.LedgerHeader{}, false, persistDuration, err
	}
	return storedHeader, initialCandidate, persistDuration, nil
}

func durationMicros(d time.Duration) uint64 {
	if d <= 0 {
		return 0
	}
	return uint64(d.Microseconds())
}
