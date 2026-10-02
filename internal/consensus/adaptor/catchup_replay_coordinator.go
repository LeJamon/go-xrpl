package adaptor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/shamap"
)

// catchupReplayCoordinator owns acquisition, catch-up targets, and replay. Router
// delivers peer events and worker results and drives bounded replay batches.
// Configuration and dependencies are fixed before Run; RPC acquisition requests
// and ledger callbacks may execute concurrently with the router loop.
//
// acquisitionMu guards replay generations, retries, recovery, and drain ownership.
// Canonical publication and cancellation take replayCommitMu before acquisitionMu;
// publication keeps replayCommitMu through storage and anchor advancement.
// Acquisition retirement and worker waits occur after releasing these locks.
// catchupMu protects the target and cooldowns, seqHashMu protects ancestry,
// historyMu protects backfill, and peersMu protects peer ledger observations.
// Header discovery takes headerDiscoveryMu before catchupMu or seqHashMu.
// lifecycleMu serializes task admission with cancellation; tasks are joined before
// the acquisition work lane and persistence lane are drained on Router shutdown.
type catchupReplayCoordinator struct {
	// Immutable service dependencies are supplied by Router construction. They
	// are the narrow services needed by catch-up/replay transitions; the
	// coordinator never reaches back into Router for mutable state.
	engine              consensus.RouterEngine
	adaptor             *Adaptor
	acquisition         ledgerAcquisitionNetwork
	peerSessions        peerSessionView
	logger              *slog.Logger
	floor               MinimumOnlineFloor
	forgetValidatorPeer func(uint64)
	onPeerDisconnect    func(peermanagement.PeerID)

	historyBackfill bool
	historyDepth    uint32

	peersMu                   sync.RWMutex
	peerStates                map[peermanagement.PeerID]*peerLedgerState
	peerStatusCandidates      map[peermanagement.PeerID]peerStatusCandidate
	headerDiscoveryMu         sync.Mutex
	headerDiscovery           *headerDiscoverySession
	headerDiscoveryGeneration uint64
	retiredHeaderRequests     map[[32]byte]time.Time

	// Lifecycle is independent of Router's wire/event dispatch. Tasks started
	// by a transition are canceled before acquisition registries are retired.
	lifecycleMu     sync.RWMutex
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	lifecycleWG     sync.WaitGroup

	replayer     *inbound.Replayer
	fetchTracker *inbound.Tracker
	fetchPacks   *fetchPackCache

	acquisitionWorkMu sync.RWMutex
	acquisitionFamily shamap.Family
	acquisitionStore  *acquisitionStoreLane
	acquisitionWork   *acquisitionWorkLane

	catchupMu          sync.Mutex
	catchup            catchupTarget
	catchupFailures    map[[32]byte]time.Time
	linkageWait        catchupLinkageWait
	peerStatusEvidence bool

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

	acquisitionMu             sync.Mutex
	replayFaultBlockWarningMu sync.Mutex
	replayFaultBlockWarningID string
	replayAvailabilityRetries map[[32]byte]replayAvailabilityRetryState
	replayFallbackRequired    map[[32]byte]uint32
	replayCommitMu            sync.Mutex
	consensusRecovery         consensusRecovery
	lastHandoffSeq            uint32
	standardReplay            standardReplayPipeline
	pendingFrozenPivot        frozenPivotPendingIntent

	standardReplayDrainWake  chan struct{}
	standardReplayDrainOwner *standardReplayDrainOwner
	stopped                  atomic.Bool

	historyMu     sync.Mutex
	history       catchupTarget
	historyFloor  uint32
	historySeeded bool

	seqHashMu     sync.Mutex
	seqHash       map[uint32]ledgerHashEntry
	seqHashAnchor uint32
}

func (c *catchupReplayCoordinator) beginShutdown() bool {
	if !c.stopped.CompareAndSwap(false, true) {
		return false
	}
	c.lifecycleMu.Lock()
	if c.lifecycleCancel != nil {
		c.lifecycleCancel()
		c.lifecycleCancel = nil
	}
	c.lifecycleMu.Unlock()
	return true
}

