package adaptor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/shamap"
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
	parentHash [32]byte
	haveParent bool
}

type peerStatusCandidate struct {
	peerLedgerState
	peerID peermanagement.PeerID
}

type peerSessionView interface {
	IsPeerConnected(peermanagement.PeerID) bool
}

type peerCountView interface {
	PeerCount() int
}

const networkPeerQuorum = 1

type peerLedgerHintView interface {
	PeerClosedLedger(peermanagement.PeerID) ([32]byte, bool)
}

// FastSyncMetrics is a bounded snapshot of finality and recovery outcomes.
type FastSyncMetrics struct {
	CompletionRecheckAccepted            uint64
	CompletionRecheckRejectedNoEvidence  uint64
	CompletionRecheckRejectedBelowQuorum uint64
	CompletionRecheckRejectedUnavailable uint64
	TargetSuperseded                     uint64
	ObsoleteAcquisitionCompleted         uint64
	ReplayPipelineRequested              uint64
	ReplayPipelineReady                  uint64
	ReplayPipelineApplied                uint64
	ReplayPipelineDiscarded              uint64
	ReplayPipelineRetried                uint64
	ReplayPipelineFallbacks              uint64
	ReplayPipelineCapacityRetargets      uint64
	ReplayPipelineBackpressureEvents     uint64
	ReplayPipelineRetargetFailures       uint64
	ReplayPipelineAcquireUs              uint64
	ReplayPipelineReadyWaitUs            uint64
	ReplayPipelineApplyUs                uint64
	ReplayPipelinePersistUs              uint64
	ReplayPipelineWindow                 uint32
	ReplayPipelinePreparedLimit          uint32
	ReplayPipelineDepth                  uint32
	ReplayPipelineReadyDepth             uint32
	ReplayPipelinePivotSeq               uint32
	ReplayPipelinePreparedTailSeq        uint32
	ReplayPipelineTrustedHeadSeq         uint32
	ReplayPipelineGeneration             uint64
	ReplayPipelinePivotStateNodesPerSec  uint64
	ReplayPipelineHeadSeq                uint32
	ReplayPipelineTargetSeq              uint32
	ReplayPipelineHeadBlockedUs          uint64
}

type peerBootstrapAcknowledger interface {
	AcknowledgePeerBootstrap(peermanagement.PeerID)
	RejectPeerBootstrap(peermanagement.PeerID)
}

