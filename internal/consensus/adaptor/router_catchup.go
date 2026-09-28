package adaptor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/shamap"
)

func (r *Router) handleStatusChange(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeStatusChange, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode status_change", "error", err, "peer", msg.PeerID)
		return
	}
	sc, ok := decoded.(*message.StatusChange)
	if !ok {
		return
	}

	r.catchupReplay.handleStatusChange(sc, msg.PeerID)
}

func (c *catchupReplayCoordinator) handleStatusChange(sc *message.StatusChange, peerID peermanagement.PeerID) {
	if c.stoppedForShutdown() {
		return
	}
	svc := c.adaptor.LedgerService()
	fastLoadProvisional := svc != nil && svc.IsFastLoadProvisional()
	c.logger.Info("peer status change",
		"peer", peerID,
		"status", sc.NewStatus,
		"event", sc.NewEvent,
		"ledger_seq", sc.LedgerSeq,
		"needs_sync", c.adaptor.NeedsInitialSync(),
		"fast_load_provisional", fastLoadProvisional,
	)

	if sc.NewEvent == message.NodeEventLostSync {
		c.peersMu.Lock()
		delete(c.peerStates, peerID)
		delete(c.peerStatusCandidates, peerID)
		c.peersMu.Unlock()
		c.invalidateCatchupPeer(uint64(peerID))
		c.invalidateHistoryPeer(uint64(peerID))
		c.adaptor.UpdatePeerLCL(uint64(peerID), consensus.LedgerID{})
		return
	}

	if sc.LedgerSeq > 0 {
		var peerHash [32]byte
		if len(sc.LedgerHash) == 32 {
			copy(peerHash[:], sc.LedgerHash)
		}
		if peerHash == ([32]byte{}) {
			c.peersMu.Lock()
			delete(c.peerStates, peerID)
			delete(c.peerStatusCandidates, peerID)
			c.peersMu.Unlock()
			c.invalidateCatchupPeer(uint64(peerID))
			c.adaptor.UpdatePeerLCL(uint64(peerID), consensus.LedgerID{})
			return
		}
		var parentHash [32]byte
		haveParent := len(sc.LedgerHashPrevious) == 32
		if haveParent {
			copy(parentHash[:], sc.LedgerHashPrevious)
		}

		admitted := c.admitPeerStatus(peerStatusCandidate{
			peerLedgerState: peerLedgerState{
				LedgerSeq:  sc.LedgerSeq,
				LedgerHash: peerHash,
				parentHash: parentHash,
				haveParent: haveParent,
			},
			peerID: peerID,
		})
		if len(admitted) == 0 {
			return
		}
		for _, status := range admitted {
			c.adaptor.UpdatePeerLCL(uint64(status.peerID), consensus.LedgerID(status.LedgerHash))
		}
		for _, status := range admitted {
			c.reconcilePeerSeqHash(status.LedgerSeq)
		}

		// During network startup or fast-load confirmation, acquire the
		// peer-preferred ledger. Don't adopt with synthetic headers — wait for
		// real state data.
		if c.needsStartupLedgerConfirmation() && sc.LedgerSeq > 1 {
			if c.peerLedgerIsPreferred(peerHash) ||
				c.isValidationCatchupTarget(sc.LedgerSeq, peerHash) {
				c.ensureCatchupAcquisition(sc.LedgerSeq, peerHash, uint64(peerID))
			}
			return
		}

		// A node materially behind the peer-preferred network tip must stop
		// advertising Full before it starts catch-up. Remaining Full would let a
		// validator keep proposing and issuing full validations on its stale LCL.
		if c.adaptor.GetOperatingMode() == consensus.OpModeFull && sc.LedgerSeq > 1 {
			svc := c.adaptor.LedgerService()
			if svc != nil {
				ourSeq := svc.GetClosedLedgerIndex()
				if aheadByMoreThan(sc.LedgerSeq, ourSeq, 2) {
					if !c.peerLedgerIsPreferred(peerHash) {
						return
					}
					leftFull := false
					if lcl, err := c.adaptor.GetLastClosedLedger(); err == nil && lcl != nil &&
						c.adaptor.networkLedgerDiffers(lcl, consensus.OpModeFull) {
						c.adaptor.SetOperatingMode(consensus.OpModeConnected)
						leftFull = true
					}
					c.logger.Warn("behind network; catching up",
						"our_seq", ourSeq,
						"peer_seq", sc.LedgerSeq,
						"gap", sc.LedgerSeq-ourSeq,
						"left_full", leftFull,
					)
					c.notePeerStatusEvidence(sc.LedgerSeq, peerHash)
					c.ensureCatchupAcquisition(sc.LedgerSeq, peerHash, uint64(peerID))
					return
				}
			}
		}

		// While not in Full mode, keep catching up until we're within 1 ledger
		// of the network.
		if c.adaptor.GetOperatingMode() != consensus.OpModeFull && sc.LedgerSeq > 1 {
			svc := c.adaptor.LedgerService()
			if svc != nil {
				ourSeq := svc.GetClosedLedgerIndex()
				if aheadByMoreThan(sc.LedgerSeq, ourSeq, 1) {
					if c.peerLedgerIsPreferred(peerHash) {
						c.notePeerStatusEvidence(sc.LedgerSeq, peerHash)
						c.ensureCatchupAcquisition(sc.LedgerSeq, peerHash, uint64(peerID))
					}
					return
				}
			}
		}

		c.checkBehind(sc.LedgerSeq, peerHash, uint64(peerID))
	}
}

func (c *catchupReplayCoordinator) admitPeerStatus(status peerStatusCandidate) []peerStatusCandidate {
	if c.isValidationCatchupTarget(status.LedgerSeq, status.LedgerHash) ||
		c.peerStatusWithinAnchor(status.LedgerSeq) {
		c.peersMu.Lock()
		delete(c.peerStatusCandidates, status.peerID)
		c.peerStates[status.peerID] = &peerLedgerState{
			LedgerSeq:  status.LedgerSeq,
			LedgerHash: status.LedgerHash,
			parentHash: status.parentHash,
			haveParent: status.haveParent,
		}
		c.peersMu.Unlock()
		return []peerStatusCandidate{status}
	}

	c.peersMu.Lock()
	c.peerStatusCandidates[status.peerID] = status
	matching := make([]peerStatusCandidate, 0, 2)
	for _, candidate := range c.peerStatusCandidates {
		if candidate.LedgerSeq == status.LedgerSeq && candidate.LedgerHash == status.LedgerHash {
			matching = append(matching, candidate)
		}
	}
	if len(matching) < 2 {
		c.peersMu.Unlock()
		return nil
	}
	for _, candidate := range matching {
		delete(c.peerStatusCandidates, candidate.peerID)
		c.peerStates[candidate.peerID] = &peerLedgerState{
			LedgerSeq:  candidate.LedgerSeq,
			LedgerHash: candidate.LedgerHash,
			parentHash: candidate.parentHash,
			haveParent: candidate.haveParent,
		}
	}
	c.peersMu.Unlock()
	return matching
}

func (c *catchupReplayCoordinator) peerLedgerIsPreferred(hash [32]byte) bool {
	closed, err := c.adaptor.GetLastClosedLedger()
	if err != nil || closed == nil {
		return false
	}
	preferred := c.adaptor.preferredLCL(closed, c.adaptor.GetOperatingMode())
	return preferred != closed.ID() && preferred == consensus.LedgerID(hash)
}

func (c *catchupReplayCoordinator) needsStartupLedgerConfirmation() bool {
	svc := c.adaptor.LedgerService()
	return svc != nil && (svc.NeedsInitialSync() || svc.IsFastLoadProvisional())
}

// maxConcurrentCatchup bounds the hash-keyed current-ledger acquisition set.
// Separate target hashes share the NodeStore and remain independently useful
// when the preferred ledger moves during a long state walk.
const maxConcurrentCatchup = 3

// Gossip-driven acquisition leaves one slot available for the exact ledger
// requested by consensus wrong-ledger recovery.
const maxConcurrentSpeculativeCatchup = maxConcurrentCatchup - 1

// A provisional fast-loaded ledger has a cold in-memory full-below cache. A
// second full-state acquisition would duplicate the same durable SHAMap scan
// and compete for storage bandwidth. Keep one pinned full-state jump in flight;
// newer trusted targets remain queued and are armed when it completes.
const maxConcurrentProvisionalFullStateCatchup = 1

// maxForwardDeltaGap bounds the initial recovery strategy and peer ancestry
// hints. A healthy frozen-pivot replay is governed by measured progress instead
// of its absolute distance from the moving tip.
const maxForwardDeltaGap = 128

const catchupLinkageGracePeriod = 2 * time.Second

// seqHashRetain bounds the ancestry retained behind the local or trusted
// frontier. Peer gossip is admitted only through maxForwardDeltaGap ahead.
const seqHashRetain = 2048

func (c *catchupReplayCoordinator) recordSeqHash(seq uint32, hash, parentHash [32]byte, haveParent bool) {
	c.recordSeqHashFrom(seq, hash, parentHash, haveParent, seqHashSourceQuorum)
}

func (c *catchupReplayCoordinator) recordValidationSeqHash(seq uint32, hash [32]byte) {
	c.recordSeqHashFrom(seq, hash, [32]byte{}, false, seqHashSourceValidation)
}

func (c *catchupReplayCoordinator) recordAcquiredSeqHash(seq uint32, hash, parentHash [32]byte) {
	c.recordSeqHashFrom(seq, hash, parentHash, true, seqHashSourceAcquired)
}

func (c *catchupReplayCoordinator) recordPeerSeqHash(seq uint32, hash, parentHash [32]byte, haveParent bool) {
	c.recordSeqHashFrom(seq, hash, parentHash, haveParent, seqHashSourcePeer)
}

func (c *catchupReplayCoordinator) reconcilePeerSeqHash(seq uint32) {
	type peerEvidence struct {
		peerID     uint64
		hash       [32]byte
		parentHash [32]byte
		haveParent bool
	}
	c.peersMu.RLock()
	peers := make([]peerEvidence, 0, len(c.peerStates))
	hashSupport := make(map[[32]byte]int)
	for peerID, state := range c.peerStates {
		if state == nil || state.LedgerSeq != seq {
			continue
		}
		peers = append(peers, peerEvidence{
			peerID:     uint64(peerID),
			hash:       state.LedgerHash,
			parentHash: state.parentHash,
			haveParent: state.haveParent,
		})
		hashSupport[state.LedgerHash]++
	}
	c.peersMu.RUnlock()
	if len(peers) == 0 {
		return
	}

	entry, known := c.lookupSeqHash(seq)
	chosenHash := entry.hash
	chosenPreferred := false
	if !known || entry.source < seqHashSourceValidation {
		chosenHash = [32]byte{}
		for _, peer := range peers {
			if c.peerLedgerIsPreferred(peer.hash) {
				chosenHash = peer.hash
				chosenPreferred = true
				break
			}
		}
		if chosenHash == ([32]byte{}) {
			best := 0
			for hash, count := range hashSupport {
				if count > best || (count == best && bytes.Compare(hash[:], chosenHash[:]) > 0) {
					chosenHash, best = hash, count
				}
			}
		}
	}
	if chosenHash == ([32]byte{}) {
		return
	}

	parents := make(map[[32]byte]int)
	var peerID uint64
	for _, peer := range peers {
		if peer.hash != chosenHash {
			continue
		}
		if peerID == 0 || peer.peerID < peerID {
			peerID = peer.peerID
		}
		if peer.haveParent {
			parents[peer.parentHash]++
		}
	}
	if peerID == 0 {
		return
	}
	parentHash, haveParent := c.preferredPeerParent(seq, parents)
	c.recordPeerSeqHash(seq, chosenHash, parentHash, haveParent)
	if !haveParent {
		c.seqHashMu.Lock()
		entry = c.seqHash[seq]
		if entry.hash == chosenHash && entry.parentFrom == seqHashSourcePeer {
			entry.parentHash = [32]byte{}
			entry.haveParent = false
			entry.parentFrom = seqHashSourcePeer
			c.seqHash[seq] = entry
		}
		if len(parents) > 1 && seq > 1 {
			parent := c.seqHash[seq-1]
			if parent.source == seqHashSourcePeer {
				delete(c.seqHash, seq-1)
			}
		}
		c.seqHashMu.Unlock()
	}

	c.catchupMu.Lock()
	if chosenPreferred && c.catchup.source == catchupSourcePeer && c.catchup.seq == seq {
		c.catchup.hash = chosenHash
		c.catchup.peerID = peerID
	}
	c.catchupMu.Unlock()
}

func (c *catchupReplayCoordinator) preferredPeerParent(seq uint32, support map[[32]byte]int) ([32]byte, bool) {
	if seq > 1 && c.adaptor != nil {
		if svc := c.adaptor.LedgerService(); svc != nil {
			if parent, err := svc.GetLedgerBySequence(seq - 1); err == nil && parent != nil {
				if support[parent.Hash()] > 0 {
					return parent.Hash(), true
				}
			}
		}
	}
	if len(support) == 1 {
		for parent := range support {
			return parent, true
		}
	}

	var chosen [32]byte
	best, second := 0, 0
	for parent, count := range support {
		if count > best || (count == best && bytes.Compare(parent[:], chosen[:]) > 0) {
			second = best
			best = count
			chosen = parent
		} else if count > second {
			second = count
		}
	}
	return chosen, best >= 2 && best > second
}

func (c *catchupReplayCoordinator) recordSeqHashFrom(
	seq uint32,
	hash, parentHash [32]byte,
	haveParent bool,
	source seqHashSource,
) {
	if seq == 0 || hash == ([32]byte{}) {
		return
	}
	localAnchor := c.localSeqHashAnchor()

	c.seqHashMu.Lock()
	defer c.seqHashMu.Unlock()

	if localAnchor > c.seqHashAnchor {
		c.seqHashAnchor = localAnchor
	}
	if source >= seqHashSourceValidation && seq > c.seqHashAnchor {
		c.seqHashAnchor = seq
	}
	c.pruneSeqHashLocked()
	if source < seqHashSourceValidation && !seqHashWithinAnchor(seq, c.seqHashAnchor) {
		return
	}

	e := c.seqHash[seq]
	if e.hash != ([32]byte{}) && e.hash != hash {
		if source < e.source ||
			(source == e.source && source != seqHashSourcePeer && source != seqHashSourceQuorum) {
			return
		}
		e = ledgerHashEntry{}
	}
	e.hash = hash
	if source > e.source {
		e.source = source
	}
	if haveParent && (!e.haveParent || source > e.parentFrom ||
		(source == seqHashSourcePeer && e.parentFrom == seqHashSourcePeer)) {
		e.parentHash = parentHash
		e.haveParent = true
		e.parentFrom = source
	}
	c.seqHash[seq] = e

	// The parent linkage also names seq-1's own hash; seed it (parentless) so a
	// same-branch check against our closed seq succeeds without a direct
	// validation for it.
	if haveParent && seq > 1 {
		pe := c.seqHash[seq-1]
		if pe.hash == ([32]byte{}) || pe.hash == parentHash || source > pe.source ||
			(source == seqHashSourcePeer && pe.source == seqHashSourcePeer) {
			if pe.hash != ([32]byte{}) && pe.hash != parentHash {
				pe = ledgerHashEntry{}
			}
			pe.hash = parentHash
			if source > pe.source {
				pe.source = source
			}
			c.seqHash[seq-1] = pe
		}
	}

	c.pruneSeqHashLocked()
}

// Caller holds seqHashMu.
func (c *catchupReplayCoordinator) recordAcquiredSeqHashChainLocked(
	headers []header.LedgerHeader,
	localAnchor uint32,
) bool {
	if len(headers) == 0 {
		return true
	}
	anchor := c.seqHashAnchor
	if localAnchor > anchor {
		anchor = localAnchor
	}
	for _, h := range headers {
		if !seqHashWithinAnchor(h.LedgerIndex, anchor) {
			return false
		}
		e := c.seqHash[h.LedgerIndex]
		if e.hash != ([32]byte{}) && e.hash != h.Hash && e.source >= seqHashSourceValidation {
			return false
		}
		if e.haveParent && e.parentHash != h.ParentHash && e.parentFrom >= seqHashSourceValidation {
			return false
		}
	}
	baseSeq := headers[0].LedgerIndex - 1
	baseHash := headers[0].ParentHash
	base := c.seqHash[baseSeq]
	if base.hash != ([32]byte{}) && base.hash != baseHash && base.source >= seqHashSourceValidation {
		return false
	}
	if base.hash != baseHash {
		base = ledgerHashEntry{hash: baseHash}
	}
	base.source = max(base.source, seqHashSourceAcquired)
	c.seqHash[baseSeq] = base
	for _, h := range headers {
		e := c.seqHash[h.LedgerIndex]
		if e.hash != h.Hash {
			e = ledgerHashEntry{hash: h.Hash}
		}
		e.source = max(e.source, seqHashSourceAcquired)
		e.parentHash = h.ParentHash
		e.haveParent = true
		e.parentFrom = max(e.parentFrom, seqHashSourceAcquired)
		c.seqHash[h.LedgerIndex] = e
	}
	c.seqHashAnchor = anchor
	c.pruneSeqHashLocked()
	return true
}

func (c *catchupReplayCoordinator) localSeqHashAnchor() uint32 {
	if c.adaptor == nil {
		return 0
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return 0
	}
	anchor := svc.GetClosedLedgerIndex()
	if validated := svc.GetValidatedLedgerIndex(); validated > anchor {
		anchor = validated
	}
	return anchor
}

func (c *catchupReplayCoordinator) peerStatusWithinAnchor(seq uint32) bool {
	localAnchor := c.localSeqHashAnchor()
	c.seqHashMu.Lock()
	defer c.seqHashMu.Unlock()
	if localAnchor > c.seqHashAnchor {
		c.seqHashAnchor = localAnchor
	}
	c.pruneSeqHashLocked()
	anchor := c.seqHashAnchor
	if anchor == 0 {
		return false
	}
	if seq >= anchor {
		return seq-anchor <= maxForwardDeltaGap
	}
	return anchor-seq <= maxForwardDeltaGap
}

func seqHashWithinAnchor(seq, anchor uint32) bool {
	if anchor == 0 {
		return false
	}
	floor := uint32(0)
	if anchor > seqHashRetain {
		floor = anchor - seqHashRetain
	}
	ceiling := anchor + maxForwardDeltaGap
	if ceiling < anchor {
		ceiling = ^uint32(0)
	}
	return seq >= floor && seq <= ceiling
}

func (c *catchupReplayCoordinator) pruneSeqHashLocked() {
	for s := range c.seqHash {
		if !seqHashWithinAnchor(s, c.seqHashAnchor) {
			delete(c.seqHash, s)
		}
	}
}

// lookupSeqHash returns the recorded network view for a ledger sequence.
func (c *catchupReplayCoordinator) lookupSeqHash(seq uint32) (ledgerHashEntry, bool) {
	c.seqHashMu.Lock()
	defer c.seqHashMu.Unlock()
	e, ok := c.seqHash[seq]
	return e, ok
}

func (c *catchupReplayCoordinator) lookupSeqForHash(hash [32]byte) (uint32, bool) {
	c.seqHashMu.Lock()
	defer c.seqHashMu.Unlock()
	for seq, entry := range c.seqHash {
		if entry.hash == hash {
			return seq, true
		}
	}
	return 0, false
}

// recoveryAnchorReachesTarget proves that anchor is on the recorded parent
// chain of target. Missing linkage is treated as unknown rather than ancestry.
func (c *catchupReplayCoordinator) recoveryAnchorReachesTarget(anchorSeq uint32, anchorHash, targetHash [32]byte) bool {
	targetSeq, ok := c.lookupSeqForHash(targetHash)
	if !ok || anchorSeq > targetSeq {
		return false
	}
	current := targetHash
	for seq := targetSeq; seq > anchorSeq; seq-- {
		entry, found := c.lookupSeqHash(seq)
		if !found || entry.hash != current || !entry.haveParent {
			return false
		}
		current = entry.parentHash
	}
	return current == anchorHash
}