func (c *catchupReplayCoordinator) setPeerSessionView(view peerSessionView) {
	c.peerSessions = view
}

func (c *catchupReplayCoordinator) setValidatorPeerForget(forget func(uint64)) {
	c.forgetValidatorPeer = forget
}

func (c *catchupReplayCoordinator) setAcquisitionFamily(family shamap.Family) {
	c.acquisitionWorkMu.Lock()
	defer c.acquisitionWorkMu.Unlock()
	if family == nil {
		c.acquisitionFamily = nil
		c.acquisitionStore = nil
		return
	}
	c.acquisitionStore = newAcquisitionStoreLane(family, c.logger, acquisitionStoreQueueDepth)
	c.acquisitionFamily = c.acquisitionStore
}

func (c *catchupReplayCoordinator) setFloor(floor MinimumOnlineFloor) {
	c.floor = floor
}

func (c *catchupReplayCoordinator) minimumOnline() uint32 {
	if floor := c.floor; floor != nil {
		return floor.MinimumOnline()
	}
	return 0
}

func (c *catchupReplayCoordinator) lifecycleContext() context.Context {
	c.lifecycleMu.RLock()
	defer c.lifecycleMu.RUnlock()
	if c.lifecycleCtx == nil {
		return context.Background()
	}
	return c.lifecycleCtx
}

func (c *catchupReplayCoordinator) runLifecycleTask(fn func(context.Context)) bool {
	c.lifecycleMu.Lock()
	if c.stopped.Load() || c.lifecycleCtx == nil || c.lifecycleCancel == nil {
		c.lifecycleMu.Unlock()
		return false
	}
	ctx := c.lifecycleCtx
	c.lifecycleWG.Add(1)
	c.lifecycleMu.Unlock()
	go func() {
		defer c.lifecycleWG.Done()
		fn(ctx)
	}()
	return true
}

func (c *catchupReplayCoordinator) startTasks(parent context.Context) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.lifecycleCancel != nil {
		return
	}
	c.lifecycleCtx, c.lifecycleCancel = context.WithCancel(parent)
	if c.stopped.Load() {
		c.lifecycleCancel()
		c.lifecycleCancel = nil
	}
}

func (c *catchupReplayCoordinator) stopTasks() {
	c.lifecycleMu.Lock()
	cancel := c.lifecycleCancel
	c.lifecycleCancel = nil
	c.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.lifecycleWG.Wait()
}

// startAcquisitionWork drains processing before persistence during cleanup.
func (c *catchupReplayCoordinator) startAcquisitionWork(ctx context.Context) (*acquisitionWorkLane, func()) {
	if c == nil {
		return nil, func() {}
	}
	c.acquisitionWorkMu.Lock()
	store := c.acquisitionStore
	if store != nil {
		store.start(ctx)
	}
	work := c.acquisitionWork
	if work == nil {
		work = newAcquisitionWorkLane(acquisitionWorkQueueDepth)
		c.acquisitionWork = work
	}
	c.acquisitionWorkMu.Unlock()
	work.flush = c.flushAcquisitionStore
	work.start(ctx)
	return work, func() {
		work.stop()
		c.acquisitionWorkMu.Lock()
		if c.acquisitionWork == work {
			c.acquisitionWork = nil
		}
		c.acquisitionWorkMu.Unlock()
		if store != nil {
			store.stopDrain()
		}
	}
}

func (c *catchupReplayCoordinator) standardReplayDrainWakeChannel() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.standardReplayDrainWake
}

