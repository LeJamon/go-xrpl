// Package rcl implements the Ripple Consensus Ledger algorithm.
// This is the default consensus algorithm used by the XRP Ledger.
package rcl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
)

var (
	errLedgerAcceptInProgress = errors.New("rcl: ledger acceptance in progress")
	errNoLastClosedLedger     = errors.New("rcl: adaptor reported no last closed ledger")
)

type roundState struct {
	Round          consensus.RoundID
	CloseTimes     consensus.CloseTimes
	OurPosition    *consensus.Proposal
	StartTime      time.Time
	PhaseStart     time.Time
	HaveCorrectLCL bool
}

// Engine implements the RCL consensus algorithm.
type Engine struct {
	lifecycleMu sync.Mutex
	started     bool
	stopped     bool
	mu          sync.RWMutex

	// Configuration
	timing     consensus.Timing
	thresholds consensus.Thresholds

	// Dependencies
	adaptor  consensus.Adaptor
	eventBus *consensus.EventBus

	// listedOracle / relayPolicy are the adaptor's optional untrusted-
	// validation extensions, resolved once at construction. Nil when the
	// adaptor doesn't implement them: nothing is listed, only trusted
	// validations relay.
	listedOracle            consensus.ListedOracle
	relayPolicy             consensus.ValidationRelayPolicy
	acceptDeferrer          consensus.LedgerAcceptDeferrer
	acceptDeferrerLifecycle consensus.LedgerAcceptDeferrerLifecycle

	// Current state
	mode  consensus.Mode
	phase consensus.Phase
	// modeAtomic mirrors mode for lock-free reads on the RPC hot path
	// (server_info → IsProposing) and to break an ABBA deadlock between
	// OnValidation and GetServerInfo. Written in setMode under e.mu.
	modeAtomic atomic.Int32
	// lastCloseAtomic mirrors (prevProposers, prevRoundTime) for lock-free
	// GetLastCloseInfo reads (same RPC-hot-path rationale as modeAtomic).
	// Written from acceptLedger under e.mu via storeLastCloseLocked.
	lastCloseAtomic atomic.Pointer[lastCloseInfo]
	// bowedOut is the per-round voluntary bow-out: the configured validator
	// list expired, so this round neither proposes nor validates. Snapshotted
	// at round start (under e.mu); atomic for the lock-free IsValidating path.
	bowedOut atomic.Bool
	// validating is the per-round validator eligibility snapshot. It is
	// independent of sync state: observing validators may emit partial
	// validations. Written at round start under e.mu and read lock-free by RPC.
	validating atomic.Bool
	state      *roundState
	prevLedger consensus.Ledger
	// acceptedLCL records the adaptor's accepted ledger while consensus is
	// intentionally working from a different validation-preferred ledger.
	acceptedLCL consensus.LedgerID
	// roundCloseResolution is derived from prevLedger at round start. It must
	// not follow the adaptor's accepted LCL during a preferred-ledger switch.
	roundCloseResolution time.Duration

	// buildInProgress is set while acceptLedger applies the LCL off e.mu
	// (rippled's jtACCEPT job window). While set, round-driving parks so no
	// second goroutine starts a round before the commit
	// tail runs. Mutated under e.mu.
	buildInProgress   bool
	buildingLedgerSeq atomic.Uint32

	ourTxSet consensus.TxSet

	// censorship tracks txs we propose but that never get included, warning
	// on persistent exclusion. Observational only; touched solely from the
	// consensus goroutine under e.mu.
	censorship censorshipDetector

	// proposalTracker owns the round-scoped peer-signal maps. Accessed only
	// under e.mu (see proposalTracker).
	proposalTracker *proposalTracker

	// validationTracker accumulates trusted validations across ledgers and
	// fires the fully-validated callback at quorum, driving
	// server_info.validated_ledger forward.
	validationTracker  *ValidationTracker
	validationConfigMu sync.Mutex

	// disputeTracker owns the per-tx DisputedTx entries and per-peer vote
	// map. Written by createDisputesAgainst / OnProposal / OnTxSet /
	// UpdateOurPositions, read during checkConvergence.
	disputeTracker *disputeTracker

	// acquiredTxSets caches peer tx sets in memory by TxSetID, populated by
	// our BuildTxSet output and OnTxSet. Dispute wiring reads it to learn
	// which txs a peer's position contains.
	acquiredTxSets map[consensus.TxSetID]consensus.TxSet

	// comparesTxSets dedupes createDisputes: once a peer tx set is diffed,
	// repeats are cheap no-ops.
	comparesTxSets map[consensus.TxSetID]struct{}

	// parms holds the avalanche-threshold parameters for per-tx re-voting.
	parms consensus.ConsensusParms

	// peerUnchangedCounter counts consecutive phaseEstablish ticks with no
	// peer dispute-vote flip; drives dispute stall detection.
	peerUnchangedCounter int

	// establishCounter counts phaseEstablish ticks since closeLedger; floors
	// the per-dispute AvalancheCounter and gates the Expired-retry dwell.
	establishCounter int

	// Heartbeat ticker — single global timer at ledgerGRANULARITY cadence.
	heartbeat *time.Ticker
	// heartbeatNow stays wall-clock based when simulations replace now.
	heartbeatNow func() time.Time

	// Lifecycle
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	done     chan error
	doneOnce sync.Once

	// now is the wall-clock source for round/phase DURATION metrics
	// (time.Now in prod, a csf virtual clock under simulation). Distinct
	// from adaptor.Now() (offset-adjusted) — durations need one consistent
	// clock; see startRoundLocked.
	now func() time.Time

	// manualTick makes Start skip the heartbeat goroutine so an external
	// driver (csf) advances the state machine via TimerEntry.
	manualTick bool

	// closeTime owns the close-time consensus state. Accessed only under
	// e.mu (see closeTimeTracker).
	closeTime *closeTimeTracker

	prevRoundTime  time.Duration
	roundStartTime time.Time

	firstRound bool

	// lastConvergePercent retains convergePercent() from the last
	// phaseEstablish tick (reset at round start) so consensus_info reports a
	// meaningful value between rounds. The live convergePercent() still
	// drives establish-phase avalanche logic.
	lastConvergePercent int
	// currentRoundTime is the establish-phase round time from the last
	// phaseEstablish tick, frozen at consensus so consensus_info reports the
	// final round time while a round result exists.
	currentRoundTime time.Duration

	// Trusted proposers in the previous round; used by shouldCloseLedger for
	// peer pressure.
	prevProposers int

	// prevCloseTime is our own observed close time carried across rounds.
	// shouldCloseLedger measures idle time from it, instead of the previous
	// ledger's stored close time, when that close can't be trusted — see
	// lastCloseBaseline.
	prevCloseTime time.Time

	// wrongLedgerID is the ledger we're acquiring in ModeWrongLedger;
	// prevents spamming handleWrongLedger.
	wrongLedgerID consensus.LedgerID

	// lastRefusedSwitch de-duplicates the switch-refused log while checkLedger
	// keeps re-deriving the same incompatible target.
	lastRefusedSwitch consensus.LedgerID

	// roundExpiredReported de-duplicates the round-expired warn/event while an
	// expired round waits at the close-time gate; reset each startRoundLocked.
	roundExpiredReported bool

	// lastSignTime is the whole-second, wire-canonical monotonic floor for
	// emitted validation SignTime: a regressing adaptor clock (NTP step, VM
	// pause) is bumped to lastSignTime+1s so peers never see non-monotonic
	// validations.
	// Protected by e.mu.
	lastSignTime time.Time

	// Highest seq this node has validated (SeqEnforcer floor); prevents
	// conflicting same-seq validations (#401). Protected by e.mu.
	ourLastValidatedSeq uint32

	// When the floor was last bumped; after validationSetExpires of silence
	// it resets to 0 so a restarted validator can resume below its old floor.
	ourLastValidatedTime time.Time

	// Stats
	consensusCount uint64

	// archive persists stale validations dropped by the tracker (optional;
	// nil is fully functional). Atomic so the fully-validated callback reads
	// it lock-free even when Add runs outside e.mu.
	archive atomic.Pointer[archiveBox]

	// inMemoryLedgers is the tracker's retention window: validations below
	// (fullyValidatedSeq - n) are dropped to the archive via OnStale. Zero
	// disables auto-expiry. Atomic, same reason as archive.
	inMemoryLedgers atomic.Uint32

	// ledgerAncestry is staged by startup wiring, applied to the tracker in
	// Start. Nil keeps flat-count semantics.
	ledgerAncestry LedgerAncestryProvider

	// pendingPostUnlock queues work produced under e.mu so it runs after Unlock.
	// Mutated only under e.mu; drained by takePendingPostUnlockLocked.
	pendingPostUnlock []func()

	// pendingValidationBroadcasts run after all queued finality deferrals drain.
	pendingValidationBroadcasts []*consensus.Validation

	// missedHeartbeats counts dropped heartbeat ticks (gap > 2× interval).
	// time.Ticker silently coalesces ticks under load; this surfaces that
	// pressure so stalls don't hide.
	missedHeartbeats atomic.Uint64

	// stallPing, when set, is called once per run-loop iteration so the
	// stall watchdog sees the loop is alive. Atomic for lock-free read; nil
	// disables it.
	stallPing atomic.Pointer[func()]

	// deferPostUnlock > 0 inside timerEntry / StartRound enables deferred work;
	// at zero the broadcast helpers send synchronously so direct callers (tests)
	// observe broadcasts immediately. Mutated under e.mu.
	deferPostUnlock int

	// previousTrustedSet is the trusted set from the previous
	// startRoundLocked; diffed against the current set each round to derive
	// the `added` delta for OnUNLChange. Seeded once (see
	// previousTrustedSeeded). Mutated under e.mu.
	previousTrustedSet map[consensus.NodeID]struct{}

	// previousTrustedSeeded latches after the first call with a non-nil
	// prevLedger. Until then the next call seeds previousTrustedSet from the
	// startup UNL and skips OnUNLChange, so the startup UNL is not reported
	// as `added`. Mutated under e.mu.
	previousTrustedSeeded bool

	// trustMu protects the callback-side trust snapshot and deferred proposal
	// purges. Trust-change callbacks deliberately do not take e.mu because a
	// refresh can publish while consensus already holds it; the next engine
	// critical section applies the queued purges before replay/tally use.
	trustMu            sync.Mutex
	trustedSnapshot    map[consensus.NodeID]struct{}
	pendingTrustPurge  map[consensus.NodeID]struct{}
	trustSnapshotReady bool
}