// catchupInFlight counts active consensus-reason acquisitions across both the
// legacy fetchTracker and the replay-delta replayer. Generic (RPC-driven)
// acquisitions are excluded so an arbitrary fetch never consumes a catch-up slot.
func (c *catchupReplayCoordinator) catchupInFlight() int {
	return c.fetchTracker.CountReason(inbound.ReasonConsensus) + c.replayer.Count()
}

func (c *catchupReplayCoordinator) protectedCatchupInFlight() int {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	return c.protectedCatchupInFlightLocked()
}

// Caller holds acquisitionMu.
func (c *catchupReplayCoordinator) protectedCatchupInFlightLocked() int {
	active := c.replayer.Count()
	for _, candidate := range c.fetchTracker.Active() {
		if candidate.Reason() == inbound.ReasonConsensus && !candidate.TransactionOnly() {
			active++
		}
	}
	if c.standardReplay.pivotHandoff != nil &&
		c.fetchTracker.Find(c.standardReplay.pivotHandoff.acquisition.Hash()) != c.standardReplay.pivotHandoff.acquisition {
		active++
	}
	return active
}

// recordCatchupTarget raises the single consensus catch-up target or refreshes
// its preferred peer when another peer advertises the same ledger.
func (c *catchupReplayCoordinator) recordCatchupTarget(seq uint32, hash [32]byte, peerID uint64) {
	c.catchupMu.Lock()
	defer c.catchupMu.Unlock()
	if c.catchup.source != catchupSourcePeer {
		if c.catchup.hash == hash {
			c.catchup.peerID = peerID
		}
		return
	}
	if seq > c.catchup.seq {
		c.catchup = catchupTarget{seq: seq, hash: hash, peerID: peerID}
	} else if seq == c.catchup.seq {
		c.catchup = catchupTarget{seq: seq, hash: hash, peerID: peerID}
	}
}

func (c *catchupReplayCoordinator) recordValidationCatchupTarget(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	source catchupTargetSource,
) {
	c.catchupMu.Lock()
	previous := c.catchup
	// Trusted evidence replaces a peer-derived target at any sequence. Once
	// validation-driven, only a higher sequence or same-sequence quorum can
	// move the frontier.
	if c.catchup.source == catchupSourcePeer ||
		(source == catchupSourceQuorum && seq == c.catchup.seq) ||
		seq > c.catchup.seq {
		if peerID == 0 && previous.seq == seq && previous.hash == hash {
			peerID = previous.peerID
		}
		c.catchup = catchupTarget{seq: seq, hash: hash, peerID: peerID, source: source}
	} else if seq == c.catchup.seq && hash == c.catchup.hash {
		if peerID != 0 {
			c.catchup.peerID = peerID
		}
	}
	if previous.hash != ([32]byte{}) && previous.hash != c.catchup.hash &&
		previous.source != catchupSourcePeer && c.catchup.source != catchupSourcePeer {
		c.targetSuperseded.Add(1)
	}
	if source != catchupSourcePeer &&
		(previous.seq != c.catchup.seq || previous.hash != c.catchup.hash) {
		// Keep a status advertisement attached to the same target when quorum
		// evidence arrives. That evidence is what makes a header walk
		// actionable; a different trusted target must establish a fresh grace
		// window instead of inheriting an old peer hint.
		c.peerStatusEvidence = false
	}
	target := c.catchup
	c.catchupMu.Unlock()

	if target.source != catchupSourcePeer {
		c.promotePeerStatusCandidates(target.seq, target.hash)
	}
}

func (c *catchupReplayCoordinator) promotePeerStatusCandidates(seq uint32, hash [32]byte) {
	c.peersMu.Lock()
	matching := make([]peerStatusCandidate, 0, 1)
	for peerID, candidate := range c.peerStatusCandidates {
		if candidate.LedgerSeq != seq || candidate.LedgerHash != hash {
			continue
		}
		matching = append(matching, candidate)
		delete(c.peerStatusCandidates, peerID)
		c.peerStates[peerID] = &peerLedgerState{
			LedgerSeq:  seq,
			LedgerHash: hash,
			parentHash: candidate.parentHash,
			haveParent: candidate.haveParent,
		}
	}
	c.peersMu.Unlock()

	for _, candidate := range matching {
		c.adaptor.UpdatePeerLCL(uint64(candidate.peerID), consensus.LedgerID(hash))
	}
	if len(matching) > 0 {
		c.reconcilePeerSeqHash(seq)
		c.recordCatchupTarget(seq, hash, uint64(matching[0].peerID))
	}
}

func (c *catchupReplayCoordinator) isValidationCatchupTarget(seq uint32, hash [32]byte) bool {
	c.catchupMu.Lock()
	defer c.catchupMu.Unlock()
	return c.catchup.source != catchupSourcePeer &&
		c.catchup.seq == seq &&
		c.catchup.hash == hash
}

func (c *catchupReplayCoordinator) invalidateCatchupPeer(peerID uint64) {
	type peerTip struct {
		peerID uint64
		seq    uint32
		hash   [32]byte
	}
	c.peersMu.RLock()
	peers := make([]peerTip, 0, len(c.peerStates))
	for id, state := range c.peerStates {
		peers = append(peers, peerTip{peerID: uint64(id), seq: state.LedgerSeq, hash: state.LedgerHash})
	}
	c.peersMu.RUnlock()

	c.catchupMu.Lock()
	defer c.catchupMu.Unlock()
	if c.catchup.peerID != peerID {
		return
	}
	if c.catchup.source != catchupSourcePeer {
		c.catchup.peerID = 0
		return
	}
	for _, peer := range peers {
		if peer.seq == c.catchup.seq && peer.hash == c.catchup.hash {
			c.catchup.peerID = peer.peerID
			return
		}
	}
	c.catchup = catchupTarget{}
}

const catchupFailureCooldown = 5 * time.Minute

func (c *catchupReplayCoordinator) markFailedCatchupAcquisition(hash [32]byte) {
	c.catchupMu.Lock()
	defer c.catchupMu.Unlock()
	if c.catchupFailures == nil {
		c.catchupFailures = make(map[[32]byte]time.Time)
	}
	now := time.Now()
	for failedHash, retryAfter := range c.catchupFailures {
		if !now.Before(retryAfter) {
			delete(c.catchupFailures, failedHash)
		}
	}
	c.catchupFailures[hash] = now.Add(catchupFailureCooldown)
}

func (c *catchupReplayCoordinator) catchupRetryBlocked(hash [32]byte, now time.Time) bool {
	c.catchupMu.Lock()
	defer c.catchupMu.Unlock()
	retryAfter, ok := c.catchupFailures[hash]
	if ok && !now.Before(retryAfter) {
		delete(c.catchupFailures, hash)
		return false
	}
	return ok
}

// bestCatchupTarget returns the current highest recorded catch-up target.
func (c *catchupReplayCoordinator) bestCatchupTarget() (seq uint32, hash [32]byte, peerID uint64) {
	c.catchupMu.Lock()
	defer c.catchupMu.Unlock()
	return c.catchup.seq, c.catchup.hash, c.catchup.peerID
}

func (c *catchupReplayCoordinator) armCatchupTowardTarget() {
	c.armCatchupTowardTargetWithPeer(0)
}

func (c *catchupReplayCoordinator) armConsensusCatchup() {
	if c.replayFaultBlocked() {
		return
	}
	c.retireLocallySatisfiedFrozenPivot("local_frontier")
	if c.armPendingConsensusLedger() {
		return
	}
	c.armCatchupTowardTarget()
}

func (c *catchupReplayCoordinator) armPendingConsensusLedger() bool {
	for {
		c.acquisitionMu.Lock()
		recovery := c.consensusRecovery
		if recovery.stepHash != ([32]byte{}) && c.isAcquiringLocked(recovery.stepHash) {
			c.acquisitionMu.Unlock()
			return true
		}
		if recovery.targetHash != ([32]byte{}) && c.isAcquiringLocked(recovery.targetHash) {
			c.consensusRecovery.stepHash = recovery.targetHash
			c.acquisitionMu.Unlock()
			return true
		}
		if recovery.stepHash != recovery.targetHash {
			c.consensusRecovery.stepHash = [32]byte{}
			recovery.stepHash = [32]byte{}
		}
		c.acquisitionMu.Unlock()

		hash := recovery.targetHash
		if hash == ([32]byte{}) {
			return false
		}
		svc := c.adaptor.LedgerService()
		if svc != nil {
			if held, err := svc.GetLedgerByHash(hash); err == nil && held != nil {
				if svc.NeedsInitialSync() || svc.IsFastLoadProvisional() {
					accepted, rearm := c.tryInitialLedgerSwitch(held.Sequence(), held.Hash())
					if accepted || !rearm {
						return true
					}
					return false
				}
				accepted, rearm := c.tryConsensusLedgerSwitch(held.Sequence(), held.Hash())
				if accepted || !rearm {
					return true
				}
				return false
			}
		}
		seq, known := c.lookupSeqForHash(hash)
		var nextSeq uint32
		var nextHash [32]byte
		var parent *ledger.Ledger
		var replay bool
		var discardAnchor bool
		if known {
			nextSeq, nextHash, parent, replay, discardAnchor = c.recoveryForwardStep(svc, seq, hash, recovery)
		}
		if discardAnchor {
			c.acquisitionMu.Lock()
			if c.consensusRecovery.targetHash == hash &&
				c.consensusRecovery.anchorHash == recovery.anchorHash &&
				c.consensusRecovery.anchorSeq == recovery.anchorSeq {
				c.consensusRecovery.anchorHash = [32]byte{}
				c.consensusRecovery.anchorSeq = 0
				recovery.anchorHash = [32]byte{}
				recovery.anchorSeq = 0
			}
			c.acquisitionMu.Unlock()
		}
		if replay && parent != nil {
			if _, replayPeerFound := c.resolveReplayPeer(parent.Sequence()+1, 0); !replayPeerFound &&
				c.tryArmStandardReplayPipeline(svc, parent, seq, hash, 0) {
				return true
			}
		}
		if svc != nil {
			target := c.credibleCatchupFrontier()
			if target.seq == seq && target.hash == hash &&
				c.maybeStartHeaderParentDiscovery(target, target.peerID) {
				return true
			}
		}

		c.acquisitionMu.Lock()
		if c.consensusRecovery.targetHash != hash {
			c.acquisitionMu.Unlock()
			continue
		}
		if c.consensusRecovery.stepHash != ([32]byte{}) && c.isAcquiringLocked(c.consensusRecovery.stepHash) {
			c.acquisitionMu.Unlock()
			return true
		}
		if c.isAcquiringLocked(hash) {
			c.consensusRecovery.stepHash = hash
			c.acquisitionMu.Unlock()
			return true
		}
		acquisitionSeq := seq
		acquisitionHash := hash
		if replay {
			acquisitionSeq = nextSeq
			acquisitionHash = nextHash
		}
		if c.isAcquiringLocked(acquisitionHash) {
			c.consensusRecovery.stepHash = acquisitionHash
			c.acquisitionMu.Unlock()
			return true
		}
		if !c.canAdmitCatchupLocked(acquisitionHash, maxConcurrentCatchup) {
			c.acquisitionMu.Unlock()
			return true
		}
		if replay {
			peer, found := c.resolveReplayPeer(nextSeq, 0)
			if found && c.startReplayDeltaAcquisition(nextSeq, nextHash, peer, parent) == nil {
				c.consensusRecovery.stepHash = nextHash
				c.acquisitionMu.Unlock()
				return true
			}
		}

		peerID, _ := c.selectAcquisitionPeer(acquisitionSeq)
		if replay && parent != nil {
			c.startLedgerReplayAcquisitionLegacyLocked(acquisitionSeq, acquisitionHash, peerID)
		} else {
			c.startLedgerAcquisitionLegacyLocked(acquisitionSeq, acquisitionHash, peerID)
		}
		started := c.fetchTracker.Find(acquisitionHash) != nil
		if started {
			c.consensusRecovery.stepHash = acquisitionHash
		}
		c.acquisitionMu.Unlock()
		return started
	}
}

func (c *catchupReplayCoordinator) resolveReplayPeer(seq uint32, preferred uint64) (uint64, bool) {
	if peer, ok := c.resolveAcquisitionPeer(seq, preferred); ok && c.acquisition.PeerSupportsReplay(peer) {
		return peer, true
	}
	peers := c.acquisition.ReplayCapablePeersExcluding(nil, 1)
	if len(peers) == 0 {
		return 0, false
	}
	return peers[0], true
}

// catchupReplayBase keeps recovery on the verified branch even when an observer
// has closed newer, noncanonical ledgers. A speculative close is not replay
// progress. Prefer it only when the recorded network chain agrees with it.
func (c *catchupReplayCoordinator) catchupReplayBase(svc *service.Service) *ledger.Ledger {
	closed := svc.GetClosedLedger()
	validated := svc.GetValidatedLedger()
	if validated == nil || closed == nil {
		if validated != nil {
			return validated
		}
		return closed
	}
	if validated.Sequence() >= closed.Sequence() {
		return validated
	}
	if entry, ok := c.lookupSeqHash(closed.Sequence()); ok && entry.hash == closed.Hash() {
		return closed
	}
	return validated
}

func (c *catchupReplayCoordinator) recoveryForwardStep(
	svc *service.Service,
	targetSeq uint32,
	targetHash [32]byte,
	recovery consensusRecovery,
) (uint32, [32]byte, *ledger.Ledger, bool, bool) {
	if svc == nil {
		return 0, [32]byte{}, nil, false, recovery.anchorHash != ([32]byte{})
	}
	parent := c.catchupReplayBase(svc)
	if parent == nil || targetSeq <= parent.Sequence() {
		return 0, [32]byte{}, nil, false, recovery.anchorHash != ([32]byte{})
	}

	maxGap := uint32(maxForwardDeltaGap)
	discardAnchor := false
	if recovery.anchorHash != ([32]byte{}) {
		anchor, err := svc.GetLedgerByHash(recovery.anchorHash)
		anchorUsable := recovery.anchorSeq > parent.Sequence() && recovery.anchorSeq < targetSeq &&
			targetSeq-recovery.anchorSeq <= seqHashRetain &&
			c.recoveryAnchorReachesTarget(recovery.anchorSeq, recovery.anchorHash, targetHash) &&
			err == nil && anchor != nil && anchor.Sequence() == recovery.anchorSeq &&
			anchor.Hash() == recovery.anchorHash
		if anchorUsable {
			parent = anchor
			maxGap = seqHashRetain
		} else {
			discardAnchor = true
		}
	}
	if targetSeq-parent.Sequence() > maxGap {
		return 0, [32]byte{}, nil, false, discardAnchor
	}

	for parent.Sequence() < targetSeq {
		nextSeq := parent.Sequence() + 1
		entry, known := c.lookupSeqHash(nextSeq)
		if !known || entry.hash == ([32]byte{}) {
			return 0, [32]byte{}, nil, false, discardAnchor
		}
		if nextSeq == targetSeq && entry.hash != targetHash {
			return 0, [32]byte{}, nil, false, discardAnchor
		}
		parentHash := parent.Hash()
		if entry.haveParent {
			if entry.parentHash != parentHash {
				return 0, [32]byte{}, nil, false, discardAnchor
			}
		} else if current, ok := c.lookupSeqHash(parent.Sequence()); (!ok || current.hash != parentHash) && !svc.IsFastLoadProvisional() {
			return 0, [32]byte{}, nil, false, discardAnchor
		}

		next, err := svc.GetLedgerByHash(entry.hash)
		if err != nil || next == nil {
			return nextSeq, entry.hash, parent, true, discardAnchor
		}
		if next.Sequence() != nextSeq || next.ParentHash() != parentHash {
			return 0, [32]byte{}, nil, false, discardAnchor
		}
		parent = next
	}
	return 0, [32]byte{}, nil, false, discardAnchor
}

func (c *catchupReplayCoordinator) armCatchupTowardTargetWithPeer(peerHint uint64) {
	if c.adaptor == nil {
		return
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return
	}
	target := c.credibleCatchupFrontier()
	if peerHint == 0 && target.source == catchupSourceQuorum {
		peerHint = target.peerID
	}
	tSeq, tHash := target.seq, target.hash
	if tSeq == 0 {
		return
	}
	base := c.catchupReplayBase(svc)
	if base == nil {
		return
	}
	if c.maybeStartHeaderParentDiscovery(target, peerHint) {
		return
	}
	if c.continueFrozenPivotRecovery(tSeq, tHash, peerHint) {
		return
	}
	c.reconcileStandardReplayTarget(tSeq, tHash)
	actualClosed := svc.GetClosedLedgerIndex()
	closed := base.Sequence()
	closedLedger := base
	if target.source == catchupSourcePeer || svc.IsFastLoadProvisional() {
		// Peer status is a liveness hint. Keep its established forward-delta
		// policy rooted at the observed closed ledger; only trusted targets use
		// the validated anchor below when speculative closes have run ahead.
		closedLedger = svc.GetClosedLedger()
		if closedLedger == nil {
			return
		}
		closed = actualClosed
	}
	if tSeq <= closed {
		if target.source != catchupSourceQuorum {
			return
		}
		if held, err := svc.GetLedgerByHash(tHash); err == nil && held != nil {
			return
		}
		if c.protectedCatchupInFlight() >= maxConcurrentSpeculativeCatchup {
			return
		}
		peer, found := c.resolveAcquisitionPeer(tSeq, peerHint)
		if !found || c.isBuildingLedger(tSeq) {
			return
		}
		c.startLedgerAcquisition(tSeq, tHash, peer)
		return
	}
	if target.source == catchupSourcePeer &&
		!svc.NeedsInitialSync() && !svc.IsFastLoadProvisional() &&
		!aheadByMoreThan(tSeq, actualClosed, 1) {
		return
	}
	if closedLedger != nil && tSeq-closed <= maxForwardDeltaGap &&
		c.recoveryAnchorReachesTarget(closedLedger.Sequence(), closedLedger.Hash(), tHash) {
		if _, replayPeerFound := c.resolveReplayPeer(closedLedger.Sequence()+1, peerHint); !replayPeerFound &&
			c.tryArmStandardReplayPipeline(svc, closedLedger, tSeq, tHash, peerHint) {
			return
		}
	}
	if c.protectedCatchupInFlight() >= maxConcurrentSpeculativeCatchup {
		return
	}

	if target.source == catchupSourcePeer || svc.IsFastLoadProvisional() {
		if seq, hash, ok := c.forwardDeltaStep(svc, actualClosed, tSeq); ok &&
			!c.locallySatisfiesLedger(seq, hash) {
			c.clearPeerStatusEvidence(actualClosed, tSeq, tHash)
			peer, found := c.resolveAcquisitionPeer(seq, peerHint)
			if !found {
				return
			}
			if c.isBuildingLedger(seq) {
				return
			}
			c.startLedgerAcquisition(seq, hash, peer)
			return
		}
	}
	if target.source == catchupSourcePeer && !svc.NeedsInitialSync() &&
		!svc.IsFastLoadProvisional() && c.peerStatusEvidencePending(actualClosed, tSeq, tHash) {
		if c.forwardLinkagePending(svc, actualClosed, tSeq) {
			if c.withinCatchupLinkageGrace(actualClosed, tSeq, tHash, time.Now()) {
				return
			}
		}
		c.clearPeerStatusEvidence(actualClosed, tSeq, tHash)
	}

	c.acquisitionMu.Lock()
	recovery := c.consensusRecovery
	if recovery.targetHash != tHash {
		recovery = consensusRecovery{}
	}
	c.acquisitionMu.Unlock()
	if seq, hash, parent, replay, _ := c.recoveryForwardStep(svc, tSeq, tHash, recovery); replay && parent != nil {
		peer, found := c.resolveAcquisitionPeer(seq, peerHint)
		if !found {
			return
		}
		if c.isBuildingLedger(seq) {
			return
		}
		c.startLedgerAcquisitionFromParent(seq, hash, peer, parent)
		return
	}
	if svc.IsFastLoadProvisional() && c.forwardLinkagePending(svc, actualClosed, tSeq) {
		if c.withinCatchupLinkageGrace(actualClosed, tSeq, tHash, time.Now()) {
			return
		}
	}
	peer, found := c.resolveAcquisitionPeer(tSeq, peerHint)
	if !found {
		return
	}
	if c.isBuildingLedger(tSeq) {
		return
	}
	c.beginFrozenPivotRecovery(tSeq, tHash, peer)
}

