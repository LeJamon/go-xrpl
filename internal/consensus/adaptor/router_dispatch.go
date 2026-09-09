package adaptor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	ledgerservice "github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/protocol"
)

type gossipNetwork interface {
	IncPeerBadData(peerID uint64, reason string)
	RecordMessageSource(suppressionHash [32]byte, peerID uint64)
	MessageRelayedRecently(suppressionHash [32]byte) bool
	UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64)
	RelayValidation(validation *consensus.Validation, exceptPeer uint64) error
}

func (r *Router) handleMessage(msg *peermanagement.InboundMessage) (transferred bool) {
	defer r.recoverFrame(msg, "dispatch")

	msgType := msg.Type
	if r.peerSessions != nil && !r.peerSessions.IsPeerConnected(msg.PeerID) &&
		msgType != message.TypeManifests {
		return false
	}
	defer func() {
		if r.peerSessions != nil && !r.peerSessions.IsPeerConnected(msg.PeerID) {
			r.HandlePeerDisconnect(msg.PeerID)
		}
	}()

	switch msgType {
	case message.TypeProposeLedger:
		r.handleProposal(msg)
	case message.TypeValidation:
		r.handleValidation(msg)
	case message.TypeTransaction:
		// Offload to the bounded worker pool so a transaction flood can't
		// starve proposal / validation / ledger-acquisition handling, which
		// share this goroutine. Mirrors rippled dispatching inbound
		// TMTransaction onto its jtTRANSACTION job queue rather than handling
		// it on the read strand.
		r.submitTxJob(msg)
		transferred = true
	case message.TypeHaveSet:
		r.handleHaveSet(msg)
	case message.TypeStatusChange:
		r.handleStatusChange(msg)
	case message.TypeGetLedger:
		// Offload the serve to the bounded worker pool so building a large
		// reply (notably the 15k-tx tx-set in serveTxSet) can't stall
		// proposal / validation / acquisition-reply handling on this
		// goroutine. Inline when the pool isn't running (synchronous tests).
		r.submitServeJob(msg)
		transferred = true
	case message.TypeLedgerData:
		transferred = r.handleLedgerData(msg)
	case message.TypeGetObjects:
		// Only fetch-pack REPLIES reach the router; the overlay serves
		// otFETCH_PACK requests inline and forwards replies here (see
		// handleGetObjectsMessage). handleFetchPackReply ignores anything
		// that isn't an otFETCH_PACK reply.
		r.handleFetchPackReply(msg)
	case message.TypeReplayDeltaResponse:
		r.handleReplayDeltaResponse(msg)
	case message.TypeManifests:
		r.submitManifestJob(msg)
	case message.TypeValidatorList:
		r.handleValidatorList(msg)
	case message.TypeValidatorListCollection:
		r.handleValidatorListCollection(msg)
	default:
	}
	return transferred
}

func (r *Router) handleInboundMessage(msg *peermanagement.InboundMessage) {
	if msg == nil {
		return
	}
	if !r.handleMessage(msg) {
		_ = msg.Close()
	}
}

