package adaptor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/inbound"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// inboundReplayDeltaTickInterval drives the periodic check for
// in-flight replay-delta acquisitions — both the sub-task retry
// (peer rotation every subTaskRetryInterval=250ms) and the outer
// budget timeout (replayDeltaTimeout=10s). Tick must be at or below
// the sub-task interval so rotation signals aren't missed; 100ms
// adds a small safety margin without CPU cost (the tick body
// short-circuits in the common case of no pending work).
const inboundReplayDeltaTickInterval = 100 * time.Millisecond

// peerLedgerState tracks the latest ledger info reported by a peer.
type peerLedgerState struct {
	LedgerSeq  uint32
	LedgerHash [32]byte
}

// Router reads inbound messages from the P2P overlay and dispatches
// them to the consensus engine and adaptor.
type Router struct {
	engine      consensus.Engine
	adaptor     *Adaptor
	modeManager *ModeManager
	inbox       <-chan *peermanagement.InboundMessage
	logger      *slog.Logger

	// Peer ledger tracking for catch-up detection
	peersMu    sync.RWMutex
	peerStates map[peermanagement.PeerID]*peerLedgerState

	// Active legacy inbound ledger acquisition (nil when not acquiring).
	// Only one legacy acquisition runs at a time; the single-goroutine
	// handleMessage loop keeps that invariant trivially. Orthogonal to
	// replayer — a legacy acquisition and any number of replay-delta
	// acquisitions can coexist.
	inboundLedger *inbound.Ledger

	// replayer coordinates concurrent mtREPLAY_DELTA_REQUEST acquisitions
	// keyed by target ledger hash, under a configurable concurrency cap.
	// Replaces the single-slot inboundReplayDelta field from Gap 6 so a
	// catchup burst across many ledgers can parallelize instead of
	// serializing. Mirrors rippled's LedgerReplayer.
	replayer *inbound.Replayer

	// messageSeen dedups inbound proposal / validation payloads so the
	// reduce-relay slot only feeds on DUPLICATE arrivals, mirroring
	// rippled's HashRouter::addSuppressionPeer !added branch at
	// PeerImp.cpp:1730-1738. Counting first-seen messages would
	// accelerate selection and produce earlier squelches than rippled
	// does for the same traffic pattern.
	messageSeen *messageSuppression

	// manifests is the validator manifest cache. Wired by the
	// Components bootstrap so the router can apply inbound TMManifests
	// frames and — on Accepted — relay them to other peers.
	// May be nil in tests that don't exercise the manifest path.
	manifests *manifest.Cache

	// overlay is held so the router can relay accepted manifests
	// directly via Overlay.BroadcastExcept. Nil in tests that
	// construct a router without manifest support.
	overlay *peermanagement.Overlay

	// testManifestSender, when non-nil, replaces r.overlay for the
	// local-manifest emission paths (SendLocalManifestTo /
	// BroadcastLocalManifest). Tests install a fake here to observe
	// the emitted frame without standing up real listeners; production
	// leaves it nil so the real overlay is used.
	testManifestSender manifestSender
}

// messageDedupTTL is how long a proposal/validation hash is
// remembered for duplicate-detection purposes. Rippled uses a
// round-bounded HashRouter; 30s comfortably covers a consensus round
// while aging out cross-round stragglers so the dedup table doesn't
// grow unbounded under sustained gossip.
const messageDedupTTL = 30 * time.Second

// messageDedupMaxEntries caps the dedup table size. One entry per
// unique (validator, position, txSet, closeTime) tuple in a healthy
// 100-validator round; 4096 gives ~40x headroom before the trim
// fires. Cheap memory — 32-byte key + 24-byte time.
const messageDedupMaxEntries = 4096

// NewRouter creates a new Router.
func NewRouter(engine consensus.Engine, adaptor *Adaptor, modeManager *ModeManager, inbox <-chan *peermanagement.InboundMessage) *Router {
	logger := slog.Default().With("component", "consensus-router")
	return &Router{
		engine:      engine,
		adaptor:     adaptor,
		modeManager: modeManager,
		inbox:       inbox,
		logger:      logger,
		peerStates:  make(map[peermanagement.PeerID]*peerLedgerState),
		replayer:    inbound.NewReplayer(logger, inbound.SystemClock, inbound.DefaultMaxInFlightReplays),
		messageSeen: newMessageSuppression(messageDedupTTL, messageDedupMaxEntries),
	}
}

// SetManifestCache installs the validator-manifest cache and the
// overlay handle used to relay accepted manifests. Calling with a nil
// cache disables the TMManifests path (the dispatch switch silently
// drops inbound manifest frames). Safe to call before Run.
func (r *Router) SetManifestCache(cache *manifest.Cache, overlay *peermanagement.Overlay) {
	r.manifests = cache
	r.overlay = overlay
}

// SetInboundClock overrides the clock used by new inbound replay-delta
// acquisitions. Intended for tests that need to drive timeout behavior
// deterministically; production callers never invoke this.
func (r *Router) SetInboundClock(c inbound.Clock) {
	r.replayer.SetClock(c)
}

// StopReplayer drains the replayer's in-flight map and returns the
// number of acquisitions that were still pending at stop time. Called
// from Components.Stop() during graceful shutdown. Exposes only the
// count so callers don't reach into the replayer's internals.
func (r *Router) StopReplayer() int {
	if r.replayer == nil {
		return 0
	}
	return r.replayer.Stop()
}