// forwardDeltaStep returns the next ledger only when the peer's advertised
// branch is linked directly to the local closed ledger. A missing or
// conflicting parent link remains unknown ancestry and is handled by the
// bounded header walk after trusted evidence arrives.
func (c *catchupReplayCoordinator) forwardDeltaStep(svc *service.Service, closed, tipSeq uint32) (seq uint32, hash [32]byte, ok bool) {
	if svc == nil || tipSeq < closed || tipSeq-closed > maxForwardDeltaGap {
		return 0, [32]byte{}, false
	}
	next := closed + 1
	entry, known := c.lookupSeqHash(next)
	if !known || entry.hash == ([32]byte{}) {
		return 0, [32]byte{}, false
	}
	closedLedger := svc.GetClosedLedger()
	if closedLedger == nil {
		return 0, [32]byte{}, false
	}
	closedHash := closedLedger.Hash()

	sameBranch := false
	if entry.haveParent {
		sameBranch = entry.parentHash == closedHash
	} else if svc.IsFastLoadProvisional() {
		sameBranch = true
	} else if cEntry, okC := c.lookupSeqHash(closed); okC && cEntry.hash != ([32]byte{}) {
		sameBranch = cEntry.hash == closedHash
	}
	if !sameBranch {
		return 0, [32]byte{}, false
	}
	return next, entry.hash, true
}

func (c *catchupReplayCoordinator) withinCatchupLinkageGrace(
	closed, seq uint32,
	hash [32]byte,
	now time.Time,
) bool {
	c.catchupMu.Lock()
	defer c.catchupMu.Unlock()
	if c.linkageWait.closed != closed || c.linkageWait.since.IsZero() {
		c.linkageWait = catchupLinkageWait{
			closed: closed,
			seq:    seq,
			hash:   hash,
			since:  now,
		}
		return true
	}
	c.linkageWait.seq = seq
	c.linkageWait.hash = hash
	return now.Sub(c.linkageWait.since) < catchupLinkageGracePeriod
}

func (c *catchupReplayCoordinator) notePeerStatusEvidence(seq uint32, hash [32]byte) {
	if seq == 0 || hash == ([32]byte{}) || c.adaptor == nil {
		return
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return
	}
	closed := svc.GetClosedLedgerIndex()
	now := time.Now()
	c.catchupMu.Lock()
	if !c.peerStatusEvidence || c.linkageWait.closed != closed ||
		c.linkageWait.seq != seq || c.linkageWait.hash != hash || c.linkageWait.since.IsZero() {
		c.linkageWait = catchupLinkageWait{
			closed: closed,
			seq:    seq,
			hash:   hash,
			since:  now,
		}
	}
	c.peerStatusEvidence = true
	c.catchupMu.Unlock()
}

func (c *catchupReplayCoordinator) peerStatusEvidencePending(closed, seq uint32, hash [32]byte) bool {
	c.catchupMu.Lock()
	pending := c.peerStatusEvidence &&
		c.catchup.source == catchupSourcePeer &&
		c.catchup.seq == seq && c.catchup.hash == hash &&
		c.linkageWait.closed == closed &&
		c.linkageWait.seq == seq && c.linkageWait.hash == hash &&
		!c.linkageWait.since.IsZero()
	c.catchupMu.Unlock()
	return pending
}

func (c *catchupReplayCoordinator) clearPeerStatusEvidence(closed, seq uint32, hash [32]byte) {
	c.catchupMu.Lock()
	if c.peerStatusEvidence && c.linkageWait.closed == closed &&
		c.linkageWait.seq == seq && c.linkageWait.hash == hash {
		c.peerStatusEvidence = false
	}
	c.catchupMu.Unlock()
}

func (c *catchupReplayCoordinator) forwardLinkagePending(svc *service.Service, closed, tipSeq uint32) bool {
	if tipSeq <= closed+1 || tipSeq-closed > maxForwardDeltaGap {
		return false
	}
	next, known := c.lookupSeqHash(closed + 1)
	if !known || next.hash == ([32]byte{}) {
		tip, tipKnown := c.lookupSeqHash(tipSeq)
		return !tipKnown || !tip.haveParent
	}
	closedLedger := svc.GetClosedLedger()
	if closedLedger == nil {
		return true
	}
	if next.haveParent {
		return false
	}
	current, known := c.lookupSeqHash(closed)
	return !known || current.hash == ([32]byte{})
}

// ensureCatchupAcquisition is the single funnel for gossip-driven consensus
// catch-up: record the best target, then arm one acquisition under the
// maxConcurrentCatchup cap. At the cap it only retargets, so a stream of
// ever-higher tips no longer fans out one acquisition per event; the completion
// path re-arms toward the latest target. Bounds CONCURRENCY only — callers do
// their own eligibility gating first.
func (c *catchupReplayCoordinator) ensureCatchupAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	c.ensureCatchupAcquisitionWithPriority(seq, hash, peerID, catchupSourcePeer)
}

// ensureValidationCatchupAcquisition acquires each trusted-validation ledger
// without allowing a lagging validator to lower the preferred catch-up frontier.
func (c *catchupReplayCoordinator) ensureValidationCatchupAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	c.ensureCatchupAcquisitionWithPriority(seq, hash, peerID, catchupSourceValidation)
}

func (c *catchupReplayCoordinator) ensureCatchupAcquisitionWithPriority(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	source catchupTargetSource,
) {
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return
	}
	if seq == 0 || seq <= svc.GetClosedLedgerIndex() {
		return
	}
	if held, err := svc.GetLedgerByHash(hash); err == nil && held != nil {
		return
	}
	peerID, _ = c.resolveAcquisitionPeer(seq, peerID)
	if source == catchupSourcePeer {
		c.recordCatchupTarget(seq, hash, peerID)
	} else {
		c.recordValidationCatchupTarget(seq, hash, peerID, source)
	}
	if il := c.fetchTracker.Find(hash); il != nil && il.Reason() == inbound.ReasonConsensus {
		c.refreshCatchupAcquisitionPeer(il, peerID)
		return
	}
	c.armCatchupTowardTargetWithPeer(peerID)
}

func (c *catchupReplayCoordinator) refreshCatchupAcquisitionPeer(il *inbound.Ledger, peerID uint64) {
	if peerID == 0 || il.State() != inbound.StateWantBase || slices.Contains(il.Peers(), peerID) {
		return
	}
	c.requestLedgerBase(il, peerID, "failed to request ledger base from replacement peer")
}

// startLedgerAcquisition picks the best available ledger-acquisition
// strategy for the given target. When we have the parent ledger locally
// and the peer advertises ledger-replay, the bandwidth-efficient
// replay-delta protocol is preferred (one request returns header + every
// tx blob). When the peer does not support that extension, standard
// mtGET_LEDGER fetches only the header + transaction SHAMap and the same local
// replay verifier derives the child state. A full header+state walk is reserved
// for targets whose parent is not held locally.
//
// This is currently the only driver of startReplayDeltaAcquisition: it
// handles a single target ledger per call. The Replayer coordinator
// supports concurrent acquisitions across many hashes, but the policy
// layer that walks a range (e.g., backward from a peer's tip via
// ParentHash) is a follow-up item.
func (c *catchupReplayCoordinator) startLedgerAcquisition(seq uint32, hash [32]byte, peerID uint64) bool {
	return c.startLedgerAcquisitionFromParent(seq, hash, peerID, nil)
}

func (c *catchupReplayCoordinator) startLedgerAcquisitionFromParent(seq uint32, hash [32]byte, peerID uint64, parent *ledger.Ledger) bool {
	if c.stoppedForShutdown() {
		return false
	}
	if seq != 0 && c.belowFloor(seq) {
		return false
	}
	if c.isBuildingLedger(seq) {
		return false
	}
	if target := c.credibleCatchupFrontier(); target.seq == seq && target.hash == hash &&
		c.maybeStartHeaderParentDiscovery(target, peerID) {
		return true
	}
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	if step := c.consensusRecovery.stepHash; step != ([32]byte{}) && step != hash && c.isAcquiringLocked(step) {
		return false
	}
	if !c.canAdmitCatchupLocked(hash, maxConcurrentSpeculativeCatchup) {
		return false
	}
	c.startLedgerAcquisitionWithParentLocked(seq, hash, peerID, parent)
	return c.isAcquiringLocked(hash)
}

// canAdmitCatchupLocked atomically combines hash deduplication with capacity
// admission. Gossip and validation paths use the speculative limit so the exact
// WrongLedger target can always claim the final slot.
func (c *catchupReplayCoordinator) canAdmitCatchupLocked(hash [32]byte, limit int) bool {
	if c.isAcquiringLocked(hash) {
		return true
	}
	return c.protectedCatchupInFlightLocked() < limit
}

func (c *catchupReplayCoordinator) startLedgerAcquisitionWithParentLocked(seq uint32, hash [32]byte, peerID uint64, parent *ledger.Ledger) {
	if c.stoppedForShutdown() {
		return
	}
	if c.catchupRetryBlocked(hash, time.Now()) {
		return
	}
	// Unified dedup across BOTH acquisition paths. A prior fix only
	// checked c.replayer.Has(hash); that still allowed the cross-path
	// race where two status changes at the same seq with different
	// hashes armed both a replay-delta AND a legacy acquisition
	// simultaneously, with adoption order then deciding which won. The
	// single-point-of-truth check is a deliberate narrowing: a tighter
	// guarantee that the same hash can't acquire through both paths.
	if c.isAcquiringLocked(hash) {
		return
	}

	// Already held locally (built or just adopted): never re-download. Without
	// this latch a consensus retrigger refetches the just-adopted ledger in a
	// tight loop, flooding peers past rippled's resource drop threshold.
	if svc := c.adaptor.LedgerService(); svc != nil {
		if l, err := svc.GetLedgerByHash(hash); err == nil && l != nil {
			return
		}
	}

	if parent == nil {
		parent = c.adaptor.GetParentLedgerForReplay(seq)
	}
	if parent != nil && c.acquisition.PeerSupportsReplay(peerID) {
		if err := c.startReplayDeltaAcquisition(seq, hash, peerID, parent); err == nil {
			return
		}
	}
	if parent != nil {
		c.startLedgerReplayAcquisitionLegacyLocked(seq, hash, peerID)
		return
	}
	c.startLedgerAcquisitionLegacyLocked(seq, hash, peerID)
}

// isAcquiring reports whether an acquisition — replay-delta or legacy
// — is currently in flight for the given ledger hash. Used as the
// single dedup entry point so a race between a replay-delta and a
// legacy acquisition for the same hash is impossible.
func (c *catchupReplayCoordinator) isAcquiring(hash [32]byte) bool {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	return c.isAcquiringLocked(hash)
}

// Caller holds acquisitionMu.
func (c *catchupReplayCoordinator) isAcquiringLocked(hash [32]byte) bool {
	if c.standardReplay.pivotHandoff != nil &&
		c.standardReplay.pivotHandoff.acquisition.Hash() == hash {
		return true
	}
	if c.replayer.Has(hash) {
		return true
	}
	if c.fetchTracker.Find(hash) != nil {
		return true
	}
	return false
}

// startReplayDeltaAcquisition registers a new acquisition with the
// Replayer coordinator and issues the corresponding
// mtREPLAY_DELTA_REQUEST.
//
// Returns ErrAcquisitionExists if a request for the same hash is
// already in flight (caller should drop the duplicate), ErrCapacityFull
// if the coordinator is at cap (caller falls back to legacy), or the
// wire-send error if the request itself failed (coordinator slot is
// freed before returning so the caller can retry).
// Caller holds acquisitionMu.
func (c *catchupReplayCoordinator) startReplayDeltaAcquisition(seq uint32, hash [32]byte, peerID uint64, parent *ledger.Ledger) error {
	if c.stoppedForShutdown() {
		return context.Canceled
	}
	if c.replayFaultBlocked() {
		return replayfault.ErrBlocked
	}
	if c.replayNeedsFullStateLocked(hash) {
		return errors.New("ledger requires full-state acquisition after replay failure")
	}
	rd, err := c.replayer.Acquire(hash, peerID, parent)
	if err != nil {
		return err
	}
	_ = rd // retained in replayer; HandleResponse retrieves it on reply.
	c.logger.Info("starting replay delta acquisition",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)
	if err := c.acquisition.RequestReplayDelta(peerID, hash); err != nil {
		c.logger.Warn("failed to request replay delta from peer", "error", err)
		c.replayer.Abandon(hash)
		return err
	}
	return nil
}

// startLedgerAcquisitionLegacy requests the full ledger (header + state
// tree) from a peer using the legacy mtGET_LEDGER protocol. This is the
// fallback path when the parent isn't locally available or replay-delta
// verification fails.
//
// Callers that enter via startLedgerAcquisition already consult
// isAcquiring across both paths — but we still re-check here because
// maintenanceTick and the replay-delta fallback paths can enter
// directly, bypassing the unified entry point.
func (c *catchupReplayCoordinator) startLedgerAcquisitionLegacy(seq uint32, hash [32]byte, peerID uint64) {
	if c.stoppedForShutdown() {
		return
	}
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	c.startLedgerAcquisitionLegacyLocked(seq, hash, peerID)
}

// Caller holds acquisitionMu.
func (c *catchupReplayCoordinator) startLedgerReplayAcquisitionLegacyLocked(seq uint32, hash [32]byte, peerID uint64) (*inbound.Ledger, bool) {
	if c.replayFaultBlocked() {
		return nil, false
	}
	if c.replayNeedsFullStateLocked(hash) {
		c.startLedgerAcquisitionLegacyLocked(seq, hash, peerID)
		return c.fetchTracker.Find(hash), false
	}
	if seq != 0 && c.belowFloor(seq) {
		return nil, false
	}
	if c.standardReplay.pivotHandoff != nil &&
		c.standardReplay.pivotHandoff.acquisition.Hash() == hash {
		return nil, false
	}
	if c.replayer.Has(hash) {
		return nil, false
	}
	if svc := c.adaptor.LedgerService(); svc != nil {
		if l, err := svc.GetLedgerByHash(hash); err == nil && l != nil {
			return nil, false
		}
	}

	opts := append(c.acquisitionOpts(), inbound.WithTransactionOnly())
	il, created := c.fetchTracker.GetOrCreateWithSequence(hash, seq, func() *inbound.Ledger {
		return inbound.New(hash, seq, peerID, c.logger, opts...)
	})
	if !created {
		return il, false
	}

	c.logger.Info("starting ledger replay acquisition (standard tx-only)",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)

	c.seedAcquisitionPeers(il)
	requested := false
	for _, candidate := range il.Peers() {
		if c.requestLedgerBaseFromPeer(il, candidate, "failed to request replay ledger base from peer") {
			requested = true
		}
	}
	if !requested {
		c.requestLedgerBase(il, 0, "failed to request replay ledger base from peer")
	}
	return il, true
}

func (c *catchupReplayCoordinator) startLedgerAcquisitionLegacyLocked(seq uint32, hash [32]byte, peerID uint64) {
	c.startLedgerAcquisitionLegacyModeLocked(seq, hash, peerID, false)
}

func (c *catchupReplayCoordinator) startLedgerAcquisitionLegacyModeLocked(seq uint32, hash [32]byte, peerID uint64, repair bool) {
	if c.stoppedForShutdown() {
		return
	}
	repairParent := repair && c.replayFaultBlocked() && c.adaptor.LedgerService().ReplayRecoveryParent(hash)
	if c.replayFaultBlocked() && !repairParent {
		return
	}
	if c.catchupRetryBlocked(hash, time.Now()) {
		return
	}
	if !repairParent && seq != 0 && c.belowFloor(seq) {
		return
	}
	if c.standardReplay.pivotHandoff != nil &&
		c.standardReplay.pivotHandoff.acquisition.Hash() == hash {
		return
	}
	if svc := c.adaptor.LedgerService(); svc != nil && !repairParent {
		if held, err := svc.GetLedgerByHash(hash); err == nil && held != nil {
			return
		}
	}
	// Safety net: if a replay-delta for the same hash is still
	// registered, don't start a legacy on top of it — one path is
	// always enough.
	if c.replayer.Has(hash) {
		return
	}
	if !c.canAdmitProvisionalFullStateLocked(hash) {
		return
	}

	il, created := c.fetchTracker.GetOrCreateWithSequence(hash, seq, func() *inbound.Ledger {
		return inbound.New(hash, seq, peerID, c.logger, c.acquisitionOpts()...)
	})
	if !created {
		// Already acquiring this hash (consensus or a prior arm).
		return
	}

	c.logger.Info("starting ledger acquisition (legacy)",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)

	c.seedAcquisitionPeers(il)
	requested := false
	for _, candidate := range il.Peers() {
		if c.requestLedgerBaseFromPeer(il, candidate, "failed to request ledger base from peer") {
			requested = true
		}
	}
	if !requested {
		c.requestLedgerBase(il, 0, "failed to request ledger base from peer")
	}
}

// Caller holds acquisitionMu.
func (c *catchupReplayCoordinator) canAdmitProvisionalFullStateLocked(hash [32]byte) bool {
	svc := c.adaptor.LedgerService()
	if svc == nil || !svc.IsFastLoadProvisional() {
		return true
	}
	if existing := c.fetchTracker.Find(hash); existing != nil {
		return true
	}
	if c.standardReplay.pivotHandoff != nil &&
		c.standardReplay.pivotHandoff.acquisition.Hash() == hash {
		return true
	}
	active := 0
	for _, candidate := range c.fetchTracker.Active() {
		if candidate.Reason() == inbound.ReasonConsensus && !candidate.TransactionOnly() {
			active++
		}
	}
	if c.standardReplay.pivotHandoff != nil &&
		c.fetchTracker.Find(c.standardReplay.pivotHandoff.acquisition.Hash()) != c.standardReplay.pivotHandoff.acquisition {
		active++
	}
	return active < maxConcurrentProvisionalFullStateCatchup
}

func (c *catchupReplayCoordinator) fallbackReplayAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	c.fallbackReplayAcquisitionForTarget(seq, hash, peerID, standardReplayTarget{})
}

func (c *catchupReplayCoordinator) fallbackReplayAvailability(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	parent *ledger.Ledger,
	triedPeers []uint64,
) {
	c.fallbackReplayAcquisitionForTargetMode(
		seq, hash, peerID, parent, triedPeers, standardReplayTarget{}, true,
	)
}

func (c *catchupReplayCoordinator) fallbackStandardReplayAcquisition(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	expected standardReplayTarget,
) {
	c.fallbackReplayAcquisitionForTarget(seq, hash, peerID, expected)
}

func (c *catchupReplayCoordinator) fallbackReplayAcquisitionForTarget(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	expected standardReplayTarget,
) {
	c.fallbackReplayAcquisitionForTargetMode(seq, hash, peerID, nil, nil, expected, false)
}

