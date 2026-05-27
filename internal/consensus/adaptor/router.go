package adaptor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/inbound"
	"github.com/LeJamon/goXRPLd/internal/ledger/openledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	validatorlist "github.com/LeJamon/goXRPLd/internal/validator/list"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/shamap"
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

	// fetchTracker aggregates the classic legacy acquisitions for the
	// fetch_info RPC (rippled's InboundLedgers). Populated from this
	// goroutine via Track; queried from RPC goroutines via FetchInfo,
	// which reads the acquisitions' own mutex-guarded state.
	fetchTracker *inbound.Tracker

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

	// validatorList is the publisher-trust subsystem. Wired by the
	// Components bootstrap when validator_list_keys is configured. Nil
	// in standalone-mode or when no publisher trust is configured —
	// the dispatch switch silently drops TMValidatorList /
	// TMValidatorListCollection frames in that case.
	validatorList *validatorlist.Aggregator

	// overrideManifestSender, when non-nil, replaces r.overlay for the
	// local-manifest emission paths (SendLocalManifestTo /
	// BroadcastLocalManifest). Tests install a fake here to observe
	// the emitted frame without standing up real listeners; production
	// leaves it nil so the real overlay is used. The relayManifest
	// path still needs r.overlay directly because BroadcastExcept has
	// no equivalent on the sender interface.
	overrideManifestSender manifestSender

	// manifestFrameMu guards the cached TMManifests emission frame and
	// its companion sequence cursor. Mirrors the (manifestMessage_,
	// manifestListSeq_) pair on rippled's OverlayImpl
	// (OverlayImpl.cpp:1184-1212): re-encode only when manifests.Sequence
	// has advanced past the value seen at last build, so back-to-back
	// peer connects reuse the same encoded bytes without re-walking the
	// cache. manifestFrameBuilt is the never-built sentinel — a zero
	// manifestFrameSeq is a valid cursor (a fresh cache starts at 0),
	// so we need an explicit "have we ever built?" flag rather than
	// using the zero value as the sentinel.
	manifestFrameMu    sync.Mutex
	manifestFrame      []byte
	manifestFrameSeq   uint64
	manifestFrameBuilt bool

	// In-flight tx-set acquisition state keyed by tx-set ID.
	// Each entry's SHAMap accumulates across multiple TMLedgerData
	// responses (rippled's TransactionAcquire) until the tree is
	// complete and leaves are handed to engine.OnTxSet.
	txSetAcquireMu sync.Mutex
	txSetAcquire   map[consensus.TxSetID]*txSetAcquireState

	// Retry-loop knobs for tx-set acquisition (issue #420). Set to
	// production defaults by NewRouter; tests inject smaller values via
	// SetTxSetRetryKnobsForTest so they don't sleep for the production
	// 250ms throttle window. See txSetRetryKnobs for the meaning of
	// each field.
	txSetRetryKnobs txSetRetryKnobs

	// activeTask, when non-nil, is the LedgerReplayTask currently
	// driving a multi-ledger backward catch-up. Single-task design:
	// deep catch-up is a one-shot operation, and serializing the
	// task entry point avoids the rippled-style MAX_TASKS bookkeeping
	// for now. Guarded by replayTaskMu.
	replayTaskMu sync.Mutex
	activeTask   *activeReplayTask
}

type txSetAcquireState struct {
	txMap      *shamap.SHAMap
	startedAt  time.Time
	lastUpdate time.Time

	// Retry bookkeeping for issue #420. lastRequest is when we most
	// recently broadcast a RequestTxSetMissingNodes; attempts counts
	// those broadcasts so we can surface failure after a cap rather
	// than spinning forever. peerNonProgress tracks consecutive
	// TMLedgerData responses from a peer that failed to extend the
	// SHAMap; peers at or over the per-peer threshold are skipped
	// during the next broadcast.
	lastRequest     time.Time
	attempts        int
	peerNonProgress map[uint64]int
}

// 60s covers consensus round (LedgerMaxConsensus ~15s) plus retries with
// margin while bounding memory under a stalled consumer.
const txSetAcquireTTL = 60 * time.Second

// txSetRetryKnobs collects the tunable parameters of the tx-set acquire
// retry loop (issue #420). goXRPL's retry is event-driven (one tick
// per inbound TMLedgerData), so these adapt — rather than directly
// mirror — rippled's timer-driven cadence in TransactionAcquire.cpp.
//
//   - MinInterval: minimum spacing between successive broadcasts for
//     the same acquisition. We reuse rippled's TX_ACQUIRE_TIMEOUT
//     period (TransactionAcquire.cpp:34, 250ms) as the cadence ceiling
//     so a chatty peer can't drive us faster than rippled's own timer
//     would have.
//   - MaxAttempts: hard cap on broadcasts per acquisition. Matches
//     rippled's MAX_TIMEOUTS = 20 (TransactionAcquire.cpp:38) — the
//     point at which rippled's onTimer flips failed_ and gives up.
//   - PeerNonProgressThreshold: consecutive non-progressing
//     TMLedgerData replies from one peer before it is skipped on the
//     next broadcast. goXRPL-specific: rippled selects peers via the
//     hasTxSet predicate and does not score them per-acquire. 3 is
//     small enough to react quickly to a truly stuck peer and large
//     enough to ride out a transient empty reply.
type txSetRetryKnobs struct {
	MinInterval              time.Duration
	MaxAttempts              int
	PeerNonProgressThreshold int
}

func defaultTxSetRetryKnobs() txSetRetryKnobs {
	return txSetRetryKnobs{
		MinInterval:              250 * time.Millisecond,
		MaxAttempts:              20,
		PeerNonProgressThreshold: 3,
	}
}