// HandlePeerDisconnect drops all per-peer state the router holds for
// peerID: the peer's last-reported ledger, its status-change vote in
// the engine's getNetworkLedger fold, and any lingering acquisition
// references. Wired from the overlay's peer-disconnect callback at
// startup so the state is freed the instant the peer goes away,
// instead of lingering until the next ledger adoption happens to
// overwrite it.
func (r *Router) HandlePeerDisconnect(peerID peermanagement.PeerID) {
	r.peersMu.Lock()
	delete(r.peerStates, peerID)
	r.peersMu.Unlock()

	// Clear the peer's LCL vote so getNetworkLedger stops counting its
	// stale hash. The adaptor uses the zero LedgerID as a delete key.
	r.adaptor.UpdatePeerLCL(uint64(peerID), consensus.LedgerID{})
}

// Run reads messages from the overlay and dispatches them.
// It blocks until the context is cancelled. A periodic maintenance tick
// also runs in this loop to time out stuck inbound replay-delta
// acquisitions and fall back to the legacy mtGET_LEDGER path.
func (r *Router) Run(ctx context.Context) {
	ticker := time.NewTicker(inboundReplayDeltaTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-r.inbox:
			if !ok {
				return
			}
			r.handleMessage(msg)
		case <-ticker.C:
			r.maintenanceTick()
		}
	}
}

// maintenanceTick runs out-of-band housekeeping: detect replay-delta
// acquisitions that have outlived their timeout, abandon each, and
// re-issue via the legacy header+state path. Sharing the message-loop
// goroutine keeps a single writer against replayer's in-flight map for
// the abandon+reissue sequence below (the Replayer's own methods are
// independently goroutine-safe, but holding to a single writer here
// means we don't have to reason about a peer response racing the
// timeout fallback for the same hash).
func (r *Router) maintenanceTick() {
	// Sub-task retry loop: rotate peers on silent-peer timeouts BEFORE
	// the outer budget kicks in. Matches rippled's LedgerDeltaAcquire
	// peer-swap (LedgerReplayer.h:49-57 — 250ms × 10 rotations inside a
	// larger outer budget). Without rotation, a single silent peer
	// burns the full 10s before the legacy fallback fires.
	for _, rd := range r.replayer.SubTaskTimedOut() {
		tried := rd.TriedPeers()
		// Ask the overlay for a fresh replay-capable peer, excluding
		// every peer we've already tried for this hash.
		candidates := r.adaptor.ReplayCapablePeersExcluding(tried, 1)
		if len(candidates) == 0 {
			// No fresh peer available — can't rotate; the outer
			// budget below will eventually time this out and fall
			// back to the legacy path. Log so operators can see
			// replay-capacity exhaustion in diagnostics.
			r.logger.Debug("replay-delta sub-task timed out but no fresh peer available",
				"seq", rd.Seq(),
				"hash", fmt.Sprintf("%x", rd.Hash()),
				"retry_count", rd.RetryCount(),
			)
			continue
		}
		newPeer := candidates[0]
		rd.NoteSubTaskRetry(newPeer)
		if err := r.adaptor.RequestReplayDelta(newPeer, rd.Hash()); err != nil {
			r.logger.Debug("replay-delta retry request failed",
				"seq", rd.Seq(),
				"hash", fmt.Sprintf("%x", rd.Hash()),
				"peer", newPeer,
				"err", err,
			)
			// Next tick will try yet another peer. Continue rather
			// than return so we process other in-flight retries.
		}
	}

	// Reap acquisitions that exceeded the OUTER budget. At this point
	// either the sub-task loop exhausted retries or the overall
	// replayDeltaTimeout fired — either way, abandon and fall back.
	for _, entry := range r.replayer.TimedOut() {
		r.logger.Warn("replay delta acquisition timed out, falling back to legacy",
			"seq", entry.Seq,
			"hash", fmt.Sprintf("%x", entry.Hash[:8]),
			"peer", entry.PeerID,
		)
		r.replayer.Abandon(entry.Hash)
		r.startLedgerAcquisitionLegacy(entry.Seq, entry.Hash, entry.PeerID)
	}

	// Reap a stuck legacy inbound ledger. Without this a single stalled
	// acquisition blocks startLedgerAcquisitionLegacy from arming a new
	// request for the SAME hash on the next statusChange — and blocks
	// the replay-delta path too via isAcquiring's inboundLedger check.
	// Matches the spirit of rippled's InboundLedgers timeout sweeps.
	if il := r.inboundLedger; il != nil && il.IsTimedOut() {
		r.logger.Warn("legacy inbound ledger acquisition timed out",
			"seq", il.Seq(),
			"hash", fmt.Sprintf("%x", il.Hash()),
			"peer", il.PeerID(),
		)
		r.inboundLedger = nil
		// Do NOT re-issue from here: legacy has no retry partner, and
		// the next statusChange from any peer will naturally arm a
		// fresh acquisition via startLedgerAcquisition once the stuck
		// reference is cleared.
	}
}

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
	case message.TypeReplayDeltaResponse:
		r.handleReplayDeltaResponse(msg)
	case message.TypeManifests:
		r.handleManifests(msg)
	default:
		// Not a consensus message — ignore
	}
}