func (c *catchupReplayCoordinator) fallbackReplayAcquisitionForTargetMode(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	parent *ledger.Ledger,
	triedPeers []uint64,
	expected standardReplayTarget,
	availability bool,
) {
	c.acquisitionMu.Lock()

	if !availability {
		c.requireReplayFullStateLocked(seq, hash)
	}
	availability = availability && !c.replayNeedsFullStateLocked(hash)
	target := c.consensusRecovery.targetHash
	if expected.hash != ([32]byte{}) {
		if target != ([32]byte{}) {
			if target != expected.hash {
				c.clearReplayAvailabilityRetryLocked(hash)
				c.acquisitionMu.Unlock()
				return
			}
		} else {
			c.catchupMu.Lock()
			current := c.catchup.seq == expected.seq && c.catchup.hash == expected.hash
			c.catchupMu.Unlock()
			if !current {
				c.clearReplayAvailabilityRetryLocked(hash)
				c.acquisitionMu.Unlock()
				return
			}
		}
	}
	if target != ([32]byte{}) && c.consensusRecovery.stepHash != hash && target != hash {
		c.clearReplayAvailabilityRetryLocked(hash)
		c.acquisitionMu.Unlock()
		return
	}
	if target != ([32]byte{}) && target != hash {
		targetSeq, known := c.lookupSeqForHash(target)
		if !known {
			c.clearReplayAvailabilityRetryLocked(hash)
			c.consensusRecovery.stepHash = [32]byte{}
			c.acquisitionMu.Unlock()
			c.armConsensusCatchup()
			return
		}
		nextSeq, nextHash, _, replay, _ := c.recoveryForwardStep(
			c.adaptor.LedgerService(),
			targetSeq,
			target,
			c.consensusRecovery,
		)
		if !replay || nextSeq != seq || nextHash != hash {
			c.clearReplayAvailabilityRetryLocked(hash)
			c.consensusRecovery.stepHash = [32]byte{}
			c.acquisitionMu.Unlock()
			c.armConsensusCatchup()
			return
		}
	}
	var parentUsable bool
	if availability {
		parentUsable = c.replayParentUsable(seq, parent)
		if prepared, startDrain := c.useCompatiblePreparedReplayLocked(seq, hash, parentUsable, parent); prepared {
			c.clearReplayAvailabilityRetryLocked(hash)
			c.acquisitionMu.Unlock()
			if startDrain {
				c.drainStandardReplayPipeline()
			}
			return
		}
	}

	if !c.canAdmitCatchupLocked(hash, maxConcurrentCatchup) {
		c.acquisitionMu.Unlock()
		return
	}

	if availability {
		if parentUsable && !c.standardReplayOwnsLocked(hash) && !c.isAcquiringLocked(hash) {
			if alternate, ok := c.nextReplayAvailabilityPeerLocked(seq, hash, peerID, triedPeers); ok {
				if err := c.startReplayDeltaAcquisition(seq, hash, alternate, parent); err == nil {
					if target != ([32]byte{}) {
						c.consensusRecovery.stepHash = hash
					}
					c.acquisitionMu.Unlock()
					return
				}
			}
		}

		// A locally verified parent allows the standard protocol to fetch only
		// the target header and transaction tree. Its replay verifier still
		// checks the derived state root before adoption.
		if parentUsable {
			il, _ := c.startLedgerReplayAcquisitionLegacyLocked(seq, hash, peerID)
			if il != nil && il.TransactionOnly() {
				if target != ([32]byte{}) {
					c.consensusRecovery.stepHash = hash
				}
				c.clearReplayAvailabilityRetryLocked(hash)
				c.acquisitionMu.Unlock()
				return
			}
		}
		c.clearReplayAvailabilityRetryLocked(hash)
	}

	c.clearReplayAvailabilityRetryLocked(hash)
	c.startLedgerAcquisitionLegacyLocked(seq, hash, peerID)
	if target != ([32]byte{}) && c.fetchTracker.Find(hash) != nil {
		c.consensusRecovery.stepHash = hash
	}
	c.acquisitionMu.Unlock()
}

func (c *catchupReplayCoordinator) replayParentUsable(seq uint32, parent *ledger.Ledger) bool {
	if parent == nil || !parent.IsClosed() || parent.Sequence()+1 != seq || parent.Hash() == ([32]byte{}) {
		return false
	}
	if c.adaptor == nil {
		return false
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return false
	}
	held, err := svc.GetLedgerByHash(parent.Hash())
	return err == nil && held != nil && held.IsClosed() && held.Sequence()+1 == seq && held.Hash() == parent.Hash()
}

// useCompatiblePreparedReplayLocked leaves an already collected standard
// replay entry in place. The caller holds acquisitionMu.
func (c *catchupReplayCoordinator) useCompatiblePreparedReplayLocked(
	seq uint32,
	hash [32]byte,
	parentUsable bool,
	parent *ledger.Ledger,
) (prepared, startDrain bool) {
	if !parentUsable || parent == nil || !c.standardReplay.active {
		return false, false
	}
	entry := c.standardReplay.entries[seq]
	if entry == nil || entry.generation != c.standardReplay.generation || entry.hash != hash ||
		entry.parentHash != parent.Hash() || entry.seq != seq || entry.failed {
		return false, false
	}
	if entry.acquisition == nil && entry.readyAt.IsZero() {
		return false, false
	}
	if entry.acquisition != nil && !entry.acquisition.TransactionOnly() {
		return false, false
	}
	startDrain = c.standardReplay.pivotReady && !c.standardReplay.applying &&
		entry.seq == c.standardReplay.anchorSeq+1 && !entry.readyAt.IsZero()
	if startDrain {
		c.standardReplay.applying = true
	}
	return true, startDrain
}

func (c *catchupReplayCoordinator) nextReplayAvailabilityPeerLocked(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	triedPeers []uint64,
) (uint64, bool) {
	now := time.Now()
	if c.replayAvailabilityRetries == nil {
		c.replayAvailabilityRetries = make(map[[32]byte]replayAvailabilityRetryState)
	}
	retry, exists := c.replayAvailabilityRetries[hash]
	if exists && !retry.expiresAt.IsZero() && !now.Before(retry.expiresAt) {
		// An expired budget is terminal for this recovery attempt. Keep the
		// marker while the replacement replay is in flight so a delayed
		// availability reply cannot reopen the budget; the next call clears
		// it after the acquisition has been abandoned.
		if !c.replayer.Has(hash) {
			delete(c.replayAvailabilityRetries, hash)
		}
		return 0, false
	}
	if !exists && len(c.replayAvailabilityRetries) >= inbound.DefaultMaxInFlightReplays {
		return 0, false
	}
	for _, tried := range triedPeers {
		retry.peers = appendUniquePeer(retry.peers, tried)
	}
	retry.peers = appendUniquePeer(retry.peers, peerID)
	if retry.retries >= replayAvailabilityRetryLimit {
		delete(c.replayAvailabilityRetries, hash)
		return 0, false
	}
	// SelectLedgerPeers keeps the retry on a connected peer and prioritizes
	// peers that advertise the requested ledger. Filter that bounded set by
	// the replay handshake feature before choosing an alternative.
	candidates := c.acquisition.SelectLedgerPeers(hash, seq, retry.peers, replayAvailabilityPeerSelectionLimit)
	var next uint64
	for _, candidate := range candidates {
		if candidate != 0 && c.acquisition.PeerSupportsReplay(candidate) {
			next = candidate
			break
		}
	}
	if next == 0 {
		delete(c.replayAvailabilityRetries, hash)
		return 0, false
	}
	retry.peers = appendUniquePeer(retry.peers, next)
	retry.retries++
	if retry.expiresAt.IsZero() {
		retry.expiresAt = now.Add(replayAvailabilityRetryTTL)
	}
	c.replayAvailabilityRetries[hash] = retry
	return next, true
}

func appendUniquePeer(peers []uint64, peerID uint64) []uint64 {
	if peerID == 0 {
		return peers
	}
	for _, existing := range peers {
		if existing == peerID {
			return peers
		}
	}
	return append(peers, peerID)
}

func (c *catchupReplayCoordinator) clearReplayAvailabilityRetry(hash [32]byte) {
	c.acquisitionMu.Lock()
	c.clearReplayAvailabilityRetryLocked(hash)
	c.acquisitionMu.Unlock()
}

func (c *catchupReplayCoordinator) clearReplayAvailabilityRetryLocked(hash [32]byte) {
	if c.replayAvailabilityRetries != nil {
		delete(c.replayAvailabilityRetries, hash)
	}
}

func (c *catchupReplayCoordinator) isReplayAvailabilityPeer(hash [32]byte, peerID uint64) bool {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	retry, ok := c.replayAvailabilityRetries[hash]
	if !ok {
		return false
	}
	for _, tried := range retry.peers {
		if tried == peerID {
			return true
		}
	}
	return false
}

func (c *catchupReplayCoordinator) expireReplayAvailabilityRetries() {
	if c.stoppedForShutdown() {
		return
	}
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	for hash := range c.replayAvailabilityRetries {
		if !c.replayer.Has(hash) {
			delete(c.replayAvailabilityRetries, hash)
		}
	}
}

func (c *catchupReplayCoordinator) requestConsensusLedger(id consensus.LedgerID) error {
	if c.stoppedForShutdown() {
		return context.Canceled
	}
	hash := [32]byte(id)
	if hash == ([32]byte{}) {
		return nil
	}

	c.acquisitionMu.Lock()
	c.consensusRecovery.targetHash = hash
	recovery := c.consensusRecovery
	step := c.consensusRecovery.stepHash
	pipelineStep := c.standardReplayOwnsLocked(step)
	if step != ([32]byte{}) && c.isAcquiringLocked(step) && !pipelineStep {
		c.acquisitionMu.Unlock()
		return nil
	}
	pipelineTarget := c.standardReplayOwnsLocked(hash)
	if c.isAcquiringLocked(hash) && !pipelineTarget {
		c.consensusRecovery.stepHash = hash
		c.acquisitionMu.Unlock()
		return nil
	}
	if !pipelineTarget {
		c.consensusRecovery.stepHash = [32]byte{}
	}
	c.acquisitionMu.Unlock()
	seq, _ := c.lookupSeqForHash(hash)
	if c.continueFrozenPivotRecovery(seq, hash, 0) {
		return nil
	}
	if svc := c.adaptor.LedgerService(); svc != nil && seq > svc.GetClosedLedgerIndex() {
		held, _ := svc.GetLedgerByHash(hash)
		provenReplay := held != nil && held.Sequence() == seq && held.Hash() == hash
		_, _, _, forwardReplay, discardAnchor := c.recoveryForwardStep(svc, seq, hash, recovery)
		provenReplay = provenReplay || forwardReplay
		if discardAnchor {
			c.acquisitionMu.Lock()
			if c.consensusRecovery.anchorSeq == recovery.anchorSeq &&
				c.consensusRecovery.anchorHash == recovery.anchorHash {
				c.consensusRecovery.anchorSeq = 0
				c.consensusRecovery.anchorHash = [32]byte{}
			}
			c.acquisitionMu.Unlock()
		}
		if !provenReplay {
			peerID, found := c.resolveAcquisitionPeer(seq, 0)
			if found && c.beginFrozenPivotRecovery(seq, hash, peerID) {
				return nil
			}
		}
	}
	c.reconcileStandardReplayTarget(seq, hash)
	c.armConsensusCatchup()
	return nil
}

const acquisitionPeerStart = 5

const (
	replayAvailabilityRetryLimit         = 3
	replayAvailabilityRetryTTL           = 10 * time.Second
	replayAvailabilityPeerSelectionLimit = 16
)

type replayAvailabilityRetryState struct {
	peers     []uint64
	retries   uint8
	expiresAt time.Time
}

func (c *catchupReplayCoordinator) seedAcquisitionPeers(il *inbound.Ledger) {
	peers := il.Peers()
	remaining := acquisitionPeerStart - len(peers)
	if remaining <= 0 {
		return
	}
	for _, peerID := range c.acquisition.SelectLedgerPeers(il.Hash(), il.Seq(), peers, remaining) {
		il.AddPeerBounded(peerID, acquisitionPeerStart)
	}
}

func (c *catchupReplayCoordinator) requestLedgerBase(il *inbound.Ledger, peerID uint64, logMessage string) bool {
	excluded := make(map[uint64]struct{})
	for {
		if peerID == 0 {
			var ok bool
			peerID, ok = c.selectAcquisitionPeerExcluding(il.Seq(), excluded)
			if !ok {
				return false
			}
		}

		if c.requestLedgerBaseFromPeer(il, peerID, logMessage) {
			return true
		}
		excluded[peerID] = struct{}{}
		peerID = 0
	}
}

func (c *catchupReplayCoordinator) requestLedgerBaseFromPeer(il *inbound.Ledger, peerID uint64, logMessage string) bool {
	if c.stoppedForShutdown() {
		return false
	}
	il.AddPeer(peerID)
	err := c.acquisition.RequestLedgerBaseFromPeer(peerID, il.Hash(), il.Seq(), il.Timeouts() > 0)
	if err == nil {
		return true
	}
	c.logger.Warn(logMessage, "error", err, "peer", peerID)
	if !isAcquisitionDisconnectError(err) {
		return true
	}
	il.RemovePeer(peerID)
	c.onPeerDisconnect(peermanagement.PeerID(peerID))
	return false
}

func isAcquisitionDisconnectError(err error) bool {
	return errors.Is(err, peermanagement.ErrPeerNotFound) ||
		errors.Is(err, peermanagement.ErrConnectionClosed)
}

func isReplayAvailabilityError(err message.ReplyError) bool {
	return err == message.ReplyErrorNoNode || err == message.ReplyErrorNoLedger
}

func (c *catchupReplayCoordinator) invalidateHistoryPeer(peerID uint64) {
	c.historyMu.Lock()
	defer c.historyMu.Unlock()
	if c.history.peerID == peerID {
		c.history.peerID = 0
	}
}

// startHistoryBackfill records the next skipped ledger to backfill after a
// jump-adopt, bounded below by floor (the pre-jump closed seq — already
// contiguous). The walk is serial and backward, each header naming its parent;
// the maintenance tick arms the fetches.
func (c *catchupReplayCoordinator) startHistoryBackfill(seq uint32, hash [32]byte, peerID uint64, floor uint32) {
	if !c.historyBackfill || c.historyDepth == 0 || seq == 0 || seq <= floor || hash == ([32]byte{}) {
		return
	}
	c.historyMu.Lock()
	c.history = catchupTarget{seq: seq, hash: hash, peerID: peerID}
	c.historyFloor = floor
	c.historySeeded = true
	c.historyMu.Unlock()
}

func (c *catchupReplayCoordinator) onLedgerSwitched(seq uint32, _ [32]byte, parentHash [32]byte, historyFloor uint32) {
	if seq == 0 {
		return
	}
	c.startHistoryBackfill(seq-1, parentHash, 0, historyFloor)
}

func (c *catchupReplayCoordinator) onLedgerFullyValidated(seq uint32, hash [32]byte) {
	// This callback also observes remote quorums before their ledger is local.
	// Retention follows our installed validated state, not an uninstalled head.
	if c.adaptor != nil && c.adaptor.LedgerService() != nil {
		c.pruneHistoryBackfill(c.adaptor.LedgerService().GetValidatedLedgerIndex())
	}
	c.recordSeqHash(seq, hash, [32]byte{}, false)
	if c.locallySatisfiesLedger(seq, hash) {
		c.retireLocallySatisfiedLedger(seq, hash, "ledger_validated")
	}

	removed := make(map[[32]byte]struct{})
	var legacy []*inbound.Ledger

	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	for _, candidate := range c.fetchTracker.Active() {
		if candidate.Reason() != inbound.ReasonConsensus ||
			candidate.Seq() != seq || candidate.Hash() == hash {
			continue
		}
		if c.fetchTracker.DiscardExpected(candidate) {
			legacy = append(legacy, candidate)
			removed[candidate.Hash()] = struct{}{}
		}
	}
	for _, replayHash := range c.replayer.AbandonOtherAtSequence(seq, hash) {
		removed[replayHash] = struct{}{}
	}
	pipelineConflict := c.standardReplay.active &&
		((seq == c.standardReplay.anchorSeq && hash != c.standardReplay.anchorHash) ||
			(seq == c.standardReplay.targetSeq && hash != c.standardReplay.targetHash))
	if entry := c.standardReplay.entries[seq]; entry != nil && entry.hash != hash {
		pipelineConflict = true
	}
	var pipelineRetirement standardReplayRetirement
	if pipelineConflict {
		for _, pipelineEntry := range c.standardReplay.entries {
			removed[pipelineEntry.hash] = struct{}{}
		}
		pipelineRetirement = c.cancelStandardReplayPipelineLocked("validation_conflict")
		legacy = append(legacy, pipelineRetirement.ledgers...)
		pipelineRetirement.ledgers = nil
	}
	if victim := c.obsoleteCatchupVictimLocked(seq); victim != nil && c.fetchTracker.DiscardExpected(victim) {
		legacy = append(legacy, victim)
		removed[victim.Hash()] = struct{}{}
	}
	if _, ok := removed[c.consensusRecovery.stepHash]; ok {
		c.consensusRecovery.stepHash = [32]byte{}
	}
	if _, ok := removed[c.consensusRecovery.targetHash]; ok {
		c.consensusRecovery = consensusRecovery{}
	}
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()

	c.recordValidationCatchupTarget(seq, hash, 0, catchupSourceQuorum)

	c.retireLegacyAcquisitions(legacy)
	c.retireStandardReplay(pipelineRetirement)
	if len(removed) > 0 {
		c.logger.Info("canceled acquisitions superseded by trusted validation quorum",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"canceled", len(removed),
		)
	}
	c.armValidatedLedgerAcquisition(seq, hash)
}

func (c *catchupReplayCoordinator) obsoleteCatchupVictimLocked(targetSeq uint32) *inbound.Ledger {
	if c.protectedCatchupInFlightLocked() < maxConcurrentSpeculativeCatchup {
		return nil
	}
	var victim *inbound.Ledger
	for _, candidate := range c.fetchTracker.Active() {
		seq := candidate.Seq()
		if candidate.Reason() != inbound.ReasonConsensus || candidate.TransactionOnly() ||
			seq == 0 || seq >= targetSeq || candidate.Hash() == c.consensusRecovery.targetHash ||
			candidate.Hash() == c.consensusRecovery.stepHash ||
			(c.standardReplay.active && !c.standardReplay.pivotReady &&
				candidate.Hash() == c.standardReplay.pivotHash) {
			continue
		}
		consecutive := candidate.ConsecutiveTimeouts()
		if consecutive < 2 && targetSeq-seq <= maxForwardDeltaGap {
			continue
		}
		if victim == nil || seq < victim.Seq() ||
			(seq == victim.Seq() && consecutive > victim.ConsecutiveTimeouts()) ||
			(seq == victim.Seq() && consecutive == victim.ConsecutiveTimeouts() &&
				hashLess(candidate.Hash(), victim.Hash())) {
			victim = candidate
		}
	}
	return victim
}

func hashLess(left, right [32]byte) bool {
	return bytes.Compare(left[:], right[:]) < 0
}

func (c *catchupReplayCoordinator) completeHistoryBackfill(seq uint32, hash, parentHash [32]byte, peerID uint64) {
	if seq == 0 {
		return
	}
	c.historyMu.Lock()
	defer c.historyMu.Unlock()
	if c.history.seq != seq || c.history.hash != hash {
		return
	}
	c.history = catchupTarget{seq: seq - 1, hash: parentHash, peerID: peerID}
}