func (r *Router) handleManifests(msg *peermanagement.InboundMessage) bool {
	if r.manifests == nil {
		// Cache not wired (tests or minimal configs) — silently drop.
		return false
	}

	count, err := message.WalkManifests(msg.Payload, nil)
	if err != nil {
		r.logger.Warn("failed to decode manifests frame", "error", err, "peer", msg.PeerID)
		reason := "manifests-decode"
		if errors.Is(err, message.ErrWireLimit) {
			reason = "wire-invalid"
		}
		if !msg.SelectPeerCharge(resource.FeeInvalidData(), reason) {
			r.gossip.IncPeerBadData(uint64(msg.PeerID), reason)
		}
		return false
	}
	if count == 0 {
		msg.SelectPeerCharge(resource.FeeUselessData(), "empty")
		return true
	}
	if count > manifestFrameMaxEntries && !msg.SelectPeerCharge(resource.FeeModerateBurdenPeer(), "manifests-oversize") {
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "manifests-oversize")
	}

	accepted := make([][]byte, 0, min(count, manifestFrameMaxEntries))
	valid := false
	badManifest := false
	untrusted := 0
	skippedUntrusted := false
	_, _ = message.WalkManifests(msg.Payload, func(wire []byte) {
		parsed, err := manifest.Deserialize(wire)
		if err != nil {
			r.logger.Debug("manifest parse failed",
				"error", err, "peer", msg.PeerID)
			badManifest = true
			return
		}
		hash := parsed.Hash()
		policy := manifest.Uncapped
		if r.manifestClassify != nil {
			policy = r.manifestClassify(parsed.MasterKey())
		}
		if policy != manifest.Uncapped {
			if untrusted >= r.manifestUntrustedLimit {
				skippedUntrusted = true
				valid = true
				return
			}
			untrusted++
			policy = manifest.Capped
		}
		switch d := r.manifests.ApplyManifest(parsed, policy); d {
		case manifest.Accepted:
			valid = true
			accepted = append(accepted, wire)
			if r.messageSeen != nil {
				r.messageSeen.observe(hash)
			}
		case manifest.Invalid, manifest.BadMasterKey, manifest.BadEphemeralKey:
			badManifest = true
		case manifest.Stale:
			valid = true
			if r.messageSeen != nil {
				r.messageSeen.observe(hash)
			}
		case manifest.UntrustedCapacity:
			valid = true
		}
	})
	if skippedUntrusted && !msg.SelectPeerCharge(resource.FeeMalformedRequest(), "manifests-untrusted-limit") {
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "manifests-untrusted-limit")
	}
	if badManifest {
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "manifest-invalid")
	}
	r.relayManifests(accepted)
	return valid
}

func (r *Router) relayManifests(serialized [][]byte) {
	if len(serialized) == 0 {
		return
	}
	frames, err := encodeManifestFrames(serialized...)
	if err != nil {
		r.logger.Warn("failed to encode manifest relay frame", "error", err)
		return
	}
	sender := r.manifestEmitter()
	if sender == nil {
		return
	}
	if err := sender.BroadcastManifestFrames(frames); err != nil {
		r.logger.Warn("failed to broadcast manifest relay frame", "error", err)
	}
}

func (r *Router) handleProposal(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeProposeLedger, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode proposal", "error", err, "peer", msg.PeerID)
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "proposal-decode")
		return
	}
	proposeSet, ok := decoded.(*message.ProposeSet)
	if !ok {
		return
	}

	// Bounds checks BEFORE the engine sees the frame, so a peer can't
	// cost-free spam oversized or implausibly-hoppy consensus traffic.
	if badField, ok := validateProposeBounds(proposeSet); !ok {
		r.logger.Debug("dropping malformed proposal",
			"peer", msg.PeerID, "bad_field", badField)
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "proposal-malformed-"+badField)
		return
	}

	proposal := proposalFromMessage(proposeSet)
	r.resolveMasterNodeID(&proposal.NodeID, proposal.SigningPubKey)
	originPeer := uint64(msg.PeerID)

	// Check duplicate-status before OnProposal, but do not admit a new hash to
	// durable suppression until signature verification and engine acceptance.
	// Hash the DECODED fields via hashProposalSuppression. Hashing
	// the raw protobuf envelope would desync dedup from peers
	// that see the same message with different optional-field framing
	// (e.g., deprecated `hops` included or omitted) — same semantic
	// proposal, but different byte payload.
	//
	// Stash the hash on the Proposal so the downstream relay path
	// can thread it to Overlay's reverse index without recomputing.
	suppressionHash := hashProposalSuppression(proposal)
	proposal.SuppressionHash = suppressionHash
	r.gossip.RecordMessageSource(suppressionHash, originPeer)
	// Drop duplicates before the engine path (re-running OnProposal
	// just re-verifies ECDSA). Still feed the IDLED-gated relay slot
	// on dupes for squelch accounting.
	//
	// Rippled counts a duplicate for reduce-relay only after the first copy
	// was relayed. Sources received before that point are accumulated and
	// counted atomically by RelayFromValidator.
	if r.messageSeen.seenRecently(suppressionHash) {
		if r.gossip.MessageRelayedRecently(suppressionHash) {
			r.gossip.UpdateRelaySlot(proposal.SigningPubKey[:], originPeer, nil)
		}
		return
	}

	if err := r.engine.OnProposal(proposal, originPeer); err != nil {
		r.logger.Debug("engine rejected proposal", "error", err, "peer", msg.PeerID)
		return
	}
	r.messageSeen.observe(suppressionHash)
}