type validationConfigProvider interface {
	GetValidationConfig() ([]consensus.NodeID, int, []consensus.NodeID)
}

type validationConfigChangeNotifier interface {
	OnValidationConfigChanged(func())
}

func validationConfig(adaptor consensus.Adaptor) ([]consensus.NodeID, int, []consensus.NodeID) {
	if provider, ok := adaptor.(validationConfigProvider); ok {
		return provider.GetValidationConfig()
	}
	trusted, quorum := adaptor.GetTrustedValidatorsAndQuorum()
	return trusted, quorum, adaptor.GetNegativeUNL()
}

func (e *Engine) setValidationConfig(trusted []consensus.NodeID, quorum int, negativeUNL []consensus.NodeID) {
	e.validationConfigMu.Lock()
	tracker := e.validationTracker
	if tracker != nil {
		tracker.updateTrustedQuorumAndNegativeUNL(trusted, quorum, negativeUNL)
	}
	e.validationConfigMu.Unlock()
	if tracker != nil {
		tracker.drainFinality()
		tracker.checkAcquired()
	}
}

func (e *Engine) refreshValidationConfig() int {
	e.validationConfigMu.Lock()
	tracker := e.validationTracker
	if tracker == nil {
		e.validationConfigMu.Unlock()
		return 0
	}
	trusted, quorum, negativeUNL := validationConfig(e.adaptor)
	tracker.updateTrustedQuorumAndNegativeUNL(trusted, quorum, negativeUNL)
	e.validationConfigMu.Unlock()
	tracker.drainFinality()
	tracker.checkAcquired()
	return quorum
}

func (e *Engine) refreshValidationConfigDeferredLocked() int {
	e.validationConfigMu.Lock()
	tracker := e.validationTracker
	if tracker == nil {
		e.validationConfigMu.Unlock()
		return 0
	}
	trusted, quorum, negativeUNL := validationConfig(e.adaptor)
	tracker.beginFinalityDeferral()
	tracker.updateTrustedQuorumAndNegativeUNL(trusted, quorum, negativeUNL)
	e.validationConfigMu.Unlock()
	e.pendingPostUnlock = append(e.pendingPostUnlock, func() {
		tracker.endFinalityDeferral()
		tracker.checkAcquired()
	})
	return quorum
}

// refreshUNLStateDeferredLocked requires e.mu and an active post-unlock scope.
func (e *Engine) refreshUNLStateDeferredLocked() {
	tracker := e.validationTracker
	if tracker == nil {
		e.adaptor.RefreshUNLState()
		return
	}
	tracker.beginFinalityDeferral()
	e.adaptor.RefreshUNLState()
	e.pendingPostUnlock = append(e.pendingPostUnlock, tracker.endFinalityDeferral)
}

var _ consensus.EngineTerminal = (*Engine)(nil)

// ValidationArchive is the archive API subset the engine consumes,
// decoupling rcl from the concrete archive type.
type ValidationArchive interface {
	OnStale(*consensus.Validation)
	NoteFullyValidated(seq uint32)
	Close(ctx context.Context) error
}

// archiveBox wraps ValidationArchive for atomic.Pointer (atomic.Value
// panics on nil store / type change).
type archiveBox struct{ a ValidationArchive }

func (e *Engine) loadArchive() ValidationArchive {
	if box := e.archive.Load(); box != nil {
		return box.a
	}
	return nil
}

// enqueueProposalBroadcastLocked stages a proposal to broadcast after e.mu
// is released (see pendingPostUnlock). Caller must hold e.mu. With no
// deferred scope active the send is synchronous.
func (e *Engine) enqueueProposalBroadcastLocked(p *consensus.Proposal) {
	if p == nil {
		return
	}
	if e.deferPostUnlock == 0 {
		e.broadcastProposal(p)
		return
	}
	e.pendingPostUnlock = append(e.pendingPostUnlock, func() {
		e.broadcastProposal(p)
	})
}

// broadcastProposal emits our own proposal, logging on failure. A silently
// dropped own-proposal makes the node stop participating in consensus while
// still appearing healthy — the invisible bow-out class the liveness audits
// chased — so the emission stays fire-and-forget but is no longer silent.
func (e *Engine) broadcastProposal(p *consensus.Proposal) {
	if err := e.adaptor.BroadcastProposal(p); err != nil {
		slog.Warn("failed to broadcast own proposal", "t", "consensus", "err", err)
	}
}

// enqueueValidationBroadcastLocked stages a validation to be broadcast
// after e.mu is released. Caller must hold e.mu.
func (e *Engine) enqueueValidationBroadcastLocked(v *consensus.Validation) {
	if v == nil {
		return
	}
	if e.deferPostUnlock == 0 {
		e.broadcastValidation(v)
		return
	}
	e.pendingValidationBroadcasts = append(e.pendingValidationBroadcasts, v)
}

// broadcastValidation emits our own validation, logging on failure. Like
// broadcastProposal, a silent drop is a liveness-critical invisible bow-out.
func (e *Engine) broadcastValidation(v *consensus.Validation) {
	if err := e.adaptor.BroadcastValidation(v); err != nil {
		slog.Warn("failed to broadcast own validation", "t", "consensus", "err", err)
	}
}

// takePendingPostUnlockLocked drains the queued post-lock closures.
// Caller must hold e.mu; pass the result to runPostUnlock after Unlock.
func (e *Engine) takePendingPostUnlockLocked() []func() {
	total := len(e.pendingPostUnlock) + len(e.pendingValidationBroadcasts)
	if total == 0 {
		return nil
	}
	out := make([]func(), 0, total)
	out = append(out, e.pendingPostUnlock...)
	for _, validation := range e.pendingValidationBroadcasts {
		out = append(out, func() {
			e.broadcastValidation(validation)
		})
	}
	e.pendingPostUnlock = nil
	e.pendingValidationBroadcasts = nil
	return out
}