// armHistoryBackfill drives one backward history-backfill acquisition from the
// maintenance tick (rippled fetchForHistory from doAdvance). Locally-held
// ledgers advance the walk without a fetch; it ends at the gap floor, the
// online-delete floor, or genesis. At most one ReasonHistory acquisition runs,
// never in the consensus catch-up slot.
func (c *catchupReplayCoordinator) armHistoryBackfill() {
	if c.adaptor == nil {
		return
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return
	}
	tip := svc.GetValidatedLedger()
	if tip == nil {
		return
	}
	c.pruneHistoryBackfill(tip.Sequence())
	if !c.historyBackfill || c.historyDepth == 0 {
		return
	}
	if targetSeq, _, _ := c.bestCatchupTarget(); targetSeq > svc.GetClosedLedgerIndex() {
		return
	}
	if svc.GetValidatedLedgerIndex() != svc.GetClosedLedgerIndex() {
		return
	}
	c.historyMu.Lock()
	// A restart may have a valid tip but missing history without a new pivot
	// switch. Seed once, then keep the backward cursor as live ledgers arrive.
	if !c.historySeeded && tip.Sequence() > 1 {
		c.history = catchupTarget{seq: tip.Sequence() - 1, hash: tip.ParentHash()}
		c.historyFloor = 0
		c.historySeeded = true
	}
	target := c.history
	floor := c.historyFloor
	c.historyMu.Unlock()
	if target.seq == 0 {
		return
	}
	for skipped := 0; ; skipped++ {
		if !c.historySequenceAllowed(target.seq) || target.seq <= floor || target.hash == ([32]byte{}) {
			c.historyMu.Lock()
			if c.history == target && c.historyFloor == floor {
				c.history = catchupTarget{}
				c.historyFloor = 0
			}
			c.historyMu.Unlock()
			return
		}
		// Do not perform an unbounded history scan on the consensus router.
		if skipped == historySkipBudget {
			return
		}
		lookupCtx, cancelLookup := context.WithTimeout(c.lifecycleContext(), 100*time.Millisecond)
		held, err := svc.GetLedgerByHashContext(lookupCtx, target.hash)
		cancelLookup()
		if errors.Is(err, svcerr.ErrLedgerNotFound) || (err == nil && held == nil) {
			break
		}
		if err != nil {
			// A transient read failure of known-complete history is not a
			// reason to start another full-state network acquisition.
			return
		}
		// Fully acquired ledgers can already be stored by hash without being
		// adopted into canonical history (e.g. a previous startup candidate).
		// Preserve that local promotion path; merely cached headers are not
		// returned by this service lookup. Already-complete history needs no
		// repeated transaction indexing or persistence enqueue.
		if !svc.HasCompleteLedgerHash(target.seq, target.hash) {
			hdr := held.Header()
			stateMap, err := held.StateMapSnapshot()
			if err != nil {
				return
			}
			txMap, err := held.TxMapSnapshot()
			if err != nil {
				return
			}
			if err := svc.IngestHistoricalLedgerWithState(c.lifecycleContext(), &hdr, stateMap, txMap); err != nil {
				c.logger.Warn("history backfill: held ledger ingest failed", "error", err, "seq", target.seq)
				return
			}
		}
		next := catchupTarget{seq: target.seq - 1, hash: held.ParentHash(), peerID: target.peerID}
		c.historyMu.Lock()
		if c.history != target || c.historyFloor != floor {
			c.historyMu.Unlock()
			return
		}
		c.history = next
		c.historyMu.Unlock()
		target = next
	}
	c.historyMu.Lock()
	stillCurrent := c.history == target && c.historyFloor == floor
	c.historyMu.Unlock()
	if !stillCurrent {
		return
	}
	// A newer jump can leave an older, still-in-window history walk alive.
	// Prefer the newest missing ledger instead of waiting for that walk.
	for _, candidate := range c.fetchTracker.Active() {
		if candidate.Reason() == inbound.ReasonHistory && candidate.Hash() != target.hash {
			c.discardHistoryAcquisition(candidate, "newer_history_target")
		}
	}
	if c.fetchTracker.CountReason(inbound.ReasonHistory) >= 1 || c.isAcquiring(target.hash) {
		return
	}
	peer, ok := c.resolveAcquisitionPeer(target.seq, target.peerID)
	if !ok {
		return
	}
	c.historyMu.Lock()
	if c.history != target || c.historyFloor != floor {
		c.historyMu.Unlock()
		return
	}
	il := c.prepareHistoryAcquisition(target.seq, target.hash, peer)
	c.historyMu.Unlock()
	if il == nil {
		return
	}
	c.requestHistoryAcquisition(il, peer)
}

func (c *catchupReplayCoordinator) prepareHistoryAcquisition(seq uint32, hash [32]byte, peerID uint64) *inbound.Ledger {
	if c.stoppedForShutdown() {
		return nil
	}
	if !c.historySequenceAllowed(seq) || c.replayer.Has(hash) {
		return nil
	}
	il, created := c.fetchTracker.GetOrCreateWithSequence(hash, seq, func() *inbound.Ledger {
		return inbound.NewHistory(hash, seq, peerID, c.logger, c.acquisitionOpts()...)
	})
	if !created {
		return nil
	}
	return il
}

// requestHistoryAcquisition requests a skipped historical ledger (header +
// state) over legacy mtGET_LEDGER. Replay-delta doesn't apply: the walk is
// backward, so the parent is never locally available.
func (c *catchupReplayCoordinator) requestHistoryAcquisition(il *inbound.Ledger, peerID uint64) {
	if c.stoppedForShutdown() {
		return
	}
	hash := il.Hash()
	c.logger.Info("starting history backfill acquisition",
		"seq", il.Seq(),
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
		"ledger_history", c.historyDepth,
	)
	c.requestLedgerBase(il, peerID, "failed to request history ledger base from peer")
}

// FetchInfo returns the inbound-ledger acquisition snapshot served by the
// fetch_info RPC. Safe to call from any goroutine.
func (c *catchupReplayCoordinator) FetchInfo() map[string]any {
	return c.fetchTracker.Info()
}

// FastSyncMetrics returns the current bounded outcome counters.
func (c *catchupReplayCoordinator) FastSyncMetrics() FastSyncMetrics {
	c.acquisitionMu.Lock()
	depth := len(c.standardReplay.entries)
	readyDepth := 0
	for _, entry := range c.standardReplay.entries {
		if !entry.readyAt.IsZero() {
			readyDepth++
		}
	}
	headSeq := uint32(0)
	blockedUs := uint64(0)
	if c.standardReplay.active {
		headSeq = c.standardReplay.anchorSeq + 1
		if !c.standardReplay.headBlockedAt.IsZero() {
			blockedUs = durationMicros(time.Since(c.standardReplay.headBlockedAt))
		}
	}
	targetSeq := c.standardReplay.targetSeq
	pivotSeq := c.standardReplay.pivotSeq
	preparedTailSeq := c.standardReplay.collectSeq
	generation := c.standardReplay.generation
	pivotHash := c.standardReplay.pivotHash
	pivotStartedAt := c.standardReplay.pivotStartedAt
	c.acquisitionMu.Unlock()
	pivotStateRate := uint64(0)
	if pivot := c.fetchTracker.Find(pivotHash); pivot != nil && !pivotStartedAt.IsZero() {
		if elapsed := time.Since(pivotStartedAt); elapsed > 0 {
			pivotStateRate = uint64(float64(pivot.Snapshot().StateUseful) / elapsed.Seconds())
		}
	}
	c.catchupMu.Lock()
	trustedHeadSeq := uint32(0)
	if c.catchup.source == catchupSourceQuorum {
		trustedHeadSeq = c.catchup.seq
	}
	c.catchupMu.Unlock()
	return FastSyncMetrics{
		CompletionRecheckAccepted:            c.completionRecheckAccepted.Load(),
		CompletionRecheckRejectedNoEvidence:  c.completionRecheckRejectedNoEvidence.Load(),
		CompletionRecheckRejectedBelowQuorum: c.completionRecheckRejectedBelowQuorum.Load(),
		CompletionRecheckRejectedUnavailable: c.completionRecheckRejectedUnavailable.Load(),
		TargetSuperseded:                     c.targetSuperseded.Load(),
		ObsoleteAcquisitionCompleted:         c.obsoleteAcquisitionCompleted.Load(),
		ReplayPipelineRequested:              c.replayPipelineRequested.Load(),
		ReplayPipelineReady:                  c.replayPipelineReady.Load(),
		ReplayPipelineApplied:                c.replayPipelineApplied.Load(),
		ReplayPipelineDiscarded:              c.replayPipelineDiscarded.Load(),
		ReplayPipelineRetried:                c.replayPipelineRetried.Load(),
		ReplayPipelineFallbacks:              c.replayPipelineFallbacks.Load(),
		ReplayPipelineBackpressureEvents:     c.replayPipelineBackpressureEvents.Load(),
		ReplayPipelineRetargetFailures:       c.replayPipelineRetargetFailures.Load(),
		ReplayPipelineAcquireUs:              c.replayPipelineAcquireUs.Load(),
		ReplayPipelineReadyWaitUs:            c.replayPipelineReadyWaitUs.Load(),
		ReplayPipelineApplyUs:                c.replayPipelineApplyUs.Load(),
		ReplayPipelinePersistUs:              c.replayPipelinePersistUs.Load(),
		ReplayPipelineWindow:                 standardReplayPipelineWindow,
		ReplayPipelinePreparedLimit:          standardReplayPreparedLimit,
		ReplayPipelineDepth:                  uint32(depth),
		ReplayPipelineReadyDepth:             uint32(readyDepth),
		ReplayPipelinePivotSeq:               pivotSeq,
		ReplayPipelinePreparedTailSeq:        preparedTailSeq,
		ReplayPipelineTrustedHeadSeq:         trustedHeadSeq,
		ReplayPipelineGeneration:             generation,
		ReplayPipelinePivotStateNodesPerSec:  pivotStateRate,
		ReplayPipelineHeadSeq:                headSeq,
		ReplayPipelineTargetSeq:              targetSeq,
		ReplayPipelineHeadBlockedUs:          blockedUs,
	}
}

func (c *catchupReplayCoordinator) recordCompletionRecheck(result validationRecheckResult) {
	switch result {
	case validationRecheckAccepted:
		c.completionRecheckAccepted.Add(1)
	case validationRecheckNoEvidence:
		c.completionRecheckRejectedNoEvidence.Add(1)
	case validationRecheckBelowQuorum:
		c.completionRecheckRejectedBelowQuorum.Add(1)
	case validationRecheckUnavailable:
		c.completionRecheckRejectedUnavailable.Add(1)
	}
}

// ClearFetchInfo resets the acquisition counters and recent-failure history,
// backing fetch_info's `clear` param.
func (c *catchupReplayCoordinator) ClearFetchInfo() {
	if c.stoppedForShutdown() {
		return
	}
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	ledgers := c.fetchTracker.Clear()
	retirement := c.cancelStandardReplayPipelineLocked("fetch_info_clear")
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	c.retireLegacyAcquisitions(ledgers)
	c.retireStandardReplay(retirement)
}

func (c *catchupReplayCoordinator) retireLegacyAcquisitions(ledgers []*inbound.Ledger) {
	for _, ledger := range ledgers {
		c.restoreReplayFallback(ledger)
		if lane := c.currentAcquisitionWork(); lane != nil {
			lane.cancelLedger(ledger)
		}
		c.retireAcquisitionStore(c.lifecycleContext(), ledger)
	}
}

// RequestLedger triggers (or joins) a generic acquisition of a ledger from
// peers, backing the ledger_request RPC. When hash is zero the target is
// resolved from the validated ledger's skip list, and a ReasonGeneric
// acquisition is started (or the in-flight one reused). started=true while
// an acquisition is in flight; (nil,false,false) when the target can't be
// resolved or no peer is available.
//
// reference distinguishes the two acquiring shapes: false when the
// snapshot is the target ledger itself; true when it is a 256-aligned
// reference ledger being fetched only to learn the target's hash.
//
// Safe to call from an RPC goroutine: the registry and each acquisition guard
// their own state.
func (c *catchupReplayCoordinator) RequestLedger(hash [32]byte, seq uint32) (acquiring map[string]any, started, reference bool) {
	if c.stoppedForShutdown() {
		return nil, false, false
	}
	// Don't acquire history online-delete has reclaimed: rippled's
	// LedgerMaster::shouldAcquire refuses to fetch a missing ledger below
	// minimumOnline. Re-fetching it would only feed the rotator another
	// delete. Forward catch-up / validation acquisitions are above the
	// validated tip (≥ floor) so they never hit this gate.
	if seq != 0 && c.belowFloor(seq) {
		c.logger.Debug("ledger_request declined: below online-delete floor",
			"seq", seq, "floor", c.floor.MinimumOnline())
		return nil, false, false
	}
	if hash == ([32]byte{}) {
		if seq == 0 {
			return nil, false, false
		}
		svc := c.adaptor.LedgerService()
		if svc == nil {
			return nil, false, false
		}
		vl := svc.GetValidatedLedger()
		if vl == nil {
			return nil, false, false
		}
		h, ok, err := vl.HashOfSeq(seq)
		if err != nil {
			return nil, false, false
		}
		if !ok {
			// seq is past the rolling window and not 256-aligned, so its hash
			// isn't directly in the validated ledger. Resolve it through a
			// 256-aligned reference ledger whose hash IS enshrined in the skip
			// list.
			refIndex := getCandidateLedger(seq)
			refHash, refOK, err := vl.HashOfSeq(refIndex)
			if err != nil || !refOK {
				return nil, false, false
			}
			refLedger, err := svc.GetLedgerByHash(refHash)
			if err != nil || refLedger == nil {
				// We lack the reference ledger needed to learn the target's
				// hash — acquire it and report it as the in-flight reference.
				if snap, ok := c.startGenericAcquisition(refHash, refIndex); ok {
					return snap, true, true
				}
				return nil, false, false
			}
			h, ok, err = refLedger.HashOfSeq(seq)
			if err != nil || !ok {
				return nil, false, false
			}
		}
		hash = h
	}

	if snap, ok := c.startGenericAcquisition(hash, seq); ok {
		return snap, true, false
	}
	return nil, false, false
}

// startGenericAcquisition begins (or joins) a ReasonGeneric acquisition for
// hash, issuing a base fetch from a selected peer only when it creates a fresh
// one. Returns the acquisition snapshot, or ok=false when no peer is available
// before creation. If the selected peer disconnects during the initial request,
// the acquisition remains active so maintenance can select a replacement. The
// fetchTracker's GetOrCreate is atomic, so a concurrent consensus catch-up
// arming the same hash is joined rather than duplicated.
func (c *catchupReplayCoordinator) startGenericAcquisition(hash [32]byte, seq uint32) (map[string]any, bool) {
	if c.stoppedForShutdown() {
		return nil, false
	}
	if svc := c.adaptor.LedgerService(); svc != nil {
		status := svc.ReplayFaultStatus()
		if status.Blocked && (status.Fault == nil || hash == status.Fault.ParentHash || hash == status.Fault.TargetHash) {
			return nil, false
		}
	}
	if il, _ := c.fetchTracker.GetOrCreateWithSequence(hash, seq, func() *inbound.Ledger { return nil }); il != nil {
		return inbound.AcquisitionJSON(il.Snapshot()), true
	}

	peerID, ok := c.selectAcquisitionPeer(seq)
	if !ok {
		return nil, false
	}

	il, created := c.fetchTracker.GetOrCreateWithSequence(hash, seq, func() *inbound.Ledger {
		return inbound.NewGeneric(hash, seq, peerID, c.logger, c.acquisitionOpts()...)
	})
	if il == nil {
		return nil, false
	}
	if created {
		c.logger.Info("starting ledger acquisition (generic, ledger_request)",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
		)
		c.requestLedgerBase(il, peerID, "ledger_request: failed to request ledger base")
	}
	return inbound.AcquisitionJSON(il.Snapshot()), true
}

// getCandidateLedger rounds seq up to the next multiple of 256 — the nearest
// ancestor whose hash is enshrined in the historical skip list and is therefore
// easy to resolve, then close enough (within 256) to hold seq's hash in its own
// rolling list.
func getCandidateLedger(seq uint32) uint32 {
	return (seq + 255) &^ 255
}

// selectAcquisitionPeer picks a connected peer to fetch a ledger from,
// preferring one whose reported ledger is at or beyond the target sequence
// (and therefore likely to hold it). When seq is unknown (0) or no peer is far
// enough along, it falls back to any connected peer. Returns (0,false) when no
// peer has reported a ledger state.
func (c *catchupReplayCoordinator) resolveAcquisitionPeer(seq uint32, preferred uint64) (uint64, bool) {
	if preferred != 0 && (c.peerSessions == nil || c.peerSessions.IsPeerConnected(peermanagement.PeerID(preferred))) {
		return preferred, true
	}
	return c.selectAcquisitionPeer(seq)
}

func (c *catchupReplayCoordinator) selectAcquisitionPeer(seq uint32) (uint64, bool) {
	return c.selectAcquisitionPeerExcluding(seq, nil)
}

func (c *catchupReplayCoordinator) selectAcquisitionPeerExcluding(seq uint32, excluded map[uint64]struct{}) (uint64, bool) {
	c.peersMu.RLock()
	type candidate struct {
		id  uint64
		seq uint32
	}
	candidates := make([]candidate, 0, len(c.peerStates))
	for pid, st := range c.peerStates {
		candidates = append(candidates, candidate{id: uint64(pid), seq: st.LedgerSeq})
	}
	c.peersMu.RUnlock()

	var fallback uint64
	var haveFallback bool
	for _, peer := range candidates {
		if _, skip := excluded[peer.id]; skip {
			continue
		}
		if c.peerSessions != nil && !c.peerSessions.IsPeerConnected(peermanagement.PeerID(peer.id)) {
			continue
		}
		if !haveFallback {
			fallback, haveFallback = peer.id, true
		}
		if seq == 0 || peer.seq >= seq {
			return peer.id, true
		}
	}
	return fallback, haveFallback
}

