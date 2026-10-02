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

type standardReplayReplacement struct {
	generation  uint64
	seq         uint32
	hash        [32]byte
	peerID      uint64
	acquisition *inbound.Ledger
	retryAt     time.Time
	installing  bool
}

const (
	standardReplayLocalCandidateCheckTimeout = time.Second
	standardReplayLocalCandidateCheckNodes   = 4096
	maxLocalReplayCandidates                 = 4
)

// Candidates retain traversal cursors, but never a storage-generation pin.
type localReplayCandidate struct {
	ledger       *ledger.Ledger
	header       header.LedgerHeader
	state, txs   *shamap.SHAMap
	verification *service.DetachedMapVerification
}

var errLocalReplayCandidateUnavailable = errors.New("local replay candidate unavailable")

// localReplayReplacementCandidate returns a complete, root-checked local
// ledger suitable for the replacement install path. A hash lookup alone is
// insufficient: the service may retain an unvalidated or partially materialized
// ledger by hash, so callers must receive usable state and transaction maps.
func (c *catchupReplayCoordinator) localReplayReplacementCandidate(
	seq uint32,
	hash [32]byte,
) (*header.LedgerHeader, *shamap.SHAMap, *shamap.SHAMap, error) {
	if seq == 0 || hash == ([32]byte{}) || c.adaptor == nil || c.stoppedForShutdown() {
		return nil, nil, nil, errLocalReplayCandidateUnavailable
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return nil, nil, nil, errLocalReplayCandidateUnavailable
	}
	// A concurrent probe must not queue another full interval behind this one.
	if !c.localReplayMu.TryLock() {
		return nil, nil, nil, errLocalReplayCandidateUnavailable
	}
	defer c.localReplayMu.Unlock()
	if c.stoppedForShutdown() {
		return nil, nil, nil, errLocalReplayCandidateUnavailable
	}
	ctx, cancel := context.WithTimeout(c.lifecycleContext(), standardReplayLocalCandidateCheckTimeout)
	defer cancel()
	ctx = shamap.WithTraversalBudget(ctx, standardReplayLocalCandidateCheckNodes)
	local, err := svc.GetLedgerByHashContext(ctx, hash)
	if err != nil || local == nil || !local.IsClosed() || local.Sequence() != seq || local.Hash() != hash {
		return nil, nil, nil, errLocalReplayCandidateUnavailable
	}
	h := local.Header()
	if h.LedgerIndex != seq || h.Hash != hash || header.CalculateHash(h) != hash {
		return nil, nil, nil, errLocalReplayCandidateUnavailable
	}
	var candidate *localReplayCandidate
	for i, cached := range c.localReplayCandidates {
		if cached.ledger == local && cached.header == h {
			candidate = cached
			copy(c.localReplayCandidates[1:i+1], c.localReplayCandidates[:i])
			c.localReplayCandidates[0] = candidate
			break
		}
	}
	if candidate == nil {
		stateMap, err := local.StateMapSnapshot()
		if err != nil || stateMap == nil {
			return nil, nil, nil, errLocalReplayCandidateUnavailable
		}
		txMap, err := local.TxMapSnapshot()
		if err != nil || txMap == nil {
			return nil, nil, nil, errLocalReplayCandidateUnavailable
		}
		candidate = &localReplayCandidate{
			ledger: local, header: h, state: stateMap, txs: txMap,
			verification: svc.NewDetachedMapVerification(stateMap, txMap),
		}
		if len(c.localReplayCandidates) < maxLocalReplayCandidates {
			c.localReplayCandidates = append(c.localReplayCandidates, nil)
		}
		copy(c.localReplayCandidates[1:], c.localReplayCandidates)
		c.localReplayCandidates[0] = candidate
	}
	stateRoot, err := candidate.state.Hash()
	if err != nil || stateRoot != h.AccountHash {
		return nil, nil, nil, errLocalReplayCandidateUnavailable
	}
	txRoot, err := candidate.txs.Hash()
	if err != nil || txRoot != h.TxHash {
		return nil, nil, nil, errLocalReplayCandidateUnavailable
	}
	if err := candidate.verification.Verify(ctx); err != nil {
		return nil, nil, nil, err
	}
	return &h, candidate.state, candidate.txs, nil
}