func (r *Router) handleValidation(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeValidation, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode validation", "error", err, "peer", msg.PeerID)
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "validation-decode")
		return
	}
	val, ok := decoded.(*message.Validation)
	if !ok {
		return
	}

	validation, err := validationFromMessage(val, r.adaptor.Now())
	if err != nil {
		r.logger.Warn("failed to parse validation", "error", err, "peer", msg.PeerID)
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "validation-parse")
		return
	}
	r.resolveMasterNodeID(&validation.NodeID, validation.SigningPubKey)

	// Post-parse bounds: the validation struct must carry sane hash
	// and signature sizes. Same rationale as in handleProposal.
	if badField, ok := validateValidationBounds(validation); !ok {
		r.logger.Debug("dropping malformed validation",
			"peer", msg.PeerID, "bad_field", badField)
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "validation-malformed-"+badField)
		return
	}

	// Ingress freshness gate: charge and drop a stale or future-dated
	// validation before suppression/relay, mirroring rippled's PeerImp
	// isCurrent check right after deserialization. The tracker applies
	// the same window for quorum/trie accounting, but by the time it
	// runs the engine has already relayed the validation.
	if !rcl.IsCurrent(r.adaptor.Now(), validation.SignTime, validation.SeenTime) {
		r.logger.Debug("dropping non-current validation",
			"peer", msg.PeerID,
			"seq", validation.LedgerSeq,
			"sign_time", validation.SignTime)
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "validation-not-current")
		return
	}

	// Operator opt-out ([relay_validations] = drop_untrusted): shed
	// validations signed outside our UNL here, before the engine spends
	// CPU verifying the signature. rippled's PeerImp does the same under
	// RELAY_UNTRUSTED_VALIDATIONS == -1.
	trusted := r.adaptor.IsTrusted(validation.NodeID)
	if r.adaptor.DropUntrustedValidations() && !trusted {
		return
	}

	originPeer := uint64(msg.PeerID)

	// Observe-before-engine for consistent duplicate accounting. Hash
	// the INNER STValidation blob carried in TMValidation.validation.
	// Hashing the TMValidation envelope instead would desync dedup the
	// same way handleProposal would if it hashed the TMProposeSet
	// envelope: deprecated outer fields vary, inner canonical blob does
	// not. We use the raw inbound bytes here — NOT a re-serialized copy
	// — so a lossy or reordered round-trip can't silently diverge the
	// hash. Stash the hash on the Validation so the downstream relay
	// path can thread it to Overlay's reverse index without recomputing.
	suppressionHash := hashValidationSuppression(val.Validation)
	validation.SuppressionHash = suppressionHash
	r.gossip.RecordMessageSource(suppressionHash, originPeer)
	firstSeen, _ := r.messageSeen.observe(suppressionHash)

	// Drop duplicates before the engine path (re-running OnValidation
	// just re-verifies ECDSA, dominating CPU under gossip fan-out).
	// Still update the relay slot for squelch accounting.
	//
	// Sources received before the verified first relay are accumulated by the
	// overlay. Later duplicates feed only their current origin into the slot.
	if !firstSeen {
		if r.gossip.MessageRelayedRecently(suppressionHash) {
			r.gossip.UpdateRelaySlot(validation.SigningPubKey[:], originPeer, nil)
		}
		return
	}

	origin, tracking := r.validationPeerContext(msg.PeerID)
	if !trusted && (tracking == peermanagement.PeerTrackingDiverged || r.isLoadedLocal()) {
		return
	}

	if _, ok := r.engine.(consensus.VerifiedValidationProcessor); !ok {
		r.handleLegacyValidation(validation, originPeer, msg.PeerID)
		return
	}

	work := validationWork{
		validation: validation,
		origin:     origin,
		trusted:    trusted,
	}
	outcome := r.submitValidationWork(work)
	if outcome != validationWorkQueued && outcome != validationWorkProcessedSynchronously {
		r.messageSeen.allowRetry(suppressionHash)
	}
}

type validationWorkAdmissionOutcome uint8

const (
	validationWorkQueued validationWorkAdmissionOutcome = iota
	validationWorkProcessedSynchronously
	validationWorkShedUntrusted
	validationWorkShedTrusted
	validationWorkStopped
)