// Router reads inbound messages from the P2P overlay and dispatches
// them to the consensus engine and adaptor.
type Router struct {
	engine      consensus.RouterEngine
	adaptor     *Adaptor
	gossip      gossipNetwork
	txSetNet    txSetNetwork
	acquisition ledgerAcquisitionNetwork
	serve       ledgerServeNetwork
	// inbox is the overlay's bounded, backpressured consensus lane.
	inbox <-chan *peermanagement.InboundMessage
	// serviceInbox carries best-effort, recoverable peer traffic. Keeping it
	// separate prevents ledger requests and other service frames from occupying
	// the consensus lane.
	serviceInbox <-chan *peermanagement.InboundMessage
	// consensusControlInbox carries status changes and transaction-set
	// availability on a protected lane separate from proposals and validations.
	consensusControlInbox <-chan *peermanagement.InboundMessage
	// txInbox is the overlay's dedicated transaction lane. Run drains it
	// alongside inbox and hands each frame to the worker pool, so a tx
	// flood on this lane can't starve consensus/acquisition frames
	// arriving on inbox (issue #1103). nil when unset — tests that drive
	// handleMessage directly leave it nil, and a nil channel is simply
	// never selected. Wired via SetTxInbox before Run.
	txInbox <-chan *peermanagement.InboundMessage
	// acqInbox is the overlay's dedicated acquisition-reply lane
	// (mtLEDGER_DATA and the replay-delta / proof-path responses). Its own
	// buffered lane keeps a flood on inbox from shedding a reply this node
	// explicitly requested; Run drains it as a co-equal select case — not
	// absolute priority, which would let a mtLEDGER_DATA flood starve
	// proposal/validation. nil when unset — a nil channel is never selected.
	// Wired via SetAcqInbox before Run.
	acqInbox <-chan *peermanagement.InboundMessage
	// manifestInbox is a dedicated, backpressured overlay lane drained by the
	// manifest worker without passing through the consensus router loop.
	manifestInbox <-chan *peermanagement.InboundMessage
	logger        *slog.Logger

	// Peer ledger tracking for catch-up detection
	peerSessions         peerSessionView
	peersMu              sync.RWMutex
	peerStates           map[peermanagement.PeerID]*peerLedgerState
	peerStatusCandidates map[peermanagement.PeerID]peerStatusCandidate

	// The overlay callback only records disconnects; a router-owned worker performs
	// cleanup so acquisition scans never block the overlay event loop.
	pendingPeerDisconnects sync.Map
	peerDisconnectWake     chan struct{}
	pendingPeerConnects    sync.Map
	peerConnectWake        chan struct{}
	// standardReplayDrainWake resumes a replay apply batch on the router loop
	// after the preceding batch yielded. Keeping the wake edge-triggered and
	// buffered prevents a ready replay window from monopolising the same loop
	// that must drain consensus, control, and acquisition traffic.
	standardReplayDrainWake chan struct{}

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
	// acquisitionWorkMu guards the lane pointer across Run startup/shutdown and
	// RPC calls such as fetch_info clear.
	acquisitionWorkMu sync.RWMutex

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
	txSeen      *transactionSuppression
	// validationWork verifies signatures outside the router goroutine. Trusted
	// and untrusted work use separate bounded queues so untrusted traffic cannot
	// occupy the trusted capacity.
	validationWork          *validationWorkLane
	validationShedTrusted   atomic.Uint64
	validationShedUntrusted atomic.Uint64

	// manifests is the validator manifest cache. Wired by the
	// Components bootstrap so the router can apply inbound TMManifests
	// frames and — on Accepted — relay them to other peers.
	// May be nil in tests that don't exercise the manifest path.
	manifests *manifest.Cache
	// manifestClassify resolves a parsed master key to the cache admission
	// policy. Production classifies listed/trusted keys as Uncapped and all
	// others as Capped before applying a manifest.
	manifestClassify       func([33]byte) manifest.ManifestRateLimitCapPolicy
	manifestUntrustedLimit int
	manifestLimitSet       bool
	manifestShuffle        func([][]byte)
	manifestWorkerCancel   context.CancelFunc
	manifestWorkerDone     chan struct{}

	// overlay is held so the router can relay accepted manifests and emit
	// the local cache to peers. Nil in tests without manifest support.
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
	// leaves it nil so the real overlay is used.
	overrideManifestSender manifestSender

	// manifestFrameMu guards the cached TMManifests emission frames and
	// its companion sequence cursor: re-encode only when manifests.Sequence
	// has advanced past the value seen at last build, so back-to-back
	// peer connects reuse the same encoded bytes without re-walking the
	// cache. manifestFrameBuilt is the never-built sentinel — a zero
	// manifestFrameSeq is a valid cursor (a fresh cache starts at 0),
	// so we need an explicit "have we ever built?" flag rather than
	// using the zero value as the sentinel.
	manifestFrameMu     sync.Mutex
	manifestFrames      [][]byte
	manifestFrameHashes [][32]byte
	manifestFrameSeq    uint64
	manifestFrameTrust  [32]byte
	manifestFrameLimit  int
	manifestFrameBuilt  bool

	// In-flight tx-set acquisition state keyed by tx-set ID.
	// Each entry's SHAMap accumulates across multiple TMLedgerData
	// responses until the tree is complete and leaves are handed to
	// engine.OnTxSet.
	txSetAcquireMu sync.Mutex
	txSetAcquire   map[consensus.TxSetID]*txSetAcquireState

	// Retry-loop knobs for tx-set acquisition. Set to production defaults by
	// newRouter; tests inject smaller values via setTxSetRetryKnobsForTest so
	// they don't sleep for the production 250ms throttle window. See
	// txSetRetryKnobs for the meaning of each field.
	txSetRetryKnobs txSetRetryKnobs

	// floor is the online-delete retention floor. When set, the router
	// refuses to acquire or serve ledgers below it — rippled gates the same
	// in LedgerMaster::shouldAcquire (acquisition) and gives the serving
	// guarantee implicitly because online-delete physically removed the data.
	// Nil when online-delete is off, leaving acquisition/serving unrestricted.
	floor MinimumOnlineFloor

	lifecycleMu     sync.RWMutex
	lifecycleState  routerLifecycleState
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	lifecycleWG     sync.WaitGroup
	lifecycleReady  chan struct{}

	prewarmSignatures func(context.Context, [][]byte)

	// txJobs is the bounded queue draining inbound peer transactions off the
	// Run message loop, mirroring rippled's jtTRANSACTION job queue. It is nil
	// before Run and after shutdown.
	txJobs chan *peermanagement.InboundMessage

	// droppedTxJobs counts inbound transactions shed because the worker pool
	// was saturated — the originating peer resends and reduce-relay covers
	// the gap, so a dropped relay frame is recoverable.
	droppedTxJobs atomic.Uint64

	// serveJobs is the bounded queue draining inbound mtGET_LEDGER serve work
	// (handleGetLedger / serveTxSet, which builds the largest map — the
	// 15k-tx tx-set — inline) off the Run message loop onto a small worker
	// pool. It is nil before Run and after shutdown.
	serveJobs chan *peermanagement.InboundMessage

	// droppedServeJobs counts inbound get_ledger requests shed because the
	// serve pool was saturated — the requesting peer retries elsewhere, so a
	// dropped request is recoverable load-shedding.
	droppedServeJobs atomic.Uint64

	// acquisitionFamily backs new inbound acquisitions with the persistent node
	// store (see SetAcquisitionFamily); nil leaves them unbacked. Set once at
	// startup, before Run.
	acquisitionFamily shamap.Family
	acquisitionStore  *acquisitionStoreLane
	acquisitionWork   *acquisitionWorkLane

	// catchupMu guards the single consensus catch-up target and recent failures.
	// The router drives at most maxConcurrentCatchup acquisitions toward the
	// highest trusted (seq,hash), matching rippled's single needed ledger.
	catchupMu                            sync.Mutex
	catchup                              catchupTarget
	catchupFailures                      map[[32]byte]time.Time
	linkageWait                          catchupLinkageWait
	completionRecheckAccepted            atomic.Uint64
	completionRecheckRejectedNoEvidence  atomic.Uint64
	completionRecheckRejectedBelowQuorum atomic.Uint64
	completionRecheckRejectedUnavailable atomic.Uint64
	targetSuperseded                     atomic.Uint64
	obsoleteAcquisitionCompleted         atomic.Uint64
	replayPipelineRequested              atomic.Uint64
	replayPipelineReady                  atomic.Uint64
	replayPipelineApplied                atomic.Uint64
	replayPipelineDiscarded              atomic.Uint64
	replayPipelineRetried                atomic.Uint64
	replayPipelineFallbacks              atomic.Uint64
	replayPipelineBackpressureEvents     atomic.Uint64
	replayPipelineRetargetFailures       atomic.Uint64
	replayPipelineAcquireUs              atomic.Uint64
	replayPipelineReadyWaitUs            atomic.Uint64
	replayPipelineApplyUs                atomic.Uint64
	replayPipelinePersistUs              atomic.Uint64

	acquisitionMu     sync.Mutex
	replayCommitMu    sync.Mutex
	consensusRecovery consensusRecovery
	lastHandoffSeq    uint32
	standardReplay    standardReplayPipeline

	// historyMu guards history, the single backward history-backfill target: the
	// next ledger a jump-adopt skipped (rippled Reason::HISTORY). The walk is
	// serial (each header names its parent) and tick-driven. historyFloor bounds
	// it to the jump gap; below it history is already contiguous, so descending
	// further would re-fetch persisted ledgers evicted from the in-memory window.
	historyMu    sync.Mutex
	history      catchupTarget
	historyFloor uint32

	// seqHashMu guards the seqHash table: the network's hash (and, when known,
	// parent hash) per ledger sequence, from trusted validations and peer
	// status_change gossip. Supplies the hash of closed+1 (the forward-delta
	// catch-up target) and the parent linkage proving closed+1 descends from our
	// closed ledger. Only local or trusted evidence advances seqHashAnchor.
	seqHashMu     sync.Mutex
	seqHash       map[uint32]ledgerHashEntry
	seqHashAnchor uint32
}