func (c *catchupReplayCoordinator) reserveStandardReplayReplacement(generation uint64, seq uint32, hash [32]byte, peerID uint64, now time.Time) bool {
	c.acquisitionMu.Lock()
	if c.stoppedForShutdown() || !c.standardReplay.active || !c.standardReplay.pivotReady ||
		c.standardReplay.generation != generation || seq <= c.standardReplay.anchorSeq || hash == ([32]byte{}) {
		c.acquisitionMu.Unlock()
		return false
	}
	if c.standardReplay.replacement != nil {
		c.acquisitionMu.Unlock()
		return false
	}
	if seq >= c.standardReplay.targetSeq {
		c.standardReplay.targetSeq, c.standardReplay.targetHash = seq, hash
	}
	c.standardReplay.replacement = &standardReplayReplacement{generation: generation, seq: seq, hash: hash, peerID: peerID}
	c.replayPipelineFallbacks.Add(1)
	c.logger.Info("retaining replay anchor while replacing unavailable replay", "generation", generation,
		"anchor_seq", c.standardReplay.anchorSeq, "replacement_seq", seq, "replacement_hash", fmt.Sprintf("%x", hash[:8]),
		"retained_entries", len(c.standardReplay.entries))
	c.acquisitionMu.Unlock()
	c.retryStandardReplayReplacement(now)
	return true
}

func (c *catchupReplayCoordinator) retryStandardReplayReplacement(now time.Time) {
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	replacement := c.standardReplay.replacement
	if c.stoppedForShutdown() || !c.standardReplay.active || replacement == nil ||
		replacement.generation != c.standardReplay.generation {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return
	}
	if replacement.seq <= c.standardReplay.anchorSeq {
		retired := c.discardObsoleteStandardReplayReplacementLocked()
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		if retired != nil {
			c.retireLegacyAcquisitions([]*inbound.Ledger{retired})
		}
		return
	}
	if replacement.installing || now.Before(replacement.retryAt) {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return
	}
	if replacement.acquisition != nil {
		if c.fetchTracker.Find(replacement.hash) == replacement.acquisition {
			c.acquisitionMu.Unlock()
			c.replayCommitMu.Unlock()
			return
		}
		replacement.acquisition = nil
	}
	generation, seq, hash, hint := replacement.generation, replacement.seq, replacement.hash, replacement.peerID
	anchorSeq, targetSeq, targetHash := c.standardReplay.anchorSeq, c.standardReplay.targetSeq, c.standardReplay.targetHash
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	localHeader, stateMap, txMap, err := c.localReplayReplacementCandidate(seq, hash)
	if errors.Is(err, shamap.ErrTraversalBudget) {
		return
	}
	if err == nil && c.completeLocalStandardReplayReplacement(generation, replacement, localHeader, stateMap, txMap) {
		return
	}
	var survivor *inbound.Ledger
	for _, candidate := range c.fetchTracker.Active() {
		if candidate.TransactionOnly() || candidate.Reason() != inbound.ReasonConsensus || candidate.Seq() <= anchorSeq {
			continue
		}
		if candidate.Hash() == hash && candidate.Seq() == seq ||
			c.canAdoptKnownFrozenPivot(candidate.Seq(), candidate.Hash(), targetSeq, targetHash) {
			survivor = candidate
			seq, hash, hint = candidate.Seq(), candidate.Hash(), candidate.PeerID()
			break
		}
	}
	peerID := hint
	if survivor == nil {
		var ok bool
		peerID, ok = c.resolveAcquisitionPeer(seq, hint)
		if !ok {
			return
		}
	}
	c.acquisitionMu.Lock()
	if c.stoppedForShutdown() || !c.standardReplay.active || c.standardReplay.generation != generation ||
		c.standardReplay.replacement != replacement || replacement.acquisition != nil ||
		c.standardReplay.targetSeq != targetSeq || c.standardReplay.targetHash != targetHash ||
		c.standardReplay.anchorSeq >= seq ||
		(survivor != nil && c.fetchTracker.Find(hash) != survivor) {
		c.acquisitionMu.Unlock()
		return
	}
	var retired []*inbound.Ledger
	if old := c.fetchTracker.Find(hash); old != nil && old.TransactionOnly() {
		if c.discardInboundAcquisitionLocked(old) {
			retired = append(retired, old)
			if entry := c.standardReplay.entries[seq]; entry != nil && entry.acquisition == old {
				entry.acquisition = nil
			}
		}
	}
	if c.canAdmitCatchupLocked(hash, maxConcurrentCatchup) {
		admission := c.startFrozenPivotReplacementLocked(seq, hash, peerID)
		if il := admission.acquisition; (admission.outcome == fullStateAdmissionStarted || admission.outcome == fullStateAdmissionJoined) &&
			il != nil && !il.TransactionOnly() && il.Reason() == inbound.ReasonConsensus && il.Seq() == seq {
			replacement.seq, replacement.hash = seq, hash
			replacement.acquisition = il
			replacement.peerID = peerID
			if c.consensusRecovery.targetHash != ([32]byte{}) {
				c.consensusRecovery.stepHash = hash
			}
		}
	}
	c.acquisitionMu.Unlock()
	c.retireLegacyAcquisitions(retired)
}

