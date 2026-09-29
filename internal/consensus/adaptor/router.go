package adaptor

import (
	"context"
	"errors"
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
	peerSessions  peerSessionView

	// The overlay callback only records disconnects; a router-owned worker performs
	// cleanup so acquisition scans never block the overlay event loop.
	pendingPeerDisconnects sync.Map
	peerDisconnectWake     chan struct{}
	pendingPeerConnects    sync.Map
	peerConnectWake        chan struct{}
	catchupReplay          *catchupReplayCoordinator

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
	// the dispatch switch silently drops validator-list collection frames in
	// that case.
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
	// txSetLearnJobs shares the transaction workers, but never makes delivery
	// of an acquired consensus set wait for open-ledger membership or apply.
	txSetLearnJobs chan txSetLearnJob

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
	coord := newCatchupReplayCoordinator(engine, adaptor, network, logger)
	r := &Router{
		engine:                 engine,
		adaptor:                adaptor,
		gossip:                 network.gossip,
		txSetNet:               network.txSet,
		acquisition:            network.acquisition,
		serve:                  network.serve,
		inbox:                  inbox,
		logger:                 logger,
		peerDisconnectWake:     make(chan struct{}, 1),
		peerConnectWake:        make(chan struct{}, 1),
		catchupReplay:          coord,
		messageSeen:            newMessageSuppression(messageDedupTTL, messageDedupMaxEntries),
		manifestUntrustedLimit: manifest.DefaultMaxUntrustedCount,
		txSeen:                 newTransactionSuppression(5*time.Minute, 1<<17),
		txSetAcquire:           make(map[consensus.TxSetID]*txSetAcquireState),
		txSetRetryKnobs:        defaultTxSetRetryKnobs(),
		lifecycleCtx:           context.Background(),
	}
	coord.onPeerDisconnect = r.HandlePeerDisconnect
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
		adaptor.SetOnLedgerRequested(r.catchupReplay.requestConsensusLedger)
		adaptor.setOnLedgerSwitched(r.catchupReplay.onLedgerSwitched)
		adaptor.setOnLedgerFullyValidated(r.catchupReplay.onLedgerFullyValidated)
		adaptor.setOnLedgerBuilt(r.catchupReplay.onLedgerBuilt)
	}
	if adaptor != nil && adaptor.LedgerService() != nil {
		adaptor.LedgerService().SetReplayTargetAuthenticator(r.catchupReplay.replayTargetAuthenticated)
		adaptor.LedgerService().SetReplayParentAcquirer(coord.acquireReplayParent)
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
	if r.catchupReplay != nil {
		r.catchupReplay.setAcquisitionFamily(family)
	}
}

// SetMinimumOnlineFloor installs the online-delete retention floor. Once set,
// the router refuses to acquire or serve ledgers below it. A nil floor leaves
// both paths unrestricted, so the disabled / standalone case is unchanged.
func (r *Router) SetMinimumOnlineFloor(floor MinimumOnlineFloor) {
	if r.catchupReplay != nil {
		r.catchupReplay.setFloor(floor)
	}
}

// belowFloor reports whether seq sits below the online-delete retention floor.
// A nil floor or a zero floor (no rotation yet) never withholds anything,
// mirroring rippled where shouldAcquire treats an unset minimumOnline as no
// lower bound.
func (r *Router) belowFloor(seq uint32) bool {
	if r.catchupReplay != nil {
		return r.catchupReplay.belowFloor(seq)
	}
	return false
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
	if r.catchupReplay != nil {
		r.catchupReplay.setPeerSessionView(view)
	}
}

// SetValidatorListAggregator installs the publisher-trust subsystem.
// Calling with a nil aggregator disables the validator-list collection path —
// the dispatch switch silently
// drops inbound frames in that case. Safe to call before Run.
func (r *Router) SetValidatorListAggregator(agg *validatorlist.Aggregator) {
	r.validatorList = agg
	if r.catchupReplay != nil {
		if agg == nil {
			r.catchupReplay.setValidatorPeerForget(nil)
		} else {
			r.catchupReplay.setValidatorPeerForget(agg.ForgetPeer)
		}
	}
}

// StopAcquisitions terminally drains both inbound-ledger acquisition paths.
// A stopped Router is not reusable; a process restart constructs new components.
func (r *Router) StopAcquisitions() (legacy, replay int) {
	if r == nil {
		return 0, 0
	}
	if r.catchupReplay == nil {
		return 0, 0
	}
	return r.catchupReplay.stopAcquisitions()
}

// HandlePeerDisconnect drops all per-peer state the router holds for
// peerID: the peer's last-reported ledger, its status-change vote in
// the engine's getNetworkLedger fold, and any lingering acquisition
// references. Wired from the overlay's peer-disconnect callback at
// startup so the state is freed the instant the peer goes away,
// instead of lingering until the next ledger adoption happens to
// overwrite it.
func (r *Router) HandlePeerDisconnect(peerID peermanagement.PeerID) {
	if r.catchupReplay != nil && r.catchupReplay.stoppedForShutdown() {
		return
	}
	r.pendingPeerConnects.Delete(peerID)
	if r.catchupReplay != nil {
		r.catchupReplay.handlePeerDisconnect(peerID)
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

// Run reads messages from the overlay and dispatches them.
// It blocks until the context is cancelled. A periodic maintenance tick
// also runs in this loop to time out stuck inbound replay-delta
// acquisitions and fall back to the legacy mtGET_LEDGER path.
func (r *Router) Run(ctx context.Context) {
	runCtx, ok := r.startLifecycle(ctx)
	if !ok {
		return
	}
	var workLane *acquisitionWorkLane
	stopAcquisitionWork := func() {}
	if r.catchupReplay != nil {
		workLane, stopAcquisitionWork = r.catchupReplay.startAcquisitionWork(runCtx)
	}
	defer stopAcquisitionWork()

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
		case <-r.catchupReplay.lifecycleContext().Done():
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
			r.catchupReplay.handleAcquisitionWorkResult(result)
		case result := <-r.trustedValidationWorkResults():
			r.handleValidationWorkResult(result)
		case result := <-r.untrustedValidationWorkResults():
			if !r.handleUntrustedValidationWorkResult(runCtx, result) {
				return
			}
		case <-r.catchupReplay.standardReplayDrainWakeChannel():
			r.catchupReplay.drainStandardReplayPipeline()
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

// DroppedTxJobs returns the cumulative count of inbound transactions and
// acquired transaction learning jobs shed at saturation or shutdown.
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
	r.catchupReplay.maintenanceTick()
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

const maxQueryDepth = 3

type logger interface {
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}