type routerNetworkConfig struct {
	gossip               gossipNetwork
	txSet                txSetNetwork
	acquisition          ledgerAcquisitionNetwork
	serve                ledgerServeNetwork
	inboundClock         inbound.Clock
	inboundSweepInterval time.Duration
}

// catchupTarget is the highest (seq,hash) the router is driving a bounded
// consensus catch-up toward, plus the peer that last advertised it.
type catchupTarget struct {
	seq    uint32
	hash   [32]byte
	peerID uint64
	source catchupTargetSource
}

type catchupLinkageWait struct {
	closed uint32
	seq    uint32
	hash   [32]byte
	since  time.Time
}

type catchupTargetSource uint8

const (
	catchupSourcePeer catchupTargetSource = iota
	catchupSourceValidation
	catchupSourceQuorum
)

type consensusRecovery struct {
	targetHash [32]byte
	stepHash   [32]byte
	anchorHash [32]byte
	anchorSeq  uint32
}

// ledgerHashEntry is the network's view of one ledger sequence: its hash and,
// when a status_change revealed it, its parent's hash. Trusted validations carry
// no parent link (hash only); status_change gossip populates both. haveParent
// distinguishes a real zero parent hash from "not yet learned".
type ledgerHashEntry struct {
	hash       [32]byte
	parentHash [32]byte
	haveParent bool
	source     seqHashSource
	parentFrom seqHashSource
}

type seqHashSource uint8

const (
	seqHashSourcePeer seqHashSource = iota
	seqHashSourceAcquired
	seqHashSourceValidation
	seqHashSourceQuorum
)

// txWorkerCount bounds the goroutines draining inbound peer transactions off
// the consensus Run loop, and txQueueDepth bounds the pending backlog before
// submitTxJob sheds load. This is the off-strand handoff, analogous to rippled
// posting inbound TMTransaction to its jtTRANSACTION job queue rather than
// processing it on the read strand: under a tx flood the per-tx submit+parse
// must not starve proposal / validation / ledger-acquisition handling, which
// all share Run's single goroutine. The MaxTransactions ceiling is enforced
// upstream on the dedicated overlay tx lane for both wire and batch-fanned
// frames (forwardTransaction sheds and counts droppedTransactions, surfaced
// as jq_trans_overflow); droppedTxJobs here is the second, worker-pool-stage
// shed signal common to both.
// txQueueDepth is sized generously on purpose, and a frame shed here is
// recoverable (the originating peer resends and reduce-relay re-delivers it via
// other peers), so over-buffering costs little.
//
// Each worker's job — decode, parse, and the ECDSA/EdDSA signature prewarm in
// SubmitPendingTx — is CPU-bound and runs concurrently with the serialized
// apply strand, so the pool scales with the available cores (floored at the
// historical 4) to keep that strand fed; a single fixed worker count throttled
// the prewarm throughput well below the apply rate on multi-core validators.
var txWorkerCount = max(4, runtime.GOMAXPROCS(0))

const txQueueDepth = 1024

// serveWorkerCount bounds the goroutines answering inbound mtGET_LEDGER
// requests off the Run loop, and serveQueueDepth bounds the pending backlog
// before submitServeJob sheds. Serving a request — especially building the
// 15k-tx tx-set reply in serveTxSet — is CPU/IO-heavy and, run inline on Run,
// stalls proposal / validation / acquisition-reply handling. The pool is
// sized at half the cores (floored at 2): serving is a background courtesy to
// peers, so it should not claim every core away from the apply strand and the
// tx-prewarm pool. A shed request is recoverable (the peer retries elsewhere).
var serveWorkerCount = max(2, runtime.GOMAXPROCS(0)/2)

const serveQueueDepth = 256

// messageDedupTTL matches rippled's five-minute suppression hold.
const messageDedupTTL = 300 * time.Second

const messageDedupMaxEntries = 4096