// handleManifests ingests a TMManifests frame. For each serialized
// manifest in the list: deserialize, apply to the cache, and — on
// Accepted — relay the single-manifest frame to every peer except the
// origin. Matches rippled's OverlayImpl::onManifests at
// OverlayImpl.cpp:633-686 (minus the DB persistence of UNL-master
// manifests and the pubManifest subscription — both out of scope for
// this PR per tasks/pr-manifests-round1.md).
//
// Decode failures attribute "manifest-decode" badData to the sender
// (mirrors rippled charging feeInvalidData on malformed TMManifests at
// PeerImp.cpp). A mix of valid and invalid entries in the same frame
// results in the valid ones being applied; the frame isn't rejected
// wholesale.
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
// frame (a list of one) — matching rippled's per-manifest relay
// (OverlayImpl.cpp:633-686 loops through and relays each one via
// overlay_.foreach). Shares its framing with the local-manifest
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

// Bounds used to reject malformed TMProposeSet / TMValidation frames
// before they reach the engine. Out-of-range values get
// feeInvalidData attributed to the sender, mirroring rippled's
// PeerImp charge on malformed consensus frames.
//
// signatureMinLen / signatureMaxLen bracket a valid DER-encoded
// secp256k1 signature (rippled rejects anything outside this range
// before attempting verify).
const (
	signatureMinLen = 64
	signatureMaxLen = 72
)

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

	// Bounds checks BEFORE the engine sees the frame. Rippled charges
	// feeInvalidData on malformed TMProposeSet at PeerImp — we mirror
	// that so a peer can't cost-free spam oversized or
	// implausibly-hoppy consensus traffic.
	if badField, ok := validateProposeBounds(proposeSet); !ok {
		r.logger.Debug("dropping malformed proposal",
			"peer", msg.PeerID, "bad_field", badField)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "proposal-malformed-"+badField)
		return
	}

	proposal := ProposalFromMessage(proposeSet)
	originPeer := uint64(msg.PeerID)

	// Record duplicate-status + last-sighting BEFORE OnProposal.
	// Hash the DECODED fields via hashProposalSuppression (matches
	// rippled's proposalUniqueId at RCLCxPeerPos.cpp:66-83). Hashing
	// the raw protobuf envelope would desync dedup from rippled peers
	// that see the same message with different optional-field framing
	// (e.g., deprecated `hops` included or omitted) — same semantic
	// proposal, but different byte payload.
	//
	// B3: stash the hash on the Proposal so the downstream relay path
	// can thread it to Overlay's reverse index without recomputing
	// (matches rippled's RCLCxPeerPos::suppressionID() instance member).
	suppressionHash := hashProposalSuppression(proposal)
	proposal.SuppressionHash = suppressionHash
	firstSeen, lastSeen := r.messageSeen.observe(suppressionHash)

	if err := r.engine.OnProposal(proposal, originPeer); err != nil {
		r.logger.Debug("engine rejected proposal", "error", err, "peer", msg.PeerID)
		return
	}

	// Feed the reduce-relay slot on duplicates that arrive within
	// IDLED of the previous sighting. Matches rippled
	// PeerImp.cpp:1730-1738 exactly: `!added && relayed &&
	// (now - *relayed) < IDLED`. NOTE: no trust gate here — rippled
	// feeds the slot for both trusted and untrusted duplicates
	// (trust-specific branching happens later for relay decisions).
	// Gating on trust pre-R5.7 under-squelched untrusted gossip
	// vs. what the rest of the network does.
	//
	// B3: feed the slot with the FULL set of known-havers, not just
	// originPeer. seenPeers is the overlay's reverse-index entry
	// populated when we previously relayed this message outward —
	// matching rippled's haveMessage set from overlay_.relay
	// (PeerImp.cpp:3010-3017) which is then passed whole to
	// updateSlotAndSquelch.
	if !firstSeen && time.Since(lastSeen) < peermanagement.Idled {
		seenPeers := r.adaptor.PeersThatHave(suppressionHash)
		r.adaptor.UpdateRelaySlot(proposal.NodeID[:], originPeer, seenPeers)
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

	// Post-parse bounds: the validation struct must carry sane hash
	// and signature sizes. Rippled drops and charges at PeerImp before
	// the engine sees it; same rationale as in handleProposal.
	if badField, ok := validateValidationBounds(validation); !ok {
		r.logger.Debug("dropping malformed validation",
			"peer", msg.PeerID, "bad_field", badField)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "validation-malformed-"+badField)
		return
	}

	originPeer := uint64(msg.PeerID)

	// Observe-before-engine for consistent duplicate accounting. Hash
	// the INNER STValidation blob carried in TMValidation.validation —
	// matches rippled's PeerImp.cpp:2374 (`sha512Half(makeSlice(
	// m->validation()))`). Hashing the TMValidation envelope instead
	// would desync dedup from rippled peers the same way handleProposal
	// would if it hashed the TMProposeSet envelope: deprecated outer
	// fields vary, inner canonical blob does not. We use the raw
	// inbound bytes here — NOT a re-serialized copy — so a lossy or
	// reordered round-trip can't silently diverge the hash.
	// B3: stash the hash on the Validation so the downstream relay
	// path can thread it to Overlay's reverse index without
	// recomputing (matches rippled's pattern of computing
	// sha512Half(m->validation()) once per inbound and carrying it).
	suppressionHash := hashValidationSuppression(val.Validation)
	validation.SuppressionHash = suppressionHash
	firstSeen, lastSeen := r.messageSeen.observe(suppressionHash)

	if err := r.engine.OnValidation(validation, originPeer); err != nil {
		r.logger.Debug("engine rejected validation", "error", err, "peer", msg.PeerID)
		return
	}

	// Same IDLED-gated, no-trust-gate feeding as handleProposal.
	// Mirrors rippled's TMValidation path at PeerImp.cpp:2385 and the
	// B3 haveMessage expansion at PeerImp.cpp:3049-3054.
	if !firstSeen && time.Since(lastSeen) < peermanagement.Idled {
		seenPeers := r.adaptor.PeersThatHave(suppressionHash)
		r.adaptor.UpdateRelaySlot(validation.NodeID[:], originPeer, seenPeers)
	}
}