const validationSaturationLogInterval = 64

func (r *Router) submitValidationWork(work validationWork) validationWorkAdmissionOutcome {
	if r.validationWork != nil && r.validationWork.running() {
		switch r.validationWork.submit(work) {
		case validationQueueAccepted:
			return validationWorkQueued
		case validationQueueStopped:
			return validationWorkStopped
		}
		if !work.trusted {
			count := r.validationShedUntrusted.Add(1)
			r.logUntrustedValidationSaturation("job", count)
			return validationWorkShedUntrusted
		}

		count := r.validationShedTrusted.Add(1)
		r.logTrustedValidationSaturation(work.origin.PeerID, count)
		return validationWorkShedTrusted
	}

	r.handleValidationWorkResult(validationWorkResult{
		validation: work.validation,
		origin:     work.origin,
		err:        r.adaptor.VerifyValidation(work.validation),
	})
	return validationWorkProcessedSynchronously
}

func (r *Router) logUntrustedValidationSaturation(stage string, count uint64) {
	if count != 1 && count%validationSaturationLogInterval != 0 {
		return
	}
	r.logger.Warn("untrusted validation verifier saturated",
		"stage", stage,
		"dropped", count)
}

func (r *Router) logTrustedValidationSaturation(peerID, count uint64) {
	if count != 1 && count%validationSaturationLogInterval != 0 {
		return
	}
	r.logger.Warn("trusted validation verifier saturated",
		"peer", peerID,
		"dropped", count)
}

func (r *Router) handleValidationWorkResult(result validationWorkResult) {
	defer result.permit.release()
	if result.err != nil {
		r.logger.Info("invalid validation signature",
			"t", "consensus",
			"event", "validation-invalid-signature",
			"error", result.err,
			"peer", result.origin.PeerID)
		r.gossip.IncPeerBadData(result.origin.PeerID, "validation-invalid-signature")
		return
	}

	processor, ok := r.engine.(consensus.VerifiedValidationProcessor)
	if !ok {
		return
	}
	disposition, err := processor.ProcessVerifiedValidation(result.validation, result.origin)
	if err != nil {
		r.logger.Info("engine rejected verified validation",
			"t", "consensus",
			"event", "validation-rejected",
			"error", err,
			"peer", result.origin.PeerID)
		return
	}

	if disposition.Status == consensus.ValidationMultiple ||
		disposition.Status == consensus.ValidationConflicting {
		level := slog.LevelError
		if !disposition.Trusted {
			level = slog.LevelInfo
		}
		r.logger.Log(context.Background(), level, "byzantine validation detected",
			"t", "consensus",
			"event", "byzantine-validation",
			"reason", disposition.Status.String(),
			"trusted", disposition.Trusted,
			"peer", result.origin.PeerID)
	}

	r.logger.Debug("inbound validation processed",
		"t", "consensus",
		"event", "validation-recv",
		"peer", result.origin.PeerID,
		"seq", result.validation.LedgerSeq,
		"status", disposition.Status.String(),
		"hash_short", fmt.Sprintf("%x", result.validation.LedgerID[:8]))

	if disposition.AcquireEligible() {
		r.maybeAcquireFromValidation(result.validation, result.origin.PeerID)
	}
	if disposition.Relay {
		if err := r.gossip.RelayValidation(result.validation, result.origin.PeerID); err != nil {
			r.logger.Debug("failed to relay validation",
				"error", err,
				"peer", result.origin.PeerID)
		}
	}
}

func (r *Router) handleLegacyValidation(
	validation *consensus.Validation,
	originPeer uint64,
	peerID peermanagement.PeerID,
) {
	if err := r.engine.OnValidation(validation, originPeer); err != nil {
		var bv *consensus.ByzantineValidationError
		if errors.As(err, &bv) {
			level := slog.LevelError
			if !bv.Trusted {
				level = slog.LevelInfo
			}
			r.logger.Log(context.Background(), level, "byzantine validation detected",
				"t", "consensus",
				"event", "byzantine-validation",
				"reason", bv.Reason,
				"trusted", bv.Trusted,
				"peer", peerID)
			return
		}
		r.logger.Info("engine rejected validation",
			"t", "consensus",
			"event", "validation-rejected",
			"error", err.Error(),
			"peer", peerID)
		return
	}
	r.maybeAcquireFromValidation(validation, originPeer)
}