// runPostUnlock runs each queued closure. MUST be called with e.mu
// released.
func runPostUnlock(pending []func()) {
	for _, fn := range pending {
		fn()
	}
}

// validationSetExpires mirrors rippled's validationSET_EXPIRES. It is
// both the SeqEnforcer reset window and the access-age retention floor
// for per-ledger validation sets: ExpireOld keeps a set resident until
// at least this long has passed since it was created or last read.
const validationSetExpires = 10 * time.Minute

// defaultInMemoryLedgers bounds the tracker's retention with no archive
// configured; without it the per-ledger maps grow unbounded. Matches the
// archive's own default window so behaviour is archive-independent.
const defaultInMemoryLedgers = uint32(256)

// Config holds RCL engine configuration.
type Config struct {
	Timing     consensus.Timing
	Thresholds consensus.Thresholds

	// Clock overrides the wall-clock source for duration metrics. Nil means
	// time.Now; csf injects a virtual clock for deterministic runs.
	Clock func() time.Time

	// ManualTick disables the heartbeat goroutine; the caller drives ticks
	// via TimerEntry. Used by csf.
	ManualTick bool
}

func DefaultConfig() Config {
	return Config{
		Timing:     consensus.DefaultTiming(),
		Thresholds: consensus.DefaultThresholds(),
	}
}

func NewEngine(adaptor consensus.Adaptor, config Config) *Engine {
	e := &Engine{
		timing:            config.Timing,
		thresholds:        config.Thresholds,
		adaptor:           adaptor,
		eventBus:          consensus.NewEventBus(100),
		mode:              consensus.ModeObserving,
		phase:             consensus.PhaseAccepted,
		proposalTracker:   newProposalTracker(),
		closeTime:         newCloseTimeTracker(),
		disputeTracker:    newDisputeTracker(),
		acquiredTxSets:    make(map[consensus.TxSetID]consensus.TxSet),
		comparesTxSets:    make(map[consensus.TxSetID]struct{}),
		parms:             consensus.DefaultConsensusParms(),
		now:               config.Clock,
		heartbeatNow:      time.Now,
		manualTick:        config.ManualTick,
		firstRound:        true,
		trustedSnapshot:   make(map[consensus.NodeID]struct{}),
		pendingTrustPurge: make(map[consensus.NodeID]struct{}),
	}
	if e.now == nil {
		e.now = time.Now
	}
	e.listedOracle, _ = adaptor.(consensus.ListedOracle)
	e.relayPolicy, _ = adaptor.(consensus.ValidationRelayPolicy)
	e.acceptDeferrer, _ = adaptor.(consensus.LedgerAcceptDeferrer)
	e.acceptDeferrerLifecycle, _ = adaptor.(consensus.LedgerAcceptDeferrerLifecycle)
	e.modeAtomic.Store(int32(e.mode))
	return e
}

// recordTrustTransition records validators removed from the live trusted set
// without taking e.mu. Applying the purge is deferred until an engine critical
// section so callback delivery cannot deadlock round driving.
func (e *Engine) recordTrustTransition(trusted []consensus.NodeID) {
	current := make(map[consensus.NodeID]struct{}, len(trusted))
	for _, nodeID := range trusted {
		current[nodeID] = struct{}{}
	}

	e.trustMu.Lock()
	for nodeID := range e.trustedSnapshot {
		if _, stillTrusted := current[nodeID]; !stillTrusted {
			e.pendingTrustPurge[nodeID] = struct{}{}
		}
	}
	e.trustedSnapshot = current
	e.trustMu.Unlock()
}

// purgePendingTrustLocked applies callback-observed trust removals to both
// proposal buffers and dispute votes. Caller holds e.mu.
func (e *Engine) purgePendingTrustLocked() {
	e.trustMu.Lock()
	pending := make([]consensus.NodeID, 0, len(e.pendingTrustPurge))
	for nodeID := range e.pendingTrustPurge {
		pending = append(pending, nodeID)
	}
	clear(e.pendingTrustPurge)
	e.trustMu.Unlock()

	for _, nodeID := range pending {
		e.proposalTracker.purgeNode(nodeID)
		if e.disputeTracker != nil {
			e.disputeTracker.unVote(nodeID)
		}
	}
}

// appendReplayCloseTimesLocked records replayed initial votes that still belong
// to the callback-linearized trusted set. An initial vote remains eligible
// after a later seqLeave removes the node's final current position. Caller
// holds e.mu.
func (e *Engine) appendReplayCloseTimesLocked(votes []replayCloseTime) {
	if e.state == nil {
		return
	}
	e.purgePendingTrustLocked()
	e.trustMu.Lock()
	if e.trustSnapshotReady {
		defer e.trustMu.Unlock()
		for _, vote := range votes {
			if _, pending := e.pendingTrustPurge[vote.NodeID]; pending {
				continue
			}
			if _, trusted := e.trustedSnapshot[vote.NodeID]; !trusted {
				continue
			}
			e.state.CloseTimes.Peers[vote.CloseTime]++
		}
		return
	}
	e.trustMu.Unlock()
	trusted := e.trustedPredicate()
	for _, vote := range votes {
		if !trusted(vote.NodeID) {
			continue
		}
		e.state.CloseTimes.Peers[vote.CloseTime]++
	}
}

// trustedPredicate returns one callback-linearized trust view for a
// multi-node decision. Production adaptors install the immutable snapshot
// before invoking the callback; copying it here prevents a trust transition
// between two entries in one tally from producing a mixed-epoch result.
// Adaptors without TrustChangeNotifier retain their direct predicate.
func (e *Engine) trustedPredicate() func(consensus.NodeID) bool {
	e.trustMu.Lock()
	if !e.trustSnapshotReady {
		e.trustMu.Unlock()
		return e.adaptor.IsTrusted
	}
	snapshot := make(map[consensus.NodeID]struct{}, len(e.trustedSnapshot))
	for nodeID := range e.trustedSnapshot {
		snapshot[nodeID] = struct{}{}
	}
	e.trustMu.Unlock()
	return func(nodeID consensus.NodeID) bool {
		_, trusted := snapshot[nodeID]
		return trusted
	}
}

// TimerEntry runs one heartbeat dispatch synchronously. For ManualTick
// mode: an external driver (csf) advances the state machine.
func (e *Engine) TimerEntry() {
	e.timerEntry()
}

// SetArchive wires (or, with nil, detaches) the validation archive.
// Detach clears the onStale callback so the archive can be Close()d
// without a use-after-close send. Safe before or after Start; callers must
// not replace the owned archive concurrently with Start or Stop.
func (e *Engine) SetArchive(a ValidationArchive) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if a == nil {
		e.archive.Store(nil)
	} else {
		e.archive.Store(&archiveBox{a: a})
	}
	if e.validationTracker == nil {
		return
	}
	if a == nil {
		e.validationTracker.SetOnStale(nil)
		return
	}
	e.validationTracker.SetOnStale(a.OnStale)
}

// SetInMemoryLedgers sets how many fully-validated ledgers of validation
// history the tracker keeps; older validations are evicted to the archive.
// Zero disables auto-eviction.
func (e *Engine) SetInMemoryLedgers(n uint32) {
	e.inMemoryLedgers.Store(n)
}

// SetLedgerAncestryProvider installs the trie's ancestry provider.
// Safe before or after Start; nil reverts to flat-count support.
func (e *Engine) SetLedgerAncestryProvider(p LedgerAncestryProvider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ledgerAncestry = p
	if e.validationTracker != nil {
		e.validationTracker.setLedgerAncestryProvider(p)
	}
}

// SetStallPing installs the stall watchdog's heartbeat callback, invoked
// once per run-loop iteration. Nil disables. Must be cheap and
// non-blocking — it runs inside the consensus loop.
func (e *Engine) SetStallPing(ping func()) {
	if ping == nil {
		e.stallPing.Store(nil)
		return
	}
	e.stallPing.Store(&ping)
}

