package adaptor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
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
	engine  consensus.Engine
	adaptor *Adaptor
	inbox   <-chan *peermanagement.InboundMessage
	logger  *slog.Logger

	// Peer ledger tracking for catch-up detection
	peersMu    sync.RWMutex
	peerStates map[peermanagement.PeerID]*peerLedgerState

	// replayer coordinates concurrent mtREPLAY_DELTA_REQUEST acquisitions
	// keyed by target ledger hash, under a configurable concurrency cap, so a
	// catchup burst across many ledgers can parallelize instead of
	// serializing.
	replayer *inbound.Replayer

	// fetchTracker is the registry of classic header+state+tx ledger
	// acquisitions, keyed by ledger hash. It is both the active in-flight set
	// (the router routes inbound TMLedgerData to the matching acquisition via
	// Find, and starts new ones via GetOrCreate) and the source of the
	// fetch_info snapshot. Consensus catch-up drives it from the single inbox
	// goroutine; the RPC-driven ledger_request path (RequestLedger) starts
	// ReasonGeneric acquisitions from RPC goroutines. Both go through the
	// tracker's own mutex, and each acquisition guards its own state, so
	// concurrent access is safe. Orthogonal to replayer — legacy and
	// replay-delta acquisitions can coexist.
	fetchTracker *inbound.Tracker

	// fetchPacks caches inbound fetch-pack SHAMap nodes keyed by node hash so
	// a stalled acquisition can complete locally (inbound.Ledger.CheckLocal)
	// instead of node-by-node over the network. Driven from the single inbox
	// goroutine (handleFetchPackReply / maintenanceTick) and guarded by its
	// own mutex.
	fetchPacks *fetchPackCache

	// messageSeen dedups inbound proposal / validation payloads so the
	// reduce-relay slot only feeds on DUPLICATE arrivals. Counting first-seen
	// messages would accelerate selection and produce earlier squelches for
	// the same traffic pattern.
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
	// its companion sequence cursor: re-encode only when manifests.Sequence
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
	// responses until the tree is complete and leaves are handed to
	// engine.OnTxSet.
	txSetAcquireMu sync.Mutex
	txSetAcquire   map[consensus.TxSetID]*txSetAcquireState

	// Retry-loop knobs for tx-set acquisition. Set to production defaults by
	// NewRouter; tests inject smaller values via SetTxSetRetryKnobsForTest so
	// they don't sleep for the production 250ms throttle window. See
	// txSetRetryKnobs for the meaning of each field.
	txSetRetryKnobs txSetRetryKnobs

	// floor is the online-delete retention floor. When set, the router
	// refuses to acquire or serve ledgers below it — rippled gates the same
	// in LedgerMaster::shouldAcquire (acquisition) and gives the serving
	// guarantee implicitly because online-delete physically removed the data.
	// Nil when online-delete is off, leaving acquisition/serving unrestricted.
	floor MinimumOnlineFloor
}

// messageDedupTTL is how long a proposal/validation hash is
// remembered for duplicate-detection purposes. 30s comfortably covers a
// consensus round while aging out cross-round stragglers so the dedup
// table doesn't grow unbounded under sustained gossip.
const messageDedupTTL = 30 * time.Second

// messageDedupMaxEntries caps the dedup table size. One entry per
// unique (validator, position, txSet, closeTime) tuple in a healthy
// 100-validator round; 4096 gives ~40x headroom before the trim
// fires. Cheap memory — 32-byte key + 24-byte time.
const messageDedupMaxEntries = 4096

// NewRouter creates a new Router.
func NewRouter(engine consensus.Engine, adaptor *Adaptor, inbox <-chan *peermanagement.InboundMessage) *Router {
	logger := slog.Default().With("component", "consensus-router")
	r := &Router{
		engine:          engine,
		adaptor:         adaptor,
		inbox:           inbox,
		logger:          logger,
		peerStates:      make(map[peermanagement.PeerID]*peerLedgerState),
		replayer:        inbound.NewReplayer(logger, inbound.SystemClock, inbound.DefaultMaxInFlightReplays),
		fetchTracker:    inbound.NewTracker(),
		fetchPacks:      newFetchPackCache(),
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
		// Wire the still-needed re-arm so every consensus re-ask of an
		// in-flight tx-set clears the per-acquisition throttle and
		// attempt-cap state.
		adaptor.SetOnTxSetRequested(r.MarkTxSetStillNeeded)
	}
	return r
}