func (r *Router) validationPeerContext(
	peerID peermanagement.PeerID,
) (consensus.ValidationOrigin, peermanagement.PeerTracking) {
	origin := consensus.ValidationOrigin{PeerID: uint64(peerID)}
	tracking := peermanagement.PeerTrackingUnknown
	view, ok := r.peerSessions.(interface {
		Peers() []peermanagement.PeerInfo
	})
	if !ok {
		return origin, tracking
	}
	for _, peer := range view.Peers() {
		if peer.ID != peerID {
			continue
		}
		tracking = peer.Tracking
		if r.overlay != nil && r.overlay.Cluster() != nil && len(peer.PublicKeyBytes) != 0 {
			_, origin.Cluster = r.overlay.Cluster().Member(peer.PublicKeyBytes)
		}
		break
	}
	return origin, tracking
}

func (r *Router) isLoadedLocal() bool {
	if r.adaptor == nil || r.adaptor.LedgerService() == nil {
		return false
	}
	feeTrack := r.adaptor.LedgerService().FeeTrack()
	return feeTrack != nil && feeTrack.IsLoadedLocal()
}

// resolveMasterNodeID looks the inbound signing pubkey up in the
// manifest cache and, when a manifest binds it to a master pubkey,
// rewrites *nid to CalcNodeID(masterKey). In the absence of a manifest
// mapping the parser's initial CalcNodeID(signingKey) value is
// preserved untouched, so non-rotated validators still round-trip
// through the engine on the signing-derived NodeID.
//
// The manifest cache is installed on the router via SetManifestCache
// before Run(). When the cache is nil (tests constructing a bare
// router), this is a no-op and the parser default stands.
func (r *Router) resolveMasterNodeID(nid *consensus.NodeID, signing consensus.SigningPubKey) {
	if r.manifests == nil {
		return
	}
	master := r.manifests.GetMasterKey([33]byte(signing))
	// GetMasterKey returns the input unchanged when no manifest has
	// bound this signing key to a master — leave nid alone in that
	// case so we don't redundantly rehash.
	if master == [33]byte(signing) {
		return
	}
	*nid = consensus.CalcNodeID(master)
}

// validateProposeBounds returns ("", true) when the decoded ProposeSet
// is within bounds; returns (field_label, false) on the first violation
// so the caller can attribute the charge with a specific reason.
func validateProposeBounds(p *message.ProposeSet) (string, bool) {
	if p == nil {
		return "nil", false
	}
	if len(p.PreviousLedger) != 32 {
		return "prev-ledger-size", false
	}
	if len(p.CurrentTxHash) != 32 {
		return "txset-size", false
	}
	if n := len(p.Signature); n < signatureMinLen || n > signatureMaxLen {
		return "sig-size", false
	}
	// Proposal pubkeys must be compressed secp256k1 (0x02/0x03 prefix).
	// ed25519 validators (0xED prefix) are not allowed in propose-set.
	// The length-only check would pass a 33-byte ed25519 key (0xED || 32
	// bytes), letting the peer slip through without attribution, so the
	// prefix gate runs alongside the size gate.
	if len(p.NodePubKey) != 33 {
		return "pubkey-size", false
	}
	if p.NodePubKey[0] != 0x02 && p.NodePubKey[0] != 0x03 {
		return "pubkey-type", false
	}
	return "", true
}

// validateValidationBounds returns ("", true) when the parsed
// Validation has sane lengths on the post-decode struct fields. Same
// attribution contract as validateProposeBounds.
func validateValidationBounds(v *consensus.Validation) (string, bool) {
	if v == nil {
		return "nil", false
	}
	if v.LedgerID == (consensus.LedgerID{}) {
		return "ledger-hash-zero", false
	}
	if v.SigningPubKey == (consensus.SigningPubKey{}) {
		return "signing-pubkey-zero", false
	}
	if n := len(v.Signature); n < signatureMinLen || n > signatureMaxLen {
		return "sig-size", false
	}
	return "", true
}