func (e *Engine) Start(ctx context.Context) error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	if e.stopped {
		return fmt.Errorf("start engine: %w", consensus.ErrEventBusStopped)
	}
	if e.started {
		return fmt.Errorf("start engine: %w", consensus.ErrEventBusStarted)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	ledger, err := e.adaptor.GetLastClosedLedger()
	if err != nil {
		return fmt.Errorf("failed to get last closed ledger: %w", err)
	}
	if err := e.eventBus.Start(); err != nil {
		return fmt.Errorf("start event bus: %w", err)
	}
	e.started = true
	if ctx == nil {
		ctx = context.Background()
	}
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.done = make(chan error, 1)
	e.prevLedger = ledger

	trusted, quorum, negativeUNL := validationConfig(e.adaptor)
	e.validationTracker = NewValidationTracker(quorum)
	e.setValidationConfig(trusted, quorum, negativeUNL)
	e.validationTracker.setQuorumUnavailableFunc(e.adaptor.IsQuorumUnavailable)
	e.trustMu.Lock()
	e.trustedSnapshot = make(map[consensus.NodeID]struct{}, len(trusted))
	for _, nodeID := range trusted {
		e.trustedSnapshot[nodeID] = struct{}{}
	}
	e.pendingTrustPurge = make(map[consensus.NodeID]struct{})
	e.trustMu.Unlock()
	if wired, ok := e.adaptor.(consensus.WireableAdaptor); ok {
		wired.SetValidationHistorian(e.validationTracker)
	}
	// Promote stored validations the moment the UNL mutates (rippled
	// trustChanged) instead of at the next accepted ledger — a stalled node
	// may never accept, and the whole point of a runtime trust grant is to
	// count what the newly-trusted validator already signed.
	if notifier, ok := e.adaptor.(consensus.TrustChangeNotifier); ok {
		tracker := e.validationTracker
		e.trustMu.Lock()
		e.trustSnapshotReady = true
		e.trustMu.Unlock()
		notifier.OnTrustChanged(func(trusted []consensus.NodeID, quorum int) {
			if _, ok := e.adaptor.(validationConfigProvider); ok {
				e.refreshValidationConfig()
			} else {
				e.setValidationConfig(trusted, quorum, e.adaptor.GetNegativeUNL())
			}
			e.recordTrustTransition(trusted)
		})
		if settled, ok := e.adaptor.(consensus.TrustChangeSettledNotifier); ok {
			settled.OnTrustSettled(tracker.recheckFinality)
		}
	}
	_, validationConfigNotified := e.adaptor.(validationConfigChangeNotifier)
	if notifier, ok := e.adaptor.(validationConfigChangeNotifier); ok {
		notifier.OnValidationConfigChanged(func() { e.refreshValidationConfig() })
		e.refreshValidationConfig()
	}
	if e.ledgerAncestry != nil {
		e.validationTracker.setLedgerAncestryProvider(e.ledgerAncestry)
	}
	// Network-adjusted clock for freshness checks — avoids rejecting our own
	// just-signed validation by the close-time offset on a skewed node.
	e.validationTracker.SetNow(e.adaptor.Now)
	if arc := e.loadArchive(); arc != nil {
		e.validationTracker.SetOnStale(arc.OnStale)
	}
	tracker := e.validationTracker
	e.validationTracker.SetFullyValidatedCallback(func(ledgerID consensus.LedgerID, seq uint32) {
		// e.archive / e.inMemoryLedgers are read via atomics to stay
		// race-free against SetArchive.
		e.adaptor.OnLedgerFullyValidated(ledgerID, seq)
		if !validationConfigNotified {
			e.refreshValidationConfig()
		}

		arc := e.loadArchive()
		inMem := e.inMemoryLedgers.Load()

		if arc != nil {
			arc.NoteFullyValidated(seq)
		}
		// Drive in-memory retention: ExpireOld fires onStale per evicted
		// validation (archive captures it first). Runs with or without an archive;
		// the archive's InMemoryLedgers overrides, else defaultInMemoryLedgers.
		retention := inMem
		if retention == 0 {
			retention = defaultInMemoryLedgers
		}
		if seq > retention {
			tracker.ExpireOld(seq - retention)
		}
	})

	// Start the main loop, unless an external driver advances ticks.
	if !e.manualTick {
		e.wg.Add(1)
		go func() {
			e.run()
			e.finish(context.Cause(e.ctx))
		}()
	}

	return nil
}

func (e *Engine) Done() <-chan error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	return e.done
}

func (e *Engine) finish(err error) {
	e.doneOnce.Do(func() {
		if e.done != nil {
			e.done <- err
			close(e.done)
		}
	})
}

// Stop shuts down the engine. A wired archive is drained and committed
// before return, and any terminal durability failure is returned.
func (e *Engine) Stop() error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	e.stopped = true

	// Guard against Stop before Start: e.cancel is nil until Start runs, and a
	// defensive doShutdown / error-path stop must not nil-panic (same class as
	// the fuzz-found doShutdown nil-panic).
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
	var cause error
	if e.ctx != nil {
		cause = context.Cause(e.ctx)
	}
	var stopErr error
	if e.acceptDeferrerLifecycle != nil {
		stopErr = e.acceptDeferrerLifecycle.StopLedgerAccept()
	}
	e.finish(cause)
	e.eventBus.Stop()

	arc := e.loadArchive()
	if arc != nil {
		// Stop new stale deliveries, but retain the owned archive so a later
		// Stop can observe terminal completion after a bounded Close times out.
		e.mu.Lock()
		if e.validationTracker != nil {
			e.validationTracker.SetOnStale(nil)
		}
		e.mu.Unlock()
	}

	if e.validationTracker != nil {
		e.validationTracker.flush()
	}

	if arc != nil {
		// Bounded close — a stuck archive must not hang shutdown.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := arc.Close(ctx)
		cancel()
		if err != nil {
			stopErr = errors.Join(stopErr, fmt.Errorf("close validation archive: %w", err))
		}
	}
	return stopErr
}

func (e *Engine) StartRound(round consensus.RoundID, proposing bool) error {
	e.mu.Lock()
	if e.buildInProgress {
		e.mu.Unlock()
		return errLedgerAcceptInProgress
	}
	e.deferPostUnlock++
	e.acceptedLCL = consensus.LedgerID{}
	err := e.startRoundLocked(round, proposing, false)
	e.deferPostUnlock--
	pending := e.takePendingPostUnlockLocked()
	e.mu.Unlock()
	runPostUnlock(pending)
	return err
}

// RestartRound starts a fresh round from the adaptor's current LCL after
// re-evaluating the trusted-validation preference. It is used by externally
// driven consensus loops that stop their timer between bounded runs.
func (e *Engine) RestartRound(proposing bool) error {
	e.mu.Lock()
	if e.buildInProgress {
		e.mu.Unlock()
		return errLedgerAcceptInProgress
	}
	e.deferPostUnlock++

	working, err := e.adaptor.GetLastClosedLedger()
	if err == nil && working == nil {
		err = errNoLastClosedLedger
	}
	if err == nil {
		preferred := working
		if id, ok := e.validationPreferredForLedgerLocked(working); ok && id != working.ID() {
			if cached, getErr := e.adaptor.GetLedger(id); getErr == nil && cached != nil {
				preferred = cached
			}
		}
		e.acceptedLCL = consensus.LedgerID{}
		if preferred.ID() != working.ID() {
			e.acceptedLCL = working.ID()
		}
		e.prevLedger = preferred
		round := consensus.RoundID{Seq: preferred.Seq() + 1, ParentHash: preferred.ID()}
		err = e.startRoundLocked(round, proposing, false)
	}

	e.deferPostUnlock--
	pending := e.takePendingPostUnlockLocked()
	e.mu.Unlock()
	runPostUnlock(pending)
	return err
}

