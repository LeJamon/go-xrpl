package adaptor

import (
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
)

func (r *Router) handleMessage(msg *peermanagement.InboundMessage) {
	msgType := message.MessageType(msg.Type)

	switch msgType {
	case message.TypeProposeLedger:
		r.handleProposal(msg)
	case message.TypeValidation:
		r.handleValidation(msg)
	case message.TypeTransaction:
		r.handleTransaction(msg)
	case message.TypeHaveSet:
		r.handleHaveSet(msg)
	case message.TypeStatusChange:
		r.handleStatusChange(msg)
	case message.TypeGetLedger:
		r.handleGetLedger(msg)
	case message.TypeLedgerData:
		r.handleLedgerData(msg)
	case message.TypeGetObjects:
		// Only fetch-pack REPLIES reach the router; the overlay serves
		// otFETCH_PACK requests inline and forwards replies here (see
		// handleGetObjectsMessage). handleFetchPackReply ignores anything
		// that isn't an otFETCH_PACK reply.
		r.handleFetchPackReply(msg)
	case message.TypeReplayDeltaResponse:
		r.handleReplayDeltaResponse(msg)
	case message.TypeManifests:
		r.handleManifests(msg)
	case message.TypeValidatorList:
		r.handleValidatorList(msg)
	case message.TypeValidatorListCollection:
		r.handleValidatorListCollection(msg)
	default:
	}
}

// handleManifests ingests a TMManifests frame. For each serialized
// manifest in the list: deserialize, apply to the cache, and — on
// Accepted — relay the single-manifest frame to every peer except the
// origin.
//
// Decode failures attribute "manifest-decode" badData to the sender. A
// mix of valid and invalid entries in the same frame results in the
// valid ones being applied; the frame isn't rejected wholesale.
func (r *Router) handleManifests(msg *peermanagement.InboundMessage) {
	if r.manifests == nil {
		// Cache not wired (tests or minimal configs) — silently drop.
		return
	}

	decoded, err := message.Decode(message.TypeManifests, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode manifests frame", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "manifests-decode")
		return
	}
	mfs, ok := decoded.(*message.Manifests)
	if !ok || len(mfs.List) == 0 {
		return
	}

	for _, wire := range mfs.List {
		parsed, err := manifest.Deserialize(wire.STObject)
		if err != nil {
			r.logger.Debug("manifest parse failed",
				"error", err, "peer", msg.PeerID)
			r.adaptor.IncPeerBadData(uint64(msg.PeerID), "manifest-parse")
			continue
		}
		switch d := r.manifests.ApplyManifest(parsed); d {
		case manifest.Accepted:
			r.relayManifest(msg.PeerID, wire.STObject)
		case manifest.Invalid, manifest.BadMasterKey, manifest.BadEphemeralKey:
			// Charge the sender — they gave us a manifest that
			// passed structural parse but failed the cache's
			// invariants (signature, key reuse, etc.).
			r.adaptor.IncPeerBadData(uint64(msg.PeerID), "manifest-"+d.String())
		case manifest.Stale:
			// Expected and harmless: a peer gossiped a manifest we
			// already have at equal or higher seq. No action.
		}
	}
}

// relayManifest rebroadcasts a single accepted manifest to every peer
// except the origin. Wraps the serialized STObject in a TMManifests
// frame (a list of one). Shares its framing with the local-manifest
// emission paths in manifest_emit.go.
func (r *Router) relayManifest(exceptPeer peermanagement.PeerID, serialized []byte) {
	if r.overlay == nil {
		return
	}
	frame, err := encodeManifestsFrame(serialized)
	if err != nil {
		r.logger.Warn("failed to encode manifest relay frame", "error", err)
		return
	}
	_ = r.overlay.BroadcastExcept(exceptPeer, frame)
}