// NewRouter creates a new Router.
func NewRouter(engine consensus.RouterEngine, adaptor *Adaptor, inbox <-chan *peermanagement.InboundMessage) *Router {
	network := routerNetworkConfig{}
	if adaptor != nil {
		network.gossip, _ = adaptor.sender.(gossipNetwork)
		network.txSet, _ = adaptor.sender.(txSetNetwork)
		network.acquisition, _ = adaptor.sender.(ledgerAcquisitionNetwork)
		network.serve, _ = adaptor.sender.(ledgerServeNetwork)
	}
	return newRouter(engine, adaptor, inbox, network)
}

func newRouter(engine consensus.RouterEngine, adaptor *Adaptor, inbox <-chan *peermanagement.InboundMessage, network routerNetworkConfig) *Router {
	logger := slog.Default().With("component", "consensus-router")
	noop := &noopSender{}
	if network.gossip == nil {
		network.gossip = noop
	}
	if network.txSet == nil {
		network.txSet = noop
	}
	if network.acquisition == nil {
		network.acquisition = noop
	}
	if network.serve == nil {
		network.serve = noop
	}
	r := &Router{
		engine:                  engine,
		adaptor:                 adaptor,
		gossip:                  network.gossip,
		txSetNet:                network.txSet,
		acquisition:             network.acquisition,
		serve:                   network.serve,
		inbox:                   inbox,
		logger:                  logger,
		peerStates:              make(map[peermanagement.PeerID]*peerLedgerState),
		peerStatusCandidates:    make(map[peermanagement.PeerID]peerStatusCandidate),
		peerDisconnectWake:      make(chan struct{}, 1),
		peerConnectWake:         make(chan struct{}, 1),
		standardReplayDrainWake: make(chan struct{}, 1),
		replayer:                inbound.NewReplayer(logger, inbound.SystemClock, inbound.DefaultMaxInFlightReplays),
		fetchTracker: inbound.NewTrackerWithClockAndSweepInterval(
			network.inboundClock,
			network.inboundSweepInterval,
		),
		fetchPacks:             newFetchPackCache(),
		messageSeen:            newMessageSuppression(messageDedupTTL, messageDedupMaxEntries),
		manifestUntrustedLimit: manifest.DefaultMaxUntrustedCount,
		txSeen:                 newTransactionSuppression(5*time.Minute, 1<<17),
		txSetAcquire:           make(map[consensus.TxSetID]*txSetAcquireState),
		txSetRetryKnobs:        defaultTxSetRetryKnobs(),
		seqHash:                make(map[uint32]ledgerHashEntry),
		lifecycleCtx:           context.Background(),
	}
	if adaptor != nil {
		if _, ok := engine.(consensus.VerifiedValidationProcessor); ok {
			r.validationWork = newValidationWorkLane(
				adaptor.VerifyValidation,
				func(peerID peermanagement.PeerID) bool {
					return r.peerSessions == nil || r.peerSessions.IsPeerConnected(peerID)
				},
				adaptor.IsTrusted,
			)
		}
		if svc := adaptor.LedgerService(); svc != nil {
			r.prewarmSignatures = svc.PrewarmSignaturesContext
		}
		// Wire the still-needed re-arm so every consensus re-ask of an
		// in-flight tx-set clears the per-acquisition throttle and
		// attempt-cap state.
		adaptor.SetOnTxSetRequested(r.MarkTxSetStillNeeded)
		adaptor.SetOnLedgerRequested(r.requestConsensusLedger)
		adaptor.setOnLedgerSwitched(r.onLedgerSwitched)
		adaptor.setOnLedgerFullyValidated(r.onLedgerFullyValidated)
		adaptor.setOnLedgerBuilt(r.onLedgerBuilt)
	}
	return r
}

// SetTxInbox installs the overlay's dedicated transaction lane. Run selects
// it alongside the consensus inbox, so transactions and consensus/acquisition
// traffic no longer share a single bounded buffer that a tx flood could
// saturate (issue #1103). Safe to call before Run; leaving it unset keeps the
// inbox-only behaviour tests rely on.
func (r *Router) SetTxInbox(txInbox <-chan *peermanagement.InboundMessage) {
	r.txInbox = txInbox
}

func (r *Router) SetServiceInbox(serviceInbox <-chan *peermanagement.InboundMessage) {
	r.serviceInbox = serviceInbox
}

func (r *Router) SetConsensusControlInbox(inbox <-chan *peermanagement.InboundMessage) {
	r.consensusControlInbox = inbox
}

// SetAcqInbox installs the overlay's dedicated acquisition-reply lane (see the
// acqInbox field). Safe to call before Run; leaving it unset keeps acquisition
// replies flowing through the shared inbox as before.
func (r *Router) SetAcqInbox(acqInbox <-chan *peermanagement.InboundMessage) {
	r.acqInbox = acqInbox
}

// SetManifestInbox installs the overlay's dedicated manifest lane. The peer
// read path applies bounded backpressure, while a separate worker keeps
// signature verification off the consensus router.
func (r *Router) SetManifestInbox(manifestInbox <-chan *peermanagement.InboundMessage) {
	r.manifestInbox = manifestInbox
}