func (c *catchupReplayCoordinator) handleReplayDeltaResponse(msg *peermanagement.InboundMessage) {
	if c.stoppedForShutdown() {
		return
	}
	decoded, err := message.Decode(message.TypeReplayDeltaResponse, msg.Payload)
	if err != nil {
		c.logger.Debug("failed to decode replay delta response", "error", err, "peer", msg.PeerID)
		c.acquisition.IncPeerBadData(uint64(msg.PeerID), "replay-delta-resp-decode")
		return
	}
	resp, ok := decoded.(*message.ReplayDeltaResponse)
	if !ok || resp == nil {
		return
	}
	if resp.HasError() && !msg.SelectPeerCharge(resource.FeeInvalidData(), "replay-delta-verify") {
		c.acquisition.IncPeerBadData(uint64(msg.PeerID), "replay-delta-verify")
	}

	rd, err := c.replayer.HandleResponseFrom(uint64(msg.PeerID), resp)
	if c.stoppedForShutdown() {
		return
	}
	if errors.Is(err, inbound.ErrNoMatchingAcquisition) {
		c.logger.Debug("replay delta response with no matching acquisition",
			"peer", msg.PeerID)
		return
	}
	if errors.Is(err, inbound.ErrUnexpectedReplayPeer) {
		hash := rd.Hash()
		if resp.HasError() && isReplayAvailabilityError(resp.Error) &&
			c.isReplayAvailabilityPeer(hash, uint64(msg.PeerID)) {
			c.logger.Debug("ignoring delayed replay availability response",
				"peer", msg.PeerID,
				"hash", fmt.Sprintf("%x", hash[:8]),
			)
			return
		}
		c.logger.Warn("replay delta response from unexpected peer",
			"peer", msg.PeerID,
			"expected_peer", rd.PeerID(),
			"hash", fmt.Sprintf("%x", hash[:8]),
		)
		if !resp.HasError() {
			c.acquisition.IncPeerBadData(uint64(msg.PeerID), "replay-delta-peer")
		}
		return
	}
	if err != nil {
		// Verification failed. rd is still registered in the Replayer so
		// we can read its provenance before abandoning the slot.
		seq := rd.Seq()
		hash := rd.Hash()
		peerID := uint64(msg.PeerID)
		availability := resp.HasError() && isReplayAvailabilityError(resp.Error)
		parent := rd.Parent()
		triedPeers := rd.TriedPeers()
		c.acquisitionMu.Lock()
		if !availability {
			c.requireReplayFullStateLocked(seq, hash)
		}
		c.replayer.Abandon(hash)
		c.acquisitionMu.Unlock()
		if availability {
			c.logger.Warn("replay delta unavailable; trying recovery fallback",
				"seq", seq,
				"hash", fmt.Sprintf("%x", hash[:8]),
				"peer", peerID,
				"reply_error", resp.Error,
			)
			c.fallbackReplayAvailability(seq, hash, peerID, parent, triedPeers)
			return
		}
		c.logger.Warn("replay delta verification failed; falling back to legacy",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		routeMismatch := errors.Is(err, inbound.ErrReplayParentMismatch) ||
			errors.Is(err, inbound.ErrReplaySequenceMismatch)
		if !routeMismatch && !resp.HasError() {
			c.acquisition.IncPeerBadData(peerID, "replay-delta-verify")
		}
		c.fallbackReplayAcquisition(seq, hash, peerID)
		return
	}

	// GotResponse verified the header hash and the tx-map root. Apply
	// re-derives the post-state by replaying every tx through the
	// engine against a mutable copy of the parent's state, then
	// verifies the resulting AccountHash matches the target header —
	// the only proof we have that our engine produced the right state.
	// Without this step the adopted ledger would carry the parent's
	// stale state map, breaking consensus on the next round.
	parent := rd.Parent()
	engineCfg := c.adaptor.EngineConfigForReplay(parent)
	derived, err := c.adaptor.LedgerService().ApplyReplay(c.lifecycleContext(), rd, engineCfg, c.replayTargetAuthenticated(rd.TargetHeader()))
	if err != nil {
		seq := rd.Seq()
		hash := rd.Hash()
		peerID := rd.PeerID()
		c.acquisitionMu.Lock()
		c.requireReplayFullStateLocked(seq, hash)
		c.replayer.Abandon(hash)
		c.acquisitionMu.Unlock()
		// The header and transaction tree passed verification; diagnose local
		// state and execution before attributing the failure to a peer.
		c.logger.Error("replay delta apply failed; validator duties blocked",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		c.fallbackReplayAcquisition(seq, hash, peerID)
		return
	}
	c.replayer.Complete(rd.Hash())
	c.clearReplayAvailabilityRetry(rd.Hash())
	if err := c.adoptVerifiedLedger(derived); err != nil {
		c.logger.Warn("failed to store replay-delta ledger", "error", err)
	}
}

func (r *Router) handleReplayDeltaResponse(msg *peermanagement.InboundMessage) {
	if r.catchupReplay != nil {
		r.catchupReplay.handleReplayDeltaResponse(msg)
	}
}

// adoptVerifiedLedger stores a ledger reconstructed from a verified replay
// delta until consensus selects it as the canonical frontier.
func (c *catchupReplayCoordinator) adoptVerifiedLedger(l *ledger.Ledger) error {
	if l == nil || c.stoppedForShutdown() {
		return context.Canceled
	}
	c.replayCommitMu.Lock()
	if c.stoppedForShutdown() {
		c.replayCommitMu.Unlock()
		return context.Canceled
	}
	storedHeader, initialCandidate, err := c.storeVerifiedLedgerLocked(l)
	c.replayCommitMu.Unlock()
	if err != nil {
		return err
	}
	c.logger.Info("acquired ledger via replay delta",
		"seq", storedHeader.LedgerIndex,
		"hash", fmt.Sprintf("%x", storedHeader.Hash[:8]),
		"initial_candidate", initialCandidate,
	)
	c.completeStoredConsensusRecovery(storedHeader.LedgerIndex, storedHeader.Hash, storedHeader.ParentHash, initialCandidate)
	return nil
}

func (c *catchupReplayCoordinator) storeVerifiedLedger(l *ledger.Ledger) (header.LedgerHeader, bool, error) {
	if c.stoppedForShutdown() {
		return header.LedgerHeader{}, false, context.Canceled
	}
	c.replayCommitMu.Lock()
	defer c.replayCommitMu.Unlock()
	if c.stoppedForShutdown() {
		return header.LedgerHeader{}, false, context.Canceled
	}
	return c.storeVerifiedLedgerLocked(l)
}

// storeVerifiedLedgerLocked publishes a verified ledger while replayCommitMu
// is held. Standard replay calls this locked form after rechecking its active
// generation; other replay paths use storeVerifiedLedger so publication is
// serialized at the coordinator boundary.
func (c *catchupReplayCoordinator) storeVerifiedLedgerLocked(l *ledger.Ledger) (header.LedgerHeader, bool, error) {
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return header.LedgerHeader{}, false, errors.New("no ledger service")
	}
	hdr := l.Header()
	stateMap, err := l.StateMapSnapshot()
	if err != nil {
		return header.LedgerHeader{}, false, fmt.Errorf("snapshot state map: %w", err)
	}
	// Pass the verified tx map through so the stored ledger carries
	// real transactions — without this, tx/tx_history/account_tx RPCs
	// can't answer for replay-delta ledgers and we can't
	// re-serve the replay-delta to other peers.
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return header.LedgerHeader{}, false, fmt.Errorf("snapshot tx map: %w", err)
	}
	initialCandidate, err := svc.BootstrapLedgerWithState(c.lifecycleContext(), &hdr, stateMap, txMap)
	if err != nil {
		return header.LedgerHeader{}, false, fmt.Errorf("store replay-delta ledger: %w", err)
	}
	return hdr, initialCandidate, nil
}

func (c *catchupReplayCoordinator) completeStoredConsensusRecovery(seq uint32, hash, parentHash [32]byte, initialCandidate bool) bool {
	if c.stoppedForShutdown() {
		return false
	}
	if c.replayFaultBlocked() {
		return false
	}
	c.acquisitionMu.Lock()
	delete(c.replayFallbackRequired, hash)
	c.acquisitionMu.Unlock()
	_, result := c.adaptor.recheckFullyValidated(seq, hash)
	c.recordCompletionRecheck(result)
	obsolete := c.isObsoleteRecoveryCompletion(seq, hash)
	if result == validationRecheckAccepted {
		c.recordAcquiredSeqHash(seq, hash, parentHash)
		c.promoteCompletedLedger(seq, hash)
	} else if !obsolete {
		c.recordAcquiredSeqHash(seq, hash, parentHash)
	}
	if obsolete {
		c.obsoleteAcquisitionCompleted.Add(1)
		c.armConsensusCatchup()
		return false
	}
	if initialCandidate {
		accepted, rearm := c.tryInitialLedgerSwitch(seq, hash)
		if rearm {
			c.armConsensusCatchup()
		}
		return accepted
	}

	if !c.shouldSwitchConsensusLedger(seq, hash) {
		_, rearm := c.finishConsensusRecoveryStep(seq, hash)
		if rearm {
			c.armConsensusCatchup()
		}
		return false
	}

	accepted, rearm := c.tryConsensusLedgerSwitch(seq, hash)
	if rearm {
		c.armConsensusCatchup()
	}
	return accepted
}

func (c *catchupReplayCoordinator) isObsoleteRecoveryCompletion(seq uint32, hash [32]byte) bool {
	c.acquisitionMu.Lock()
	target := c.consensusRecovery.targetHash
	step := c.consensusRecovery.stepHash
	c.acquisitionMu.Unlock()
	if target != ([32]byte{}) {
		return target != hash && step != hash
	}
	frontier := c.credibleCatchupFrontier()
	if frontier.source == catchupSourcePeer {
		return frontier.seq == seq && frontier.hash != hash
	}
	return frontier.hash != ([32]byte{}) &&
		frontier.hash != hash && frontier.seq >= seq
}

func (c *catchupReplayCoordinator) promoteCompletedLedger(seq uint32, hash [32]byte) {
	if c.engine == nil {
		return
	}
	acceptable, err := c.engine.CanAcceptLedger(consensus.LedgerID(hash))
	if err != nil {
		c.logger.Debug("validated ledger acceptance check failed", "error", err, "seq", seq)
		return
	}
	if !acceptable {
		return
	}
	signTime, result := c.adaptor.recheckFullyValidated(seq, hash)
	if result != validationRecheckAccepted {
		return
	}
	if svc := c.adaptor.LedgerService(); svc != nil {
		svc.PromoteStoredValidatedLedgerAt(seq, hash, signTime)
	}
}

func (c *catchupReplayCoordinator) shouldSwitchConsensusLedger(seq uint32, hash [32]byte) bool {
	frontier := c.credibleCatchupFrontier()

	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()

	target := c.consensusRecovery.targetHash
	if target != ([32]byte{}) {
		return target == hash
	}
	if frontier.seq == seq && frontier.hash != ([32]byte{}) && frontier.hash != hash {
		return false
	}
	return !aheadByMoreThan(frontier.seq, seq, 1) && seq > c.lastHandoffSeq
}

func (c *catchupReplayCoordinator) tryConsensusLedgerSwitch(seq uint32, hash [32]byte) (accepted, rearm bool) {
	result := consensus.LedgerSwitchIrrelevant
	if c.engine != nil {
		var err error
		result, err = c.engine.TrySwitchToLedger(consensus.LedgerID(hash))
		if err != nil {
			c.logger.Debug("consensus ledger switch failed", "error", err, "seq", seq)
			result = consensus.LedgerSwitchIrrelevant
		}
	}
	if result != consensus.LedgerSwitchAccepted {
		c.retainConsensusLedgerSwitch(hash)
		return false, false
	}

	_, rearm = c.finishConsensusRecoveryStep(seq, hash)
	if c.adaptor.GetOperatingMode() < consensus.OpModeTracking {
		c.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
	return true, rearm
}

func (c *catchupReplayCoordinator) retainConsensusLedgerSwitch(hash [32]byte) {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()

	if c.consensusRecovery.targetHash == ([32]byte{}) {
		c.consensusRecovery.targetHash = hash
	}
}

func (c *catchupReplayCoordinator) finishConsensusRecoveryStep(seq uint32, hash [32]byte) (notify, rearm bool) {
	frontierSeq := c.credibleCatchupFrontier().seq

	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()

	target := c.consensusRecovery.targetHash
	if target != ([32]byte{}) {
		if c.consensusRecovery.stepHash == hash {
			c.consensusRecovery.stepHash = [32]byte{}
		}
		if target != hash {
			currentAnchorUsable := c.consensusRecovery.anchorHash != ([32]byte{}) &&
				c.recoveryAnchorReachesTarget(
					c.consensusRecovery.anchorSeq,
					c.consensusRecovery.anchorHash,
					target,
				)
			if c.recoveryAnchorReachesTarget(seq, hash, target) &&
				(!currentAnchorUsable || seq > c.consensusRecovery.anchorSeq) {
				c.consensusRecovery.anchorSeq = seq
				c.consensusRecovery.anchorHash = hash
			}
			return false, true
		}
		c.consensusRecovery.anchorSeq = seq
		c.consensusRecovery.anchorHash = hash
		c.consensusRecovery.targetHash = [32]byte{}
		c.consensusRecovery.stepHash = [32]byte{}
		c.recordConsensusHandoffLocked(seq)
		return true, false
	}

	if aheadByMoreThan(frontierSeq, seq, 1) {
		return false, true
	}
	if seq <= c.lastHandoffSeq {
		return false, aheadByMoreThan(frontierSeq, seq, 0)
	}
	c.lastHandoffSeq = seq
	return true, false
}

func (c *catchupReplayCoordinator) tryInitialLedgerSwitch(seq uint32, hash [32]byte) (accepted, rearm bool) {
	result := consensus.LedgerSwitchIrrelevant
	if c.engine != nil {
		var err error
		result, err = c.engine.TrySwitchToLedger(consensus.LedgerID(hash))
		if err != nil {
			c.logger.Debug("initial ledger switch failed", "error", err, "seq", seq)
			result = consensus.LedgerSwitchIrrelevant
		}
	}

	rearm = c.finishInitialLedgerSwitch(seq, hash, result)
	if result == consensus.LedgerSwitchAccepted {
		if c.adaptor.GetOperatingMode() < consensus.OpModeTracking {
			c.adaptor.SetOperatingMode(consensus.OpModeTracking)
		}
		return true, rearm
	}
	return false, rearm
}

func (c *catchupReplayCoordinator) finishInitialLedgerSwitch(
	seq uint32,
	hash [32]byte,
	result consensus.LedgerSwitchResult,
) bool {
	frontierSeq := c.credibleCatchupFrontier().seq

	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()

	if c.consensusRecovery.stepHash == hash {
		c.consensusRecovery.stepHash = [32]byte{}
	}
	target := c.consensusRecovery.targetHash
	if result == consensus.LedgerSwitchBusy {
		if target == ([32]byte{}) {
			c.consensusRecovery.targetHash = hash
		}
		return false
	}

	if target == hash {
		c.consensusRecovery.anchorSeq = seq
		c.consensusRecovery.anchorHash = hash
		c.consensusRecovery.targetHash = [32]byte{}
	}
	if target != ([32]byte{}) && target != hash &&
		c.recoveryAnchorReachesTarget(seq, hash, target) {
		currentAnchorUsable := c.consensusRecovery.anchorHash != ([32]byte{}) &&
			c.recoveryAnchorReachesTarget(
				c.consensusRecovery.anchorSeq,
				c.consensusRecovery.anchorHash,
				target,
			)
		if !currentAnchorUsable || seq > c.consensusRecovery.anchorSeq {
			c.consensusRecovery.anchorSeq = seq
			c.consensusRecovery.anchorHash = hash
		}
	}

	if result == consensus.LedgerSwitchAccepted {
		c.recordConsensusHandoffLocked(seq)
	}
	return (target != ([32]byte{}) && target != hash) ||
		aheadByMoreThan(frontierSeq, seq, 0)
}

func (c *catchupReplayCoordinator) recordConsensusHandoffLocked(seq uint32) {
	if seq > c.lastHandoffSeq {
		c.lastHandoffSeq = seq
	}
}

func (c *catchupReplayCoordinator) failConsensusRecoveryStep(hash [32]byte) {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	if c.consensusRecovery.stepHash != hash {
		return
	}
	if c.consensusRecovery.targetHash == hash {
		c.consensusRecovery = consensusRecovery{
			anchorHash: c.consensusRecovery.anchorHash,
			anchorSeq:  c.consensusRecovery.anchorSeq,
		}
		return
	}
	c.consensusRecovery.stepHash = [32]byte{}
}

// maybeAcquireFromValidation arms inbound acquisition for a ledger attested
// by a single TRUSTED validation, before the hash reaches quorum. It is the
// non-quorum counterpart to armValidatedLedgerAcquisition, acquiring the
// ledger on EVERY trusted current validation when we don't already have it
// — quorum is not required. With only the quorum-gated path, a node below
// quorum (3 of 4 trusted validators on the network tip) never fetched that
// tip and stalled in the wrongLedger chase loop.
//
// This only ACQUIRES. Advancing validatedLedger still flows through the
// quorum gate (onFullyValidated → SetValidatedLedger), so a sub-quorum
// fetch cannot move our validated tip and carries no state-divergence
// risk; it just makes the ledger locally available so the node can rejoin
// consensus on the network's chain instead of holding no position.
func (c *catchupReplayCoordinator) maybeAcquireFromValidation(v *consensus.Validation, originPeer uint64) {
	if v == nil || v.LedgerSeq == 0 {
		return
	}
	// Only trusted validators steer chain selection.
	if !c.adaptor.IsTrusted(v.NodeID) {
		return
	}
	// Record the hash for this seq regardless of the acquire gate below: the
	// forward-delta decision needs it for closed+1 and for our own closed seq
	// (same-branch check). Validations carry no parent hash.
	c.recordValidationSeqHash(v.LedgerSeq, [32]byte(v.LedgerID))

	svc := c.adaptor.LedgerService()
	if svc == nil {
		return
	}
	ourSeq := svc.GetClosedLedgerIndex()
	if c.adaptor.GetOperatingMode() == consensus.OpModeFull && aheadByMoreThan(v.LedgerSeq, ourSeq, 2) {
		c.adaptor.SetOperatingMode(consensus.OpModeConnected)
		c.logger.Warn("trusted validation is ahead; leaving Full mode",
			"our_seq", ourSeq,
			"validated_seq", v.LedgerSeq,
		)
	}
	// Gate on the VALIDATED tip, never the closed/built tip. The equal-sequence
	// exception retains the validating peer only while a fast-loaded ledger is
	// provisional; promotion still requires quorum.
	validated := svc.GetValidatedLedgerIndex()
	if v.LedgerSeq < validated ||
		(v.LedgerSeq == validated && !svc.IsFastLoadProvisional()) {
		return
	}
	hash := [32]byte(v.LedgerID)
	c.recordValidationCatchupTarget(
		v.LedgerSeq,
		hash,
		originPeer,
		catchupSourceValidation,
	)
	// Already have it (built or adopted) — nothing to fetch.
	if l, err := svc.GetLedgerByHash(hash); err == nil && l != nil {
		return
	}
	// A trusted tip AT OR BELOW our closed tip on a chain we don't hold is a
	// consensus-island signature: we ran ahead on our own branch while the
	// majority validated another. The forward funnel below never fetches behind
	// closed, so acquire it directly — without it the validation trie can never
	// place the majority branch (rippled RCLValidationsAdaptor::acquire).
	if v.LedgerSeq <= svc.GetClosedLedgerIndex() {
		if c.isBuildingLedger(v.LedgerSeq) {
			return
		}
		c.startLedgerAcquisition(v.LedgerSeq, hash, originPeer)
		return
	}
	c.ensureValidationCatchupAcquisition(v.LedgerSeq, hash, originPeer)
}

func (c *catchupReplayCoordinator) armValidatedLedgerAcquisition(seq uint32, hash [32]byte) {
	defer func() {
		if rv := recover(); rv != nil {
			c.logger.Error("armValidatedLedgerAcquisition panic recovered",
				"seq", seq,
				"hash", fmt.Sprintf("%x", hash[:8]),
				"panic", rv,
			)
		}
	}()
	if seq == 0 {
		return
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return
	}
	// Ordinary validated history stays monotonic. The equal-sequence exception
	// is limited to replacing a provisional fast-loaded ledger after quorum;
	// gating on the closed-ledger index would also swallow recovery from a
	// private chain whose closed frontier ran ahead of its validated frontier.
	validated := svc.GetValidatedLedgerIndex()
	if seq < validated ||
		(seq == validated && !svc.IsFastLoadProvisional()) {
		return
	}
	if held, err := svc.GetLedgerByHash(hash); err == nil && held != nil && held.Sequence() == seq {
		c.acquisitionMu.Lock()
		c.consensusRecovery.targetHash = hash
		c.acquisitionMu.Unlock()
		c.runLifecycleTask(func(context.Context) {
			c.promoteCompletedLedger(seq, hash)
			c.armConsensusCatchup()
		})
		return
	}

	// Walk peers in ID order so the chosen peer (and the emitted log)
	// is reproducible across runs. Any peer with the hash can serve it.
	c.peersMu.RLock()
	peerIDs := make([]peermanagement.PeerID, 0, len(c.peerStates))
	for pid := range c.peerStates {
		peerIDs = append(peerIDs, pid)
	}
	slices.Sort(peerIDs)
	var (
		preferredPeerID uint64
		fallbackPeerID  uint64
	)
	for _, pid := range peerIDs {
		st := c.peerStates[pid]
		if fallbackPeerID == 0 {
			fallbackPeerID = uint64(pid)
		}
		if st != nil && st.LedgerSeq >= seq {
			preferredPeerID = uint64(pid)
			break
		}
	}
	c.peersMu.RUnlock()
	if preferredPeerID == 0 {
		preferredPeerID = fallbackPeerID
	}
	if preferredPeerID == 0 {
		return
	}

	// Keep the newest target even when the speculative slots are full; completion
	// or maintenance will re-arm it when capacity becomes available.
	c.recordValidationCatchupTarget(
		seq,
		hash,
		preferredPeerID,
		catchupSourceQuorum,
	)
	if c.isBuildingLedger(seq) {
		return
	}
	c.logger.Info("arming acquisition for stashed validation",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"preferred_peer", preferredPeerID,
	)
	c.armCatchupTowardTargetWithPeer(preferredPeerID)
}

type buildingLedgerEngine interface {
	BuildingLedgerSeq() uint32
}

func (c *catchupReplayCoordinator) isBuildingLedger(seq uint32) bool {
	engine, ok := c.engine.(buildingLedgerEngine)
	return ok && seq != 0 && engine.BuildingLedgerSeq() == seq
}

func (c *catchupReplayCoordinator) onLedgerBuilt(seq uint32, hash [32]byte) {
	c.retireLocallySatisfiedLedger(seq, hash, "ledger_built")
	c.armCatchupTowardTarget()
}

// checkBehind decides what to do based on how far behind a peer
// reports. Two outcomes:
//
//   - peerSeq <= ourSeq+1: we're caught up. If still in Tracking and
//     our LCL hash matches peers' majority, transition to Full.
//     Otherwise stay in Tracking until network preference is established.
//   - peerSeq > ourSeq+1: we're behind by more than one ledger. If the
//     peer's tip is network-preferred, arm one acquisition. Subsequent
//     status changes chain acquisitions forward as we adopt each ledger
//     and ourSeq advances.
//
// Only one acquisition fires per call. A faster "range walk" that
// issues concurrent requests for every seq between ourLCL+1 and
// peerSeq would need the intermediate ledger hashes, which we don't
// know until each acquired header reveals its ParentHash; we rely on
// forward status gossip instead. Replayer already supports concurrent
// in-flight acquisitions, so switching to backward-walk later is a
// localized change in this function.
func (c *catchupReplayCoordinator) checkBehind(peerSeq uint32, peerHash [32]byte, peerID uint64) {
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return
	}

	ourSeq := svc.GetClosedLedgerIndex()
	networkSeq := ourSeq
	if validatedSeq := svc.GetValidatedLedgerIndex(); validatedSeq > networkSeq {
		networkSeq = validatedSeq
	}
	if validatedSeq := c.adaptor.networkValidatedSeq.Load(); validatedSeq > networkSeq {
		networkSeq = validatedSeq
	}
	frontier := c.credibleCatchupFrontier()
	if frontier.seq > networkSeq {
		networkSeq = frontier.seq
	}
	peerPreferred := c.peerLedgerIsPreferred(peerHash)
	if peerPreferred && peerSeq > networkSeq {
		networkSeq = peerSeq
	}

	// If we're caught up (gap ≤ 1) and not yet Full, transition to Full
	// only if our LCL hash matches what the majority of peers report.
	if !aheadByMoreThan(networkSeq, ourSeq, 1) {
		if c.adaptor.GetOperatingMode() == consensus.OpModeTracking {
			validatedSeq := svc.GetValidatedLedgerIndex()
			if !aheadByMoreThan(networkSeq, validatedSeq, 1) && c.ourLCLMatchesPeers() {
				c.logger.Info("caught up with network, transitioning to Full",
					"our_seq", ourSeq,
					"peer_seq", networkSeq,
				)
				c.adaptor.SetOperatingMode(consensus.OpModeFull)
			} else {
				c.logger.Info("caught up but validated LCL is not aligned, staying in Tracking",
					"our_seq", ourSeq,
					"validated_seq", validatedSeq,
					"peer_seq", networkSeq,
				)
			}
		}
		return
	}
	if !aheadByMoreThan(peerSeq, ourSeq, 1) {
		return
	}
	if !peerPreferred {
		return
	}

	c.logger.Info("behind network, driving catch-up toward peer tip",
		"our_seq", ourSeq,
		"peer_seq", peerSeq,
		"gap", peerSeq-ourSeq,
		"peer", peerID,
	)

	// Funnel through the bounded catch-up. Both acquisition paths install their
	// own state machines, so responses have a live consumer; a bare mtGET_LEDGER
	// broadcast would arrive with none and drop.
	c.ensureCatchupAcquisition(peerSeq, peerHash, peerID)
}