// SetTxSetRetryKnobsForTest overrides the issue-#420 retry knobs on
// this Router. Tests use it to dial timings down so they don't sleep
// for the production throttle window. The lock matches the read in
// handleTxSetData so racing this against an active inbox goroutine
// is safe under -race; production is not expected to call this.
func (r *Router) SetTxSetRetryKnobsForTest(knobs txSetRetryKnobs) {
	r.txSetAcquireMu.Lock()
	defer r.txSetAcquireMu.Unlock()
	r.txSetRetryKnobs = knobs
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
	r := &Router{
		engine:          engine,
		adaptor:         adaptor,
		modeManager:     modeManager,
		inbox:           inbox,
		logger:          logger,
		peerStates:      make(map[peermanagement.PeerID]*peerLedgerState),
		replayer:        inbound.NewReplayer(logger, inbound.SystemClock, inbound.DefaultMaxInFlightReplays),
		fetchTracker:    inbound.NewTracker(),
		messageSeen:     newMessageSuppression(messageDedupTTL, messageDedupMaxEntries),
		txSetAcquire:    make(map[consensus.TxSetID]*txSetAcquireState),
		txSetRetryKnobs: defaultTxSetRetryKnobs(),
	}
	// Wire the stash → acquisition hook so quorum decisions on unknown
	// ledgers don't sit silently in pendingLedgerValidations.
	if adaptor != nil {
		if svc := adaptor.LedgerService(); svc != nil {
			svc.SetOnPendingValidationStashed(r.armValidationStashAcquisition)
		}
		// Wire stillNeed re-arm so every consensus re-ask of an
		// in-flight tx-set clears the per-acquisition throttle and
		// attempt-cap state. Mirrors rippled's
		// TransactionAcquire::stillNeed (TransactionAcquire.cpp:256-264)
		// triggered from InboundTransactionsImp::getSet
		// (InboundTransactions.cpp:107-114). Issue #420.
		adaptor.SetOnTxSetRequested(r.MarkTxSetStillNeeded)
	}
	return r
}

// SetManifestCache installs the validator-manifest cache and the
// overlay handle used to relay accepted manifests. Calling with a nil
// cache disables the TMManifests path (the dispatch switch silently
// drops inbound manifest frames). Safe to call before Run.
func (r *Router) SetManifestCache(cache *manifest.Cache, overlay *peermanagement.Overlay) {
	r.manifests = cache
	r.overlay = overlay
}