// SetAcquisitionFamily installs the node-store family that backs new inbound
// ledger acquisitions, so a forked or catching-up node satisfies the shared
// majority of a state/tx tree from its local store and only fetches the
// genuinely-missing nodes from peers (issue #1158). A nil family leaves
// acquisitions unbacked, preserving the fetch-everything path for storeless
// deployments. Call before Run.
func (r *Router) SetAcquisitionFamily(family shamap.Family) {
	if family == nil {
		r.acquisitionFamily = nil
		r.acquisitionStore = nil
		return
	}
	r.acquisitionStore = newAcquisitionStoreLane(family, r.logger, acquisitionStoreQueueDepth)
	r.acquisitionFamily = r.acquisitionStore
}

func (r *Router) flushAcquisitionStore(ctx context.Context, ledger *inbound.Ledger) error {
	if r.acquisitionStore == nil || ledger == nil {
		return nil
	}
	return ledger.FlushPersistence(ctx)
}

func (r *Router) retireAcquisitionStore(ctx context.Context, ledger *inbound.Ledger) {
	if ledger == nil {
		return
	}
	if err := ledger.RetirePersistence(ctx); err != nil && !errors.Is(err, context.Canceled) {
		r.logger.Warn("inbound ledger: failed to retire persistence scope", "error", err, "seq", ledger.Seq())
	}
}

func (r *Router) promoteAcquisitionStore(ctx context.Context, ledger *inbound.Ledger) error {
	if ledger == nil {
		return nil
	}
	return ledger.PromotePersistence(ctx)
}

// acquisitionOpts returns the inbound.Option set applied to every new
// acquisition.
func (r *Router) acquisitionOpts() []inbound.Option {
	opts := []inbound.Option{inbound.WithHeaderAdmission(r.admitInboundHeader)}
	if r.acquisitionStore != nil {
		opts = append(opts, inbound.WithFamily(r.acquisitionStore.scope()))
	}
	return opts
}

func (r *Router) admitInboundHeader(seq uint32) error {
	if !r.belowFloor(seq) {
		return nil
	}
	return fmt.Errorf("ledger %d is below the minimum online floor", seq)
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
	if cache != nil && !r.manifestLimitSet {
		r.manifestUntrustedLimit = cache.MaxUntrustedCount()
	}
}

// SetManifestClassifier installs the listed/trusted resolver used for
// inbound admission and outbound snapshot selection. A nil classifier keeps
// the standalone/test default of treating every manifest as uncapped.
func (r *Router) SetManifestClassifier(classify func([33]byte) manifest.ManifestRateLimitCapPolicy) {
	r.manifestClassify = classify
}

// SetManifestUntrustedLimit sets the per-message unlisted-manifest budget and
// the outbound snapshot's unlisted selection limit. It is independent of the
// wire frame's entry-count/byte batching limits.
func (r *Router) SetManifestUntrustedLimit(limit int) {
	if limit < 0 {
		limit = 0
	}
	r.manifestUntrustedLimit = limit
	r.manifestLimitSet = true
}

func (r *Router) setPeerSessionView(view peerSessionView) {
	r.peerSessions = view
}

// SetValidatorListAggregator installs the publisher-trust subsystem.
// Calling with a nil aggregator disables the TMValidatorList /
// TMValidatorListCollection paths — the dispatch switch silently
// drops inbound frames in that case. Safe to call before Run.
func (r *Router) SetValidatorListAggregator(agg *validatorlist.Aggregator) {
	r.validatorList = agg
}

// StopAcquisitions terminally drains both inbound-ledger acquisition paths.
// A stopped Router is not reusable; a process restart constructs new components.
func (r *Router) StopAcquisitions() (legacy, replay int) {
	if r == nil {
		return 0, 0
	}
	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	legacyLedgers := r.fetchTracker.Stop()
	legacy = len(legacyLedgers)
	if r.replayer != nil {
		replay = r.replayer.Stop()
	}
	retirement := r.cancelStandardReplayPipelineLocked()
	r.consensusRecovery = consensusRecovery{}
	r.lastHandoffSeq = 0
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()

	r.catchupMu.Lock()
	r.catchup = catchupTarget{}
	r.catchupFailures = nil
	r.linkageWait = catchupLinkageWait{}
	r.catchupMu.Unlock()
	r.retireLegacyAcquisitions(legacyLedgers)
	if releaseDone := r.retireStandardReplay(retirement); releaseDone != nil {
		<-releaseDone
	}
	return legacy, replay
}

// HandlePeerDisconnect drops all per-peer state the router holds for
// peerID: the peer's last-reported ledger, its status-change vote in
// the engine's getNetworkLedger fold, and any lingering acquisition
// references. Wired from the overlay's peer-disconnect callback at
// startup so the state is freed the instant the peer goes away,
// instead of lingering until the next ledger adoption happens to
// overwrite it.
func (r *Router) HandlePeerDisconnect(peerID peermanagement.PeerID) {
	r.pendingPeerConnects.Delete(peerID)
	r.peersMu.Lock()
	delete(r.peerStates, peerID)
	delete(r.peerStatusCandidates, peerID)
	r.peersMu.Unlock()
	r.invalidateCatchupPeer(uint64(peerID))
	r.invalidateHistoryPeer(uint64(peerID))
	r.removePeerFromAcquisitions(uint64(peerID))

	// Clear the peer's LCL vote so getNetworkLedger stops counting its
	// stale hash. The adaptor uses the zero LedgerID as a delete key.
	r.adaptor.UpdatePeerLCL(uint64(peerID), consensus.LedgerID{})

	// Drop the peer's per-publisher sequence record so the publisher-
	// trust aggregator's peerSeq map doesn't grow unbounded across the
	// lifetime of the process.
	if r.validatorList != nil {
		r.validatorList.ForgetPeer(uint64(peerID))
	}
	r.reconcilePeerAvailability()
}