func aheadByMoreThan(seq, base, distance uint32) bool {
	return seq > base && seq-base > distance
}

func (c *catchupReplayCoordinator) catchupFrontierIsCredible(target catchupTarget) bool {
	if target.hash == ([32]byte{}) {
		return false
	}
	if target.source != catchupSourcePeer {
		return true
	}

	found := false
	c.peersMu.RLock()
	for _, state := range c.peerStates {
		if state != nil && state.LedgerSeq == target.seq && state.LedgerHash == target.hash {
			found = true
			break
		}
	}
	c.peersMu.RUnlock()
	return found && c.peerLedgerIsPreferred(target.hash)
}

func (c *catchupReplayCoordinator) credibleCatchupFrontier() catchupTarget {
	c.catchupMu.Lock()
	target := c.catchup
	c.catchupMu.Unlock()
	if !c.catchupFrontierIsCredible(target) {
		return catchupTarget{}
	}
	return target
}

// ourLCLMatchesPeers checks if our closed ledger hash matches what the
// majority of tracked peers report. Returns true if we have no peer data
// (to avoid blocking startup).
func (c *catchupReplayCoordinator) ourLCLMatchesPeers() bool {
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return true
	}
	closedLedger := svc.GetClosedLedger()
	if closedLedger == nil {
		return true
	}
	ourHash := closedLedger.Hash()
	ourSeq := svc.GetClosedLedgerIndex()

	c.peersMu.RLock()
	defer c.peersMu.RUnlock()

	if len(c.peerStates) == 0 {
		return true
	}

	matching := 0
	total := 0
	for _, ps := range c.peerStates {
		if ps.LedgerSeq == ourSeq {
			total++
			if ps.LedgerHash == ourHash {
				matching++
			}
		}
	}

	// If no peers at our seq, allow transition (they may have advanced)
	if total == 0 {
		return true
	}

	return matching > total/2
}

func (r *Router) handleLedgerData(msg *peermanagement.InboundMessage) bool {
	if r.catchupReplay != nil && r.catchupReplay.stoppedForShutdown() {
		return false
	}
	decoded, err := message.Decode(message.TypeLedgerData, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode ledger_data", "error", err, "peer", msg.PeerID)
		if !msg.SelectPeerCharge(resource.FeeMalformedRequest(), "ledger-data-decode") {
			r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-decode")
		}
		return false
	}
	ld, ok := decoded.(*message.LedgerData)
	if !ok {
		return false
	}
	if len(ld.LedgerHash) != 32 {
		r.logger.Warn("invalid ledger_data ledger hash", "peer", msg.PeerID, "length", len(ld.LedgerHash))
		if !msg.SelectPeerCharge(resource.FeeInvalidData(), "ledger-data-hash") {
			r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-hash")
		}
		return false
	}
	if ld.InfoType < message.LedgerInfoBase || ld.InfoType > message.LedgerInfoTsCandidate {
		r.logger.Warn("invalid ledger_data info type", "peer", msg.PeerID, "info_type", ld.InfoType)
		if !msg.SelectPeerCharge(resource.FeeInvalidData(), "ledger-data-type") {
			r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-type")
		}
		return false
	}
	if (ld.InfoType == message.LedgerInfoTsCandidate && ld.LedgerSeq != 0) ||
		(ld.InfoType != message.LedgerInfoTsCandidate && r.invalidFutureLedgerSequence(ld.LedgerSeq)) {
		r.logger.Warn("invalid ledger_data ledger sequence", "peer", msg.PeerID, "seq", ld.LedgerSeq)
		if !msg.SelectPeerCharge(resource.FeeInvalidData(), "ledger-data-sequence") {
			r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-sequence")
		}
		return false
	}
	if ld.HasError() &&
		(ld.Error < message.ReplyErrorNoLedger || ld.Error > message.ReplyErrorBadRequest) {
		r.logger.Warn("invalid ledger_data reply error", "peer", msg.PeerID, "error", ld.Error)
		if !msg.SelectPeerCharge(resource.FeeInvalidData(), "ledger-data-error") {
			r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-error")
		}
		return false
	}
	if ld.HasError() {
		r.logger.Warn("inbound ledger: peer returned reply error",
			"peer", msg.PeerID,
			"seq", ld.LedgerSeq,
			"info_type", ld.InfoType,
			"reply_error", ld.Error,
			"nodes", len(ld.Nodes),
		)
	}
	if err := inbound.ValidateReplyNodeCount(ld.Nodes); err != nil {
		r.logger.Warn("invalid ledger_data node count",
			"error", err,
			"peer", msg.PeerID,
			"seq", ld.LedgerSeq,
			"info_type", ld.InfoType,
			"reply_error", ld.Error,
			"nodes", len(ld.Nodes),
		)
		if !msg.SelectPeerCharge(resource.FeeInvalidData(), "ledger-data-count") {
			r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-count")
		}
		return false
	}
	if r.catchupReplay.handleHeaderDiscoveryReply(ld, uint64(msg.PeerID)) {
		return false
	}

	// A reply carrying a request_cookie answers a GetLedger we relayed on
	// another peer's behalf. Route it back to the original requester named
	// by the cookie and do not consume it locally. Mirrors rippled
	// onMessage(TMLedgerData).
	if ld.HasRequestCookie() {
		r.routeRelayedLedgerData(ld, msg.PeerID, msg)
		return false
	}

	var il *inbound.Ledger
	if len(ld.LedgerHash) == 32 {
		var h [32]byte
		copy(h[:], ld.LedgerHash)
		if r.catchupReplay != nil {
			il = r.catchupReplay.findAcquisition(h)
		}
	}

	r.logger.Debug("received ledger data",
		"peer", msg.PeerID,
		"seq", ld.LedgerSeq,
		"nodes", len(ld.Nodes),
		"itype", ld.InfoType,
		"has_inbound", il != nil,
	)

	// liTS_CANDIDATE response — feeds the engine via the tx-set path
	// (consensus-time only).
	if ld.InfoType == message.LedgerInfoTsCandidate {
		r.handleTxSetData(ld, uint64(msg.PeerID))
		return false
	}

	if il != nil {
		if consumed, transferred := r.catchupReplay.handleInboundLedgerDataOwned(il, ld, uint64(msg.PeerID), msg); consumed {
			return transferred
		}
	}
	if il == nil && ld.InfoType == message.LedgerInfoAsNode {
		r.cacheStaleStateNodes(ld)
	}
	return false
}

func (c *catchupReplayCoordinator) cacheStaleStateNodes(ld *message.LedgerData) {
	if c.stoppedForShutdown() {
		return
	}
	now := time.Now()
	for _, node := range ld.Nodes {
		if _, err := node.SHAMapNodeID(); err != nil {
			return
		}
		entry, err := shamap.FlushEntryFromWire(node.NodeData, ld.LedgerSeq, shamap.TypeState)
		if err != nil {
			return
		}
		c.fetchPacks.add(entry.Hash, entry.Data, now)
	}
}

func (r *Router) cacheStaleStateNodes(ld *message.LedgerData) {
	if r.catchupReplay != nil {
		r.catchupReplay.cacheStaleStateNodes(ld)
	}
}

// handleInboundLedgerData feeds LedgerData to the given InboundLedger
// acquisition (already matched by hash in handleLedgerData). Returns true if
// the data was consumed by the acquisition.
func (r *Router) handleInboundLedgerData(il *inbound.Ledger, ld *message.LedgerData, peerID uint64) bool {
	consumed, _ := r.catchupReplay.handleInboundLedgerDataOwned(il, ld, peerID, nil)
	return consumed
}

func (c *catchupReplayCoordinator) handleInboundLedgerDataOwned(
	il *inbound.Ledger,
	ld *message.LedgerData,
	peerID uint64,
	owner *peermanagement.InboundMessage,
) (bool, bool) {
	if c.stoppedForShutdown() {
		return false, false
	}
	if il == nil {
		return false, false
	}
	if lane := c.currentAcquisitionWork(); lane != nil {
		switch ld.InfoType {
		case message.LedgerInfoBase, message.LedgerInfoAsNode, message.LedgerInfoTxNode:
			if lane.submit(il, acquisitionWorkEvent{
				kind: acquisitionWorkData, data: ld, owner: owner, peerID: peerID,
			}) {
				return true, owner != nil
			}
			c.logger.Warn("inbound ledger reply deferred: acquisition worker saturated",
				"peer", peerID, "seq", il.Seq(), "info_type", ld.InfoType)
			return true, false
		}
	}

	switch ld.InfoType {
	case message.LedgerInfoBase:
		if len(ld.Nodes) < 2 {
			c.logger.Debug("inbound ledger: response has < 2 nodes", "nodes", len(ld.Nodes))
			c.discardFailedInboundAcquisitionWithSnapshot(
				il,
				il.Snapshot(),
				fmt.Errorf("inbound ledger base response has %d nodes; expected at least 2", len(ld.Nodes)),
			)
			return true, false
		}
		if err := il.GotBase(ld.Nodes); err != nil {
			c.logger.Warn("inbound ledger: GotBase failed", "error", err)
			if errors.Is(err, inbound.ErrHeaderRejected) {
				c.failInboundAcquisitionWithSnapshot(il, il.Snapshot(), err)
			} else {
				c.acquisition.IncPeerBadData(peerID, "ledger-data-base")
				c.discardFailedInboundAcquisitionWithSnapshot(il, il.Snapshot(), err)
			}
			return true, false
		}
		c.promoteResolvedFrozenPivot(il, peerID)

		if il.IsComplete() {
			c.completeInboundLedger(il)
			return true, false
		}

		// Re-request the missing state and transaction nodes from the peer
		// that answered, mirroring rippled trigger(peer) on a reply.
		c.requestMissingAcquisitionNodes(il, peerID)
		return true, false

	case message.LedgerInfoAsNode:
		il.ReleaseMissingPeer(peerID)
		useful, err := il.GotStateNodesUseful(ld.Nodes)
		if err != nil {
			c.logger.Warn("inbound ledger: GotStateNodes failed", "error", err)
			if errors.Is(err, inbound.ErrInvalidPeerNode) {
				c.acquisition.IncPeerBadData(peerID, "ledger-data-state")
			}
			return true, false
		}

		if il.IsComplete() {
			c.completeInboundLedger(il)
			return true, false
		}

		if useful > 0 {
			c.requestMissingAcquisitionNodes(il, peerID)
		}
		return true, false

	case message.LedgerInfoTxNode:
		il.ReleaseMissingPeer(peerID)
		useful, err := il.GotTransactionNodesUseful(ld.Nodes)
		if err != nil {
			c.logger.Warn("inbound ledger: GotTransactionNodes failed", "error", err)
			if errors.Is(err, inbound.ErrInvalidPeerNode) {
				c.acquisition.IncPeerBadData(peerID, "ledger-data-tx")
			}
			return true, false
		}

		if il.IsComplete() {
			c.completeInboundLedger(il)
			return true, false
		}

		if useful > 0 {
			c.requestMissingAcquisitionNodes(il, peerID)
		}
		return true, false
	}

	return false, false
}

// requestMissingAcquisitionNodes asks for the acquisition's outstanding nodes,
// finishing account state before requesting the transaction tree.
// When target is non-zero (the reply path) the re-request goes to just that
// peer — the one that answered — mirroring rippled's trigger(peer) on a reply;
// when target is zero (the no-progress timeout path) it fans out to every peer
// in the (possibly broadened) set, mirroring trigger(nullptr). Each call is a
// no-op for a tree already complete.
//
// Once the acquisition has timed out at least once we mark the requests
// indirect (query_type=qtINDIRECT) so peers relay them on our behalf,
// mirroring rippled's InboundLedger::trigger timeouts_ != 0 gate.
func (c *catchupReplayCoordinator) requestMissingAcquisitionNodes(il *inbound.Ledger, target uint64) {
	if target != 0 {
		requests, complete, err := il.CollectMissingReplyRequestsContext(context.Background(), []uint64{target})
		if err != nil {
			c.logger.Warn("inbound ledger: failed to collect missing nodes", "error", err)
			return
		}
		if complete {
			c.completeInboundLedger(il)
			return
		}
		released := 0
		for _, request := range requests {
			if c.sendMissingReplyRequest(il, request) {
				released++
			}
		}
		c.retryMissingAcquisitionNodes(il, missingNodeRetry{}, released)
		return
	}

	stateIDs, txIDs, complete, err := il.CollectMissingRequestContext(context.Background(), false)
	if err != nil {
		c.logger.Warn("inbound ledger: failed to collect missing nodes", "error", err)
		return
	}
	if complete {
		c.completeInboundLedger(il)
		return
	}
	if len(stateIDs) == 0 && len(txIDs) == 0 {
		return
	}
	peers := il.Peers()
	retry := c.sendMissingAcquisitionNodes(il, peers, stateIDs, txIDs, 0)
	c.retryMissingAcquisitionNodes(il, retry, 0)
}

func (c *catchupReplayCoordinator) requestMissingAcquisitionNodesFromAddedPeer(il *inbound.Ledger, peerID uint64) {
	requests, complete, err := il.CollectMissingAddedRequestsContext(context.Background(), []uint64{peerID})
	if err != nil {
		c.logger.Warn("inbound ledger: failed to collect missing nodes for added peer", "error", err)
		return
	}
	if complete {
		c.completeInboundLedger(il)
		return
	}
	released := 0
	for _, request := range requests {
		if c.sendMissingReplyRequest(il, request) {
			released++
		}
	}
	c.retryMissingAcquisitionNodes(il, missingNodeRetry{}, released)
}

func (c *catchupReplayCoordinator) handleMissingNodeSendFailure(
	il *inbound.Ledger,
	peerID uint64,
	transaction bool,
	err error,
) bool {
	hash := il.Hash()
	c.logger.Warn(
		"inbound ledger: failed to request missing nodes",
		"peer", peerID,
		"ledger_seq", il.Seq(),
		"ledger_hash", fmt.Sprintf("%x", hash[:8]),
		"transaction", transaction,
		"error", err,
	)
	if !isAcquisitionDisconnectError(err) {
		return false
	}
	il.RemovePeer(peerID)
	c.onPeerDisconnect(peermanagement.PeerID(peerID))
	return true
}

func (c *catchupReplayCoordinator) retryMissingAcquisitionNodes(
	il *inbound.Ledger,
	retry missingNodeRetry,
	released int,
) {
	if len(retry.stateIDs) == 0 && len(retry.txIDs) == 0 && released == 0 {
		return
	}

	peers := il.Peers()
	replacements := released
	if len(retry.stateIDs) > 0 || len(retry.txIDs) > 0 {
		replacements++
	}
	replacements = min(max(replacements, 1), acquisitionMaxUsefulPeers)
	added := make([]uint64, 0, replacements)
	for _, peerID := range c.acquisition.SelectLedgerPeers(il.Hash(), il.Seq(), peers, replacements) {
		if il.AddPeer(peerID) {
			added = append(added, peerID)
		}
	}
	candidates := acquisitionRequestCandidates(added, il.Peers())
	if len(candidates) > acquisitionMaxUsefulPeers {
		candidates = candidates[:acquisitionMaxUsefulPeers]
	}
	if len(candidates) == 0 {
		if len(retry.stateIDs) > 0 || len(retry.txIDs) > 0 {
			il.ReleaseUnreservedMissingNodes()
		}
		return
	}

	queued := c.submitAcquisitionWork(il, acquisitionWorkEvent{
		kind:       acquisitionWorkRetarget,
		peers:      candidates,
		stateIDs:   append([][]byte(nil), retry.stateIDs...),
		txIDs:      append([][]byte(nil), retry.txIDs...),
		queryDepth: retry.queryDepth,
		collect:    released > 0,
	})
	if !queued {
		if len(retry.stateIDs) > 0 || len(retry.txIDs) > 0 {
			il.ReleaseUnreservedMissingNodes()
		}
		hash := il.Hash()
		c.logger.Warn(
			"inbound ledger: missing-node retarget deferred; acquisition worker saturated",
			"ledger_seq", il.Seq(),
			"ledger_hash", fmt.Sprintf("%x", hash[:8]),
		)
	}
}

// escalateAcquisition runs one no-progress escalation rung for a stalled
// acquisition, mirroring rippled InboundLedger::onTimer's !wasProgress branch:
// try to complete locally from the fetch-pack cache, broaden the source-peer
// set, re-request the missing nodes (timer-driven, so a silent peer cannot
// stall it), arm a one-shot fetch-pack, and once aggressive ask the peer set
// for the missing nodes by content hash.
func (c *catchupReplayCoordinator) escalateAcquisition(il *inbound.Ledger, now time.Time) bool {
	if il.State() == inbound.StateWantBase {
		c.broadenAcquisitionPeers(il)
		c.requestAcquisitionBase(il)
		return false
	}
	if c.currentAcquisitionWork() != nil {
		existingPeers := il.Peers()
		addedPeers := c.broadenAcquisitionPeers(il)
		c.tryFetchPackEscalation(il)
		fetch := func(h [32]byte) ([]byte, bool) { return c.fetchPacks.get(h, time.Now()) }
		queued := c.submitAcquisitionWork(il, acquisitionWorkEvent{
			kind: acquisitionWorkTimer, fetch: fetch, peers: existingPeers, added: addedPeers,
		})
		if !queued {
			c.logger.Warn("inbound ledger: timeout traversal deferred; acquisition worker saturated", "seq", il.Seq())
		}
		return queued
	}
	if il.CheckLocal(func(h [32]byte) ([]byte, bool) { return c.fetchPacks.get(h, now) }) && il.IsComplete() {
		c.completeInboundLedger(il)
		return false
	}
	c.requestMissingAcquisitionNodes(il, 0)
	for _, peerID := range c.broadenAcquisitionPeers(il) {
		c.requestMissingAcquisitionNodesFromAddedPeer(il, peerID)
	}
	c.tryFetchPackEscalation(il)
	c.requestAcquisitionNodesByHash(il)
	return false
}