// SetValidatorListAggregator installs the publisher-trust subsystem.
// Calling with a nil aggregator disables the TMValidatorList /
// TMValidatorListCollection paths — the dispatch switch silently
// drops inbound frames in that case. Safe to call before Run.
func (r *Router) SetValidatorListAggregator(agg *validatorlist.Aggregator) {
	r.validatorList = agg
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

	// Drop the peer's per-publisher sequence record so the publisher-
	// trust aggregator's peerSeq map doesn't grow unbounded across the
	// lifetime of the process. Mirrors the implicit cleanup rippled
	// gets from publisherListSequences_ living on the PeerImp itself
	// (PeerImp.h:183) — when the peer object dies the map dies with it.
	if r.validatorList != nil {
		r.validatorList.ForgetPeer(uint64(peerID))
	}
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
		// Dispatch the actual network send in a goroutine so a slow or
		// back-pressured overlay write doesn't block r.inbox ingest.
		// Replayer-state mutation (NoteSubTaskRetry above) already
		// happened on the loop goroutine, preserving the single-writer
		// invariant against handleMessage; on send failure the next
		// tick will rotate to another peer (the per-hash timeout
		// continues to run).
		seq := rd.Seq()
		hash := rd.Hash()
		go func() {
			if err := r.adaptor.RequestReplayDelta(newPeer, hash); err != nil {
				r.logger.Debug("replay-delta retry request failed",
					"seq", seq,
					"hash", fmt.Sprintf("%x", hash),
					"peer", newPeer,
					"err", err,
				)
			}
		}()
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

	// Reap an in-flight skip-list whose outer budget expired. The
	// router holds exactly one active task at a time; aborting it
	// frees the slot for the next StartReplayTask. Without this a
	// silent peer wedges the activeTask gate indefinitely.
	if timedOut := r.replayer.SkipListTimedOut(); len(timedOut) > 0 {
		r.logger.Warn("skip-list acquisition timed out; aborting replay task",
			"count", len(timedOut),
		)
		for _, h := range timedOut {
			r.replayer.AbandonSkipList(h)
		}
		r.AbortActiveReplayTask(errors.New("skip-list timed out"))
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
	case message.TypeProofPathResponse:
		r.handleProofPathResponse(msg)
	case message.TypeManifests:
		r.handleManifests(msg)
	case message.TypeValidatorList:
		r.handleValidatorList(msg)
	case message.TypeValidatorListCollection:
		r.handleValidatorListCollection(msg)
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
	r.resolveMasterNodeID(&proposal.NodeID, proposal.SigningPubKey)
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

	// Drop duplicates before the engine path (re-running OnProposal
	// just re-verifies ECDSA). Still feed the IDLED-gated relay slot
	// on dupes for squelch accounting.
	//
	// Deliberate deviation from rippled: rippled tracks suppression
	// per (hash, peer) — PeerImp.cpp:1730-1738 addSuppressionPeerWithStatus
	// returns added=true for a new (hash, peer) pair, re-running the
	// handler so per-peer slot entries grow on each new sender. Our
	// dedup is hash-only, so a second peer's copy is dropped at the
	// gate. Quorum/position tracking unaffected (first arrival counts
	// the validator); reduce-relay accuracy is partly compensated via
	// PeersThatHave + UpdateRelaySlot below.
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
	_ = lastSeen
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

	// Drop duplicates before the engine path (re-running OnValidation
	// just re-verifies ECDSA, dominating CPU under gossip fan-out).
	// Still update the relay slot for squelch accounting.
	//
	// Deliberate deviation from rippled: rippled's per-(hash, peer)
	// suppression at PeerImp.cpp:2374-2424 re-processes new senders;
	// our hash-only dedup drops them at the gate. See handleProposal
	// for the full rationale.
	if !firstSeen {
		if time.Since(lastSeen) < peermanagement.Idled {
			seenPeers := r.adaptor.PeersThatHave(suppressionHash)
			r.adaptor.UpdateRelaySlot(validation.SigningPubKey[:], originPeer, seenPeers)
		}
		return
	}

	if err := r.engine.OnValidation(validation, originPeer); err != nil {
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

	_ = lastSeen
}

// resolveMasterNodeID looks the inbound signing pubkey up in the
// manifest cache and, when a manifest binds it to a master pubkey,
// rewrites *nid to calcNodeID(masterKey). Mirrors rippled's
// RCLValidations.cpp:165-186 calcNodeID(getTrustedKey(signingKey) ??
// signingKey): in the absence of a manifest mapping the parser's
// initial calcNodeID(signingKey) value is preserved untouched, so
// non-rotated validators still round-trip through the engine on the
// signing-derived NodeID.
//
// Wiring: the manifest cache is installed on the router via
// SetManifestCache before Run(). When the cache is nil (tests
// constructing a bare router), this is a no-op and the parser default
// stands.
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

	// Mirrors rippled NetworkOPs.cpp:1685-1712 — relay immediately on
	// the inbound job, not one ledger later via OpenLedger.Accept's
	// once-per-LCL callback; that one-ledger lag is a direct
	// contributor to memory issue-401 tx-propagation latency.
	//
	// Gate: rippled relays only when `e.applied || e.result == terQUEUED`
	// for the peer-relay case (e.local=false, FailHard::no collapses the
	// middle branch). openledger.Submit folds terQUEUED into
	// ResultSuccess (openledger.go:381-385) and tec into ResultSuccess
	// (apply.go:128-139), so ResultSuccess is the exact superset of
	// rippled's applied|terQUEUED. ResultRetry (non-queued ter*) and
	// ResultFailure (tef/tem/tel) do NOT relay.
	if err == nil && res == openledger.ResultSuccess {
		r.relayTransaction(msg.PeerID, blob)
	}
}

// relayTransaction rebroadcasts an accepted peer-originated TMTransaction.
// Mirrors rippled's overlay_.relay(*stx, txID, toSkip) call in
// processTransaction at NetworkOPs.cpp:1710, where toSkip is the suppression
// set produced by HashRouter::shouldRelay — i.e. the originating peer.
//
// The outbound wire shape mirrors rippled NetworkOPs.cpp:1700-1708:
// status normalized to tsCURRENT (the inbound peer's claimed status
// is informational only) and receivetimestamp freshly stamped from
// the local Ripple clock.
//
// Overlay.RelayTransaction applies rippled's reduce-relay peer selection:
// the full frame goes to a subset of peers and the rest learn of the tx via
// the TMHaveTransactions announce. We don't consult a HashRouter equivalent
// for the multi-hop suppression set because goXRPL's de-dup happens
// implicitly via openledger.Submit's view.TxExists pre-filter
// (openledger.go:362-365): a duplicate arrival from another peer classifies
// as ResultFailure and the relay gate above never fires. Excluding the origin
// is the minimum correctness boundary — without it the originator would
// receive its own packet back and either re-charge us bandwidth or, in a
// 2-peer cycle, oscillate indefinitely.
func (r *Router) relayTransaction(except peermanagement.PeerID, blob []byte) {
	if r.overlay == nil {
		return
	}
	out := &message.Transaction{
		RawTransaction:   blob,
		Status:           message.TxStatusCurrent,
		ReceiveTimestamp: uint64(time.Now().Unix() - protocol.RippleEpochUnix),
	}
	encoded, err := message.Encode(out)
	if err != nil {
		r.logger.Warn("relay transaction encode failed", "error", err)
		return
	}
	frame, err := message.BuildWireMessage(message.TypeTransaction, encoded)
	if err != nil {
		r.logger.Warn("relay transaction frame build failed", "error", err)
		return
	}
	// Reduce-relay peer selection: relays the full frame to a subset of
	// peers and lets the rest learn via the TMHaveTransactions announce
	// (Overlay.RelayTransaction, mirroring OverlayImpl::relay).
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

	// PeerImp::getTxSet (PeerImp.cpp:3255-3287): ledger_hash carries the
	// tx-set ID, response is TMLedgerData{type=liTS_CANDIDATE, ...}.
	if req.InfoType == message.LedgerInfoTsCandidate {
		r.serveTxSet(msg.PeerID, req)
		return
	}

	// Only handle base (header) requests beyond this point.
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

// liTS_CANDIDATE serve-path caps matching rippled's softMaxReplyNodes /
// hardMaxReplyNodes (rippled/src/xrpld/overlay/detail/Tuning.h:39,42).
// Soft cap stops starting new subtrees; hard cap truncates mid-subtree.
// Declared as vars so tests can dial them down via txSetReplyCapsForTest /
// setTxSetReplyCapsForTest. Production callers must not mutate.
var (
	txSetSoftMaxReplyNodes = 8192
	txSetHardMaxReplyNodes = 12288
)

func txSetReplyCapsForTest() (soft, hard int) {
	return txSetSoftMaxReplyNodes, txSetHardMaxReplyNodes
}

func setTxSetReplyCapsForTest(soft, hard int) {
	txSetSoftMaxReplyNodes = soft
	txSetHardMaxReplyNodes = hard
}

// SHAMapNodeID wire length: 32-byte path + 1-byte depth.
const shamapNodeIDLen = 33

// PeerImp.cpp:3382 uses isHighLatency() ? 2 : 1. Without a latency signal
// we overspec at 2 — extra level is harmless, too few stalls the requestor.
const defaultQueryDepth = 2

// serveTxSet replies to TMGetLedger{itype=liTS_CANDIDATE} with the tx set
// encoded as TMLedgerData{type=liTS_CANDIDATE, ledger_hash=<txSetID>,
// nodes=[<SHAMapNodeID, wire-serialized SHAMap node>...]}. Mirrors
// PeerImp::processLedgerRequest at PeerImp.cpp:3304-3411: for each requested
// NodeID, walk QueryDepth levels via GetNodeFatByPath, honouring soft/hard
// caps. Empty nodeids falls back to a full pre-order walk for legacy
// goxrpl→goxrpl fixtures; rippled requestors always send at least the root.
func (r *Router) serveTxSet(peerID peermanagement.PeerID, req *message.GetLedger) {
	if len(req.LedgerHash) != 32 {
		return
	}
	var txSetID consensus.TxSetID
	copy(txSetID[:], req.LedgerHash)

	ts, ok := r.adaptor.txSetCache.Get(txSetID)
	if !ok {
		r.logger.Debug("peer requested tx-set we don't have",
			"peer", peerID, "txset", fmt.Sprintf("%x", txSetID[:8]))
		return
	}

	// SHAMap is non-nil for any TxSet that reached the cache —
	// NewTxSet returns an error before stashing on shamap.New failure.
	txMap := ts.shamap()

	queryDepth := int(req.QueryDepth)
	if queryDepth == 0 {
		queryDepth = defaultQueryDepth
	}
	// PeerImp.cpp:3318 hardcodes fatLeaves=false for liTS_CANDIDATE.
	const fatLeaves = false

	nodes := buildTxSetReplyNodes(txMap, req.NodeIDs, queryDepth, fatLeaves, r.logger, peerID, txSetID)

	resp := &message.LedgerData{
		LedgerHash:    req.LedgerHash,
		LedgerSeq:     0, // tx-set responses carry no ledger seq (rippled sets 0 too)
		InfoType:      message.LedgerInfoTsCandidate,
		Nodes:         nodes,
		RequestCookie: uint32(req.RequestCookie),
	}

	frame, err := encodeFrame(message.TypeLedgerData, resp)
	if err != nil {
		r.logger.Warn("failed to encode tx-set response", "error", err)
		return
	}

	if err := r.adaptor.SendToPeer(uint64(peerID), frame); err != nil {
		r.logger.Debug("failed to send tx-set response", "error", err, "peer", peerID)
		return
	}
	r.logger.Debug("served tx-set to peer",
		"peer", peerID,
		"txset", fmt.Sprintf("%x", txSetID[:8]),
		"shamap_nodes", len(nodes),
		"txs", ts.Size(),
		"query_depth", queryDepth,
		"requested_nodes", len(req.NodeIDs))
}

// buildTxSetReplyNodes builds the LedgerNode payload of a liTS_CANDIDATE
// reply, honouring requested NodeIDs/QueryDepth and soft/hard reply caps.
func buildTxSetReplyNodes(
	txMap *shamap.SHAMap,
	requestedNodeIDs [][]byte,
	queryDepth int,
	fatLeaves bool,
	logger logger,
	peerID peermanagement.PeerID,
	txSetID consensus.TxSetID,
) []message.LedgerNode {
	if len(requestedNodeIDs) == 0 {
		wireNodes, err := txMap.WalkWireNodes()
		if err != nil {
			logger.Warn("failed to walk tx-set SHAMap for serve",
				"error", err, "peer", peerID, "txset", fmt.Sprintf("%x", txSetID[:8]))
			return nil
		}
		nodes := make([]message.LedgerNode, 0, len(wireNodes))
		for _, n := range wireNodes {
			if len(nodes) >= txSetHardMaxReplyNodes {
				break
			}
			nodes = append(nodes, message.LedgerNode{NodeID: n.NodeID, NodeData: n.Data})
		}
		return nodes
	}

	nodes := make([]message.LedgerNode, 0)
	for i, rawID := range requestedNodeIDs {
		// Soft cap — PeerImp.cpp:3387.
		if len(nodes) >= txSetSoftMaxReplyNodes {
			logger.Debug("tx-set serve: soft-cap reached, stopping subtree iteration",
				"peer", peerID, "txset", fmt.Sprintf("%x", txSetID[:8]),
				"nodes_so_far", len(nodes), "remaining_requested", len(requestedNodeIDs)-i)
			break
		}
		path, depth, ok := parseSHAMapNodeID(rawID)
		if !ok {
			logger.Debug("tx-set serve: bad SHAMapNodeID in request, skipping",
				"peer", peerID, "txset", fmt.Sprintf("%x", txSetID[:8]),
				"node_idx", i, "len", len(rawID))
			continue
		}
		subtree, err := txMap.GetNodeFatByPath(path, depth, queryDepth, fatLeaves)
		if err != nil {
			logger.Debug("tx-set serve: GetNodeFatByPath failed, skipping",
				"peer", peerID, "txset", fmt.Sprintf("%x", txSetID[:8]),
				"error", err.Error())
			continue
		}
		for _, n := range subtree {
			// Hard cap — PeerImp.cpp:3406-3407.
			if len(nodes) >= txSetHardMaxReplyNodes {
				logger.Debug("tx-set serve: hard-cap reached, truncating subtree",
					"peer", peerID, "txset", fmt.Sprintf("%x", txSetID[:8]),
					"nodes", len(nodes))
				return nodes
			}
			nodes = append(nodes, message.LedgerNode{NodeID: n.NodeID, NodeData: n.Data})
		}
	}
	return nodes
}

// parseSHAMapNodeID decodes the 33-byte wire representation into (path,
// depth). Mirrors deserializeSHAMapNodeID at PeerImp.cpp:1442.
func parseSHAMapNodeID(raw []byte) (path [32]byte, depth int, ok bool) {
	if len(raw) != shamapNodeIDLen {
		return path, 0, false
	}
	copy(path[:], raw[:32])
	depth = int(raw[32])
	if depth < 0 || depth > 64 {
		return path, 0, false
	}
	return path, depth, true
}

type logger interface {
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}

// handleTxSetData consumes a TMLedgerData{type=liTS_CANDIDATE} response.
// Each node is a SHAMap node (root/inner/leaf), not a raw transaction.
// Mirrors TransactionAcquire::takeNodes (TransactionAcquire.cpp:175-235):
// accumulate nodes across responses, then either finish (→ engine.OnTxSet)
// or request missing nodes (TransactionAcquire.cpp:144-171). State is keyed
// by tx-set ID so partial responses can resume.
//
// originPeer is the peer ID of the sender, used to attribute non-progress
// for issue #420's per-peer de-prioritization.
func (r *Router) handleTxSetData(ld *message.LedgerData, originPeer uint64) {
	if len(ld.LedgerHash) != 32 {
		return
	}
	var txSetID consensus.TxSetID
	copy(txSetID[:], ld.LedgerHash)

	r.txSetAcquireMu.Lock()
	state, exists := r.txSetAcquire[txSetID]
	if !exists {
		txMap, err := shamap.New(shamap.TypeTransaction)
		if err != nil {
			r.txSetAcquireMu.Unlock()
			r.logger.Info("tx-set sync: shamap construction failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"error", err.Error())
			return
		}
		if err := txMap.StartSync(); err != nil {
			r.txSetAcquireMu.Unlock()
			r.logger.Info("tx-set sync: StartSync failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"error", err.Error())
			return
		}
		state = &txSetAcquireState{
			txMap:           txMap,
			startedAt:       time.Now(),
			peerNonProgress: make(map[uint64]int),
		}
		r.txSetAcquire[txSetID] = state
	}
	state.lastUpdate = time.Now()
	r.sweepStaleTxSetAcquireLocked()
	txMap := state.txMap
	r.txSetAcquireMu.Unlock()

	// Root NodeID is 33 zero bytes. AddRootNode is idempotent
	// (ErrRootAlreadySet treated as success). rootAccepted feeds
	// per-peer progress accounting so a peer whose reply contains
	// only the root still counts as making progress — matches
	// TransactionAcquire::takeNodes (TransactionAcquire.cpp:194-226)
	// where any successful add (root or non-root) returns useful().
	rootAccepted := false
	for _, node := range ld.Nodes {
		if !isShamapRootNodeID(node.NodeID) {
			continue
		}
		err := txMap.AddRootNode([32]byte(txSetID), node.NodeData)
		switch {
		case err == nil, errors.Is(err, shamap.ErrRootAlreadySet):
			rootAccepted = true
		default:
			r.logger.Info("tx-set sync: AddRootNode failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"error", err.Error())
		}
		break
	}

	// Use NodeID-based placement (mirrors rippled SHAMap::addKnownNode):
	// path-based descent works when the peer's response contains nodes
	// deeper than our currently-loaded layer of stubs. The previous
	// hash-search approach (AddKnownNodeUnchecked) silently rejected
	// every node it couldn't place beneath a loaded parent, producing
	// the missing-nodes retry storm of issue #413.
	//
	// replyValid stays true unless any non-root node fails parse or
	// AddKnownNodeByID — matches rippled's takeNodes
	// (TransactionAcquire.cpp:217-220) where a single bad non-root
	// poisons the WHOLE reply (`return SHAMapAddNode::invalid()`),
	// which the caller (InboundTransactions.cpp:177-178) then charges
	// as feeUselessData. We can't charge resource fees, but we MUST
	// reflect the same all-or-nothing reply outcome in per-peer
	// non-progress accounting so a peer trickling junk alongside one
	// good node can't keep its counter pinned at zero.
	added := 0
	replyValid := true
	for _, node := range ld.Nodes {
		if isShamapRootNodeID(node.NodeID) {
			continue
		}
		if len(node.NodeData) == 0 {
			continue
		}
		parsedID, err := shamap.UnmarshalBinary(node.NodeID)
		if err != nil {
			replyValid = false
			r.logger.Debug("tx-set sync: malformed node ID",
				"t", "consensus", "event", "txset-node-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"node_id_len", len(node.NodeID),
				"error", err.Error())
			continue
		}
		if err := txMap.AddKnownNodeByID(parsedID, node.NodeData); err != nil {
			replyValid = false
			r.logger.Debug("tx-set sync: node rejected",
				"t", "consensus", "event", "txset-node-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"node_id", fmt.Sprintf("%x", node.NodeID),
				"node_data_len", len(node.NodeData),
				"error", err.Error())
			continue
		}
		added++
	}

	if err := txMap.FinishSync(); err != nil {
		// Mirror TransactionAcquire::trigger (TransactionAcquire.cpp:144-171):
		// request the missing nodes. Before going to peers, fill any
		// missing TX-leaf nodes from our own pending pool — rippled's
		// peer reply omits leaf blobs (fatLeaves=false at
		// PeerImp.cpp:3318) and expects the local node to source them
		// from its TransactionMaster via ConsensusTransSetSF::getNode
		// (ConsensusTransSetSF.cpp:82-101). For tnTRANSACTION_NM the
		// leaf-node hash equals the tx ID, so a single tx-ID lookup
		// resolves the missing leaf. Without this step, the missing-
		// node request loops forever because peers will never send the
		// leaves back, livelocking tx-set acquisition under fuzz.
		missing := txMap.GetMissingNodes(256, nil)
		if len(missing) == 0 {
			r.deleteTxSetAcquire(txSetID)
			r.logger.Info("tx-set sync: stuck",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"err", err.Error())
			return
		}

		filledFromPool := 0
		remaining := missing[:0]
		for _, m := range missing {
			blob, getErr := r.adaptor.GetTx(consensus.TxID(m.Hash))
			if getErr != nil || len(blob) == 0 {
				remaining = append(remaining, m)
				continue
			}
			// SHAMap wire format for a tx leaf is `tx_blob || WireTypeTransaction`.
			// See shamap/leaf_node.go:NewTransactionLeafFromWire and the
			// matching DeserializeNodeFromWire dispatch at node.go:111.
			wire := make([]byte, len(blob)+1)
			copy(wire, blob)
			wire[len(blob)] = byte(protocol.WireTypeTransaction)
			if addErr := txMap.AddKnownNode(m.Hash, wire); addErr != nil {
				remaining = append(remaining, m)
				continue
			}
			filledFromPool++
		}

		if filledFromPool > 0 {
			if syncErr := txMap.FinishSync(); syncErr == nil {
				// Tree complete after local fill — fall through to the
				// leaf-walk + engine feed below.
				r.logger.Info("tx-set sync: completed via local pool",
					"t", "consensus", "event", "txset-local-fill",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"filled", filledFromPool,
					"non_root_added", added,
				)
			} else {
				// Still incomplete — recompute remaining via the SHAMap
				// since AddKnownNode may have revealed deeper holes.
				remaining = txMap.GetMissingNodes(256, nil)
			}
		}

		if len(remaining) > 0 {
			// Issue #420: throttle retries, cap total attempts, and
			// route around peers that have repeatedly failed to extend
			// the SHAMap. Without these guards a non-progressing peer
			// triggers 100+ retries/sec until the 60s TTL sweep fires.
			// madeProgress folds rippled's takeNodes useful() semantics
			// (TransactionAcquire.cpp:194-226): a reply is useful only
			// when it was non-empty AND every non-root node parsed and
			// added cleanly. Any single bad non-root → invalid reply →
			// not progress for the originating peer.
			madeProgress := replyValid && (added > 0 || rootAccepted)
			r.txSetAcquireMu.Lock()
			// Knobs are read under txSetAcquireMu so a concurrent
			// SetTxSetRetryKnobsForTest (test-only API) can't tear a
			// half-updated struct into the hot path.
			knobs := r.txSetRetryKnobs
			if originPeer != 0 {
				if madeProgress {
					state.peerNonProgress[originPeer] = 0
				} else {
					state.peerNonProgress[originPeer]++
				}
			}
			if !state.lastRequest.IsZero() && time.Since(state.lastRequest) < knobs.MinInterval {
				r.txSetAcquireMu.Unlock()
				r.logger.Debug("tx-set sync: retry throttled",
					"t", "consensus", "event", "txset-retry-throttle",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"missing", len(remaining),
				)
				return
			}
			if state.attempts >= knobs.MaxAttempts {
				attempts := state.attempts
				delete(r.txSetAcquire, txSetID)
				r.txSetAcquireMu.Unlock()
				// Drop the entry rather than mark it permanently
				// failed: if consensus is still proposing this set
				// (rippled handles the equivalent via stillNeed —
				// TransactionAcquire.cpp:256-264), the next inbound
				// TMLedgerData should start a fresh acquire rather
				// than be silently dropped for the TTL window.
				r.logger.Info("tx-set sync: max attempts exceeded",
					"t", "consensus", "event", "txset-reject",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"attempts", attempts,
					"missing", len(remaining),
				)
				return
			}
			state.attempts++
			state.lastRequest = time.Now()
			var excluded map[uint64]bool
			for pid, count := range state.peerNonProgress {
				if count >= knobs.PeerNonProgressThreshold {
					if excluded == nil {
						excluded = make(map[uint64]bool)
					}
					excluded[pid] = true
				}
			}
			attempts := state.attempts
			r.txSetAcquireMu.Unlock()

			nodeIDs := make([][]byte, len(remaining))
			for i, m := range remaining {
				nodeIDs[i] = m.NodeID.Bytes()
			}
			r.logger.Info("tx-set sync: requesting missing nodes",
				"t", "consensus", "event", "txset-retry",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"non_root_added", added,
				"filled_local", filledFromPool,
				"missing", len(remaining),
				"attempts", attempts,
				"excluded_peers", len(excluded),
			)
			if reqErr := r.adaptor.RequestTxSetMissingNodes(txSetID, nodeIDs, excluded); reqErr != nil {
				r.logger.Info("tx-set sync: missing-nodes request failed",
					"t", "consensus", "event", "txset-reject",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"error", reqErr.Error())
			}
			return
		}
		// fall through: tree complete via local fill
	}

	// Walk leaves into blobs, feed the engine, drop the acquire so dispute
	// resolution flipping back to the same set starts fresh.
	blobs := make([][]byte, 0, added+1)
	if err := txMap.ForEach(func(item *shamap.Item) bool {
		blobs = append(blobs, item.Data())
		return true
	}); err != nil {
		r.deleteTxSetAcquire(txSetID)
		return
	}
	r.deleteTxSetAcquire(txSetID)

	r.logger.Info("received tx-set from peer",
		"t", "consensus", "event", "txset-recv",
		"txset", fmt.Sprintf("%x", txSetID[:8]),
		"node_count", len(ld.Nodes),
		"tx_count", len(blobs))

	// Duplicate response after a completed acquire — no root, ForEach
	// yields 0 items, engine would fail with "tx set ID mismatch". Drop.
	if len(blobs) == 0 {
		return
	}

	if err := r.engine.OnTxSet(txSetID, blobs); err != nil {
		r.logger.Info("engine rejected tx-set",
			"t", "consensus", "event", "txset-reject",
			"error", err.Error(),
			"txset", fmt.Sprintf("%x", txSetID[:8]),
			"tx_count", len(blobs))
	}
}

func (r *Router) deleteTxSetAcquire(txSetID consensus.TxSetID) {
	r.txSetAcquireMu.Lock()
	delete(r.txSetAcquire, txSetID)
	r.txSetAcquireMu.Unlock()
}

// MarkTxSetStillNeeded is the active re-arm hook fired every time
// consensus re-asks for a tx-set via Adaptor.RequestTxSet. If an
// in-flight acquisition for this set still exists, attempts and
// lastRequest are cleared so the next inbound TMLedgerData broadcasts
// immediately instead of being throttled or silently dropped past the
// max-attempts cap. Mirrors rippled's TransactionAcquire::stillNeed
// (TransactionAcquire.cpp:256-264), invoked from
// InboundTransactionsImp::getSet when consensus re-acquires a known
// in-flight set (InboundTransactions.cpp:107-114). A no-op if the
// router has no entry for txSetID (e.g. first request, or already
// completed and swept). Issue #420.
func (r *Router) MarkTxSetStillNeeded(txSetID consensus.TxSetID) {
	r.txSetAcquireMu.Lock()
	defer r.txSetAcquireMu.Unlock()
	state, ok := r.txSetAcquire[txSetID]
	if !ok {
		return
	}
	state.attempts = 0
	state.lastRequest = time.Time{}
}

// sweepStaleTxSetAcquireLocked drops entries older than txSetAcquireTTL.
// Caller must hold r.txSetAcquireMu.
func (r *Router) sweepStaleTxSetAcquireLocked() {
	cutoff := time.Now().Add(-txSetAcquireTTL)
	for id, state := range r.txSetAcquire {
		if state.lastUpdate.Before(cutoff) {
			delete(r.txSetAcquire, id)
		}
	}
}

// isShamapRootNodeID matches the SHAMap root wire encoding (33 zero bytes
// = zero path + depth=0). See SHAMapNodeID::getRawString in rippled.
func isShamapRootNodeID(b []byte) bool {
	if len(b) != shamap.NodeIDSize {
		return false
	}
	for _, by := range b {
		if by != 0 {
			return false
		}
	}
	return true
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
	r.fetchTracker.Track(r.inboundLedger)
	if err := r.adaptor.RequestLedgerBaseFromPeer(peerID, hash, seq); err != nil {
		r.logger.Warn("failed to request ledger base from peer", "error", err)
		r.inboundLedger = nil
	}
}

// FetchInfo returns the inbound-ledger acquisition snapshot served by the
// fetch_info RPC. Safe to call from any goroutine.
func (r *Router) FetchInfo() map[string]any {
	return r.fetchTracker.Info()
}

// ClearFetchInfo resets the acquisition counters and recent-failure history,
// backing fetch_info's `clear` param.
func (r *Router) ClearFetchInfo() {
	r.fetchTracker.Clear()
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

	// A delta acquired by an active LedgerReplayTask is owned by that
	// task and never re-enters the generic InboundLedger flow. Mirrors
	// rippled's LedgerReplayer routing.
	if r.routeDeltaToActiveTask(resp) {
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
	peerID := rd.PeerID()
	r.replayer.Complete(rd.Hash())
	if err := r.adoptVerifiedLedger(derived, peerID); err != nil {
		r.logger.Warn("failed to adopt replay-delta ledger", "error", err)
	}
}

// adoptVerifiedLedger commits a ledger reconstructed from a verified replay
// delta. Mirrors LedgerDeltaAcquire.cpp:209 — installs the peer-provided
// tx-blob tree alongside the state map. Routes through SubmitHeldAdoption
// so out-of-order arrivals are stashed by awaited parent seq; on stash we
// arm a backward-chain acquisition for the parent (issue #397).
func (r *Router) adoptVerifiedLedger(l *ledger.Ledger, peerID uint64) error {
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
	res, err := svc.SubmitHeldAdoption(context.TODO(), &hdr, stateMap, txMap)
	if err != nil {
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
	if res.Stashed {
		r.armParentAcquisition(svc, res.ParentSeq, res.ParentHash, peerID)
	}
	return nil
}

// armValidationStashAcquisition arms inbound acquisition for a (seq, hash)
// that SetValidatedLedger stashed. Mirrors LedgerMaster::checkAccept(hash,
// seq) at LedgerMaster.cpp:917-919 (app_.getInboundLedgers().acquire). Prefers
// a peer advertising LCL >= seq, falls back to any tracked peer.
func (r *Router) armValidationStashAcquisition(seq uint32, hash [32]byte) {
	defer func() {
		if rv := recover(); rv != nil {
			r.logger.Error("armValidationStashAcquisition panic recovered",
				"seq", seq,
				"hash", fmt.Sprintf("%x", hash[:8]),
				"panic", rv,
			)
		}
	}()
	if seq == 0 {
		return
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	// Skip only when seq is at or below the last *validated* ledger
	// (mirrors rippled's LedgerMaster::checkAccept gate at
	// LedgerMaster.cpp:883 — `if (seq < mValidLedgerSeq) return`). Gating
	// on the closed-ledger index instead silently swallowed recovery for
	// a node that had run ahead on a private chain: when the validation
	// tracker observed quorum on canonical seq=N with a different hash
	// than our local seq=N, the acquire was skipped because closedSeq
	// >> validatedSeq, leaving us stuck on the private chain forever.
	if seq <= svc.GetValidatedLedgerIndex() {
		return
	}

	// Walk peers in ID order so the chosen peer (and the emitted log)
	// is reproducible across runs. Any peer with the hash can serve it.
	r.peersMu.RLock()
	peerIDs := make([]peermanagement.PeerID, 0, len(r.peerStates))
	for pid := range r.peerStates {
		peerIDs = append(peerIDs, pid)
	}
	sort.Slice(peerIDs, func(i, j int) bool { return peerIDs[i] < peerIDs[j] })
	var (
		preferredPeerID uint64
		fallbackPeerID  uint64
	)
	for _, pid := range peerIDs {
		st := r.peerStates[pid]
		if fallbackPeerID == 0 {
			fallbackPeerID = uint64(pid)
		}
		if st != nil && st.LedgerSeq >= seq {
			preferredPeerID = uint64(pid)
			break
		}
	}
	r.peersMu.RUnlock()
	if preferredPeerID == 0 {
		preferredPeerID = fallbackPeerID
	}
	if preferredPeerID == 0 {
		return
	}

	r.logger.Info("arming acquisition for stashed validation",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"preferred_peer", preferredPeerID,
	)
	r.startLedgerAcquisition(seq, hash, preferredPeerID)
}

// armParentAcquisition fires a backward-chain acquisition for the parent of
// a stashed held-adoption candidate (issue #397). Skips at-or-below closed
// (already adopted or fork-dropped).
func (r *Router) armParentAcquisition(svc *service.Service, parentSeq uint32, parentHash [32]byte, preferredPeerID uint64) {
	if parentSeq == 0 {
		return
	}
	if parentSeq <= svc.GetClosedLedgerIndex() {
		return
	}
	r.logger.Info("arming backward-chain acquisition for stashed held-adoption parent",
		"parent_seq", parentSeq,
		"parent_hash", fmt.Sprintf("%x", parentHash[:8]),
		"preferred_peer", preferredPeerID,
	)
	r.startLedgerAcquisition(parentSeq, parentHash, preferredPeerID)
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

	// liTS_CANDIDATE response — InboundTransactions::gotData feeds the
	// engine via gotTxSet (consensus-time only). Issue #401.
	if ld.InfoType == message.LedgerInfoTsCandidate {
		r.handleTxSetData(ld, uint64(msg.PeerID))
		return
	}

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

		// Request the missing state and transaction nodes in parallel,
		// mirroring rippled InboundLedger::trigger (InboundLedger.cpp:621,696).
		r.requestMissingAcquisitionNodes(il)
		return true

	case message.LedgerInfoAsNode:
		// Phase 2a: Got state tree nodes
		if err := il.GotStateNodes(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotStateNodes failed", "error", err)
			r.adaptor.IncPeerBadData(il.PeerID(), "ledger-data-state")
			return true
		}

		if il.IsComplete() {
			r.completeInboundLedger()
			return true
		}

		r.requestMissingAcquisitionNodes(il)
		return true

	case message.LedgerInfoTxNode:
		// Phase 2b: Got transaction tree nodes
		if err := il.GotTransactionNodes(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotTransactionNodes failed", "error", err)
			r.adaptor.IncPeerBadData(il.PeerID(), "ledger-data-tx")
			return true
		}

		if il.IsComplete() {
			r.completeInboundLedger()
			return true
		}

		r.requestMissingAcquisitionNodes(il)
		return true
	}

	return false
}

// requestMissingAcquisitionNodes asks the source peer for the outstanding
// account-state and transaction tree nodes of the active acquisition. Mirrors
// rippled InboundLedger::trigger, which requests both trees in parallel
// (InboundLedger.cpp:621,696). Each call is a no-op for a tree already complete.
func (r *Router) requestMissingAcquisitionNodes(il *inbound.Ledger) {
	if nodeIDs := il.NeedsMissingNodeIDs(); len(nodeIDs) > 0 {
		if err := r.adaptor.RequestStateNodes(il.PeerID(), il.Hash(), nodeIDs); err != nil {
			r.logger.Warn("inbound ledger: failed to request state nodes", "error", err)
		}
	}
	if nodeIDs := il.NeedsMissingTxNodeIDs(); len(nodeIDs) > 0 {
		if err := r.adaptor.RequestTransactionNodes(il.PeerID(), il.Hash(), nodeIDs); err != nil {
			r.logger.Warn("inbound ledger: failed to request tx nodes", "error", err)
		}
	}
}

// completeInboundLedger finalizes an InboundLedger acquisition and adopts the ledger.
func (r *Router) completeInboundLedger() {
	il := r.inboundLedger
	r.inboundLedger = nil

	h, stateMap, txMap, err := il.Result()
	if err != nil {
		r.logger.Warn("inbound ledger: failed to get result", "error", err)
		return
	}
	peerID := il.PeerID()

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	// The acquisition fetches the header, state map, and transaction map; txMap
	// is nil only when the ledger has no transactions (empty tx tree), in which
	// case the service installs the genesis-shaped empty tx map.
	//
	// F6: route through SubmitHeldAdoption so out-of-order catchup arrivals
	// either fast-path (parent already present) or stash for cascade when the
	// awaited parent lands. Legacy mtGET_LEDGER is sequential at the wire level
	// today, but nothing in the protocol forbids interleaving — the held-queue
	// is the correct seam regardless.
	// context.TODO: same as adoptVerifiedLedger — reached from a peer-message
	// handler stack with no plumbed context. See note there.
	res, err := svc.SubmitHeldAdoption(context.TODO(), h, stateMap, txMap)
	if err != nil {
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
	if res.Stashed {
		r.armParentAcquisition(svc, res.ParentSeq, res.ParentHash, peerID)
	}
}