func (r *Router) reconcilePeerAvailability() {
	if r.adaptor == nil {
		return
	}
	peers, ok := r.peerSessions.(peerCountView)
	if !ok {
		return
	}

	mode := r.adaptor.GetOperatingMode()
	if peers.PeerCount() < networkPeerQuorum {
		if mode != consensus.OpModeDisconnected {
			r.adaptor.SetOperatingMode(consensus.OpModeDisconnected)
		}
		return
	}
	if mode == consensus.OpModeDisconnected {
		r.adaptor.SetOperatingMode(consensus.OpModeConnected)
	}
}

func (r *Router) queuePeerDisconnect(peerID peermanagement.PeerID) {
	r.pendingPeerConnects.Delete(peerID)
	r.pendingPeerDisconnects.Store(peerID, struct{}{})
	select {
	case r.peerDisconnectWake <- struct{}{}:
	default:
	}
}

func (r *Router) drainPeerDisconnects() {
	r.pendingPeerDisconnects.Range(func(key, _ any) bool {
		peerID := key.(peermanagement.PeerID)
		if _, loaded := r.pendingPeerDisconnects.LoadAndDelete(peerID); loaded {
			r.HandlePeerDisconnect(peerID)
		}
		return true
	})
}

func (r *Router) runPeerDisconnectCleanup(ctx context.Context) {
	r.drainPeerDisconnects()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.peerDisconnectWake:
			r.drainPeerDisconnects()
		}
	}
}

func (r *Router) removePeerFromAcquisitions(peerID uint64) {
	if r.fetchTracker != nil {
		for _, il := range r.fetchTracker.Active() {
			il.RemovePeer(peerID)
		}
	}
}

// Run reads messages from the overlay and dispatches them.
// It blocks until the context is cancelled. A periodic maintenance tick
// also runs in this loop to time out stuck inbound replay-delta
// acquisitions and fall back to the legacy mtGET_LEDGER path.
func (r *Router) Run(ctx context.Context) {
	runCtx, ok := r.startLifecycle(ctx)
	if !ok {
		return
	}
	if r.acquisitionStore != nil {
		r.acquisitionStore.start(runCtx)
		defer r.acquisitionStore.stopDrain()
	}
	r.acquisitionWorkMu.Lock()
	workLane := r.acquisitionWork
	if workLane == nil {
		workLane = newAcquisitionWorkLane(acquisitionWorkQueueDepth)
		r.acquisitionWork = workLane
	}
	r.acquisitionWorkMu.Unlock()
	workLane.flush = r.flushAcquisitionStore
	workLane.start(runCtx)
	defer func() {
		workLane.stop()
		r.acquisitionWorkMu.Lock()
		if r.acquisitionWork == workLane {
			r.acquisitionWork = nil
		}
		r.acquisitionWorkMu.Unlock()
	}()

	disconnectCtx, stopDisconnectCleanup := context.WithCancel(runCtx)
	disconnectCleanupDone := make(chan struct{})
	go func() {
		defer close(disconnectCleanupDone)
		r.runPeerDisconnectCleanup(disconnectCtx)
	}()
	defer func() {
		stopDisconnectCleanup()
		<-disconnectCleanupDone
	}()

	r.startManifestWorker(runCtx)
	defer r.stopManifestWorker()
	if r.validationWork != nil {
		r.validationWork.start(runCtx)
		defer r.validationWork.stop()
	}
	ticker := time.NewTicker(inboundReplayDeltaTickInterval)
	defer ticker.Stop()
	defer r.stopLifecycle()
	r.drainPeerConnects()
	for {
		if !r.drainTrustedValidationResults(runCtx) {
			return
		}
		if !r.drainConsensusInbox(runCtx) {
			return
		}
		acqInbox := r.acqInbox
		if !workLane.canAcceptData() {
			acqInbox = nil
		}
		select {
		case <-runCtx.Done():
			return
		case msg, ok := <-r.inbox:
			if !ok {
				return
			}
			r.handleInboundMessage(msg)
		case msg, ok := <-r.serviceInbox:
			if !ok {
				r.serviceInbox = nil
				continue
			}
			r.handleInboundMessage(msg)
		case msg, ok := <-r.consensusControlInbox:
			if !ok {
				r.consensusControlInbox = nil
				continue
			}
			r.handleInboundMessage(msg)
		case msg, ok := <-acqInbox:
			// Dedicated acquisition-reply lane (liBASE and the replay-delta /
			// proof-path responses). Its own buffered lane keeps a flood on
			// inbox from shedding it; drained as a CO-EQUAL select case so it
			// neither starves nor is starved by consensus/tx traffic. An
			// absolute-priority drain here would let a mtLEDGER_DATA flood
			// starve proposal/validation handling and wedge consensus. nil
			// when unwired/closed — a nil channel is never selected.
			if !ok {
				r.acqInbox = nil
				continue
			}
			r.handleInboundMessage(msg)
		case msg, ok := <-r.txInbox:
			if !ok {
				// Lane closed (or never wired): stop selecting it so we
				// don't busy-spin on a closed channel. The consensus
				// inbox / ctx.Done drive shutdown.
				r.txInbox = nil
				continue
			}
			r.submitTxJob(msg)
		case result := <-workLane.results():
			r.handleAcquisitionWorkResult(result)
		case result := <-r.trustedValidationWorkResults():
			r.handleValidationWorkResult(result)
		case result := <-r.untrustedValidationWorkResults():
			if !r.handleUntrustedValidationWorkResult(runCtx, result) {
				return
			}
		case <-r.standardReplayDrainWake:
			r.drainStandardReplayPipeline()
		case <-r.peerConnectWake:
			r.drainPeerConnects()
		case <-ticker.C:
			r.drainAcquisitionInboxBeforeMaintenance(workLane)
			r.maintenanceTick()
		}
	}
}