func (r *Router) handleProposal(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeProposeLedger, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode proposal", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "proposal-decode")
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
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "proposal-malformed-"+badField)
		return
	}

	proposal := ProposalFromMessage(proposeSet)
	r.resolveMasterNodeID(&proposal.NodeID, proposal.SigningPubKey)
	originPeer := uint64(msg.PeerID)

	// Record duplicate-status + last-sighting BEFORE OnProposal.
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
	firstSeen, lastSeen := r.messageSeen.observe(suppressionHash)

	// Drop duplicates before the engine path (re-running OnProposal
	// just re-verifies ECDSA). Still feed the IDLED-gated relay slot
	// on dupes for squelch accounting.
	//
	// Deliberate deviation: rippled tracks suppression per (hash, peer),
	// re-running the handler so per-peer slot entries grow on each new
	// sender. Our dedup is hash-only, so a second peer's copy is dropped
	// at the gate. Quorum/position tracking unaffected (first arrival
	// counts the validator); reduce-relay accuracy is partly compensated
	// via PeersThatHave + UpdateRelaySlot below.
	if !firstSeen {
		if time.Since(lastSeen) < peermanagement.Idled {
			seenPeers := r.adaptor.PeersThatHave(suppressionHash)
			r.adaptor.UpdateRelaySlot(proposal.SigningPubKey[:], originPeer, seenPeers)
		}
		return
	}

	if err := r.engine.OnProposal(proposal, originPeer); err != nil {
		r.logger.Debug("engine rejected proposal", "error", err, "peer", msg.PeerID)
		return
	}
}

func (r *Router) handleValidation(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeValidation, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode validation", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "validation-decode")
		return
	}
	val, ok := decoded.(*message.Validation)
	if !ok {
		return
	}

	validation, err := ValidationFromMessage(val)
	if err != nil {
		r.logger.Warn("failed to parse validation", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "validation-parse")
		return
	}
	r.resolveMasterNodeID(&validation.NodeID, validation.SigningPubKey)

	// Post-parse bounds: the validation struct must carry sane hash
	// and signature sizes. Same rationale as in handleProposal.
	if badField, ok := validateValidationBounds(validation); !ok {
		r.logger.Debug("dropping malformed validation",
			"peer", msg.PeerID, "bad_field", badField)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "validation-malformed-"+badField)
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
	firstSeen, lastSeen := r.messageSeen.observe(suppressionHash)

	// Drop duplicates before the engine path (re-running OnValidation
	// just re-verifies ECDSA, dominating CPU under gossip fan-out).
	// Still update the relay slot for squelch accounting.
	//
	// Deliberate deviation: rippled's per-(hash, peer) suppression
	// re-processes new senders; our hash-only dedup drops them at the
	// gate. See handleProposal for the full rationale.
	if !firstSeen {
		if time.Since(lastSeen) < peermanagement.Idled {
			seenPeers := r.adaptor.PeersThatHave(suppressionHash)
			r.adaptor.UpdateRelaySlot(validation.SigningPubKey[:], originPeer, seenPeers)
		}
		return
	}

	if err := r.engine.OnValidation(validation, originPeer); err != nil {
		// A same-seq double-sign (conflicting/multiple) is Byzantine
		// behaviour, but the validation is well-formed and correctly
		// signed and the engine has already relayed it. Like rippled
		// (handleNewValidation logs at error level and forwards; no
		// Resource::Charge), log it loudly and do NOT charge the delivering
		// peer — it is an innocent relay.
		var bv *consensus.ByzantineValidationError
		if errors.As(err, &bv) {
			r.logger.Error("byzantine validation detected",
				"t", "consensus",
				"event", "byzantine-validation",
				"reason", bv.Reason,
				"peer", msg.PeerID)
			return
		}
		r.logger.Info("engine rejected validation",
			"t", "consensus",
			"event", "validation-rejected",
			"error", err.Error(),
			"peer", msg.PeerID)
		return
	}
	r.logger.Info("inbound validation accepted",
		"t", "consensus",
		"event", "validation-recv",
		"peer", msg.PeerID,
		"seq", validation.LedgerSeq,
		"hash_short", fmt.Sprintf("%x", validation.LedgerID[:8]))

	// Per-validation catch-up acquire on EVERY trusted current
	// validation, not only at quorum. Under sustained load a node that
	// falls one ledger behind enters the wrongLedger chase loop holding
	// no position (our_pos_seq=0); with only 3 of the 4-quorum trusted
	// validators on the network tip, the quorum-gated stash acquire
	// (armValidationStashAcquisition) never fires, so the node never
	// fetches the tip the network is converging on and the chain stalls
	// below quorum until a slow periodic sweep recovers it. Acquiring on
	// each trusted validation breaks that loop.
	r.maybeAcquireFromValidation(validation, originPeer)
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