// stopAcquisitions is the sole terminal transition for acquisition state. It
// takes the publication lock before the acquisition lock so no replay apply or
// canonical store can race retirement, then releases both before waiting for
// persistence retirement.
func (c *catchupReplayCoordinator) stopAcquisitions() (legacy, replay int) {
	if c == nil || !c.beginShutdown() {
		return 0, 0
	}
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	var legacyLedgers []*inbound.Ledger
	if c.fetchTracker != nil {
		legacyLedgers = c.fetchTracker.Stop()
	}
	legacy = len(legacyLedgers)
	if c.replayer != nil {
		replay = c.replayer.Stop()
	}
	retirement := c.cancelStandardReplayPipelineLocked("shutdown")
	c.consensusRecovery = consensusRecovery{}
	c.pendingFrozenPivot = frozenPivotPendingIntent{}
	c.lastHandoffSeq = 0
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()

	c.catchupMu.Lock()
	c.catchup = catchupTarget{}
	c.catchupFailures = nil
	c.linkageWait = catchupLinkageWait{}
	c.peerStatusEvidence = false
	c.catchupMu.Unlock()
	c.cancelHeaderDiscovery()
	c.retireLegacyAcquisitions(legacyLedgers)
	if releaseDone := c.retireStandardReplay(retirement); releaseDone != nil {
		<-releaseDone
	}
	return legacy, replay
}

func (c *catchupReplayCoordinator) belowFloor(seq uint32) bool {
	floor := c.floor
	if floor == nil {
		return false
	}
	minimum := floor.MinimumOnline()
	return minimum != 0 && seq < minimum
}

func (c *catchupReplayCoordinator) admitInboundHeader(seq uint32) error {
	if !c.belowFloor(seq) {
		return nil
	}
	return fmt.Errorf("ledger %d is below the minimum online floor", seq)
}

func (c *catchupReplayCoordinator) acquisitionOpts() []inbound.Option {
	opts := []inbound.Option{inbound.WithHeaderAdmission(c.admitInboundHeader)}
	c.acquisitionWorkMu.RLock()
	store := c.acquisitionStore
	c.acquisitionWorkMu.RUnlock()
	if store != nil {
		opts = append(opts, inbound.WithFamily(store.scope()))
	}
	return opts
}

func (c *catchupReplayCoordinator) findAcquisition(hash [32]byte) *inbound.Ledger {
	if c == nil || c.fetchTracker == nil {
		return nil
	}
	return c.fetchTracker.Find(hash)
}

func (c *catchupReplayCoordinator) abandonTimedOutReplay(entry inbound.TimedOutEntry) {
	if c == nil || c.replayer == nil {
		return
	}
	c.acquisitionMu.Lock()
	c.requireReplayFullStateLocked(entry.Seq, entry.Hash)
	c.replayer.Abandon(entry.Hash)
	c.acquisitionMu.Unlock()
}

func (c *catchupReplayCoordinator) removePeerFromAcquisitions(peerID uint64) {
	if c.fetchTracker == nil {
		return
	}
	for _, il := range c.fetchTracker.Active() {
		il.RemovePeer(peerID)
	}
}

func (c *catchupReplayCoordinator) handlePeerDisconnect(peerID peermanagement.PeerID) {
	if c.stoppedForShutdown() {
		return
	}
	c.forgetPeer(peerID)
	c.removePeerFromAcquisitions(uint64(peerID))
	if adaptor := c.adaptor; adaptor != nil {
		adaptor.UpdatePeerLCL(uint64(peerID), consensus.LedgerID{})
	}
	if c.forgetValidatorPeer != nil {
		c.forgetValidatorPeer(uint64(peerID))
	}
}

func (c *catchupReplayCoordinator) invalidFutureLedgerSequence(seq uint32) bool {
	adaptor := c.adaptor
	if adaptor == nil {
		return false
	}
	svc := adaptor.LedgerService()
	if svc == nil || svc.GetValidatedLedgerAge() > 10*time.Second {
		return false
	}
	validated := svc.GetValidatedLedgerIndex()
	return seq > validated && seq-validated > 10
}

func (c *catchupReplayCoordinator) forgetPeer(peerID peermanagement.PeerID) {
	if c.stoppedForShutdown() {
		return
	}
	c.peersMu.Lock()
	delete(c.peerStates, peerID)
	delete(c.peerStatusCandidates, peerID)
	c.peersMu.Unlock()
	c.invalidateCatchupPeer(uint64(peerID))
	c.headerDiscoveryPeerDisconnected(uint64(peerID))
	c.invalidateHistoryPeer(uint64(peerID))
}