const (
	consensusDrainBatch         = 32
	trustedValidationDrainBatch = 32
)

func (r *Router) drainTrustedValidationResults(ctx context.Context) bool {
	for range trustedValidationDrainBatch {
		select {
		case <-ctx.Done():
			return false
		case result := <-r.trustedValidationWorkResults():
			r.handleValidationWorkResult(result)
		default:
			return true
		}
	}
	return true
}

func (r *Router) handleUntrustedValidationWorkResult(
	ctx context.Context,
	result validationWorkResult,
) bool {
	if !r.drainTrustedValidationResults(ctx) {
		result.permit.release()
		return false
	}
	r.handleValidationWorkResult(result)
	return true
}

func (r *Router) drainConsensusInbox(ctx context.Context) bool {
	for range consensusDrainBatch {
		select {
		case <-ctx.Done():
			return false
		case msg, ok := <-r.inbox:
			if !ok {
				return false
			}
			r.handleInboundMessage(msg)
		default:
			return true
		}
	}
	return true
}

func (r *Router) drainAcquisitionInboxBeforeMaintenance(workLane *acquisitionWorkLane) int {
	drained := 0
	for drained < acquisitionWorkBatchLimit && workLane.canAcceptData() {
		select {
		case msg, ok := <-r.acqInbox:
			if !ok {
				r.acqInbox = nil
				return drained
			}
			r.handleInboundMessage(msg)
			drained++
		default:
			return drained
		}
	}
	return drained
}

// submitTxJob hands an inbound transaction to the worker pool, off the Run
// message loop. Before the first Run it handles synchronously for direct
// dispatch tests. Once shutdown begins, admission remains closed.
func (r *Router) submitTxJob(msg *peermanagement.InboundMessage) {
	if msg == nil {
		return
	}
	r.lifecycleMu.RLock()
	state := r.lifecycleState
	jobs := r.txJobs
	if state == routerLifecycleInitial {
		r.lifecycleMu.RUnlock()
		defer func() { _ = msg.Close() }()
		r.handleTransaction(msg)
		return
	}
	if state != routerLifecycleRunning || jobs == nil {
		r.lifecycleMu.RUnlock()
		r.droppedTxJobs.Add(1)
		_ = msg.Close()
		return
	}
	select {
	case jobs <- msg:
	default:
		r.droppedTxJobs.Add(1)
		_ = msg.Close()
		r.logger.Debug("inbound tx dropped: worker pool saturated",
			"t", "consensus", "event", "tx-shed", "peer", msg.PeerID)
	}
	r.lifecycleMu.RUnlock()
}

// DroppedTxJobs returns the cumulative count of inbound transactions shed
// because the worker pool was saturated.
func (r *Router) DroppedTxJobs() uint64 {
	return r.droppedTxJobs.Load()
}

// submitServeJob hands an inbound get_ledger request to the serve pool, off
// the Run message loop. Before the first Run it handles synchronously for
// direct dispatch tests. Once shutdown begins, admission remains closed.
func (r *Router) submitServeJob(msg *peermanagement.InboundMessage) {
	if msg == nil {
		return
	}
	r.lifecycleMu.RLock()
	state := r.lifecycleState
	jobs := r.serveJobs
	if state == routerLifecycleInitial {
		r.lifecycleMu.RUnlock()
		defer func() { _ = msg.Close() }()
		r.handleGetLedger(msg)
		return
	}
	if state != routerLifecycleRunning || jobs == nil {
		r.lifecycleMu.RUnlock()
		r.droppedServeJobs.Add(1)
		_ = msg.Close()
		return
	}
	select {
	case jobs <- msg:
	default:
		r.droppedServeJobs.Add(1)
		_ = msg.Close()
		r.logger.Debug("inbound get_ledger dropped: serve pool saturated",
			"t", "consensus", "event", "serve-shed", "peer", msg.PeerID)
	}
	r.lifecycleMu.RUnlock()
}

// DroppedServeJobs returns the cumulative count of inbound get_ledger
// requests shed because the serve pool was saturated.
func (r *Router) DroppedServeJobs() uint64 {
	return r.droppedServeJobs.Load()
}

func (r *Router) startManifestWorker(ctx context.Context) {
	workerCtx, cancel := context.WithCancel(ctx)
	r.manifestWorkerCancel = cancel
	r.manifestWorkerDone = make(chan struct{})
	inbox := r.manifestInbox
	done := r.manifestWorkerDone

	go func() {
		defer close(done)
		for {
			select {
			case <-workerCtx.Done():
				for {
					select {
					case msg, ok := <-inbox:
						if !ok {
							return
						}
						r.processManifestJobContext(workerCtx, msg)
					default:
						return
					}
				}
			case msg, ok := <-inbox:
				if !ok {
					return
				}
				r.processManifestJobContext(workerCtx, msg)
			}
		}
	}()
}

func (r *Router) stopManifestWorker() {
	r.manifestWorkerCancel()
	<-r.manifestWorkerDone
	r.manifestWorkerCancel = nil
	r.manifestWorkerDone = nil
}