type transactionDispatchResult struct {
	charge        resource.Charge
	chargeContext string
	submitResult  openledger.Result
	submitError   error
	deferred      bool
	relayed       bool
}

func (r *Router) handleTransaction(msg *peermanagement.InboundMessage) (dispatch transactionDispatchResult) {
	defer r.recoverFrame(msg, "transaction")

	// Frames fanned out from a TMTransactions batch arrive already
	// decoded in Tx; only wire-sourced frames need decoding from Payload.
	txMsg := msg.Tx
	if txMsg == nil {
		decoded, err := message.Decode(message.TypeTransaction, msg.Payload)
		if err != nil {
			r.logger.Warn("failed to decode transaction", "error", err, "peer", msg.PeerID)
			return dispatch
		}
		var ok bool
		txMsg, ok = decoded.(*message.Transaction)
		if !ok {
			r.logger.Warn("decoded transaction has unexpected type",
				"peer", msg.PeerID,
				"got", fmt.Sprintf("%T", decoded))
			return dispatch
		}
	}

	blob := transactionFromMessage(txMsg)
	if len(blob) == 0 {
		r.logger.Warn("inbound transaction has empty blob",
			"peer", msg.PeerID,
			"status", txMsg.Status)
		return dispatch
	}
	pending, pendingErr := openledger.ParsePendingTx(blob)
	canonicalBlob := blob
	if pendingErr == nil {
		canonicalBlob = pending.Blob
	}
	if pendingErr == nil && pending.Parsed.GetCommon().GetFlags()&tx.TfInnerBatchTxn != 0 {
		dispatch.charge = resource.FeeModerateBurdenPeer()
		dispatch.chargeContext = "inner batch txn"
		msg.SelectPeerCharge(dispatch.charge, dispatch.chargeContext)
		return dispatch
	}
	admittedBad := false
	if pendingErr == nil && r.txSeen != nil {
		shouldProcess, bad := r.txSeen.claim(pending.Hash, uint64(msg.PeerID))
		if !shouldProcess {
			if bad {
				dispatch.charge = resource.FeeUselessData()
				dispatch.chargeContext = "transaction-known-bad"
				msg.SelectPeerCharge(dispatch.charge, dispatch.chargeContext)
			}
			return dispatch
		}
		admittedBad = bad
	}
	if pendingErr == nil && pending.Parsed.GetCommon().LastLedgerSequence != nil &&
		r.adaptor != nil && r.adaptor.ledgerService != nil &&
		*pending.Parsed.GetCommon().LastLedgerSequence < r.adaptor.ledgerService.GetValidatedLedgerIndex() {
		if r.txSeen != nil {
			r.txSeen.markBad(pending.Hash)
		}
		dispatch.charge = resource.FeeUselessData()
		dispatch.chargeContext = "transaction-expired"
		msg.SelectPeerCharge(dispatch.charge, dispatch.chargeContext)
		return dispatch
	}
	if admittedBad {
		dispatch.charge = resource.FeeInvalidSignature()
		dispatch.chargeContext = "transaction-known-bad-signature"
		msg.SelectPeerCharge(dispatch.charge, dispatch.chargeContext)
		return dispatch
	}

	// Peer-relay path — the originating peer manages its own resends,
	// so we don't pin the blob in our LocalTxs held pool.
	outcome, err := r.adaptor.SubmitPendingTx(canonicalBlob, false)
	dispatch.submitResult = outcome.Class
	dispatch.submitError = err
	dispatch.deferred = outcome.Queued
	if errors.Is(err, txengine.ErrInvalidSignature) {
		if pendingErr == nil && r.txSeen != nil {
			r.txSeen.markBad(pending.Hash)
		}
		dispatch.charge = resource.FeeInvalidSignature()
		dispatch.chargeContext = "transaction-invalid-signature"
		msg.SelectPeerCharge(dispatch.charge, dispatch.chargeContext)
	} else if errors.Is(err, ledgerservice.ErrInvalidLocalTransaction) {
		if pendingErr == nil && r.txSeen != nil {
			r.txSeen.markBad(pending.Hash)
		}
		dispatch.charge = resource.FeeInvalidSignature()
		dispatch.chargeContext = "transaction-local-checks"
		msg.SelectPeerCharge(dispatch.charge, dispatch.chargeContext)
	}
	// Relay immediately on the inbound job, not one ledger later via
	// OpenLedger.Accept's once-per-LCL callback; that one-ledger lag is a
	// direct contributor to tx-propagation latency.
	//
	// Gate: relay only for the applied-or-terQUEUED case. openledger.Submit
	// folds both terQUEUED and tec into ResultSuccess, so ResultSuccess is
	// the exact superset that should relay. ResultRetry (non-queued ter*)
	// and ResultFailure (tef/tem/tel) do NOT relay.
	if err == nil && outcome.Class == openledger.ResultSuccess {
		// Debug, not Info: at thousands of tx/s an unconditional Info write here
		// is a per-transaction blocking syscall on the submit hot path.
		r.logger.Debug("inbound tx accepted into pending pool",
			"t", "consensus",
			"event", "tx-inbound",
			"peer", msg.PeerID,
			"blob_size", len(canonicalBlob),
			"status", txMsg.Status,
		)
		r.relayTransaction(r.transactionRelaySkip(pending.Hash, msg.PeerID), canonicalBlob, outcome.Queued)
		dispatch.relayed = true
	} else {
		r.logger.Debug("inbound tx rejected by pending pool",
			"t", "consensus",
			"event", "tx-inbound-rejected",
			"peer", msg.PeerID,
			"blob_size", len(canonicalBlob),
			"status", txMsg.Status,
			"result", outcome.Class,
			"error", err,
		)
	}
	return dispatch
}