// validateProposeBounds returns ("", true) when the decoded ProposeSet
// is within the bounds rippled enforces at PeerImp::onMessage; returns
// (field_label, false) on the first violation so the caller can
// attribute the charge with a specific reason.
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
	// ed25519 validators (0xED prefix) are not allowed in propose-set
	// per rippled PeerImp.cpp:1679-1680
	// (publicKeyType(...) != KeyType::secp256k1). The length-only check
	// would pass a 33-byte ed25519 key (0xED || 32 bytes), letting the
	// peer slip through without attribution, so the prefix gate runs
	// alongside the size gate.
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
	if v.NodeID == (consensus.NodeID{}) {
		return "node-id-zero", false
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
		return
	}

	blob := TransactionFromMessage(txMsg)
	if len(blob) == 0 {
		return
	}

	r.adaptor.AddPendingTx(blob)
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
		// Peer has a tx set we might need — if the engine is waiting for it,
		// we could request the full set. For now, just log.
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

func (r *Router) handleGetLedger(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeGetLedger, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode get_ledger", "error", err, "peer", msg.PeerID)
		return
	}
	req, ok := decoded.(*message.GetLedger)
	if !ok {
		return
	}

	r.logger.Debug("peer requests ledger",
		"peer", msg.PeerID,
		"itype", req.InfoType,
		"seq", req.LedgerSeq,
		"hash_len", len(req.LedgerHash),
	)

	// Only handle base (header) requests for now
	if req.InfoType != message.LedgerInfoBase {
		return
	}

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	// Find the requested ledger
	var l *ledger.Ledger
	if len(req.LedgerHash) == 32 {
		var hash [32]byte
		copy(hash[:], req.LedgerHash)
		l, err = svc.GetLedgerByHash(hash)
	} else if req.LedgerSeq > 0 {
		l, err = svc.GetLedgerBySequence(req.LedgerSeq)
	} else {
		l = svc.GetClosedLedger()
	}
	if err != nil || l == nil {
		return
	}

	hash := l.Hash()
	resp := &message.LedgerData{
		LedgerHash: hash[:],
		LedgerSeq:  l.Sequence(),
		InfoType:   message.LedgerInfoBase,
		Nodes: []message.LedgerNode{
			{NodeData: l.SerializeHeader()},
		},
		RequestCookie: uint32(req.RequestCookie),
	}

	frame, err := encodeFrame(message.TypeLedgerData, resp)
	if err != nil {
		r.logger.Warn("failed to encode ledger_data response", "error", err)
		return
	}

	if err := r.adaptor.SendToPeer(uint64(msg.PeerID), frame); err != nil {
		r.logger.Debug("failed to send ledger_data to peer", "error", err, "peer", msg.PeerID)
	}
}

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

	r.logger.Info("peer status change",
		"peer", msg.PeerID,
		"status", sc.NewStatus,
		"event", sc.NewEvent,
		"ledger_seq", sc.LedgerSeq,
		"needs_sync", r.adaptor.NeedsInitialSync(),
	)

	// Track peer's reported ledger state
	if sc.LedgerSeq > 0 {
		var peerHash [32]byte
		if len(sc.LedgerHash) == 32 {
			copy(peerHash[:], sc.LedgerHash)
		}
		var parentHash [32]byte
		if len(sc.LedgerHashPrevious) == 32 {
			copy(parentHash[:], sc.LedgerHashPrevious)
		}

		r.peersMu.Lock()
		r.peerStates[msg.PeerID] = &peerLedgerState{
			LedgerSeq:  sc.LedgerSeq,
			LedgerHash: peerHash,
		}
		r.peersMu.Unlock()

		// Surface the peer's reported LCL to the adaptor so the
		// engine's getNetworkLedger can consider it as a vote even
		// when no proposal has (yet) arrived from this peer.
		r.adaptor.UpdatePeerLCL(uint64(msg.PeerID), consensus.LedgerID(peerHash))

		// During initial sync, fetch full ledger from peer (like rippled).
		// Don't adopt with synthetic headers — wait for real state data.
		if r.adaptor.NeedsInitialSync() && sc.LedgerSeq > 1 {
			r.startLedgerAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
			return
		}

		// When in Full mode and significantly behind (gap > 2), acquire the
		// latest ledger from the peer but stay in Full mode so we keep
		// participating in consensus.
		if r.adaptor.GetOperatingMode() == consensus.OpModeFull && sc.LedgerSeq > 1 {
			svc := r.adaptor.LedgerService()
			if svc != nil {
				ourSeq := svc.GetClosedLedgerIndex()
				if sc.LedgerSeq > ourSeq+2 {
					r.logger.Warn("behind network while in Full mode, catching up",
						"our_seq", ourSeq,
						"peer_seq", sc.LedgerSeq,
						"gap", sc.LedgerSeq-ourSeq,
					)
					r.startLedgerAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
					return
				}
			}
		}

		// While not in Full mode, keep acquiring from peers until
		// we're within 1 ledger of the network.
		if r.adaptor.GetOperatingMode() != consensus.OpModeFull && sc.LedgerSeq > 1 {
			svc := r.adaptor.LedgerService()
			if svc != nil {
				ourSeq := svc.GetClosedLedgerIndex()
				if sc.LedgerSeq > ourSeq+1 {
					r.startLedgerAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
					return
				}
			}
		}

		// Hash-divergence catch-up. A late-join node (or a node whose
		// consensus ran in isolation while disconnected) can end up at
		// the same seq as its peers but with a different ledger hash.
		// The seq-based branches above don't fire because ourSeq ==
		// peerSeq; we need to detect that our LCL hash differs from the
		// peer's and acquire theirs. Mirrors rippled's wrongLedger mode
		// recovery path where the node asks a peer for the fork it's
		// seeing network consensus on. Only fire if we're NOT already
		// acquiring that hash (startLedgerAcquisition dedupes internally
		// via the replayer / inboundLedger guards, but checking here
		// saves a lookup in the hot path).
		svc := r.adaptor.LedgerService()
		if svc != nil && sc.LedgerSeq > 1 && len(sc.LedgerHash) == 32 {
			closed := svc.GetClosedLedger()
			if closed != nil {
				ourSeq := closed.Sequence()
				ourHash := closed.Hash()
				if ourSeq == sc.LedgerSeq && ourHash != peerHash {
					r.logger.Warn("ledger hash divergence at same seq, acquiring peer's ledger",
						"seq", sc.LedgerSeq,
						"our_hash", fmt.Sprintf("%x", ourHash[:8]),
						"peer_hash", fmt.Sprintf("%x", peerHash[:8]),
						"peer", msg.PeerID,
					)
					r.startLedgerAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
					return
				}
			}
		}

		// Check if we're behind and need to catch up
		r.checkBehind(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
	}
}