// startRoundLocked is the inner StartRound; caller must hold e.mu.
// recovering (entered after a preferred-ledger switch,
// rippled's "switchedLedger") makes the node observe for one round — no
// proposal or validation even as a full validator — because its view of
// the new round's tx-set isn't coherent yet and a stale emission would
// poison convergence.
func (e *Engine) startRoundLocked(round consensus.RoundID, proposing, recovering bool) error {
	// First round after boot has no prior round to measure; seed prevRoundTime
	// to the idle interval so round-1 convergePercent uses the 15s divisor, not
	// the 5s floor (else avalanche state escalates ~3x faster than rippled).
	if e.firstRound {
		e.prevRoundTime = e.timing.LedgerIdleInterval
		e.firstRound = false
	}

	// Before the mode switch so it runs in every mode (preStartRound parity).
	e.driveNegativeUNLNewValidatorsLocked()

	// A recovery restart means we just jumped to a different LCL — tell peers
	// via SWITCHED_LEDGER so their tallies drop our abandoned ledger.
	if recovering && e.prevLedger != nil {
		if err := e.adaptor.OnLedgerSwitched(e.prevLedger); err != nil {
			return fmt.Errorf("switch ledger: %w", err)
		}
	}

	// Carry our observed close time across normal round boundaries. A recovery
	// switch restarts the current round internally and must retain the existing
	// baseline rather than treating the abandoned round as completed.
	if !recovering {
		if e.state == nil {
			if e.prevLedger != nil {
				e.prevCloseTime = e.prevLedger.CloseTime()
			}
		} else {
			e.prevCloseTime = e.state.CloseTimes.Self
		}
	}

	// Kick off a trust-view refresh so the bow-out reacts to an expiring list
	// within a round or two rather than only on the aggregator's 30s tick
	// (rippled recomputes via updateTrusted at every ledger close).
	e.refreshUNLStateDeferredLocked()
	// RefreshUNLState may synchronously publish a trust-change callback in
	// tests and lightweight adaptors. Apply its queued removals before the
	// round resets or replays any buffered proposal state.
	e.purgePendingTrustLocked()

	// Voluntary bow-out: an expired validator list means our trust view is
	// stale, so this round neither proposes nor validates (rippled
	// preStartRound). Independent of sync state — a syncing validator must
	// not emit partials on stale trust either. Amendment-blocked nodes skip
	// the check (rippled gates it on validating_, already false for them).
	bowedOut := e.adaptor.IsValidator() && !e.adaptor.IsStandalone() &&
		!e.adaptor.IsAmendmentBlocked() && e.adaptor.IsUNLBlocked()
	if bowedOut {
		slog.Error("Voluntarily bowing out of consensus process because of an expired validator list.",
			"t", "consensus",
			"event", "unl-expired-bow-out",
			"seq", round.Seq,
		)
	}
	e.bowedOut.Store(bowedOut)

	// Determine mode. recovering forces switchedLedger for exactly one round
	// even when we'd otherwise propose; the next round gets normal treatment.
	// belowFloor holds a restarted validator in observing until the network
	// passes the pre-restart persisted tip, so it can't re-sign a sequence it
	// may already have validated (round.Seq is prevLedger.Seq()+1, making
	// this rippled preStartRound's prevLgr.seq() >= maxDisallowedLedger).
	// An amendment-blocked node observes only: it can no longer build correct
	// ledgers, so proposing or validating them would poison the network.
	belowFloor := round.Seq <= e.adaptor.GetMaxDisallowedLedgerSeq()
	e.validating.Store(e.adaptor.IsValidator() &&
		!belowFloor &&
		!e.adaptor.IsAmendmentBlocked() &&
		!bowedOut)
	preferredLCL := e.isCurrentPreferredLCLLocked(e.prevLedger)
	if e.adaptor.GetOperatingMode() == consensus.OpModeFull && !preferredLCL {
		e.adaptor.SetOperatingMode(consensus.OpModeConnected)
		slog.Info("Observing: trusted validations prefer another LCL",
			"t", "consensus",
			"event", "preferred-lcl-observe",
			"round_seq", round.Seq,
		)
	}
	fullValidator := e.adaptor.IsValidator() &&
		e.adaptor.GetOperatingMode() == consensus.OpModeFull &&
		!e.adaptor.IsAmendmentBlocked() && !bowedOut && preferredLCL
	switch {
	case belowFloor:
		if proposing && e.adaptor.IsValidator() {
			slog.Info("Observing: round at or below restart validation floor",
				"t", "consensus",
				"event", "restart-floor-observe",
				"round_seq", round.Seq,
				"floor", e.adaptor.GetMaxDisallowedLedgerSeq(),
			)
		}
		e.setMode(consensus.ModeObserving)
	case recovering:
		e.setMode(consensus.ModeSwitchedLedger)
	case proposing && fullValidator:
		e.setMode(consensus.ModeProposing)
	default:
		e.setMode(consensus.ModeObserving)
	}
	e.roundCloseResolution = e.nextCloseTimeResolution()

	// Init round state. StartTime uses e.now() (its consumers measure via
	// e.now().Sub()); PhaseStart uses adaptor.Now() (checkConvergence reads
	// it via adaptor.Now().Sub()) — each clock paired with its reader.
	e.state = &roundState{
		Round:          round,
		CloseTimes:     consensus.CloseTimes{Peers: make(map[time.Time]int)},
		StartTime:      e.now(),
		PhaseStart:     e.adaptor.Now(),
		HaveCorrectLCL: true,
	}

	// Reset tracking maps. Dead-node set is round-scoped, so a validator that
	// bowed out last round can rejoin.
	e.proposalTracker.resetRound()
	e.disputeTracker = newDisputeTracker()
	e.acquiredTxSets = make(map[consensus.TxSetID]consensus.TxSet)
	e.comparesTxSets = make(map[consensus.TxSetID]struct{})
	e.peerUnchangedCounter = 0
	e.establishCounter = 0
	e.ourTxSet = nil
	e.lastConvergePercent = 0
	e.currentRoundTime = 0
	e.roundExpiredReported = false
	e.closeTime.reset()
	// Duration metric — e.now(), NOT adaptor.Now(): its consumers measure via
	// e.now().Sub(), and mixing in adaptor.Now()'s closeOffset yields a
	// negative measured duration (the last_close artifact).
	e.roundStartTime = e.now()

	e.setPhase(consensus.PhaseOpen)

	e.eventBus.Publish(&consensus.RoundStartedEvent{
		Round:     round,
		Mode:      e.mode,
		Timestamp: e.adaptor.Now(),
	})

	// Replay buffered proposals for this round's prevLedger.
	if e.prevLedger != nil {
		replayTrusted := e.trustedPredicate()
		closeTimes, _, relay := e.proposalTracker.replay(e.prevLedger.ID(), replayTrusted)
		e.unvoteDeadProposalsLocked()
		// Trust can change while replay is running. Remove any positions
		// that are no longer trusted before deriving close-time votes,
		// dispute state, or the replay pressure count.
		e.pruneUntrustedProposalsLocked()
		replayed := e.proposalTracker.countTrusted(e.trustedPredicate())
		e.appendReplayCloseTimesLocked(closeTimes)

		// Re-share replayed positions so peers that missed a proposal on this
		// prevLedger get re-fed it from us during the recovery window.
		relayTrusted := e.trustedPredicate()
		for _, p := range relay {
			if !relayTrusted(p.NodeID) {
				continue
			}
			e.adaptor.RelayProposal(p, 0)
		}

		// Peer pressure: if a majority of prior proposers already closed,
		// consider closing now — still gated by shouldCloseLedger timing.
		if replayed > e.prevProposers/2 {
			if e.shouldCloseLedger() {
				e.closeLedger()
				// No checkConvergence here: accepting on only replayed close
				// times causes hash mismatches; the establish timer
				// evaluates after fresh proposals arrive.
			}
		}
	}

	return nil
}

// driveNegativeUNLNewValidatorsLocked diffs the trusted set against the
// previous round's snapshot and calls adaptor.OnUNLChange for the added
// validators when NegativeUNL is enabled on the parent ledger. The seq is
// prevLedger.Seq()+1 (matching the voting-path purge key in
// GenerateNegativeUNLPseudoTx). previousTrustedSet is seeded once so the
// first round doesn't misreport the startup UNL as `added`. Caller holds e.mu.
func (e *Engine) driveNegativeUNLNewValidatorsLocked() {
	if e.prevLedger == nil {
		return
	}
	if !e.adaptor.IsFeatureEnabledOnLedger(e.prevLedger, "NegativeUNL") {
		return
	}
	current := e.adaptor.GetTrustedValidators()

	// Seed once: treating the startup UNL as `added` would grant every mature
	// validator a fresh grace period after a restart.
	if !e.previousTrustedSeeded {
		e.previousTrustedSet = make(map[consensus.NodeID]struct{}, len(current))
		for _, n := range current {
			e.previousTrustedSet[n] = struct{}{}
		}
		e.previousTrustedSeeded = true
		return
	}

	var added []consensus.NodeID
	for _, n := range current {
		if _, seen := e.previousTrustedSet[n]; !seen {
			added = append(added, n)
		}
	}
	if len(added) > 0 {
		e.adaptor.OnUNLChange(e.prevLedger.Seq()+1, added)
	}
	next := make(map[consensus.NodeID]struct{}, len(current))
	for _, n := range current {
		next[n] = struct{}{}
	}
	e.previousTrustedSet = next
}