func (c *catchupReplayCoordinator) stoppedForShutdown() bool {
	return c.stopped.Load()
}

func newCatchupReplayCoordinator(engine consensus.RouterEngine, adaptor *Adaptor, network routerNetworkConfig, logger *slog.Logger) *catchupReplayCoordinator {
	return &catchupReplayCoordinator{
		engine:                    engine,
		adaptor:                   adaptor,
		acquisition:               network.acquisition,
		logger:                    logger,
		historyBackfill:           true,
		historyDepth:              256,
		peerStates:                make(map[peermanagement.PeerID]*peerLedgerState),
		peerStatusCandidates:      make(map[peermanagement.PeerID]peerStatusCandidate),
		replayer:                  inbound.NewReplayer(logger, inbound.SystemClock, inbound.DefaultMaxInFlightReplays),
		fetchTracker:              inbound.NewTrackerWithClockAndSweepInterval(network.inboundClock, network.inboundSweepInterval),
		fetchPacks:                newFetchPackCache(),
		catchupFailures:           make(map[[32]byte]time.Time),
		replayAvailabilityRetries: make(map[[32]byte]replayAvailabilityRetryState),
		replayFallbackRequired:    make(map[[32]byte]uint32),
		standardReplayDrainWake:   make(chan struct{}, 1),
		seqHash:                   make(map[uint32]ledgerHashEntry),
		lifecycleCtx:              context.Background(),
	}
}

func (c *catchupReplayCoordinator) acquireReplayParent(seq uint32, hash [32]byte) error {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	c.startLedgerAcquisitionLegacyModeLocked(seq, hash, 0, true)
	if !c.isAcquiringLocked(hash) {
		return fmt.Errorf("could not acquire replay parent %x", hash)
	}
	return nil
}