// SetMinimumOnlineFloor installs the online-delete retention floor. Once set,
// the router refuses to acquire or serve ledgers below it. A nil floor leaves
// both paths unrestricted, so the disabled / standalone case is unchanged.
func (r *Router) SetMinimumOnlineFloor(floor MinimumOnlineFloor) {
	r.floor = floor
}

// belowFloor reports whether seq sits below the online-delete retention floor.
// A nil floor or a zero floor (no rotation yet) never withholds anything,
// mirroring rippled where shouldAcquire treats an unset minimumOnline as no
// lower bound.
func (r *Router) belowFloor(seq uint32) bool {
	if r.floor == nil {
		return false
	}
	floor := r.floor.MinimumOnline()
	return floor != 0 && seq < floor
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
	// lifetime of the process.
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
	// the outer budget kicks in (250ms × 10 rotations inside a larger
	// outer budget). Without rotation, a single silent peer burns the
	// full 10s before the legacy fallback fires.
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

	// Reap stuck legacy inbound ledgers. Without this a stalled acquisition
	// blocks startLedgerAcquisitionLegacy from arming a new request for the
	// SAME hash on the next statusChange — and blocks the replay-delta path
	// too via isAcquiring's registry check.
	for _, il := range r.fetchTracker.ActiveTimedOut() {
		// Before giving up, try a fetch-pack: if we can name a child ledger
		// of the stalled one, ask a peer for a bulk pack and grant one more
		// timeout window for it to arrive and complete the acquisition
		// locally (handleFetchPackReply → CheckLocal). Attempted at most once
		// per acquisition; on the next timeout it reaps as before.
		if r.tryFetchPackEscalation(il) {
			continue
		}
		r.logger.Warn("legacy inbound ledger acquisition timed out",
			"seq", il.Seq(),
			"hash", fmt.Sprintf("%x", il.Hash()),
			"peer", il.PeerID(),
		)
		r.fetchTracker.Remove(il.Hash(), false)
		// Do NOT re-issue from here: legacy has no retry partner, and
		// the next statusChange from any peer will naturally arm a
		// fresh acquisition via startLedgerAcquisition once the stuck
		// reference is cleared.
	}

	// Expire stale fetch-pack nodes so the cache doesn't retain a stalled
	// acquisition's nodes past their usefulness.
	r.fetchPacks.sweep(time.Now())

	// Timer-driven tx-set acquisition re-trigger. The inbound retry
	// (handleTxSetData) only advances when a TMLedgerData arrives; if a peer
	// falls silent mid-acquire nothing re-requests the remaining nodes and
	// the node stalls into wrongLedger.
	r.retryStalledTxSetAcquires()
}

// Bounds used to reject malformed TMProposeSet / TMValidation frames
// before they reach the engine. Out-of-range values get feeInvalidData
// attributed to the sender.
//
// signatureMinLen / signatureMaxLen bracket a valid DER-encoded
// secp256k1 signature; anything outside this range is rejected before
// attempting verify.
const (
	signatureMinLen = 64
	signatureMaxLen = 72
)

// Ledger-data serve-path caps, shared across liTS_CANDIDATE, liAS_NODE,
// and liTX_NODE replies. Soft cap stops starting new subtrees; hard cap
// truncates mid-subtree. Declared as vars so tests can dial them down via
// txSetReplyCapsForTest / setTxSetReplyCapsForTest. Production callers must
// not mutate.
var (
	txSetSoftMaxReplyNodes = 8192
	txSetHardMaxReplyNodes = 12288
)

// SHAMapNodeID wire length: 32-byte path + 1-byte depth.
const shamapNodeIDLen = 33

// Without a latency signal we overspec the query depth at 2 — an extra
// level is harmless, too few stalls the requestor.
const defaultQueryDepth = 2

type logger interface {
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}