func (c *catchupReplayCoordinator) requestAcquisitionBase(il *inbound.Ledger) {
	requested := false
	for _, peerID := range il.Peers() {
		if c.requestLedgerBaseFromPeer(il, peerID, "failed to retry ledger base request") {
			requested = true
		}
	}
	if requested {
		return
	}
	peerID, ok := c.selectAcquisitionPeer(il.Seq())
	if !ok {
		return
	}
	c.requestLedgerBase(il, peerID, "failed to retry ledger base request")
}

const acquisitionPeerBroaden = 3

// broadenAcquisitionPeers adds up to acquisitionPeerBroaden fresh peers to a
// stalled acquisition's source set. Known holders rank ahead of fallbacks.
func (c *catchupReplayCoordinator) broadenAcquisitionPeers(il *inbound.Ledger) []uint64 {
	peers := il.Peers()
	limit := acquisitionPeerBroaden
	if len(peers) == 0 {
		limit = acquisitionPeerStart
	}
	var added []uint64
	for _, peerID := range c.acquisition.SelectLedgerPeers(il.Hash(), il.Seq(), peers, limit) {
		if il.AddPeer(peerID) {
			added = append(added, peerID)
		}
	}
	return added
}

// failInboundAcquisition reaps an acquisition whose retry budget is exhausted.
// For a consensus-driven acquisition it also tells the engine, so a node pinned
// in wrongLedger on an unacquirable ledger can drop to a recoverable resync
// rather than starving the ledger loop into a fatal watchdog abort (issue #985).
func (c *catchupReplayCoordinator) failInboundAcquisition(il *inbound.Ledger) {
	if il == nil {
		return
	}
	c.failInboundAcquisitionWithSnapshot(il, il.Snapshot(), inboundAcquisitionTimerFailure(il))
}

func inboundAcquisitionTimerFailure(il *inbound.Ledger) error {
	if il == nil {
		return errors.New("inbound ledger acquisition timer expired")
	}
	return fmt.Errorf("inbound ledger acquisition timer expired after %d timeouts", il.Timeouts())
}

func inboundAcquisitionFailureCause(cause error) error {
	if cause != nil {
		return cause
	}
	return errors.New("inbound ledger acquisition failed")
}

// recordReplayAcquisitionFailure records a terminal cause only for the
// consensus acquisition that was reserved for the current replay fault. The
// tracker check keeps a late result for an old acquisition from overwriting a
// replacement's diagnostic.
func (c *catchupReplayCoordinator) recordReplayAcquisitionFailure(il *inbound.Ledger, cause error) {
	if il == nil || cause == nil || il.Reason() != inbound.ReasonConsensus || c.adaptor == nil {
		return
	}
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return
	}
	hash := il.Hash()
	if !svc.ReplayRecoveryParent(hash) {
		return
	}
	if c.fetchTracker != nil {
		if current := c.fetchTracker.Find(hash); current != nil && current != il {
			return
		}
	}
	svc.RecordReplayAcquisitionFailure(hash, cause)
}

func (c *catchupReplayCoordinator) removeInboundAcquisitionWithSession(
	il *inbound.Ledger,
	snapshot inbound.Snapshot,
	discard bool,
) (standardReplayRetirement, bool, bool) {
	if il == nil {
		return standardReplayRetirement{}, false, false
	}
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	owned := c.ownsFrozenPivotAcquisitionLocked(il)
	removed := false
	if discard {
		removed = c.fetchTracker.DiscardExpected(il)
	} else {
		removed = c.fetchTracker.RemoveExpectedWithSnapshot(il, snapshot, false)
	}
	if removed && il.FullStateRequired() {
		c.requireReplayFullStateLocked(il.Seq(), il.Hash())
	}
	retirement := standardReplayRetirement{}
	if removed && owned {
		retirement = c.cancelStandardReplayPipelineLocked("pivot_acquisition_failed")
		if c.consensusRecovery.stepHash == il.Hash() {
			c.consensusRecovery.stepHash = [32]byte{}
		}
	}
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	return retirement, owned, removed
}

func (c *catchupReplayCoordinator) failInboundAcquisitionWithSnapshot(
	il *inbound.Ledger,
	snapshot inbound.Snapshot,
	cause error,
) {
	if il == nil {
		return
	}
	hash := il.Hash()
	reason := il.Reason()
	retirement, _, removed := c.removeInboundAcquisitionWithSession(il, snapshot, false)
	if !removed {
		return
	}
	c.recordReplayAcquisitionFailure(il, inboundAcquisitionFailureCause(cause))
	c.retireStandardReplay(retirement)
	c.retireAcquisitionStore(c.lifecycleContext(), il)
	c.logger.Warn("inbound ledger acquisition failed",
		"seq", il.Seq(),
		"hash", fmt.Sprintf("%x", hash[:8]),
		"timeouts", il.Timeouts(),
	)
	if reason == inbound.ReasonConsensus && il.TransactionOnly() && c.failStandardReplayPipelineEntry(il) {
		return
	}
	if reason == inbound.ReasonConsensus {
		c.markFailedCatchupAcquisition(hash)
		c.failConsensusRecoveryStep(hash)
	}
	if reason == inbound.ReasonConsensus && c.engine != nil {
		c.engine.OnLedgerAcquireFailed(consensus.LedgerID(hash))
	}
	if reason == inbound.ReasonConsensus {
		c.armConsensusCatchup()
	}
}

func (c *catchupReplayCoordinator) discardFailedInboundAcquisition(il *inbound.Ledger, cause error) {
	if il == nil {
		return
	}
	if cause == nil {
		cause = errors.New("transient parent-state acquisition failure")
	}
	retirement, pivotRetired, removed := c.removeInboundAcquisitionWithSession(il, inbound.Snapshot{}, true)
	if !removed {
		return
	}
	c.recordReplayAcquisitionFailure(il, inboundAcquisitionFailureCause(cause))
	c.finishDiscardedInboundAcquisitionOwned(il, retirement, pivotRetired)
}

func (c *catchupReplayCoordinator) discardFailedInboundAcquisitionWithSnapshot(
	il *inbound.Ledger,
	snapshot inbound.Snapshot,
	cause error,
) {
	if il == nil {
		return
	}
	retirement, pivotRetired, removed := c.removeInboundAcquisitionWithSession(il, snapshot, false)
	if !removed {
		return
	}
	c.recordReplayAcquisitionFailure(il, inboundAcquisitionFailureCause(cause))
	c.finishDiscardedInboundAcquisitionOwned(il, retirement, pivotRetired)
}

func (c *catchupReplayCoordinator) finishDiscardedInboundAcquisitionOwned(
	il *inbound.Ledger,
	retirement standardReplayRetirement,
	pivotRetired bool,
) {
	c.retireStandardReplay(retirement)
	c.retireAcquisitionStore(c.lifecycleContext(), il)
	if il.Reason() != inbound.ReasonConsensus {
		return
	}
	if il.TransactionOnly() {
		c.failStandardReplayPipelineEntry(il)
		return
	}
	if pivotRetired {
		c.failConsensusRecoveryStep(il.Hash())
		c.armConsensusCatchup()
	}
}

// inboundByHashBatch bounds how many missing-node content hashes a single
// by-hash escalation requests per tree. By-hash is a targeted divergent-path
// fallback, not a bulk-transfer path, so the set is kept small — matching
// rippled's getNeededHashes cap of 4 per tree (InboundLedger::neededStateHashes/
// neededTxHashes).
const inboundByHashBatch = 4

// requestAcquisitionNodesByHash performs the by-hash escalation rung: once the
// acquisition has gone aggressive it asks the peer set for the missing
// state/tx nodes by content hash (TMGetObjectByHash), the unambiguous fallback
// for a node on a divergent path that path-based requests cannot place. Replies
// are served from peers' node stores and routed back through the fetch-pack
// cache + CheckLocal placement.
func (c *catchupReplayCoordinator) requestAcquisitionNodesByHash(il *inbound.Ledger) {
	state, tx := il.TakeByHashRequest(inboundByHashBatch)
	if len(state) == 0 && len(tx) == 0 {
		return
	}
	peers := il.Peers()
	hash := il.Hash()
	seq := il.Seq()
	c.sendNodesByHash(peers, hash, seq, state, message.ObjectTypeStateNode)
	c.sendNodesByHash(peers, hash, seq, tx, message.ObjectTypeTransactionNode)
}

// sendNodesByHash issues a TMGetObjectByHash query for the given node content
// hashes to every peer in the set.
func (c *catchupReplayCoordinator) sendNodesByHash(peers []uint64, ledgerHash [32]byte, seq uint32, hashes [][32]byte, objType message.ObjectType) {
	if len(hashes) == 0 || len(peers) == 0 {
		return
	}
	objs := make([]message.IndexedObject, 0, len(hashes))
	for i := range hashes {
		h := hashes[i]
		objs = append(objs, message.IndexedObject{Hash: h[:], LedgerSeq: seq})
	}
	req := &message.GetObjectByHash{
		ObjType:    objType,
		Query:      true,
		LedgerHash: ledgerHash[:],
		Objects:    objs,
	}
	frame, err := message.EncodeFrame(req)
	if err != nil {
		c.logger.Debug("inbound ledger: encode by-hash request failed", "error", err)
		return
	}
	for _, peerID := range peers {
		if err := c.acquisition.SendPriorityToPeer(peerID, frame); err != nil {
			c.logger.Debug("inbound ledger: by-hash request send failed", "peer", peerID, "error", err)
		}
	}
	c.logger.Info("inbound ledger: requesting nodes by hash",
		"seq", seq,
		"hash", fmt.Sprintf("%x", ledgerHash[:4]),
		"count", len(hashes),
		"obj_type", objType,
		"peers", len(peers),
	)
}

// completeInboundLedger finalizes an InboundLedger acquisition and adopts the
// ledger. A ReasonGeneric acquisition (RPC-driven, ledger_request) is persisted
// for querying but does not flip operating mode or notify consensus, so an
// arbitrary historical fetch can't disturb the active chain.
func (c *catchupReplayCoordinator) completeInboundLedger(il *inbound.Ledger) {
	if c.stoppedForShutdown() {
		return
	}
	if err := c.flushAcquisitionStore(c.lifecycleContext(), il); err != nil {
		c.logger.Warn("inbound ledger: verified-node persistence failed", "error", err, "seq", il.Seq())
		c.discardFailedInboundAcquisition(il, err)
		return
	}
	c.completeInboundLedgerReady(il)
}

func (c *catchupReplayCoordinator) completeInboundLedgerReady(il *inbound.Ledger) {
	if c.stoppedForShutdown() {
		return
	}
	if il.Reason() == inbound.ReasonHistory && !c.historySequenceAllowed(il.Seq()) {
		c.discardHistoryAcquisition(il, "outside_history_window")
		return
	}
	h, stateMap, txMap, err := il.Result()
	if err != nil {
		c.logger.Warn("inbound ledger: failed to get result", "error", err)
		c.discardFailedInboundAcquisition(il, err)
		return
	}
	if c.adaptor == nil {
		c.discardFailedInboundAcquisition(il, errors.New("inbound ledger: adaptor unavailable"))
		return
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		c.discardFailedInboundAcquisition(il, errors.New("inbound ledger: ledger service unavailable"))
		return
	}
	if err = c.promoteAcquisitionStore(c.lifecycleContext(), il); err != nil {
		c.logger.Warn("inbound ledger: failed to promote persistence scope", "error", err, "seq", il.Seq())
		c.discardFailedInboundAcquisition(il, err)
		return
	}
	peerID := il.PeerID()

	// A forward child fetched from a standard rippled peer contains only its
	// header and transaction SHAMap. Rebuild the state from the locally-held
	// parent, verify the derived roots against the peer header, then feed the
	// same adoption path as the optional replay-delta protocol.
	if il.TransactionOnly() {
		c.acquisitionMu.Lock()
		if !c.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), true) {
			c.acquisitionMu.Unlock()
			c.retireAcquisitionStore(c.lifecycleContext(), il)
			return
		}
		handled, startDrain := c.completeStandardReplayPipelineEntryLocked(il, h, txMap, peerID)
		c.acquisitionMu.Unlock()
		if handled {
			c.refillStandardReplayCollector(peerID)
			if startDrain {
				c.drainStandardReplayPipeline()
			}
			return
		}
		c.completeStandardTransactionReplay(h, txMap, peerID)
		return
	}
	var handoff standardReplayPivotHandoff
	pivotHandoff := false
	c.replayCommitMu.Lock()
	if c.stoppedForShutdown() {
		c.replayCommitMu.Unlock()
		return
	}
	c.acquisitionMu.Lock()
	if il.Reason() == inbound.ReasonConsensus {
		handoff, pivotHandoff = c.claimStandardReplayPivotHandoffLocked(il)
	}
	removed := c.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), true)
	if removed && il.FullStateRequired() {
		c.requireReplayFullStateLocked(il.Seq(), il.Hash())
	}
	if !removed && pivotHandoff {
		c.clearStandardReplayPivotHandoffLocked(handoff)
	}
	recoveryTarget := c.consensusRecovery.targetHash == h.Hash
	c.acquisitionMu.Unlock()
	if !removed {
		c.replayCommitMu.Unlock()
		c.retireAcquisitionStore(c.lifecycleContext(), il)
		return
	}

	// A history backfill installs validated sequence history below the closed tip,
	// then advances the backward walk to its parent. It never touches operating
	// mode or the consensus engine.
	if il.Reason() == inbound.ReasonHistory {
		c.replayCommitMu.Unlock()
		if err = svc.IngestHistoricalLedgerWithState(c.lifecycleContext(), h, stateMap, txMap); err != nil {
			c.logger.Warn("inbound ledger: history backfill ingest failed",
				"error", err, "seq", h.LedgerIndex)
			return
		}
		if recoveryTarget {
			c.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, false)
		} else {
			c.completeHistoryBackfill(h.LedgerIndex, h.Hash, h.ParentHash, peerID)
		}
		return
	}

	// The acquisition fetches the header, state map, and transaction map; txMap
	// is nil only when the ledger has no transactions (empty tx tree), in which
	// case the service installs the genesis-shaped empty tx map.
	//
	// Generic acquisitions are queryable by hash but never mutate the service's
	// canonical frontier or feed consensus.
	if il.Reason() == inbound.ReasonGeneric {
		c.replayCommitMu.Unlock()
		if err = svc.StoreLedgerWithState(c.lifecycleContext(), h, stateMap, txMap); err != nil {
			c.logger.Warn("inbound ledger: generic store failed", "error", err, "seq", h.LedgerIndex)
			return
		}
		c.logger.Info("acquired ledger (generic) with full state from peer",
			"seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]),
		)
		if recoveryTarget {
			c.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, false)
		}
		return
	}

	initialCandidate, err := svc.BootstrapLedgerWithState(c.lifecycleContext(), h, stateMap, txMap)
	c.replayCommitMu.Unlock()
	if err != nil {
		c.logger.Warn("inbound ledger: failed to store consensus ledger", "error", err, "seq", h.LedgerIndex)
		c.recordReplayAcquisitionFailure(il, fmt.Errorf("store consensus ledger: %w", err))
		frozenPivot := c.failFrozenPivotHandoff(handoff)
		c.retireAcquisitionStore(c.lifecycleContext(), il)
		if frozenPivot {
			c.armConsensusCatchup()
		}
		return
	}

	c.logger.Info("acquired ledger with full state from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
		"initial_candidate", initialCandidate,
	)
	if pivotHandoff {
		c.completeFrozenPivotAcquisitionOwned(h, initialCandidate, handoff)
		return
	}
	c.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, initialCandidate)
}

func (c *catchupReplayCoordinator) completeStandardTransactionReplay(
	h *header.LedgerHeader,
	txMap *shamap.SHAMap,
	peerID uint64,
) {
	if c.stoppedForShutdown() {
		return
	}
	if h == nil {
		return
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return
	}
	parentHeld := false
	fallback := func(err error) {
		parent, _ := svc.GetLedgerByHash(h.ParentHash)
		if parent != nil {
			svc.RecordReplayPreparationFailure(c.lifecycleContext(), *h, txMap, parent, c.replayTargetAuthenticated(*h), err)
		}
		c.logger.Warn("standard transaction replay unavailable",
			"seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]),
			"peer", peerID,
			"error", err,
		)
		c.acquisitionMu.Lock()
		if parentHeld {
			c.requireReplayFullStateLocked(h.LedgerIndex, h.Hash)
		}
		c.startLedgerAcquisitionLegacyLocked(h.LedgerIndex, h.Hash, peerID)
		c.acquisitionMu.Unlock()
	}

	parent, err := svc.GetLedgerByHash(h.ParentHash)
	if err != nil || parent == nil {
		if err == nil {
			err = errors.New("locally-held replay parent is unavailable")
		}
		fallback(err)
		return
	}
	if parent.Sequence()+1 != h.LedgerIndex {
		fallback(fmt.Errorf("replay parent sequence %d is not predecessor of %d", parent.Sequence(), h.LedgerIndex))
		return
	}
	parentHeld = true

	stateMap, err := parent.StateMapSnapshot()
	if err != nil {
		fallback(fmt.Errorf("snapshot replay parent state: %w", err))
		return
	}
	if txMap == nil {
		if h.TxHash != ([32]byte{}) {
			fallback(errors.New("missing transaction map for non-empty transaction root"))
			return
		}
		txMap = shamap.New(shamap.TypeTransaction)
	}

	// NewStoredLedgerReplay only needs a header and verified transaction map
	// from target. Carrying the parent's state snapshot here is intentional:
	// ReplayDelta.Apply replaces it with the derived child state and checks the
	// resulting AccountHash before adoption.
	target, err := ledger.NewFromHeader(*h, stateMap, txMap, parent.Fees())
	if err != nil {
		fallback(fmt.Errorf("construct transaction-only replay target: %w", err))
		return
	}
	replay, err := inbound.NewStoredLedgerReplay(parent, target, c.logger)
	if err != nil {
		fallback(fmt.Errorf("prepare standard transaction replay: %w", err))
		return
	}
	derived, err := svc.ApplyReplay(c.lifecycleContext(), replay, c.adaptor.EngineConfigForReplay(parent), c.replayTargetAuthenticated(*h))
	if err != nil {
		c.logger.Error("standard transaction replay apply failed; validator duties blocked",
			"seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]),
			"error", err,
		)
		fallback(err)
		return
	}
	c.logger.Info("acquired ledger via standard transaction replay",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"peer", peerID,
	)
	if err := c.adoptVerifiedLedger(derived); err != nil {
		c.logger.Warn("failed to store standard transaction replay ledger", "error", err)
	}
}

// Router forwards catch-up/replay transitions to the coordinator.

func (r *Router) FetchInfo() map[string]any {
	return r.catchupReplay.FetchInfo()
}

func (r *Router) FastSyncMetrics() FastSyncMetrics {
	return r.catchupReplay.FastSyncMetrics()
}

func (r *Router) ClearFetchInfo() {
	r.catchupReplay.ClearFetchInfo()
}

func (r *Router) RequestLedger(hash [32]byte, seq uint32) (acquiring map[string]any, started, reference bool) {
	return r.catchupReplay.RequestLedger(hash, seq)
}