// Mode returns the current consensus mode via the lock-free atomic mirror
// (see modeAtomic).
func (e *Engine) Mode() consensus.Mode {
	return consensus.Mode(e.modeAtomic.Load())
}

func (e *Engine) Phase() consensus.Phase {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.phase
}

// CurrentRound returns the selected round without exposing mutable engine state.
func (e *Engine) CurrentRound() (consensus.RoundID, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.state == nil {
		return consensus.RoundID{}, false
	}
	return e.state.Round, true
}

// BuildingLedgerSeq returns the ledger sequence being built after the open
// phase, or zero when no ledger build is active.
func (e *Engine) BuildingLedgerSeq() uint32 {
	return e.buildingLedgerSeq.Load()
}

// IsProposing reports whether we're actively proposing (lock-free atomic
// read; called on the RPC hot path under ledger.service.s.mu — see modeAtomic).
func (e *Engine) IsProposing() bool {
	return consensus.Mode(e.modeAtomic.Load()) == consensus.ModeProposing
}

// IsValidating reports whether the node is eligible to issue validations in
// this round. Sync state only determines whether those validations are full or
// partial. Takes no engine lock, safe on the server_info hot path.
func (e *Engine) IsValidating() bool {
	return e.validating.Load()
}

// avMinConsensusTime floors the convergePercent divisor so a short prior
// round can't make the percentage run away.
const avMinConsensusTime = 5 * time.Second

// GetJSON returns the consensus-round state as a JSON map. Backs the
// consensus_info RPC (always full).
func (e *Engine) GetJSON(full bool) map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()

	mode := consensus.Mode(e.modeAtomic.Load())
	closeRes := int64(e.currentCloseTimeResolution() / time.Second)

	trusted := e.trustedPredicate()
	proposers := 0
	for nodeID := range e.proposalTracker.all() {
		if trusted(nodeID) {
			proposers++
		}
	}
	ret := map[string]any{
		"proposing": mode == consensus.ModeProposing,
		"proposers": proposers,
	}

	if mode != consensus.ModeWrongLedger {
		ret["synched"] = true
		if e.prevLedger != nil {
			ret["ledger_seq"] = e.prevLedger.Seq() + 1
		}
		ret["close_granularity"] = closeRes
	} else {
		ret["synched"] = false
	}

	ret["phase"] = e.phase.String()

	disputeCount := 0
	if e.disputeTracker != nil {
		disputeCount = e.disputeTracker.count()
	}
	if disputeCount > 0 && !full {
		ret["disputes"] = disputeCount
	}

	if e.state != nil {
		if e.state.OurPosition != nil {
			ret["our_position"] = proposalJSON(e.state.OurPosition)
		} else if e.ourTxSet != nil && e.prevLedger != nil {
			// Non-proposing nodes still have a position (tx set + close time)
			// without a broadcast Proposal; render it from tracked components.
			// Position 0 = observer that never advanced.
			ret["our_position"] = proposalJSON(&consensus.Proposal{
				PreviousLedger: e.prevLedger.ID(),
				TxSet:          e.ourTxSet.ID(),
				CloseTime:      e.state.CloseTimes.Self,
			})
		}
	}

	if full {
		// current_ms whenever a round result exists (e.ourTxSet), not only
		// during establish.
		if e.ourTxSet != nil {
			ret["current_ms"] = e.currentRoundTime.Milliseconds()
		}
		// converge_percent emitted unconditionally in full mode from the
		// retained value, so it stays meaningful between rounds.
		ret["converge_percent"] = e.lastConvergePercent
		ret["close_resolution"] = closeRes
		ret["have_time_consensus"] = e.closeTime.haveConsensus
		ret["previous_proposers"] = e.prevProposers
		ret["previous_mseconds"] = e.prevRoundTime.Milliseconds()

		if proposers > 0 {
			ppj := make(map[string]any, proposers)
			for nodeID, p := range e.proposalTracker.all() {
				if !trusted(nodeID) {
					continue
				}
				ppj[fmt.Sprintf("%X", nodeID[:])] = proposalJSON(p)
			}
			ret["peer_positions"] = ppj
		}

		if len(e.acquiredTxSets) > 0 {
			acq := make([]string, 0, len(e.acquiredTxSets))
			for id := range e.acquiredTxSets {
				acq = append(acq, fmt.Sprintf("%X", id[:]))
			}
			ret["acquired"] = acq
		}

		if disputeCount > 0 {
			dsj := make(map[string]any, disputeCount)
			for _, d := range e.disputeTracker.all() {
				dsj[fmt.Sprintf("%X", d.TxID[:])] = disputeJSON(d)
			}
			ret["disputes"] = dsj
		}

		if e.state != nil && len(e.state.CloseTimes.Peers) > 0 {
			ctj := make(map[string]any, len(e.state.CloseTimes.Peers))
			for t, c := range e.state.CloseTimes.Peers {
				ctj[fmt.Sprintf("%d", protocol.RippleSeconds(t))] = c
			}
			ret["close_times"] = ctj
		}

		if e.proposalTracker.deadNodeCount() > 0 {
			deadIDs := e.proposalTracker.deadNodeIDs()
			dnj := make([]string, 0, len(deadIDs))
			for _, nodeID := range deadIDs {
				dnj = append(dnj, fmt.Sprintf("%X", nodeID[:]))
			}
			ret["dead_nodes"] = dnj
		}
	}

	// validating mirrors rippled's dynamic validating_ flag
	// (RCLConsensus.cpp:937 → adaptor_.validating()).
	ret["validating"] = e.IsValidating()
	return ret
}

// proposalJSON renders a proposal as JSON. A bow-out (Position ==
// seqLeave) omits transaction_hash/propose_seq.
func proposalJSON(p *consensus.Proposal) map[string]any {
	j := map[string]any{
		"previous_ledger": fmt.Sprintf("%X", p.PreviousLedger[:]),
		// close_time is a string, not a bare integer.
		"close_time": fmt.Sprintf("%d", protocol.RippleSeconds(p.CloseTime)),
	}
	if p.Position != 0xFFFFFFFF { // not a bow-out (seqLeave)
		j["transaction_hash"] = fmt.Sprintf("%X", p.TxSet[:])
		j["propose_seq"] = p.Position
	}
	return j
}

func disputeJSON(d *consensus.DisputedTx) map[string]any {
	j := map[string]any{
		"yays":     d.Yays,
		"nays":     d.Nays,
		"our_vote": d.OurVote,
	}
	if len(d.Votes) > 0 {
		votes := make(map[string]any, len(d.Votes))
		for nodeID, vote := range d.Votes {
			votes[fmt.Sprintf("%X", nodeID[:])] = vote
		}
		j["votes"] = votes
	}
	return j
}

// lastCloseInfo packs GetLastCloseInfo's two values so atomic.Pointer
// publishes them together without tearing.
type lastCloseInfo struct {
	Proposers int
	RoundTime time.Duration
}

// GetLastCloseInfo returns the proposer count and convergence time for
// server_info.last_close: the last accepted round's snapshot, or — before
// any round is accepted — a freshness-bounded count of recent trusted
// proposers so a cold start doesn't report 0 while peers propose.
func (e *Engine) GetLastCloseInfo() (proposers int, convergeTime time.Duration) {
	if info := e.lastCloseAtomic.Load(); info != nil {
		proposers = info.Proposers
		convergeTime = info.RoundTime
	}
	if proposers > 0 {
		return proposers, convergeTime
	}
	return e.recentTrustedProposerCount(), convergeTime
}