func (r *Router) handleTransaction(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeTransaction, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode transaction", "error", err, "peer", msg.PeerID)
		return
	}
	txMsg, ok := decoded.(*message.Transaction)
	if !ok {
		r.logger.Warn("decoded transaction has unexpected type",
			"peer", msg.PeerID,
			"got", fmt.Sprintf("%T", decoded))
		return
	}

	blob := TransactionFromMessage(txMsg)
	if len(blob) == 0 {
		r.logger.Warn("inbound transaction has empty blob",
			"peer", msg.PeerID,
			"status", txMsg.Status)
		return
	}

	// Peer-relay path — the originating peer manages its own resends,
	// so we don't pin the blob in our LocalTxs held pool.
	res, err := r.adaptor.SubmitPendingTx(blob, false)
	r.logger.Info("inbound tx accepted into pending pool",
		"t", "consensus",
		"event", "tx-inbound",
		"peer", msg.PeerID,
		"blob_size", len(blob),
		"status", txMsg.Status,
	)

	// Relay immediately on the inbound job, not one ledger later via
	// OpenLedger.Accept's once-per-LCL callback; that one-ledger lag is a
	// direct contributor to tx-propagation latency.
	//
	// Gate: relay only for the applied-or-terQUEUED case. openledger.Submit
	// folds both terQUEUED and tec into ResultSuccess, so ResultSuccess is
	// the exact superset that should relay. ResultRetry (non-queued ter*)
	// and ResultFailure (tef/tem/tel) do NOT relay.
	if err == nil && res == openledger.ResultSuccess {
		r.relayTransaction(msg.PeerID, blob)
	}
}

// relayTransaction rebroadcasts an accepted peer-originated TMTransaction,
// excluding the originating peer.
//
// The outbound wire shape: status normalized to tsCURRENT (the inbound
// peer's claimed status is informational only) and receivetimestamp
// freshly stamped from the local Ripple clock.
//
// Overlay.RelayTransaction applies reduce-relay peer selection: the full
// frame goes to a subset of peers and the rest learn of the tx via the
// TMHaveTransactions announce. We don't consult a separate suppression
// set for the multi-hop case because de-dup happens implicitly via
// openledger.Submit's view.TxExists pre-filter: a duplicate arrival from
// another peer classifies as ResultFailure and the relay gate above never
// fires. Excluding the origin is the minimum correctness boundary —
// without it the originator would receive its own packet back and either
// re-charge us bandwidth or, in a 2-peer cycle, oscillate indefinitely.
func (r *Router) relayTransaction(except peermanagement.PeerID, blob []byte) {
	if r.overlay == nil {
		return
	}
	out := &message.Transaction{
		RawTransaction:   blob,
		Status:           message.TxStatusCurrent,
		ReceiveTimestamp: uint64(time.Now().Unix() - protocol.RippleEpochUnix),
	}
	frame, err := encodeFrame(message.TypeTransaction, out)
	if err != nil {
		r.logger.Warn("relay transaction encode failed", "error", err)
		return
	}
	// Reduce-relay peer selection: relays the full frame to a subset of
	// peers and lets the rest learn via the TMHaveTransactions announce.
	r.overlay.RelayTransaction(except, frame)
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

	txSetID, status := HaveSetFromMessage(hts)

	switch status {
	case message.TxSetStatusHave:
		// Record the advertisement so an inbound GetLedger we can't satisfy
		// can be relayed to this peer (rippled getPeerWithTree).
		r.adaptor.NotePeerHasTxSet(uint64(msg.PeerID), [32]byte(txSetID))
		r.logger.Debug("peer has txset", "txset", txSetID, "peer", msg.PeerID)
	case message.TxSetStatusNeed:
		// Peer needs a tx set we might have — check cache and respond.
		if ts, ok := r.adaptor.txSetCache.Get(txSetID); ok {
			// We have it — notify the engine with the tx set data
			if err := r.engine.OnTxSet(ts.ID(), ts.Txs()); err != nil {
				r.logger.Debug("engine rejected txset", "error", err)
			}
		}
	}
}