func (c *catchupReplayCoordinator) maintenanceTick() {
	if c.stoppedForShutdown() {
		return
	}
	c.expireReplayAvailabilityRetries()

	// Sub-task retry loop: rotate peers on silent-peer timeouts BEFORE
	// the outer budget kicks in (250ms × 10 rotations inside a larger
	// outer budget). Without rotation, a single silent peer burns the
	// full 10s before the legacy fallback fires.
	for _, rd := range c.replayer.SubTaskTimedOut() {
		tried := rd.TriedPeers()
		// Ask the overlay for a fresh replay-capable peer, excluding
		// every peer we've already tried for this hash.
		candidates := c.acquisition.ReplayCapablePeersExcluding(tried, 1)
		if len(candidates) == 0 {
			// No fresh peer available — can't rotate; the outer
			// budget below will eventually time this out and fall
			// back to the legacy path. Log so operators can see
			// replay-capacity exhaustion in diagnostics.
			c.logger.Debug("replay-delta sub-task timed out but no fresh peer available",
				"seq", rd.Seq(),
				"hash", fmt.Sprintf("%x", rd.Hash()),
				"retry_count", rd.RetryCount(),
			)
			continue
		}
		newPeer := candidates[0]
		rd.NoteSubTaskRetry(newPeer)
		// Dispatch the actual network send in a goroutine so a slow or
		// back-pressured overlay write doesn't block router message dispatch.
		// Replayer-state mutation (NoteSubTaskRetry above) already
		// happened on the loop goroutine, preserving the single-writer
		// invariant against handleMessage; on send failure the next
		// tick will rotate to another peer (the per-hash timeout
		// continues to run).
		seq := rd.Seq()
		hash := rd.Hash()
		c.runLifecycleTask(func(context.Context) {
			if err := c.acquisition.RequestReplayDelta(newPeer, hash); err != nil {
				c.logger.Debug("replay-delta retry request failed",
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
	for _, entry := range c.replayer.TimedOut() {
		c.logger.Warn("replay delta acquisition timed out, falling back to legacy",
			"seq", entry.Seq,
			"hash", fmt.Sprintf("%x", entry.Hash[:8]),
			"peer", entry.PeerID,
		)
		c.abandonTimedOutReplay(entry)
		c.fallbackReplayAcquisition(entry.Seq, entry.Hash, entry.PeerID)
	}

	// Drive the timer-based retry loop over every in-flight legacy acquisition,
	// porting rippled's TimeoutCounter/InboundLedger::onTimer. A no-progress
	// interval escalates (broaden peers, re-request, fetch-pack, and once
	// aggressive ask for the missing nodes by content hash); an exhausted retry
	// budget fails the acquisition cleanly instead of re-arming the same stall
	// forever. Reaping here also unblocks startLedgerAcquisitionLegacy and the
	// replay-delta path, both of which refuse to arm while the hash is in flight.
	now := time.Now()

	c.fetchTracker.Sweep()
	// Retry the actionable standard-replay head before rearming unrelated
	// acquisitions or extending the prepared suffix.
	c.retryStandardReplayAvailability(now)
	if generation, seq, hash, peerID, ok := c.standardReplayAvailabilityExhaustedState(); ok {
		c.reserveStandardReplayReplacement(generation, seq, hash, peerID, now)
	}
	c.retryInboundLedgerAcquisitions(now)
	c.tickHeaderDiscovery(now)
	c.retryStandardReplayReplacement(now)
	c.rebootstrapFrozenPivotIfStalled(now)

	// Timer-driven catch-up re-arm (rippled LedgerMaster::doAdvance cadence): a
	// reaped/failed sole acquisition (cap=1) can't park catch-up until the next
	// gossip event. No-ops while an acquisition is in flight or the target is
	// reached; startLedgerAcquisition dedups the in-flight hash.
	c.armConsensusCatchup()

	// Backward history backfill of jump-adopt gaps (rippled fetchForHistory
	// from doAdvance), off the consensus catch-up slot.
	c.armHistoryBackfill()

	// Expire stale fetch-pack nodes so the cache doesn't retain a stalled
	// acquisition's nodes past their usefulness.
	c.fetchPacks.sweep(time.Now())
}

func (c *catchupReplayCoordinator) retryInboundLedgerAcquisitions(now time.Time) {
	workLane := c.currentAcquisitionWork()
	for _, il := range c.fetchTracker.Active() {
		if workLane != nil && !workLane.has(il) && !workLane.canAcceptNew() {
			il.RearmTimer(now)
			continue
		}
		if il.State() == inbound.StateFailed {
			if !c.submitAcquisitionWork(il, acquisitionWorkEvent{kind: acquisitionWorkFailure}) {
				c.logger.Warn("inbound ledger: failure snapshot deferred; acquisition worker saturated", "seq", il.Seq())
			}
			continue
		}
		if !il.TimerDue(now) {
			continue
		}
		if !c.submitAcquisitionWork(il, acquisitionWorkEvent{kind: acquisitionWorkTimerCheck, at: now}) {
			il.RearmTimer(now)
			c.logger.Warn("inbound ledger: timer check deferred; acquisition worker saturated", "seq", il.Seq())
		}
	}
}

func (c *catchupReplayCoordinator) flushAcquisitionStore(ctx context.Context, ledger *inbound.Ledger) error {
	if c.acquisitionStore == nil || ledger == nil {
		return nil
	}
	return ledger.FlushPersistence(ctx)
}

func (c *catchupReplayCoordinator) retireAcquisitionStore(ctx context.Context, ledger *inbound.Ledger) {
	if ledger == nil {
		return
	}
	if err := ledger.RetirePersistence(ctx); err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Warn("inbound ledger: failed to retire persistence scope", "error", err, "seq", ledger.Seq())
	}
}

func (c *catchupReplayCoordinator) promoteAcquisitionStore(ctx context.Context, ledger *inbound.Ledger) error {
	if ledger == nil {
		return nil
	}
	return ledger.PromotePersistence(ctx)
}