// recentTrustedProposerCount counts trusted nodes with a buffered
// proposal inside the freshness window. Uses the cross-round buffer so
// the count survives wrongLedger round restarts. Takes e.mu.RLock().
func (e *Engine) recentTrustedProposerCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	fresh := e.proposalTracker.latestFresh(e.trustedPredicate(), e.adaptor.Now(), e.timing.ProposeFreshness)
	return len(fresh)
}

// storeLastCloseLocked publishes round-completion stats to the atomic
// mirror. Caller must hold e.mu.
func (e *Engine) storeLastCloseLocked() {
	e.lastCloseAtomic.Store(&lastCloseInfo{
		Proposers: e.prevProposers,
		RoundTime: e.prevRoundTime,
	})
}

func (e *Engine) Subscribe(sub consensus.EventSubscriber) {
	e.eventBus.Subscribe(sub)
}

// setMode changes the consensus mode. Caller must hold e.mu.
func (e *Engine) setMode(newMode consensus.Mode) {
	if e.mode == newMode {
		return
	}

	oldMode := e.mode
	e.mode = newMode
	// Mirror to the atomic for lock-free Mode/IsProposing reads. Paired with
	// the e.mu-held write above; an int32 store can't tear, so a reader sees
	// old or new — fine for the snapshot.
	e.modeAtomic.Store(int32(newMode))

	e.adaptor.OnModeChange(oldMode, newMode)

	e.eventBus.Publish(&consensus.ModeChangedEvent{
		OldMode:   oldMode,
		NewMode:   newMode,
		Timestamp: e.adaptor.Now(),
	})

	// Leaving proposing/observing resets censorship tracking: entries recorded
	// under the old mode no longer reflect a set we keep proposing, so warning
	// on them would be bogus (setMode already guarantees oldMode != newMode).
	if oldMode == consensus.ModeProposing || oldMode == consensus.ModeObserving {
		e.censorship.reset()
	}
}

func (e *Engine) setPhase(newPhase consensus.Phase) {
	if e.phase == newPhase {
		return
	}

	oldPhase := e.phase
	oldPhaseDuration := time.Duration(0)
	if e.state != nil && !e.state.PhaseStart.IsZero() {
		oldPhaseDuration = e.adaptor.Now().Sub(e.state.PhaseStart)
	}
	slog.Info("phase transition",
		"t", "consensus",
		"event", "phase-transition",
		"from", oldPhase.String(),
		"to", newPhase.String(),
		"from_duration_ms", oldPhaseDuration.Milliseconds(),
		"mode", e.mode.String(),
	)

	e.phase = newPhase
	if e.state != nil {
		e.state.PhaseStart = e.adaptor.Now()
	}

	e.eventBus.Publish(&consensus.PhaseChangedEvent{
		Round:     e.state.Round,
		OldPhase:  oldPhase,
		NewPhase:  newPhase,
		Timestamp: e.adaptor.Now(),
	})

	e.adaptor.OnPhaseChange(oldPhase, newPhase)
}

// shouldCloseLedger decides whether to close now, in gate order: no prev
// ledger → never; out-of-bounds close times → recover; peer pressure →
// stay in step; else the elapsed-time timers.
func (e *Engine) shouldCloseLedger() bool {
	if e.prevLedger == nil {
		return false
	}
	openTime := e.now().Sub(e.state.StartTime)
	timeSincePrevClose := e.adaptor.Now().Sub(e.lastCloseBaseline())

	if e.closeTimesOutOfBounds(timeSincePrevClose) {
		return true
	}

	proposersClosed, proposersValidated := e.closedProposerCounts()
	if e.underPeerPressureToClose(proposersClosed, proposersValidated) {
		slog.Info("shouldClose peer-pressure",
			"t", "consensus",
			"event", "should-close-pressure",
			"prev_proposers", e.prevProposers,
			"closed", proposersClosed,
			"validated", proposersValidated,
			"open_ms", openTime.Milliseconds(),
		)
		return true
	}
	e.traceCloseMiss(openTime, proposersClosed, proposersValidated)

	return e.closeOnTimers(openTime, timeSincePrevClose)
}

type parentCloseTimeReporter interface {
	ParentCloseTime() time.Time
}

// closeAgreementReporter is optionally implemented by a prevLedger that can
// report whether its close time was reached by consensus. Ledgers that don't
// (simulation/test ledgers) are treated as having agreed — the normal case.
type closeAgreementReporter interface {
	parentCloseTimeReporter
	CloseAgree() bool
}

type closeTimingReporter interface {
	CloseTimeResolution() time.Duration
	CloseAgree() bool
}

func (e *Engine) nextCloseTimeResolution() time.Duration {
	reporter, ok := e.prevLedger.(closeTimingReporter)
	if !ok {
		return e.adaptor.CloseTimeResolution()
	}
	parent := reporter.CloseTimeResolution()
	seconds := parent / time.Second
	if parent <= 0 || parent%time.Second != 0 || seconds > time.Duration(^uint32(0)) {
		return e.adaptor.CloseTimeResolution()
	}
	next := consensus.GetNextLedgerTimeResolution(
		uint32(seconds),
		reporter.CloseAgree(),
		e.prevLedger.Seq()+1,
	)
	if next == 0 {
		return e.adaptor.CloseTimeResolution()
	}
	return time.Duration(next) * time.Second
}

func (e *Engine) currentCloseTimeResolution() time.Duration {
	if e.roundCloseResolution > 0 {
		return e.roundCloseResolution
	}
	return e.adaptor.CloseTimeResolution()
}

func (e *Engine) previousCloseTimeResolution() time.Duration {
	if reporter, ok := e.prevLedger.(interface{ CloseTimeResolution() time.Duration }); ok {
		if resolution := reporter.CloseTimeResolution(); resolution > 0 {
			return resolution
		}
	}
	return e.adaptor.PrevCloseTimeResolution()
}

// lastCloseBaseline returns the reference close time the idle/close timers
// measure from. When the previous close was reached by consensus it's the
// previous ledger's stored close time; otherwise it's our own observed close
// carried across rounds (prevCloseTime).
func (e *Engine) lastCloseBaseline() time.Time {
	if e.previousCloseCorrect() {
		return e.prevLedger.CloseTime()
	}
	return e.prevCloseTime
}

// previousCloseCorrect reports whether the previous ledger's stored close
// time can be trusted: we're not on the wrong ledger, its close time was
// agreed, and it isn't the defaulted parentClose+1s.
func (e *Engine) previousCloseCorrect() bool {
	if e.mode == consensus.ModeWrongLedger {
		return false
	}
	rep, ok := e.prevLedger.(closeAgreementReporter)
	if !ok {
		return true
	}
	if !rep.CloseAgree() {
		return false
	}
	return !e.prevLedger.CloseTime().Equal(rep.ParentCloseTime().Add(time.Second))
}

// closeTimesOutOfBounds reports close times so unreasonable we should
// close to recover.
func (e *Engine) closeTimesOutOfBounds(timeSincePrevClose time.Duration) bool {
	return e.prevRoundTime < -1*time.Second || e.prevRoundTime > 10*time.Minute ||
		timeSincePrevClose > 10*time.Minute
}

// closedProposerCounts returns trusted peers that have closed (proposed
// this round) and trusted validators that have validated our prev ledger.
// proposersValidated reads the PERSISTENT tracker, not the round-scoped
// map (empty early in a round), so early validation pressure is visible.
func (e *Engine) closedProposerCounts() (proposersClosed, proposersValidated int) {
	e.purgePendingTrustLocked()
	proposersClosed = e.proposalTracker.countTrusted(e.trustedPredicate())
	if e.prevLedger != nil && e.validationTracker != nil {
		proposersValidated = e.validationTracker.proposersValidated(e.prevLedger.ID())
	}
	return proposersClosed, proposersValidated
}

// underPeerPressureToClose reports whether a majority of prior proposers
// have closed or validated — close now to stay in step.
func (e *Engine) underPeerPressureToClose(proposersClosed, proposersValidated int) bool {
	return proposersClosed+proposersValidated > e.prevProposers/2
}