// startLedgerAcquisition picks the best available ledger-acquisition
// strategy for the given target. When we have the parent ledger locally
// and the peer advertises ledger-replay, the bandwidth-efficient
// replay-delta protocol is preferred (one request returns header + every
// tx blob); otherwise we fall back to the legacy mtGET_LEDGER
// header+state walk. Mirrors rippled's preference for LedgerDeltaAcquire
// over InboundLedger when the parent is available.
//
// This is currently the only driver of startReplayDeltaAcquisition: it
// handles a single target ledger per call. The Replayer coordinator
// supports concurrent acquisitions across many hashes, but the policy
// layer that walks a range (e.g., backward from a peer's tip via
// ParentHash, à la rippled's LedgerReplayer) is a follow-up item — the
// Gap 7 deliverable is the coordinator itself and the migration off
// the single-slot field.
func (r *Router) startLedgerAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	// Unified dedup across BOTH acquisition paths. A prior fix only
	// checked r.replayer.Has(hash); that still allowed the cross-path
	// race where two status changes at the same seq with different
	// hashes armed both a replay-delta AND a legacy acquisition
	// simultaneously, with adoption order then deciding which won.
	// Stricter than rippled: rippled's InboundLedgers and
	// LedgerDeltaAcquire maintain SEPARATE per-state-machine maps, so
	// the same hash can in principle acquire through both paths
	// concurrently there too. Our single-point-of-truth check is a
	// tighter guarantee than the rippled reference — a deliberate
	// narrowing, not a mirror.
	if r.isAcquiring(hash) {
		return
	}

	parent := r.adaptor.GetParentLedgerForReplay(seq)
	if parent != nil && r.adaptor.PeerSupportsReplay(peerID) {
		if err := r.startReplayDeltaAcquisition(seq, hash, peerID, parent); err == nil {
			return
		}
		// Fall through to the legacy path on issue failure.
	}
	r.startLedgerAcquisitionLegacy(seq, hash, peerID)
}

// isAcquiring reports whether an acquisition — replay-delta or legacy
// — is currently in flight for the given ledger hash. Used as the
// single dedup entry point so a race between a replay-delta and a
// legacy acquisition for the same hash is impossible.
func (r *Router) isAcquiring(hash [32]byte) bool {
	if r.replayer.Has(hash) {
		return true
	}
	if r.inboundLedger != nil && r.inboundLedger.Hash() == hash {
		return true
	}
	return false
}