func (r *Router) processManifestJob(msg *peermanagement.InboundMessage) {
	r.processManifestJobContext(context.Background(), msg)
}

func (r *Router) processManifestJobContext(ctx context.Context, msg *peermanagement.InboundMessage) {
	defer func() { _ = msg.Close() }()
	processed := false
	defer func() {
		if !processed {
			if acknowledger, ok := r.peerSessions.(peerBootstrapAcknowledger); ok {
				acknowledger.RejectPeerBootstrap(msg.PeerID)
			}
		}
	}()
	defer r.recoverFrame(msg, "manifest")
	if msg.ManifestFrame != nil {
		defer func() {
			if err := msg.ManifestFrame.Close(); err != nil {
				r.logger.Warn("failed to close manifest spool", "error", err, "peer", msg.PeerID)
			}
		}()
		payload, err := msg.ManifestFrame.Materialize(ctx)
		if err != nil {
			if errors.Is(err, message.ErrDecompressFailed) && r.gossip != nil &&
				!msg.SelectPeerCharge(resource.FeeInvalidData(), "decompress-lz4-failed") {
				r.gossip.IncPeerBadData(uint64(msg.PeerID), "decompress-lz4-failed")
			}
			r.logger.Warn("failed to materialize manifest spool", "error", err, "peer", msg.PeerID)
			return
		}
		msg.Payload = payload
	}
	processed = r.handleManifests(msg)
	msg.CompletePeerCharge()
	if processed {
		if acknowledger, ok := r.peerSessions.(peerBootstrapAcknowledger); ok {
			acknowledger.AcknowledgePeerBootstrap(msg.PeerID)
		}
	}
}

func (r *Router) submitManifestJob(msg *peermanagement.InboundMessage) {
	r.processManifestJob(msg)
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
	r.reconcilePeerAvailability()

	// Sub-task retry loop: rotate peers on silent-peer timeouts BEFORE
	// the outer budget kicks in (250ms × 10 rotations inside a larger
	// outer budget). Without rotation, a single silent peer burns the
	// full 10s before the legacy fallback fires.
	for _, rd := range r.replayer.SubTaskTimedOut() {
		tried := rd.TriedPeers()
		// Ask the overlay for a fresh replay-capable peer, excluding
		// every peer we've already tried for this hash.
		candidates := r.acquisition.ReplayCapablePeersExcluding(tried, 1)
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
		r.runLifecycleTask(func(context.Context) {
			if err := r.acquisition.RequestReplayDelta(newPeer, hash); err != nil {
				r.logger.Debug("replay-delta retry request failed",
					"seq", seq,
					"hash", fmt.Sprintf("%x", hash),
					"peer", newPeer,
					"err", err,
				)
			}
		})
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
		r.fallbackReplayAcquisition(entry.Seq, entry.Hash, entry.PeerID)
	}

	// Drive the timer-based retry loop over every in-flight legacy acquisition,
	// porting rippled's TimeoutCounter/InboundLedger::onTimer. A no-progress
	// interval escalates (broaden peers, re-request, fetch-pack, and once
	// aggressive ask for the missing nodes by content hash); an exhausted retry
	// budget fails the acquisition cleanly instead of re-arming the same stall
	// forever. Reaping here also unblocks startLedgerAcquisitionLegacy and the
	// replay-delta path, both of which refuse to arm while the hash is in flight.
	now := time.Now()

	r.fetchTracker.Sweep()
	r.retryInboundLedgerAcquisitions(now)
	r.rebootstrapFrozenPivotIfStalled(now)

	// Timer-driven catch-up re-arm (rippled LedgerMaster::doAdvance cadence): a
	// reaped/failed sole acquisition (cap=1) can't park catch-up until the next
	// gossip event. No-ops while an acquisition is in flight or the target is
	// reached; startLedgerAcquisition dedups the in-flight hash.
	r.armConsensusCatchup()

	// Backward history backfill of jump-adopt gaps (rippled fetchForHistory
	// from doAdvance), off the consensus catch-up slot.
	r.armHistoryBackfill()

	// Expire stale fetch-pack nodes so the cache doesn't retain a stalled
	// acquisition's nodes past their usefulness.
	r.fetchPacks.sweep(time.Now())

	// Timer-driven tx-set acquisition re-trigger. The inbound retry
	// (handleTxSetData) only advances when a TMLedgerData arrives; if a peer
	// falls silent mid-acquire nothing re-requests the remaining nodes and
	// the node stalls into wrongLedger.
	r.retryStalledTxSetAcquires()
}

func (r *Router) retryInboundLedgerAcquisitions(now time.Time) {
	workLane := r.currentAcquisitionWork()
	for _, il := range r.fetchTracker.Active() {
		if workLane != nil && !workLane.has(il) && !workLane.canAcceptNew() {
			il.RearmTimer(now)
			continue
		}
		if il.State() == inbound.StateFailed {
			if !r.submitAcquisitionWork(il, acquisitionWorkEvent{kind: acquisitionWorkFailure}) {
				r.logger.Warn("inbound ledger: failure snapshot deferred; acquisition worker saturated", "seq", il.Seq())
			}
			continue
		}
		if !il.TimerDue(now) {
			continue
		}
		if !r.submitAcquisitionWork(il, acquisitionWorkEvent{kind: acquisitionWorkTimerCheck, at: now}) {
			il.RearmTimer(now)
			r.logger.Warn("inbound ledger: timer check deferred; acquisition worker saturated", "seq", il.Seq())
		}
	}
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

const maxQueryDepth = 3

type logger interface {
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}