// traceCloseMiss emits a rate-limited trace (first tick + ~1s) when peer
// pressure didn't close this tick.
func (e *Engine) traceCloseMiss(openTime time.Duration, proposersClosed, proposersValidated int) {
	if openTime < 100*time.Millisecond || (openTime > 1000*time.Millisecond && openTime < 1100*time.Millisecond) {
		slog.Info("shouldClose peer-pressure miss",
			"t", "consensus",
			"event", "should-close-miss",
			"prev_proposers", e.prevProposers,
			"closed", proposersClosed,
			"validated", proposersValidated,
			"open_ms", openTime.Milliseconds(),
		)
	}
}

// closeOnTimers decides to close on elapsed-time thresholds alone, after
// peer pressure is ruled out.
func (e *Engine) closeOnTimers(openTime, timeSincePrevClose time.Duration) bool {
	// No transactions: only close at the idle interval. rippled uses
	// max(ledgerIDLE_INTERVAL, 2*previousLedger_.closeTimeResolution())
	// (Consensus.h:1212-1214) — the LCL's raw stored resolution, not the
	// next-ledger rounding basis — so a coarse close-time resolution doesn't
	// let an empty ledger close before a full resolution step has elapsed.
	if len(e.adaptor.GetPendingTxs()) == 0 {
		idle := e.timing.LedgerIdleInterval
		if twoRes := 2 * e.previousCloseTimeResolution(); twoRes > idle {
			idle = twoRes
		}
		return timeSincePrevClose >= idle
	}

	// Preserve minimum ledger open time.
	if openTime < e.timing.LedgerMinClose {
		return false
	}

	// Don't close faster than half the previous round time, so slower
	// validators can keep up.
	if openTime < e.prevRoundTime/2 {
		return false
	}

	return true
}

// phaseOpen closes the ledger if shouldCloseLedger. Caller must hold e.mu.
func (e *Engine) phaseOpen() {
	if e.shouldCloseLedger() {
		e.eventBus.Publish(&consensus.TimerFiredEvent{
			Timer:     consensus.TimerLedgerClose,
			Round:     e.state.Round,
			Timestamp: e.adaptor.Now(),
		})
		e.closeLedger()
	}
}

// closeLedger transitions from open to establish phase.
func (e *Engine) closeLedger() {
	e.purgePendingTrustLocked()
	// #422: log when prior proposers + self can't meet quorum (likely stall);
	// skipped before the first completed round.
	if e.consensusCount > 0 {
		trusted, quorum := e.adaptor.GetTrustedValidatorsAndQuorum()
		if e.prevProposers+1 < quorum {
			seq := uint32(0)
			if e.prevLedger != nil {
				seq = e.prevLedger.Seq() + 1
			}
			slog.Info("consensus close — peer proposers below quorum (likely stall)",
				"t", "consensus",
				"event", "close-below-quorum",
				"peer_proposers", e.prevProposers,
				"quorum", quorum,
				"unl_size", len(trusted),
				"seq", seq,
			)
		}
	}

	e.buildingLedgerSeq.Store(e.state.Round.Seq)

	// Filter pending txs through the open-ledger gate when proposing;
	// non-proposing modes skip the per-round apply cost (position isn't broadcast).
	var txs [][]byte
	if e.mode == consensus.ModeProposing || e.adaptor.IsStandalone() {
		txs = e.adaptor.GetProposableTxs(e.prevLedger)
	} else {
		txs = e.adaptor.GetPendingTxs()
	}

	// Inject flag/voting-ledger pseudo-txs BEFORE building the set so the
	// tx-set hash matches rippled's. Gate = standalone || (proposing, which
	// already excludes wrongLedger); standalone keeps single-node tests
	// injecting before they propose.
	if e.prevLedger != nil && (e.mode == consensus.ModeProposing || e.adaptor.IsStandalone()) {
		prev := e.prevLedger
		switch {
		case protocol.IsFlagLedger(prev.Seq()):
			var parentSeq uint32
			if prev.Seq() > 0 {
				parentSeq = prev.Seq() - 1
			}
			parentVals := e.parentValidations(prev.ParentID(), parentSeq)
			if extra := e.adaptor.GenerateFlagLedgerPseudoTxs(prev, parentVals); len(extra) > 0 {
				txs = append(txs, extra...)
			}
		case protocol.IsVotingLedger(prev.Seq()) && e.adaptor.IsFeatureEnabledOnLedger(prev, "NegativeUNL"):
			if extra := e.adaptor.GenerateNegativeUNLPseudoTx(prev); len(extra) > 0 {
				txs = append(txs, extra...)
			}
		}
	}

	txSet, err := e.adaptor.BuildTxSet(txs)
	if err != nil {
		slog.Error("Failed to build tx set, falling back to empty set",
			"t", "Consensus",
			"round", e.state.Round,
			"pending_txs", len(txs),
			"err", err,
		)

		// Fall back to an empty tx set so consensus can still advance.
		txSet, err = e.adaptor.BuildTxSet(nil)
		if err != nil {
			slog.Error("Failed to build empty tx set, cannot close ledger",
				"t", "Consensus",
				"round", e.state.Round,
				"err", err,
			)
			e.setMode(consensus.ModeObserving)
			return
		}
	}
	e.ourTxSet = txSet
	// Our own tx set is immediately "acquired" so dispute wiring recognizes
	// proposals referencing our position.
	e.acquiredTxSets[txSet.ID()] = txSet

	// Record the set we're proposing this round for censorship detection,
	// unless we're acquiring the correct ledger.
	if e.prevLedger != nil && e.mode != consensus.ModeWrongLedger {
		e.censorship.propose(txSet.TxIDs(), e.prevLedger.Seq()+1)
	}

	// Raw now; rounding happens later via effCloseTime at acceptance.
	closeTime := e.adaptor.Now()
	e.state.CloseTimes.Self = closeTime

	// Reset the round-time clock at open→establish so phaseEstablish's
	// roundTime consumers measure only the establish phase. (e.now() per the
	// duration-metric rationale above.)
	e.roundStartTime = e.now()

	// If proposing, create and broadcast our proposal
	if e.mode == consensus.ModeProposing {
		nodeID, err := e.adaptor.GetValidatorKey()
		if err == nil {
			proposal := &consensus.Proposal{
				Round:          e.state.Round,
				NodeID:         nodeID,
				Position:       0,
				TxSet:          txSet.ID(),
				CloseTime:      closeTime,
				PreviousLedger: e.prevLedger.ID(),
				Timestamp:      e.adaptor.Now(),
			}

			if err := e.adaptor.SignProposal(proposal); err == nil {
				e.state.OurPosition = proposal
				e.enqueueProposalBroadcastLocked(proposal)
				txSetID := txSet.ID()
				prevID := e.prevLedger.ID()
				slog.Info("our initial position",
					"t", "consensus-build",
					"event", "our-position",
					"round_seq", e.state.Round.Seq,
					"prev", fmt.Sprintf("%x", prevID[:8]),
					"tx_set", fmt.Sprintf("%x", txSetID[:8]),
					"tx_count", len(txs),
					"close_time", closeTime.UTC().Format(time.RFC3339Nano),
					"mode", e.mode.String(),
				)
			}
		}
	}

	// Seed disputes against every peer position whose tx set we hold, and
	// acquire the rest — needed because OnProposal isn't re-fired for replayed
	// proposals.
	e.pruneUntrustedProposalsLocked()
	requested := make(map[consensus.TxSetID]struct{})
	trusted := e.trustedPredicate()
	for nodeID, p := range e.proposalTracker.all() {
		if !trusted(nodeID) {
			continue
		}
		if peerSet, ok := e.acquiredTxSets[p.TxSet]; ok {
			e.createDisputesAgainst(peerSet)
			continue
		}
		if e.ourTxSet != nil && p.TxSet == e.ourTxSet.ID() {
			continue
		}
		// Try adaptor cache; otherwise dedupe-by-id and request.
		if peerSet, err := e.adaptor.GetTxSet(p.TxSet); err == nil && peerSet != nil {
			e.acquiredTxSets[p.TxSet] = peerSet
			e.createDisputesAgainst(peerSet)
			continue
		}
		if _, already := requested[p.TxSet]; already {
			continue
		}
		requested[p.TxSet] = struct{}{}
		e.adaptor.RequestTxSet(p.TxSet)
	}

	e.setPhase(consensus.PhaseEstablish)
}