// startReplayDeltaAcquisition registers a new acquisition with the
// Replayer coordinator and issues the corresponding
// mtREPLAY_DELTA_REQUEST. Mirrors rippled's LedgerDeltaAcquire::trigger.
//
// Returns ErrAcquisitionExists if a request for the same hash is
// already in flight (caller should drop the duplicate), ErrCapacityFull
// if the coordinator is at cap (caller falls back to legacy), or the
// wire-send error if the request itself failed (coordinator slot is
// freed before returning so the caller can retry).
func (r *Router) startReplayDeltaAcquisition(seq uint32, hash [32]byte, peerID uint64, parent *ledger.Ledger) error {
	rd, err := r.replayer.Acquire(hash, peerID, parent)
	if err != nil {
		return err
	}
	_ = rd // retained in replayer; HandleResponse retrieves it on reply.
	r.logger.Info("starting replay delta acquisition",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)
	if err := r.adaptor.RequestReplayDelta(peerID, hash); err != nil {
		r.logger.Warn("failed to request replay delta from peer", "error", err)
		r.replayer.Abandon(hash)
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
func (r *Router) startLedgerAcquisitionLegacy(seq uint32, hash [32]byte, peerID uint64) {
	// Safety net: if a replay-delta for the same hash is still
	// registered, don't start a legacy on top of it — one path is
	// always enough.
	if r.replayer.Has(hash) {
		return
	}

	// If already acquiring this exact hash, skip
	if r.inboundLedger != nil {
		if r.inboundLedger.Hash() == hash {
			return
		}
		// Acquiring a different (older) hash — abandon it for the newer one
		if r.inboundLedger.IsTimedOut() {
			r.logger.Info("inbound ledger: timed out, retrying with new peer",
				"old_seq", r.inboundLedger.Seq(),
				"new_seq", seq,
			)
		}
		r.inboundLedger = nil
	}

	r.logger.Info("starting ledger acquisition (legacy)",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)

	r.inboundLedger = inbound.New(hash, seq, peerID, r.logger)
	if err := r.adaptor.RequestLedgerBaseFromPeer(peerID, hash, seq); err != nil {
		r.logger.Warn("failed to request ledger base from peer", "error", err)
		r.inboundLedger = nil
	}
}

// handleReplayDeltaResponse verifies an inbound mtREPLAY_DELTA_RESPONSE
// against its matching in-flight acquisition (routed by ledger hash)
// and adopts the resulting ledger. On verification or apply failure the
// acquisition is abandoned and the legacy path is started for the same
// target. Unsolicited/stale responses (no matching acquisition) are
// silently dropped — rippled does the same, and it's a normal race
// when a peer batch-forwards replies after we've already moved on.
func (r *Router) handleReplayDeltaResponse(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeReplayDeltaResponse, msg.Payload)
	if err != nil {
		r.logger.Debug("failed to decode replay delta response", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "replay-delta-resp-decode")
		return
	}
	resp, ok := decoded.(*message.ReplayDeltaResponse)
	if !ok || resp == nil {
		return
	}

	rd, err := r.replayer.HandleResponse(resp)
	if errors.Is(err, inbound.ErrNoMatchingAcquisition) {
		// Stale or unsolicited — drop silently without charging the
		// peer. A misbehaving peer sending genuinely bogus data would
		// fail its ACTIVE acquisition's verifier (branch below), which
		// IS attributed via IncPeerBadData.
		r.logger.Debug("replay delta response with no matching acquisition",
			"peer", msg.PeerID)
		return
	}
	if err != nil {
		// Verification failed. rd is still registered in the Replayer so
		// we can read its provenance before abandoning the slot.
		seq := rd.Seq()
		hash := rd.Hash()
		peerID := rd.PeerID()
		r.replayer.Abandon(hash)
		r.logger.Warn("replay delta verification failed; falling back to legacy",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		r.adaptor.IncPeerBadData(peerID, "replay-delta-verify")
		r.startLedgerAcquisitionLegacy(seq, hash, peerID)
		return
	}

	// GotResponse verified the header hash and the tx-map root. Apply
	// re-derives the post-state by replaying every tx through the
	// engine against a mutable copy of the parent's state, then
	// verifies the resulting AccountHash matches the target header —
	// the only proof we have that our engine doesn't diverge from
	// rippled. Without this step the adopted ledger would carry the
	// parent's stale state map, breaking consensus on the next round.
	parent := rd.Parent()
	engineCfg := r.adaptor.EngineConfigForReplay(parent)
	derived, err := rd.Apply(engineCfg)
	if err != nil {
		seq := rd.Seq()
		hash := rd.Hash()
		peerID := rd.PeerID()
		r.replayer.Abandon(hash)
		// DO NOT charge the peer here. GotResponse already verified the
		// peer's header hash and tx-map root; a subsequent Apply failure
		// means OUR engine produced a divergent AccountHash — an engine
		// bug, not peer misbehavior. Charging here would wrongly evict
		// honest peers for our bugs. Matches rippled's
		// LedgerDeltaAcquire::tryBuild (LedgerDeltaAcquire.cpp:211-223)
		// which fails silently on state-map divergence.
		r.logger.Error("ENGINE DIVERGENCE: replay delta apply failed; falling back to legacy",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		r.startLedgerAcquisitionLegacy(seq, hash, peerID)
		return
	}
	r.replayer.Complete(rd.Hash())
	if err := r.adoptVerifiedLedger(derived); err != nil {
		r.logger.Warn("failed to adopt replay-delta ledger", "error", err)
	}
}

// adoptVerifiedLedger commits a ledger reconstructed from a verified
// replay delta. Mirrors completeInboundLedger's adoption logic: install
// state and tx maps via the ledger service, advance to Tracking if
// we're below it, and log the new tip. Mirrors rippled's
// LedgerDeltaAcquire.cpp:209 which installs the peer-provided tx-blob
// tree alongside the state map.
//
// F6: routed through SubmitHeldAdoption rather than AdoptLedgerWithState.
// This gives us rippled's tryAdvance behavior for free: if the awaited
// parent seq is already in history and its hash matches the verified
// ledger's ParentHash, SubmitHeldAdoption fast-paths into the immediate
// adopt (same call-shape as before). If the parent hasn't landed yet
// — i.e. the replay-delta arrived out of order — the ledger is stashed
// in the held-adoptions map keyed by its awaited parent seq, and
// cascade-promoted automatically from inside the parent's eventual
// adopt. The router no longer needs to observe ordering.
func (r *Router) adoptVerifiedLedger(l *ledger.Ledger) error {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return errors.New("no ledger service")
	}
	hdr := l.Header()
	stateMap, err := l.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot state map: %w", err)
	}
	// Pass the verified tx map through so the adopted ledger carries
	// real transactions — without this, tx/tx_history/account_tx RPCs
	// can't answer for replay-delta-adopted ledgers and we can't
	// re-serve the replay-delta to other peers. See R5.1.
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot tx map: %w", err)
	}
	// context.TODO: adoptVerifiedLedger is reached from a peer-message
	// handler stack that does not currently carry a context. Threading
	// one through the message-dispatch chain is tracked separately from
	// this issue (#185).
	if err := svc.SubmitHeldAdoption(context.TODO(), &hdr, stateMap, txMap); err != nil {
		return fmt.Errorf("adopt with state: %w", err)
	}
	if r.adaptor.GetOperatingMode() < consensus.OpModeTracking {
		r.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
	r.logger.Info("adopted ledger via replay delta",
		"seq", hdr.LedgerIndex,
		"hash", fmt.Sprintf("%x", hdr.Hash[:8]),
	)
	// Notify the consensus engine so it can flip out of
	// ModeWrongLedger via Engine.OnLedger (rcl/engine.go:801). Without
	// this, the engine remains stuck in wrongLedger indefinitely after
	// a successful inbound acquisition. Issue #359.
	if r.engine != nil {
		if err := r.engine.OnLedger(consensus.LedgerID(hdr.Hash), nil); err != nil {
			r.logger.Debug("engine rejected adopted ledger", "error", err, "seq", hdr.LedgerIndex)
		}
	}
	return nil
}

// checkBehind decides what to do based on how far behind a peer
// reports. Two outcomes:
//
//   - peerSeq <= ourSeq+1: we're caught up. If still in Tracking and
//     our LCL hash matches peers' majority, transition to Full.
//     Otherwise stay in Tracking — the hash-mismatch branch in
//     handleStatusChange will have already fired the right acquisition.
//   - peerSeq > ourSeq+1: we're behind by more than one ledger. Arm a
//     single acquisition for the peer's tip. Subsequent status changes
//     from peers will chain more acquisitions forward as we adopt each
//     ledger and ourSeq advances.
//
// Only one acquisition fires per call. A faster "range walk" that
// issues concurrent requests for every seq between ourLCL+1 and
// peerSeq would need the intermediate ledger hashes, which we don't
// know until each acquired header reveals its ParentHash. Rippled's
// LedgerReplayer does that backward chain; we rely on forward status
// gossip instead. Replayer already supports concurrent in-flight
// acquisitions, so switching to backward-walk later is a localized
// change in this function.
func (r *Router) checkBehind(peerSeq uint32, peerHash [32]byte, peerID uint64) {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	ourSeq := svc.GetClosedLedgerIndex()

	// If we're caught up (gap ≤ 1) and not yet Full, transition to Full
	// only if our LCL hash matches what the majority of peers report.
	if peerSeq <= ourSeq+1 {
		if r.adaptor.GetOperatingMode() == consensus.OpModeTracking {
			if r.ourLCLMatchesPeers() {
				r.logger.Info("caught up with network, transitioning to Full",
					"our_seq", ourSeq,
					"peer_seq", peerSeq,
				)
				r.adaptor.SetOperatingMode(consensus.OpModeFull)
			} else {
				r.logger.Info("caught up but LCL hash differs, staying in Tracking",
					"our_seq", ourSeq,
					"peer_seq", peerSeq,
				)
			}
		}
		return
	}

	r.logger.Info("behind network, acquiring peer tip",
		"our_seq", ourSeq,
		"peer_seq", peerSeq,
		"gap", peerSeq-ourSeq,
		"peer", peerID,
	)

	// Arm a real acquisition instead of broadcasting a bare
	// mtGET_LEDGER. RequestLedgerByHashAndSeq would broadcast the
	// request but never arm the InboundLedger state machine, so any
	// response would arrive with no active consumer and be dropped.
	// startLedgerAcquisition picks replay-delta or legacy per the
	// routing policy and both paths install their own state machines.
	r.startLedgerAcquisition(peerSeq, peerHash, peerID)
}

// ourLCLMatchesPeers checks if our closed ledger hash matches what the
// majority of tracked peers report. Returns true if we have no peer data
// (to avoid blocking startup).
func (r *Router) ourLCLMatchesPeers() bool {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return true
	}
	closedLedger := svc.GetClosedLedger()
	if closedLedger == nil {
		return true
	}
	ourHash := closedLedger.Hash()
	ourSeq := svc.GetClosedLedgerIndex()

	r.peersMu.RLock()
	defer r.peersMu.RUnlock()

	if len(r.peerStates) == 0 {
		return true
	}

	matching := 0
	total := 0
	for _, ps := range r.peerStates {
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

func (r *Router) handleLedgerData(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeLedgerData, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode ledger_data", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "ledger-data-decode")
		return
	}
	ld, ok := decoded.(*message.LedgerData)
	if !ok {
		return
	}

	r.logger.Info("received ledger data",
		"peer", msg.PeerID,
		"seq", ld.LedgerSeq,
		"nodes", len(ld.Nodes),
		"itype", ld.InfoType,
		"has_inbound", r.inboundLedger != nil,
	)

	// Feed data to active inbound ledger acquisition
	if r.inboundLedger != nil {
		if r.handleInboundLedgerData(ld) {
			return
		}
		// If handleInboundLedgerData returned false (e.g. GotBase failed),
		// fall through to the legacy header-only adoption path
	}

	// During initial sync, try to adopt the ledger header from peers
	if ld.InfoType == message.LedgerInfoBase && len(ld.Nodes) > 0 && r.adaptor.NeedsInitialSync() {
		headerData := ld.Nodes[0].NodeData
		if err := r.adaptor.AdoptLedgerFromHeader(headerData); err != nil {
			r.logger.Debug("failed to adopt ledger header", "error", err, "peer", msg.PeerID)
		} else {
			r.logger.Info("adopted ledger from peer",
				"seq", ld.LedgerSeq,
				"peer", msg.PeerID,
			)
			return
		}
	}

	// Pass the ledger data to the consensus engine
	if len(ld.LedgerHash) == 32 {
		var ledgerID consensus.LedgerID
		copy(ledgerID[:], ld.LedgerHash)

		var payload []byte
		for _, node := range ld.Nodes {
			payload = append(payload, node.NodeData...)
		}

		if err := r.engine.OnLedger(ledgerID, payload); err != nil {
			r.logger.Debug("engine rejected ledger data", "error", err, "peer", msg.PeerID)
		}
	}
}

// handleInboundLedgerData feeds LedgerData to the active InboundLedger acquisition.
// Returns true if the data was consumed by the acquisition.
func (r *Router) handleInboundLedgerData(ld *message.LedgerData) bool {
	il := r.inboundLedger
	if il == nil {
		return false
	}

	// Verify the response is for our active acquisition
	if len(ld.LedgerHash) == 32 {
		expectedHash := il.Hash()
		var responseHash [32]byte
		copy(responseHash[:], ld.LedgerHash)
		if responseHash != expectedHash {
			return false // Not for us
		}
	}

	switch ld.InfoType {
	case message.LedgerInfoBase:
		// Phase 1: Got header + root nodes
		if len(ld.Nodes) < 2 {
			// Response doesn't include root nodes — can't do full acquisition.
			// Clear inbound and fall through to legacy adoption.
			r.logger.Debug("inbound ledger: response has < 2 nodes, falling back", "nodes", len(ld.Nodes))
			r.inboundLedger = nil
			return false
		}
		if err := il.GotBase(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotBase failed, falling back", "error", err)
			r.adaptor.IncPeerBadData(il.PeerID(), "ledger-data-base")
			r.inboundLedger = nil
			return false
		}

		if il.IsComplete() {
			r.completeInboundLedger()
			return true
		}

		// Request missing state nodes
		nodeIDs := il.NeedsMissingNodeIDs()
		if len(nodeIDs) > 0 {
			if err := r.adaptor.RequestStateNodes(il.PeerID(), il.Hash(), nodeIDs); err != nil {
				r.logger.Warn("inbound ledger: failed to request state nodes", "error", err)
			}
		}
		return true

	case message.LedgerInfoAsNode:
		// Phase 2: Got state tree nodes
		if err := il.GotStateNodes(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotStateNodes failed", "error", err)
			r.adaptor.IncPeerBadData(il.PeerID(), "ledger-data-state")
			return true
		}

		if il.IsComplete() {
			r.completeInboundLedger()
			return true
		}

		// Request more missing nodes if needed
		nodeIDs := il.NeedsMissingNodeIDs()
		if len(nodeIDs) > 0 {
			if err := r.adaptor.RequestStateNodes(il.PeerID(), il.Hash(), nodeIDs); err != nil {
				r.logger.Warn("inbound ledger: failed to request state nodes", "error", err)
			}
		}
		return true
	}

	return false
}

// completeInboundLedger finalizes an InboundLedger acquisition and adopts the ledger.
func (r *Router) completeInboundLedger() {
	il := r.inboundLedger
	r.inboundLedger = nil

	h, stateMap, err := il.Result()
	if err != nil {
		r.logger.Warn("inbound ledger: failed to get result", "error", err)
		return
	}

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	// Legacy header+state catchup path: no per-ledger tx tree is
	// fetched in this mode (only the header and state map), so pass
	// nil and let the service install the genesis-shaped empty tx
	// map. The replay-delta path at adoptVerifiedLedger (above)
	// passes the verified tx map — see R5.1.
	//
	// F6: same as the replay-delta path, route through
	// SubmitHeldAdoption so out-of-order catchup arrivals either
	// fast-path (parent already present) or stash for cascade when
	// the awaited parent lands. Legacy mtGET_LEDGER is sequential at
	// the wire level today, but nothing in the protocol forbids
	// interleaving — the held-queue is the correct seam regardless.
	// context.TODO: same as adoptVerifiedLedger — reached from a peer-
	// message handler stack with no plumbed context. See note there.
	if err := svc.SubmitHeldAdoption(context.TODO(), h, stateMap, nil); err != nil {
		r.logger.Warn("inbound ledger: failed to adopt with state", "error", err)
		return
	}

	// Only upgrade to Tracking if still in a lower mode.
	// Never demote from Full — that would break consensus participation.
	if r.adaptor.GetOperatingMode() < consensus.OpModeTracking {
		r.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
	r.logger.Info("adopted ledger with full state from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
	)
	// Notify the consensus engine so it can flip out of
	// ModeWrongLedger via Engine.OnLedger. Mirrors the replay-delta
	// path in adoptVerifiedLedger — see Issue #359.
	if r.engine != nil {
		if err := r.engine.OnLedger(consensus.LedgerID(h.Hash), nil); err != nil {
			r.logger.Debug("engine rejected adopted ledger", "error", err, "seq", h.LedgerIndex)
		}
	}
}