// Caller holds replayCommitMu and acquisitionMu.
func (c *catchupReplayCoordinator) discardObsoleteStandardReplayReplacementLocked() *inbound.Ledger {
	replacement := c.standardReplay.replacement
	if !c.standardReplay.active || replacement == nil || replacement.generation != c.standardReplay.generation ||
		replacement.seq > c.standardReplay.anchorSeq {
		return nil
	}
	var retired *inbound.Ledger
	if replacement.acquisition != nil && c.discardInboundAcquisitionLocked(replacement.acquisition) {
		retired = replacement.acquisition
	}
	if c.consensusRecovery.stepHash == replacement.hash {
		c.consensusRecovery.stepHash = [32]byte{}
	}
	c.standardReplay.replacement = nil
	return retired
}

func (c *catchupReplayCoordinator) failStandardReplayReplacement(il *inbound.Ledger, cause error) bool {
	c.acquisitionMu.Lock()
	replacement := c.standardReplay.replacement
	if replacement == nil || replacement.acquisition != il || replacement.generation != c.standardReplay.generation {
		c.acquisitionMu.Unlock()
		return false
	}
	replacement.acquisition = nil
	replacement.installing = false
	replacement.retryAt = time.Now().Add(catchupFailureCooldown)
	c.logger.Warn("replacement acquisition failed; replay anchor retained", "seq", replacement.seq,
		"hash", fmt.Sprintf("%x", replacement.hash[:8]), "anchor_seq", c.standardReplay.anchorSeq,
		"retry_at", replacement.retryAt, "error", cause)
	c.acquisitionMu.Unlock()
	return true
}

func (c *catchupReplayCoordinator) completeStandardReplayReplacement(il *inbound.Ledger, h *header.LedgerHeader, stateMap, txMap *shamap.SHAMap) bool {
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	replacement := c.standardReplay.replacement
	if !c.standardReplay.active || replacement == nil || replacement.acquisition != il ||
		replacement.generation != c.standardReplay.generation {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return false
	}
	if c.stoppedForShutdown() || h == nil || h.LedgerIndex != replacement.seq || h.Hash != replacement.hash ||
		!c.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), true) {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		c.retireAcquisitionStore(c.lifecycleContext(), il)
		return true
	}
	replacement.installing = true
	c.acquisitionMu.Unlock()
	initial, err := c.adaptor.LedgerService().BootstrapLedgerWithState(c.lifecycleContext(), h, stateMap, txMap)
	if err != nil {
		c.replayCommitMu.Unlock()
		c.recordReplayAcquisitionFailure(il, err)
		c.failStandardReplayReplacement(il, err)
		c.retireAcquisitionStore(c.lifecycleContext(), il)
		return true
	}
	c.acquisitionMu.Lock()
	retirement, installed := c.installStandardReplayReplacementLocked(replacement, h, initial)
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	if !installed {
		return true
	}
	return c.finishStandardReplayReplacement(replacement, h, initial, retirement)
}