func (r *Router) transactionRelaySkip(
	hash [32]byte,
	origin peermanagement.PeerID,
) map[peermanagement.PeerID]struct{} {
	toSkip := map[peermanagement.PeerID]struct{}{origin: {}}
	if r.txSeen == nil {
		return toSkip
	}
	for peerID := range r.txSeen.releasePeers(hash) {
		toSkip[peermanagement.PeerID(peerID)] = struct{}{}
	}
	return toSkip
}

// relayTransaction rebroadcasts an accepted peer-originated TMTransaction,
// excluding peers known to already hold it.
//
// The outbound wire shape: status normalized to tsCURRENT (the inbound
// peer's claimed status is informational only) and receivetimestamp
// freshly stamped from the local Ripple clock.
//
// Overlay.RelayTransactionSkipping applies reduce-relay peer selection: the
// full frame goes to a subset of peers and the rest learn of the tx via the
// TMHaveTransactions announce.
func (r *Router) relayTransaction(toSkip map[peermanagement.PeerID]struct{}, blob []byte, deferred bool) {
	if r.overlay == nil {
		return
	}
	out := relayTransactionMessage(blob, deferred)
	frame, err := message.EncodeFrame(out)
	if err != nil {
		r.logger.Warn("relay transaction encode failed", "error", err)
		return
	}
	// Reduce-relay peer selection derives the transaction ID from the wire
	// frame itself. If the frame cannot be decoded, the overlay falls back to
	// full relay rather than announcing an unfulfillable hash.
	r.overlay.RelayTransactionSkipping(toSkip, frame)
}

func relayTransactionMessage(blob []byte, deferred bool) *message.Transaction {
	return &message.Transaction{
		RawTransaction:   blob,
		Status:           message.TxStatusCurrent,
		ReceiveTimestamp: uint64(protocol.RippleSeconds(time.Now())),
		Deferred:         deferred,
	}
}

func (r *Router) handleHaveSet(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeHaveSet, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode have_set", "error", err, "peer", msg.PeerID)
		return
	}
	hts, ok := decoded.(*message.HaveTransactionSet)
	if !ok {
		return
	}

	txSetID, status, err := haveSetFromMessage(hts)
	if err != nil {
		r.gossip.IncPeerBadData(uint64(msg.PeerID), "have-set-hashsize")
		return
	}

	if status == message.TxSetStatusHave {
		// Record the advertisement so an inbound GetLedger we can't satisfy
		// can be relayed to this peer (rippled getPeerWithTree).
		if !r.txSetNet.NotePeerHasTxSet(uint64(msg.PeerID), [32]byte(txSetID)) {
			r.gossip.IncPeerBadData(uint64(msg.PeerID), "have-set-duplicate")
			return
		}
		r.logger.Debug("peer has txset", "txset", txSetID, "peer", msg.PeerID)
	}
}