// The candidate must pass localReplayReplacementCandidate before publication.
func (c *catchupReplayCoordinator) completeLocalStandardReplayReplacement(
	generation uint64,
	replacement *standardReplayReplacement,
	h *header.LedgerHeader,
	stateMap, txMap *shamap.SHAMap,
) bool {
	if replacement == nil || h == nil || stateMap == nil || txMap == nil {
		return false
	}
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	if c.stoppedForShutdown() || !c.standardReplay.active || c.standardReplay.generation != generation ||
		c.standardReplay.replacement != replacement || replacement.generation != generation ||
		replacement.acquisition != nil || replacement.installing || replacement.seq != h.LedgerIndex ||
		replacement.hash != h.Hash || c.standardReplay.anchorSeq >= h.LedgerIndex {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return false
	}
	replacement.installing = true
	c.acquisitionMu.Unlock()
	ctx, cancel := context.WithTimeout(c.lifecycleContext(), standardReplayLocalCandidateCheckTimeout)
	defer cancel()
	ctx = shamap.WithTraversalBudget(ctx, standardReplayLocalCandidateCheckNodes)
	initial, err := c.adaptor.LedgerService().BootstrapLedgerWithState(ctx, h, stateMap, txMap)
	if err != nil {
		c.acquisitionMu.Lock()
		if c.standardReplay.active && c.standardReplay.generation == generation &&
			c.standardReplay.replacement == replacement && replacement.installing {
			replacement.installing = false
		}
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		c.logger.Warn("verified local replacement candidate could not be bootstrapped", "seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]), "error", err)
		return false
	}
	c.acquisitionMu.Lock()
	retirement, installed := c.installStandardReplayReplacementLocked(replacement, h, initial)
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	if !installed {
		return false
	}
	return c.finishStandardReplayReplacement(replacement, h, initial, retirement)
}

// Caller holds replayCommitMu and acquisitionMu after successful Bootstrap.
func (c *catchupReplayCoordinator) installStandardReplayReplacementLocked(
	replacement *standardReplayReplacement,
	h *header.LedgerHeader,
	initial bool,
) (standardReplayRetirement, bool) {
	if replacement == nil || h == nil || !c.standardReplay.active ||
		c.standardReplay.replacement != replacement || replacement.generation != c.standardReplay.generation ||
		replacement.seq != h.LedgerIndex || replacement.hash != h.Hash {
		return standardReplayRetirement{}, false
	}
	var retired []*inbound.Ledger
	if c.standardReplay.anchorSeq < h.LedgerIndex {
		lastSeq, lastHash := h.LedgerIndex, h.Hash
		for {
			entry := c.standardReplay.entries[lastSeq+1]
			if entry == nil || entry.parentHash != lastHash {
				break
			}
			lastSeq, lastHash = entry.seq, entry.hash
		}
		for seq, entry := range c.standardReplay.entries {
			if seq <= h.LedgerIndex || seq > lastSeq {
				if entry.acquisition != nil && c.discardInboundAcquisitionLocked(entry.acquisition) {
					retired = append(retired, entry.acquisition)
				}
				if c.consensusRecovery.stepHash == entry.hash {
					c.consensusRecovery.stepHash = [32]byte{}
				}
				delete(c.standardReplay.entries, seq)
				c.replayPipelineDiscarded.Add(1)
			}
		}
		c.standardReplay.pivotSeq, c.standardReplay.pivotHash = h.LedgerIndex, h.Hash
		c.standardReplay.anchorSeq, c.standardReplay.anchorHash = h.LedgerIndex, h.Hash
		c.standardReplay.collectSeq, c.standardReplay.collectHash = lastSeq, lastHash
		c.standardReplay.initialCandidate = initial
		c.standardReplay.progressSampleAt = time.Now()
		c.standardReplay.sampleAnchorSeq = h.LedgerIndex
		c.standardReplay.stalledSamples = 0
		c.standardReplay.retargetAttemptAt = time.Time{}
		c.standardReplay.headBlockedAt = time.Time{}
		if c.consensusRecovery.targetHash != ([32]byte{}) {
			c.consensusRecovery.anchorSeq, c.consensusRecovery.anchorHash = h.LedgerIndex, h.Hash
			c.consensusRecovery.stepHash = h.Hash
		}
	}
	c.standardReplay.replacement = nil
	delete(c.replayFallbackRequired, h.Hash)
	reachedTarget := c.standardReplay.targetSeq == h.LedgerIndex && c.standardReplay.targetHash == h.Hash
	retirement := standardReplayRetirement{ledgers: retired}
	if reachedTarget {
		retirement.baseLedger, retirement.release = c.standardReplay.baseLedger, c.standardReplay.baseRelease
		c.standardReplay.baseLedger, c.standardReplay.baseRelease = nil, nil
		c.standardReplay.active = false
		c.standardReplay.applying = false
		c.standardReplay.backpressured = false
		c.standardReplay.entries = nil
	}
	return retirement, true
}

func (c *catchupReplayCoordinator) finishStandardReplayReplacement(
	replacement *standardReplayReplacement,
	h *header.LedgerHeader,
	initial bool,
	retirement standardReplayRetirement,
) bool {
	c.retireStandardReplay(retirement)
	c.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, initial)
	c.acquisitionMu.Lock()
	current := c.standardReplay.active && c.standardReplay.generation == replacement.generation
	c.acquisitionMu.Unlock()
	if !current {
		return true
	}
	c.refillStandardReplayCollector(replacement.peerID)
	c.acquisitionMu.Lock()
	c.scheduleReadyStandardReplayDrainLocked()
	c.acquisitionMu.Unlock()
	return true
}
