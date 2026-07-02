// Package rcl implements the Ripple Consensus Ledger algorithm.
// This is the default consensus algorithm used by the XRP Ledger.
package rcl

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
)

// Engine implements the RCL consensus algorithm.
type Engine struct {
	mu sync.RWMutex

	// Configuration
	timing     consensus.Timing
	thresholds consensus.Thresholds

	// Dependencies
	adaptor  consensus.Adaptor
	eventBus *consensus.EventBus

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
	state           *consensus.RoundState
	prevLedger      consensus.Ledger

	// buildInProgress is set while acceptLedger applies the LCL off e.mu
	// (rippled's jtACCEPT job window). While set, round-driving (timerEntry,
	// OnLedger) parks so no second goroutine starts a round before the commit
	// tail runs. Mutated under e.mu.
	buildInProgress bool

	ourTxSet  consensus.TxSet
	converged bool

	// proposalTracker owns the round-scoped peer-signal maps. Accessed only
	// under e.mu (see ProposalTracker).
	proposalTracker *ProposalTracker

	// validationTracker accumulates trusted validations across ledgers and
	// fires the fully-validated callback at quorum, driving
	// server_info.validated_ledger forward.
	validationTracker *ValidationTracker

	// disputeTracker owns the per-tx DisputedTx entries and per-peer vote
	// map. Written by createDisputesAgainst / OnProposal / OnTxSet /
	// UpdateOurPositions, read during checkConvergence.
	disputeTracker *DisputeTracker

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

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

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

	// wrongLedgerAcquireFailures counts clean acquisition failures of
	// wrongLedgerID; at wrongLedgerAcquireMaxFailures the engine drops to a
	// degraded resync rather than freezing.
	wrongLedgerAcquireFailures int

	// wrongLedgerSince is when we last entered ModeWrongLedger (zero when not
	// pinned); it measures continuous time stuck regardless of target-hash
	// churn, arming the wrongLedgerStuckTimeout watchdog.
	wrongLedgerSince time.Time

	// degradedResyncUntil, when in the future, suppresses re-pinning
	// ModeWrongLedger so the node keeps closing ledgers (observer-mode
	// advancement) while it retries acquisition. Engine-global: every
	// wrongLedger pin is skipped while the window is open.
	degradedResyncUntil time.Time

	// lastRefusedSwitch de-duplicates the switch-refused log while checkLedger
	// keeps re-deriving the same incompatible target.
	lastRefusedSwitch consensus.LedgerID

	// roundExpiredReported de-duplicates the round-expired warn/event while an
	// expired round waits at the close-time gate; reset each startRoundLocked.
	roundExpiredReported bool

	// lastSignTime is the monotonic floor for emitted validation SignTime: a
	// regressing adaptor clock (NTP step, VM pause) is bumped to
	// lastSignTime+1s so peers never see non-monotonic validations.
	// Protected by e.mu.
	lastSignTime time.Time

	// Highest seq this node has validated (SeqEnforcer floor); prevents
	// conflicting same-seq validations (#401). Protected by e.mu.
	ourLastValidatedSeq uint32

	// When the floor was last bumped; after validationSetExpires of silence
	// it resets to 0 so a restarted validator can resume below its old floor.
	ourLastValidatedTime time.Time

	// Stats
	roundCount     uint64
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

	// pendingBroadcasts queues broadcasts produced under e.mu so they flush
	// after Unlock: holding e.mu across BroadcastProposal/Validation blocks
	// ingress on e.mu.RLock and can stall consensus on a slow peer send
	// queue. Mutated only under e.mu; drained by takePendingBroadcastsLocked.
	pendingBroadcasts []func()

	// missedHeartbeats counts dropped heartbeat ticks (gap > 2× interval).
	// time.Ticker silently coalesces ticks under load; this surfaces that
	// pressure so stalls don't hide.
	missedHeartbeats atomic.Uint64

	// stallPing, when set, is called once per run-loop iteration so the
	// stall watchdog sees the loop is alive. Atomic for lock-free read; nil
	// disables it.
	stallPing atomic.Pointer[func()]

	// deferBroadcasts > 0 inside timerEntry / StartRound enables deferred
	// broadcast batching; at zero the enqueue helpers send synchronously so
	// direct callers (tests) observe broadcasts immediately. Mutated under e.mu.
	deferBroadcasts int

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
}

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
// is released (see pendingBroadcasts). Caller must hold e.mu. With no
// deferred scope active the send is synchronous.
func (e *Engine) enqueueProposalBroadcastLocked(p *consensus.Proposal) {
	if p == nil {
		return
	}
	if e.deferBroadcasts == 0 {
		_ = e.adaptor.BroadcastProposal(p)
		return
	}
	e.pendingBroadcasts = append(e.pendingBroadcasts, func() {
		_ = e.adaptor.BroadcastProposal(p)
	})
}

// enqueueValidationBroadcastLocked stages a validation to be broadcast
// after e.mu is released. Caller must hold e.mu.
func (e *Engine) enqueueValidationBroadcastLocked(v *consensus.Validation) {
	if v == nil {
		return
	}
	if e.deferBroadcasts == 0 {
		_ = e.adaptor.BroadcastValidation(v)
		return
	}
	e.pendingBroadcasts = append(e.pendingBroadcasts, func() {
		_ = e.adaptor.BroadcastValidation(v)
	})
}

// takePendingBroadcastsLocked drains the queued broadcast closures.
// Caller must hold e.mu; pass the result to flushBroadcasts after Unlock.
func (e *Engine) takePendingBroadcastsLocked() []func() {
	if len(e.pendingBroadcasts) == 0 {
		return nil
	}
	out := e.pendingBroadcasts
	e.pendingBroadcasts = nil
	return out
}

// flushBroadcasts runs each queued broadcast. MUST be called with e.mu
// released.
func flushBroadcasts(pending []func()) {
	for _, fn := range pending {
		fn()
	}
}

// SeqEnforcer reset window (validationSET_EXPIRES).
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
		timing:          config.Timing,
		thresholds:      config.Thresholds,
		adaptor:         adaptor,
		eventBus:        consensus.NewEventBus(100),
		mode:            consensus.ModeObserving,
		phase:           consensus.PhaseAccepted,
		proposalTracker: NewProposalTracker(),
		closeTime:       newCloseTimeTracker(),
		disputeTracker:  NewDisputeTracker(),
		acquiredTxSets:  make(map[consensus.TxSetID]consensus.TxSet),
		comparesTxSets:  make(map[consensus.TxSetID]struct{}),
		parms:           consensus.DefaultConsensusParms(),
		now:             config.Clock,
		manualTick:      config.ManualTick,
		firstRound:      true,
	}
	if e.now == nil {
		e.now = time.Now
	}
	e.modeAtomic.Store(int32(e.mode))
	return e
}

// TimerEntry runs one heartbeat dispatch synchronously. For ManualTick
// mode: an external driver (csf) advances the state machine.
func (e *Engine) TimerEntry() {
	e.timerEntry()
}

// SetArchive wires (or, with nil, detaches) the validation archive.
// Detach clears the onStale callback so the archive can be Close()d
// without a use-after-close send. Safe before/after Start and with Stop,
// not with Start.
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
		e.validationTracker.SetLedgerAncestryProvider(p)
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
	e.mu.Lock()
	defer e.mu.Unlock()

	e.ctx, e.cancel = context.WithCancel(ctx)
	e.eventBus.Start()

	ledger, err := e.adaptor.GetLastClosedLedger()
	if err != nil {
		return fmt.Errorf("failed to get last closed ledger: %w", err)
	}
	e.prevLedger = ledger

	// Wire the validation tracker: trusted set + quorum from the adaptor;
	// its callback flips the ledger service's validated_ledger pointer.
	e.validationTracker = NewValidationTracker(e.adaptor.GetQuorum(), 5*time.Minute)
	e.validationTracker.SetTrusted(e.adaptor.GetTrustedValidators())
	if wired, ok := e.adaptor.(consensus.WireableAdaptor); ok {
		wired.SetValidationHistorian(e.validationTracker)
	}
	if e.ledgerAncestry != nil {
		e.validationTracker.SetLedgerAncestryProvider(e.ledgerAncestry)
	}
	// Network-adjusted clock for freshness checks — avoids rejecting our own
	// just-signed validation by the close-time offset on a skewed node.
	e.validationTracker.SetNow(e.adaptor.Now)
	if arc := e.loadArchive(); arc != nil {
		e.validationTracker.SetOnStale(arc.OnStale)
	}
	tracker := e.validationTracker
	e.validationTracker.SetFullyValidatedCallback(func(ledgerID consensus.LedgerID, seq uint32) {
		// Callback contract: production callers (OnValidation,
		// sendValidation) hold e.mu; tests call Add without it. So it MUST
		// NOT take e.mu (non-recursive RWMutex → self-deadlock).
		// e.archive / e.inMemoryLedgers are read via atomics to stay
		// race-free against SetArchive.
		e.adaptor.OnLedgerFullyValidated(ledgerID, seq)

		arc := e.loadArchive()
		inMem := e.inMemoryLedgers.Load()

		if arc != nil {
			arc.NoteFullyValidated(seq)
		}
		// Drive in-memory retention: ExpireOld fires onStale per evicted
		// validation (archive captures it first) and takes vt.mu, not e.mu,
		// so it's safe under the held e.mu. Runs with or without an archive;
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
		go e.run()
	}

	return nil
}

// Stop shuts down the engine. A wired archive is drained and committed
// before return so no stale validations are lost (modulo SaveBatch
// failures, which the writer re-queues).
func (e *Engine) Stop() error {
	e.cancel()
	e.wg.Wait()
	e.eventBus.Stop()

	// Flush has no archive interaction, so its ordering relative to the
	// archive close below is irrelevant.
	if e.validationTracker != nil {
		e.validationTracker.Flush()
	}

	if arc := e.loadArchive(); arc != nil {
		// Bounded close — a stuck archive must not hang shutdown.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = arc.Close(ctx)
		cancel()
	}
	return nil
}

func (e *Engine) StartRound(round consensus.RoundID, proposing bool) error {
	e.mu.Lock()
	e.deferBroadcasts++
	err := e.startRoundLocked(round, proposing, false)
	e.deferBroadcasts--
	pending := e.takePendingBroadcastsLocked()
	e.mu.Unlock()
	flushBroadcasts(pending)
	return err
}

// startRoundLocked is the inner StartRound; caller must hold e.mu.
// recovering (entered after handleWrongLedger / OnLedger adoption,
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

	// Carry our own observed close time across rounds. The first round seeds
	// from the seed ledger; afterwards we take the self close time of the round
	// that just ended, read from e.state before it is replaced below (this runs
	// for every round-start path).
	if e.state == nil {
		if e.prevLedger != nil {
			e.prevCloseTime = e.prevLedger.CloseTime()
		}
	} else {
		e.prevCloseTime = e.state.CloseTimes.Self
	}

	// Determine mode. recovering forces switchedLedger for exactly one round
	// even when we'd otherwise propose; the next round gets normal treatment.
	switch {
	case recovering && e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull:
		e.setMode(consensus.ModeSwitchedLedger)
	case proposing && e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull:
		e.setMode(consensus.ModeProposing)
	default:
		e.setMode(consensus.ModeObserving)
	}

	// Init round state. StartTime uses e.now() (its consumers measure via
	// e.now().Sub()); PhaseStart uses adaptor.Now() (checkConvergence reads
	// it via adaptor.Now().Sub()) — each clock paired with its reader.
	e.state = &consensus.RoundState{
		Round:          round,
		Mode:           e.mode,
		Phase:          consensus.PhaseOpen,
		Proposals:      make(map[consensus.NodeID]*consensus.Proposal),
		Disputed:       make(map[consensus.TxID]*consensus.DisputedTx),
		CloseTimes:     consensus.CloseTimes{Peers: make(map[time.Time]int)},
		StartTime:      e.now(),
		PhaseStart:     e.adaptor.Now(),
		HaveCorrectLCL: true,
	}

	// Reset tracking maps. Dead-node set is round-scoped, so a validator that
	// bowed out last round can rejoin.
	e.proposalTracker.ResetRound()
	e.disputeTracker = NewDisputeTracker()
	e.acquiredTxSets = make(map[consensus.TxSetID]consensus.TxSet)
	e.comparesTxSets = make(map[consensus.TxSetID]struct{})
	e.peerUnchangedCounter = 0
	e.establishCounter = 0
	e.converged = false
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
		closeTimes, replayed, relay := e.proposalTracker.Replay(e.prevLedger.ID(), e.adaptor.IsTrusted)
		for _, ct := range closeTimes {
			e.state.CloseTimes.Peers[ct]++
		}

		// Re-share replayed positions so peers that missed a proposal on this
		// prevLedger get re-fed it from us during the recovery window.
		for _, p := range relay {
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

	e.roundCount++
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

// OnProposal handles an incoming proposal. originPeer (0 = self) is
// excluded from the RelayProposal gossip forward.
func (e *Engine) OnProposal(proposal *consensus.Proposal, originPeer uint64) error {
	// Verify before taking e.mu: verification is pure, and doing it under the
	// write lock would serialize gossip-rate verifies behind round driving.
	if err := e.adaptor.VerifyProposal(proposal); err != nil {
		return fmt.Errorf("invalid proposal signature: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Drop untrusted proposals: buffering them would let throwaway keypairs
	// grow the tracker unboundedly and feed phantom proposers into
	// convergence counts.
	if !e.adaptor.IsTrusted(proposal.NodeID) {
		return nil
	}

	// Buffer for future playback, even between rounds.
	e.proposalTracker.BufferRecent(proposal)

	// Between rounds (accepted phase) only buffer, don't process.
	if e.phase == consensus.PhaseAccepted {
		return nil
	}

	// Reject proposals on a different previous ledger.
	if e.prevLedger != nil && proposal.PreviousLedger != e.prevLedger.ID() {
		return nil
	}

	// Ignore already-dead nodes. Must precede the bow-out arm: otherwise a
	// dead node could re-insert itself by re-sending seqLeave.
	if e.proposalTracker.IsDead(proposal.NodeID) {
		return nil
	}

	// Bow-out: a validator's final position sets ProposeSeq to seqLeave.
	// Erase its position, mark it dead, and un-vote it from every dispute —
	// otherwise the seqLeave position keeps voting forever.
	const seqLeave = uint32(0xFFFFFFFF)
	if proposal.Position == seqLeave {
		e.proposalTracker.MarkDead(proposal.NodeID)
		// Drop its dispute votes so they stop counting toward convergence.
		if e.disputeTracker != nil {
			e.disputeTracker.UnVote(proposal.NodeID)
		}
		return nil
	}

	// Drop non-increasing positions before counting close-time votes,
	// relaying, or updating disputes — otherwise a re-sent or equivocating
	// proposal at an already-seen ProposeSeq votes again.
	if !e.proposalTracker.Store(proposal) {
		return nil
	}

	// Record close time only from initial (Position == 0) proposals.
	if proposal.Position == 0 {
		e.state.CloseTimes.Peers[proposal.CloseTime]++
	}

	e.eventBus.Publish(&consensus.ProposalReceivedEvent{
		Proposal:  proposal,
		Trusted:   true,
		Timestamp: e.adaptor.Now(),
	})

	e.adaptor.RelayProposal(proposal, originPeer)

	{
		var ourTxSet consensus.TxSetID
		ourTxLen := -1
		if e.ourTxSet != nil {
			ourTxSet = e.ourTxSet.ID()
			ourTxLen = e.ourTxSet.Size()
		}
		_, peerCacheHit := e.acquiredTxSets[proposal.TxSet]
		if !peerCacheHit {
			if cached, _ := e.adaptor.GetTxSet(proposal.TxSet); cached != nil {
				peerCacheHit = true
			}
		}
		slog.Info("proposal received",
			"t", "consensus",
			"event", "propose-recv",
			"seq", proposal.Round.Seq,
			"peer", originPeer,
			"node", fmt.Sprintf("%x", proposal.NodeID[:6]),
			"pos_seq", proposal.Position,
			"peer_txset", fmt.Sprintf("%x", proposal.TxSet[:8]),
			"our_txset", fmt.Sprintf("%x", ourTxSet[:8]),
			"our_tx_count", ourTxLen,
			"peer_txset_cache_hit", peerCacheHit,
			"diff", proposal.TxSet != ourTxSet,
		)
	}

	// If the adaptor already has the tx set, cache it for dispute wiring;
	// else request it.
	if peerSet, err := e.adaptor.GetTxSet(proposal.TxSet); err == nil && peerSet != nil {
		if _, already := e.acquiredTxSets[proposal.TxSet]; !already {
			e.acquiredTxSets[proposal.TxSet] = peerSet
		}
	} else {
		e.adaptor.RequestTxSet(proposal.TxSet)
	}

	// If we hold the peer's tx set, run create/update-disputes for this
	// position (self-originated sets were already seeded in closeLedger).
	if e.ourTxSet != nil && proposal.TxSet != e.ourTxSet.ID() {
		if peerSet, ok := e.acquiredTxSets[proposal.TxSet]; ok {
			e.createDisputesAgainst(peerSet)
			if e.disputeTracker.UpdateDisputes(proposal.NodeID, peerSet) {
				e.peerUnchangedCounter = 0
			}
		}
	}

	if e.phase == consensus.PhaseEstablish {
		e.checkConvergence()
	}

	return nil
}

// OnValidation handles an incoming validation. originPeer (0 = self) is
// excluded from the RelayValidation gossip forward.
func (e *Engine) OnValidation(validation *consensus.Validation, originPeer uint64) error {
	// Verify before taking e.mu — see OnProposal.
	if err := e.adaptor.VerifyValidation(validation); err != nil {
		return fmt.Errorf("invalid validation signature: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	trusted := e.adaptor.IsTrusted(validation.NodeID)

	// Same-seq Byzantine detection: a trusted validator must not sign two
	// ledgers (or re-sign differently) for one seq. On conflict, keep it out
	// of quorum/trie but STILL relay it (peers should observe it too) and
	// charge no one. The returned error only tells the router to skip the
	// catch-up acquire — not to penalise the relaying peer.
	if trusted && e.validationTracker != nil {
		if reason, conflict := validationConflict(
			e.validationTracker.GetLatestValidation(validation.NodeID),
			validation,
		); conflict {
			e.adaptor.RelayValidation(validation, originPeer)
			return &consensus.ByzantineValidationError{NodeID: validation.NodeID, Reason: reason}
		}
	}

	// Store trusted-only: an untrusted key could grow the map unboundedly.
	if trusted {
		e.proposalTracker.SetValidation(validation)
	}

	// Feed the tracker — the gate that advances validated_ledger once a
	// quorum of trusted FULL validations accumulates (partials steer the trie
	// but don't count). Trust-gate to avoid a byNode entry per untrusted key.
	if trusted && e.validationTracker != nil {
		e.validationTracker.Add(validation)
	}

	e.eventBus.Publish(&consensus.ValidationReceivedEvent{
		Validation: validation,
		Trusted:    trusted,
		Timestamp:  e.adaptor.Now(),
	})

	// Relay trusted validations (excluding origin); drop untrusted for the
	// same spam reason as OnProposal.
	if trusted {
		e.adaptor.RelayValidation(validation, originPeer)
	}

	return nil
}

// validationConflict classifies a new validation against the latest
// tracked one for the same node. conflict=true only when they share a
// seq but disagree: different ledger (or same ledger, different sign
// time) → "conflicting"; same ledger+time, different cookie → "multiple".
// nil prev, a different seq, or an identical resend is no conflict. Only
// the latest seq per node is checked, so a conflict at an already-passed
// seq is missed — harmless, it can't affect quorum.
func validationConflict(prev, v *consensus.Validation) (string, bool) {
	if prev == nil || prev.LedgerSeq != v.LedgerSeq {
		return "", false
	}
	if prev.LedgerID != v.LedgerID {
		return "conflicting", true
	}
	if !prev.SignTime.Equal(v.SignTime) {
		return "conflicting", true
	}
	if prev.Cookie != v.Cookie {
		return "multiple", true
	}
	return "", false
}

// OnTxSet handles receiving a transaction set we requested.
func (e *Engine) OnTxSet(id consensus.TxSetID, txs [][]byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	txSet, err := e.adaptor.BuildTxSet(txs)
	if err != nil {
		return fmt.Errorf("failed to build tx set: %w", err)
	}

	if txSet.ID() != id {
		return fmt.Errorf("tx set ID mismatch: expected %x, got %x", id, txSet.ID())
	}

	// Cache for dispute wiring. A late tx set retroactively populates any
	// dispute whose tx it contains for some peer.
	if _, already := e.acquiredTxSets[id]; !already {
		e.acquiredTxSets[id] = txSet
		if e.ourTxSet != nil && id != e.ourTxSet.ID() {
			e.createDisputesAgainst(txSet)
			for nodeID, p := range e.proposalTracker.All() {
				if p.TxSet == id {
					if e.disputeTracker.UpdateDisputes(nodeID, txSet) {
						e.peerUnchangedCounter = 0
					}
				}
			}
		}
	}

	if e.phase == consensus.PhaseEstablish {
		e.checkConvergence()
	}

	return nil
}

// createDisputesAgainst creates a DisputedTx for every tx in only one
// side of the symmetric difference between a peer's set and ours,
// back-filling per-peer votes for each. Caller must hold e.mu.
func (e *Engine) createDisputesAgainst(peerTxSet consensus.TxSet) {
	if e.ourTxSet == nil || peerTxSet == nil {
		return
	}
	id := peerTxSet.ID()
	if _, seen := e.comparesTxSets[id]; seen {
		return
	}
	e.comparesTxSets[id] = struct{}{}

	if id == e.ourTxSet.ID() {
		return
	}

	ourIDs := e.ourTxSet.TxIDs()
	peerIDs := peerTxSet.TxIDs()

	ours := make(map[consensus.TxID]struct{}, len(ourIDs))
	for _, txID := range ourIDs {
		ours[txID] = struct{}{}
	}
	peers := make(map[consensus.TxID]struct{}, len(peerIDs))
	for _, txID := range peerIDs {
		peers[txID] = struct{}{}
	}

	// txs only in our set: seed ourVote=true and peer-vote=false.
	ourBlobs := e.ourTxSet.Txs()
	for idx, txID := range ourIDs {
		if _, also := peers[txID]; also {
			continue
		}
		if e.disputeTracker.Has(txID) {
			continue
		}
		var blob []byte
		if idx < len(ourBlobs) {
			blob = ourBlobs[idx]
		}
		dispute := e.disputeTracker.CreateDispute(txID, blob, true)
		e.seedDisputeVotes(dispute.TxID)
	}

	// txs only in peer's set: seed ourVote=false.
	peerBlobs := peerTxSet.Txs()
	for idx, txID := range peerIDs {
		if _, also := ours[txID]; also {
			continue
		}
		if e.disputeTracker.Has(txID) {
			continue
		}
		var blob []byte
		if idx < len(peerBlobs) {
			blob = peerBlobs[idx]
		}
		dispute := e.disputeTracker.CreateDispute(txID, blob, false)
		e.seedDisputeVotes(dispute.TxID)
	}
}

// seedDisputeVotes records each known peer's vote on a new dispute from
// its acquired tx set. Caller must hold e.mu.
func (e *Engine) seedDisputeVotes(txID consensus.TxID) {
	for nodeID, p := range e.proposalTracker.All() {
		peerSet, ok := e.acquiredTxSets[p.TxSet]
		if !ok {
			continue
		}
		if e.disputeTracker.SetVote(txID, nodeID, peerSet.Contains(txID)) {
			e.peerUnchangedCounter = 0
		}
	}
}

// OnLedger handles receiving a ledger we were missing.
func (e *Engine) OnLedger(id consensus.LedgerID, ledger []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Don't move prevLedger / start a round while an off-lock LCL build is in
	// flight; the commit tail owns round state until it completes.
	if e.buildInProgress {
		return nil
	}

	// If we were on wrong ledger, check if this helps
	if e.mode == consensus.ModeWrongLedger {
		l, err := e.adaptor.GetLedger(id)
		if err == nil && l != nil {
			// Never regress on out-of-order acquisition arrivals — EXCEPT for
			// the hash checkLedger explicitly pinned: the network-preferred
			// ledger may sit at a lower seq on a different chain than a
			// self-built run-ahead (Validations.h:892-895), and rippled's
			// switchLastClosedLedger takes that rewind.
			if e.prevLedger != nil && l.Seq() <= e.prevLedger.Seq() && id != e.wrongLedgerID {
				return nil
			}
			// Advance prevLedger to the FURTHEST locally-available closed
			// ledger that chains forward from l by parent hash, not one per
			// acquisition: under load a one-at-a-time catch-up stays
			// perpetually behind (#724). Only the build-on LCL moves (the
			// validated tip still moves solely via quorum); the ParentID chain
			// check prevents adopting a sibling fork, and an overshoot
			// self-corrects next round via checkLedger.
			for {
				next, nerr := e.adaptor.GetLedgerBySeq(l.Seq() + 1)
				if nerr != nil || next == nil || next.ParentID() != l.ID() {
					break
				}
				l = next
			}
			if !e.canSwitchToLedgerLocked(l) {
				return nil
			}
			lID := l.ID()
			slog.Info("Acquired missing ledger, restarting round",
				"seq", l.Seq(), "hash", fmt.Sprintf("%x", lID[:8]))
			e.prevLedger = l
			e.wrongLedgerID = consensus.LedgerID{}
			e.wrongLedgerAcquireFailures = 0
			if e.state != nil {
				e.state.HaveCorrectLCL = true
			}
			nextRound := consensus.RoundID{
				Seq:        l.Seq() + 1,
				ParentHash: l.ID(),
			}
			// recovering=true drops a would-be proposer to switchedLedger for
			// one round, suppressing emission; the next round promotes back
			// normally.
			proposing := e.adaptor.IsValidator() &&
				e.adaptor.GetOperatingMode() == consensus.OpModeFull
			e.startRoundLocked(nextRound, proposing, true)
		}
	}

	return nil
}

// parentValidations returns the trusted validations recorded for id, fed
// to GenerateFlagLedgerPseudoTxs for fee/amendment vote tallying. Callers
// pass prevLedger.ParentID(). Nil when the tracker isn't wired.
func (e *Engine) parentValidations(id consensus.LedgerID) []*consensus.Validation {
	if e.validationTracker == nil {
		return nil
	}
	return e.validationTracker.GetTrustedValidations(id)
}

func (e *Engine) State() *consensus.RoundState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
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

// IsProposing reports whether we're actively proposing (lock-free atomic
// read; called on the RPC hot path under ledger.service.s.mu — see modeAtomic).
func (e *Engine) IsProposing() bool {
	return consensus.Mode(e.modeAtomic.Load()) == consensus.ModeProposing
}

// IsValidating reports whether the node is issuing validations this round:
// a configured validator AND synced to OpModeFull. Lock-free reads, safe on
// the server_info hot path under ledger.service.s.mu.
func (e *Engine) IsValidating() bool {
	return e.adaptor.IsValidator() &&
		e.adaptor.GetOperatingMode() == consensus.OpModeFull
}
func (e *Engine) Timing() consensus.Timing {
	return e.timing
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
	closeRes := int64(e.adaptor.CloseTimeResolution() / time.Second)

	ret := map[string]any{
		"proposing": mode == consensus.ModeProposing,
		"proposers": e.proposalTracker.Count(),
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
		disputeCount = e.disputeTracker.Count()
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

		if e.proposalTracker.Count() > 0 {
			ppj := make(map[string]any, e.proposalTracker.Count())
			for nodeID, p := range e.proposalTracker.All() {
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
			for _, d := range e.disputeTracker.GetAll() {
				dsj[fmt.Sprintf("%X", d.TxID[:])] = disputeJSON(d)
			}
			ret["disputes"] = dsj
		}

		if e.state != nil && len(e.state.CloseTimes.Peers) > 0 {
			ctj := make(map[string]any, len(e.state.CloseTimes.Peers))
			for t, c := range e.state.CloseTimes.Peers {
				ctj[fmt.Sprintf("%d", t.Unix()-protocol.RippleEpochUnix)] = c
			}
			ret["close_times"] = ctj
		}

		if e.proposalTracker.DeadNodeCount() > 0 {
			deadIDs := e.proposalTracker.DeadNodeIDs()
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
		"close_time": fmt.Sprintf("%d", p.CloseTime.Unix()-protocol.RippleEpochUnix),
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
	fresh := e.proposalTracker.LatestFresh(e.adaptor.IsTrusted, e.adaptor.Now(), e.timing.ProposeFreshness)
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

func (e *Engine) Events() <-chan consensus.Event {
	return e.eventBus.Events()
}

// run is the main consensus loop on a single global heartbeat. It also
// detects ticks time.Ticker silently coalesced (gap > 2× interval) and
// logs them — observational only; the next tick still runs.
func (e *Engine) run() {
	defer e.wg.Done()

	// Heartbeat cadence = ledgerGRANULARITY (1s), floored by LedgerMinClose
	// so sub-granularity test configs keep up.
	interval := e.timing.LedgerGranularity
	if interval <= 0 {
		interval = time.Second
	}
	if e.timing.LedgerMinClose > 0 && e.timing.LedgerMinClose < interval {
		interval = e.timing.LedgerMinClose
	}
	e.heartbeat = time.NewTicker(interval)
	defer e.heartbeat.Stop()

	last := time.Now()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-e.heartbeat.C:
			if ping := e.stallPing.Load(); ping != nil {
				(*ping)()
			}
			now := time.Now()
			if gap := now.Sub(last); gap > 2*interval {
				missed := int64(gap/interval) - 1
				if missed > 0 {
					e.missedHeartbeats.Add(uint64(missed))
					slog.Warn("heartbeat ticks missed",
						"t", "consensus",
						"event", "tick-missed",
						"missed", missed,
						"gap_ms", gap.Milliseconds(),
						"interval_ms", interval.Milliseconds(),
						"total_missed", e.missedHeartbeats.Load(),
					)
				}
			}
			last = now
			e.timerEntry()
		}
	}
}

// MissedHeartbeats returns the count of dropped heartbeat ticks since
// start.
func (e *Engine) MissedHeartbeats() uint64 {
	return e.missedHeartbeats.Load()
}

// timerEntry is the single heartbeat dispatch; runs each
// ledgerGRANULARITY and dispatches on current phase.
func (e *Engine) timerEntry() {
	tickStart := time.Now()
	e.mu.Lock()
	e.deferBroadcasts++
	var pending []func()
	defer func() {
		e.deferBroadcasts--
		pending = e.takePendingBroadcastsLocked()
		e.mu.Unlock()
		flushBroadcasts(pending)
		// 50ms threshold — the 250ms heartbeat needs headroom.
		dur := time.Since(tickStart)
		if dur > 50*time.Millisecond {
			slog.Info("timer tick slow",
				"t", "consensus",
				"event", "tick-slow",
				"dur_ms", dur.Milliseconds(),
				"phase", e.phase.String(),
				"mode", e.mode.String(),
			)
		}
	}()

	// Phase work runs in every non-disconnected mode; the proposing gate is
	// per-round (closeLedger/sendValidation gate on ModeProposing). Without
	// observer-mode advancement a genesis bootstrap deadlocks at
	// OpModeConnected — no round closes, so auto-promote never fires.
	if e.adaptor.GetOperatingMode() == consensus.OpModeDisconnected {
		return
	}

	// A peer-triggered accept may be applying the LCL off e.mu on another
	// goroutine; don't drive rounds until its commit tail runs (rippled parks
	// the timer thread while the jtACCEPT job holds no lock).
	if e.buildInProgress {
		return
	}

	// Sweep validations that aged past the isCurrent window off the steering
	// indexes each tick (rippled doSweep → current()); a silent validator
	// must not keep steering preferred-ledger selection through a stall.
	if e.validationTracker != nil {
		e.validationTracker.FlushStale()
	}

	// Runs every tick regardless of phase: a WrongLedger pin taken at
	// PhaseAccepted advances no rounds, so the checkLedger path below never runs.
	e.checkStuckWrongLedger()

	// checkLedger runs in every non-disconnected mode — the Syncing/Tracking
	// → Full recovery path; gating on Full would wedge us after a wrongLedger
	// demotion.
	if e.phase != consensus.PhaseAccepted {
		e.checkLedger()
	}

	switch e.phase {
	case consensus.PhaseOpen:
		e.phaseOpen()
	case consensus.PhaseEstablish:
		e.phaseEstablish()
	case consensus.PhaseAccepted:
		e.checkAndStartRoundInner()
		// Evaluate the new phase in the same tick after starting a round.
		if e.phase == consensus.PhaseOpen {
			e.phaseOpen()
		}
	}
}

// checkAndStartRoundInner is the fallback round-start when acceptLedger's
// auto-advance didn't fire (e.g. first round). Caller must hold e.mu.
func (e *Engine) checkAndStartRoundInner() {
	if e.phase != consensus.PhaseAccepted {
		return
	}
	if e.mode == consensus.ModeWrongLedger {
		return
	}

	ledger, err := e.adaptor.GetLastClosedLedger()
	if err != nil {
		return
	}

	// Buffered proposals → start immediately (peer pressure closes open
	// phase); otherwise wait for the idle interval.
	ledgerID := ledger.ID()
	hasBufferedProposals := e.proposalTracker.HasBufferedFor(ledgerID)

	if !hasBufferedProposals {
		timeSinceClose := e.adaptor.Now().Sub(ledger.CloseTime())
		if timeSinceClose < e.timing.LedgerIdleInterval {
			return
		}
	}

	proposing := e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull

	// Refresh prevLedger — an InboundLedger adoption may have changed the LCL.
	e.prevLedger = ledger

	// Normal idle-timeout round start (not recovery).
	round := consensus.RoundID{
		Seq:        ledger.Seq() + 1,
		ParentHash: ledger.ID(),
	}
	e.startRoundLocked(round, proposing, false)
}

// checkStuckWrongLedger drops to a degraded resync once pinned in
// ModeWrongLedger past wrongLedgerStuckTimeout, backing the clean-failure hatch
// which can't arm under a livelock or moving target. Caller must hold e.mu.
func (e *Engine) checkStuckWrongLedger() {
	if e.mode == consensus.ModeWrongLedger && !e.wrongLedgerSince.IsZero() &&
		e.adaptor.Now().Sub(e.wrongLedgerSince) > wrongLedgerStuckTimeout {
		e.dropToDegradedResync("stuck-timeout")
	}
}

// checkLedger compares prevLedger against the network-preferred ledger
// and calls handleWrongLedger on a mismatch.
func (e *Engine) checkLedger() {
	if e.prevLedger == nil {
		return
	}
	ourID := e.prevLedger.ID()
	netLgr := e.getNetworkLedger()
	if netLgr != ourID {
		// Network is on our parent: we're ahead, not wrong — wait, don't
		// switch back.
		if netLgr == e.prevLedger.ParentID() {
			return
		}

		// Already targeting this hash: re-resolve once in case it became
		// locally available (held adoption that didn't fire OnLedger) and
		// complete the switch; otherwise we'd spin in wrongLedger forever
		// (#724). Still missing → don't spam the acquire.
		var target consensus.Ledger
		if e.mode == consensus.ModeWrongLedger && e.wrongLedgerID == netLgr {
			if target = e.resolveTargetLedger(netLgr); target == nil {
				return
			}
		}
		slog.Warn("Consensus view changed",
			"phase", e.phase,
			"mode", e.mode,
			"our", fmt.Sprintf("%x", ourID[:8]),
			"net", fmt.Sprintf("%x", netLgr[:8]),
		)
		e.handleWrongLedger(netLgr, target)
	}
}

// getNetworkLedger returns the network-preferred prevLedger. Trusted
// validations decide first, like rippled's getPrevLedger (pure
// vals.getPreferred, RCLConsensus.cpp:301-303) — only validations break a
// proposal-count tie between two self-agreeing islands. The proposal+peer-LCL
// majority is the fallback for validation-less phases (boot).
func (e *Engine) getNetworkLedger() consensus.LedgerID {
	if e.prevLedger == nil {
		return consensus.LedgerID{}
	}
	ourID := e.prevLedger.ID()

	if id, ok := e.validationPreferredLocked(); ok {
		return id
	}

	freshness := e.timing.ProposeFreshness
	now := e.adaptor.Now()

	// For each trusted node, take the most recent fresh proposal.
	type vote struct {
		prevLedger consensus.LedgerID
	}
	votes := make(map[consensus.NodeID]vote)
	for nodeID, p := range e.proposalTracker.LatestFresh(e.adaptor.IsTrusted, now, freshness) {
		votes[nodeID] = vote{prevLedger: p.PreviousLedger}
	}

	// Include our own position as a vote: otherwise the >len/2 majority is
	// computed over peers only, so two disagreeing peers flip our LCL where a
	// fair vote (with us) would tie.
	if e.state != nil && e.state.OurPosition != nil {
		pos := e.state.OurPosition
		if now.Sub(pos.Timestamp) <= freshness {
			if key, err := e.adaptor.GetValidatorKey(); err == nil {
				votes[key] = vote{prevLedger: pos.PreviousLedger}
			}
		}
	}

	// Hashes already voted via trusted proposals. Skip peer-LCL votes for
	// these so a validator that's also a peer isn't double-counted.
	proposalHashes := make(map[consensus.LedgerID]struct{}, len(votes))
	for _, v := range votes {
		proposalHashes[v.prevLedger] = struct{}{}
	}

	// Fold in peer-reported LCLs from statusChange (a peer that advanced its
	// LCL but hasn't gossiped a proposal yet). Keyed on a synthetic NodeID so
	// one peer counts once; deduped against trusted-proposer votes. Votes are
	// counted ungated, like rippled's checkLastClosedLedger peer tally
	// (NetworkOPs.cpp:1915-1921); safety against adopting a bogus gossiped
	// hash lives in the acquire-then-verify checks at the switch site
	// (canSwitchToLedgerLocked), not in vote suppression.
	for i, h := range e.adaptor.PeerReportedLedgers() {
		if _, already := proposalHashes[h]; already {
			continue
		}
		var synthKey consensus.NodeID
		// 0xFF is unused by XRPL pubkey encoding, so synthetic keys can't
		// collide with a real validator key.
		synthKey[0] = 0xFF
		synthKey[1] = byte(i >> 8)
		synthKey[2] = byte(i)
		// Fill the rest with the ledger hash so different reported LCLs from
		// the same ordinal slot stay distinguishable.
		copy(synthKey[3:], h[:30])
		votes[synthKey] = vote{prevLedger: h}
	}

	if len(votes) == 0 {
		return ourID
	}

	counts := make(map[consensus.LedgerID]int)
	for _, v := range votes {
		counts[v.prevLedger]++
	}

	var bestID consensus.LedgerID
	bestCount := 0
	for id, count := range counts {
		if count > bestCount {
			bestID = id
			bestCount = count
		}
	}

	if bestID != ourID && bestCount > len(votes)/2 {
		return bestID
	}
	return ourID
}

// validationPreferredLocked derives the network-preferred prevLedger from
// trusted validations, mirroring rippled getPreferred (Validations.h:849-917):
// trie tip then the stay/switch rules, gated so the result never rewinds
// behind the validated index. ok=false when no trusted validation signal
// exists. Caller holds e.mu.
func (e *Engine) validationPreferredLocked() (consensus.LedgerID, bool) {
	if e.validationTracker == nil {
		return consensus.LedgerID{}, false
	}
	minSeq := e.validatedSeqLocked()
	id, seq, ok := e.validationTracker.GetPreferred(e.ourLastValidatedSeq)
	if !ok {
		id, seq, ok = e.validationTracker.PreferredFromValidations(minSeq)
	}
	if !ok {
		return consensus.LedgerID{}, false
	}

	ourID := e.prevLedger.ID()
	ourSeq := e.prevLedger.Seq()
	if id == ourID {
		return ourID, true
	}
	if seq == ourSeq+1 {
		if l, err := e.adaptor.GetLedger(id); err == nil && l != nil && l.ParentID() == ourID {
			return ourID, true
		}
	}
	if seq < minSeq {
		return ourID, true
	}
	if seq > ourSeq {
		return id, true
	}
	if e.ancestorAtLocked(seq) != id {
		return id, true
	}
	return ourID, true
}

// ancestorAtLocked resolves our chain's ledger ID at targetSeq by walking
// locally-held parents from prevLedger; the zero ID when unresolvable —
// treated as a different chain, like rippled's out-of-skip-list ID{0}
// (RCLValidations.cpp:78-95). Caller holds e.mu.
func (e *Engine) ancestorAtLocked(targetSeq uint32) consensus.LedgerID {
	const maxWalk = 256 // rippled's skip-list reach
	seq := e.prevLedger.Seq()
	if targetSeq > seq || seq-targetSeq > maxWalk {
		return consensus.LedgerID{}
	}
	if targetSeq == seq {
		return e.prevLedger.ID()
	}
	cur := e.prevLedger.ParentID()
	for s := seq - 1; s > targetSeq; s-- {
		l, err := e.adaptor.GetLedger(cur)
		if err != nil || l == nil {
			return consensus.LedgerID{}
		}
		cur = l.ParentID()
	}
	return cur
}

// resolveTargetLedger returns the locally-held ledger for id (by-hash
// store, then the just-adopted LCL), or nil if not held yet.
func (e *Engine) resolveTargetLedger(id consensus.LedgerID) consensus.Ledger {
	if l, err := e.adaptor.GetLedger(id); err == nil && l != nil {
		return l
	}
	if lcl, err := e.adaptor.GetLastClosedLedger(); err == nil && lcl != nil && lcl.ID() == id {
		return lcl
	}
	return nil
}

// handleWrongLedger switches to the network's preferred ledger. target is
// an already-resolved ledger (nil to resolve here).
func (e *Engine) handleWrongLedger(netLedgerID consensus.LedgerID, target consensus.Ledger) {
	// Resolve and verify BEFORE mutating any round state, so a refused
	// switch leaves the in-progress round untouched (rippled verifies with
	// canBeCurrent/isCompatible before switching, NetworkOPs.cpp:1948-1962).
	// An unresolvable target is verified later, at adoption (OnLedger).
	newLedger := target
	if newLedger == nil {
		newLedger = e.resolveTargetLedger(netLedgerID)
	}
	if newLedger != nil && !e.canSwitchToLedgerLocked(newLedger) {
		// Off the validated chain or implausibly timed/sequenced — refuse the
		// switch and stay on our ledger.
		if e.lastRefusedSwitch != netLedgerID {
			e.lastRefusedSwitch = netLedgerID
			slog.Info("Refusing switch to incompatible network ledger",
				"t", "consensus",
				"event", "switch-refused",
				"seq", newLedger.Seq(),
				"hash", fmt.Sprintf("%x", netLedgerID[:8]),
			)
		}
		return
	}

	// Stop proposing.
	if e.mode == consensus.ModeProposing {
		e.setMode(consensus.ModeObserving)
	}

	// Clear consensus state and replay (only for a new target ledger).
	if e.prevLedger == nil || netLedgerID != e.prevLedger.ID() {
		e.proposalTracker.ResetProposals()
		e.disputeTracker = NewDisputeTracker()
		e.acquiredTxSets = make(map[consensus.TxSetID]consensus.TxSet)
		e.comparesTxSets = make(map[consensus.TxSetID]struct{})
		e.peerUnchangedCounter = 0
		e.establishCounter = 0
		e.converged = false
		e.closeTime.haveConsensus = false
		if e.state != nil {
			e.state.CloseTimes.Peers = make(map[time.Time]int)
		}

		// Replay proposals for the new ledger; close-time votes only if a
		// round state exists.
		closeTimes, _, relay := e.proposalTracker.Replay(netLedgerID, e.adaptor.IsTrusted)
		if e.state != nil {
			for _, ct := range closeTimes {
				e.state.CloseTimes.Peers[ct]++
			}
		}

		for _, p := range relay {
			e.adaptor.RelayProposal(p, 0)
		}
	}

	if newLedger != nil {
		// Found — restart with recovering=true so we enter switchedLedger for
		// one round (suppress our proposal/validation to avoid poisoning
		// convergence with a stale view); the next round promotes back normally.
		slog.Info("Switching to network ledger",
			"t", "consensus",
			"event", "switch-lcl",
			"seq", newLedger.Seq(),
			"hash", fmt.Sprintf("%x", netLedgerID[:8]),
		)
		e.prevLedger = newLedger
		e.wrongLedgerID = consensus.LedgerID{}
		e.wrongLedgerAcquireFailures = 0
		if e.state != nil {
			e.state.HaveCorrectLCL = true
		}
		nextRound := consensus.RoundID{
			Seq:        newLedger.Seq() + 1,
			ParentHash: newLedger.ID(),
		}
		proposing := e.adaptor.IsValidator() &&
			e.adaptor.GetOperatingMode() == consensus.OpModeFull
		e.startRoundLocked(nextRound, proposing, true)
	} else {
		// Not found — request from peers. Inside the degraded-resync cooldown,
		// stay advancing rather than re-pinning wrongLedger: a pinned node
		// closes no ledgers and makes no progress toward the network tip.
		e.adaptor.RequestLedger(netLedgerID)
		if e.adaptor.Now().Before(e.degradedResyncUntil) {
			slog.Info("Retrying network ledger in degraded resync",
				"t", "consensus",
				"event", "wrong-lcl-degraded-retry",
				"hash", fmt.Sprintf("%x", netLedgerID[:8]),
			)
			return
		}
		slog.Info("Cannot acquire network ledger, entering wrongLedger mode",
			"t", "consensus",
			"event", "wrong-lcl",
			"hash", fmt.Sprintf("%x", netLedgerID[:8]),
		)
		if e.state != nil {
			e.state.HaveCorrectLCL = false
		}
		e.wrongLedgerID = netLedgerID
		e.setMode(consensus.ModeWrongLedger)
	}
}

// wrongLedgerAcquireMaxFailures bounds clean acquisition failures before
// dropping to a degraded resync; degradedResyncCooldown is how long it
// then stays unpinned and advancing.
const (
	wrongLedgerAcquireMaxFailures = 3
	degradedResyncCooldown        = 20 * time.Second

	// wrongLedgerStuckTimeout bounds continuous time pinned in ModeWrongLedger.
	// The clean-failure hatch can fail to arm — a livelocked acquisition never
	// times out, and a target moving as the network advances leaves each clean
	// failure on a stale id the hatch ignores — so without this bound the node
	// wedges forever. Set above the clean-failure budget so it only backstops
	// a genuinely stuck node.
	wrongLedgerStuckTimeout = 60 * time.Second
)

// OnLedgerAcquireFailed reports a clean acquisition failure for id. If
// pinned in wrongLedger on id it must not stay frozen (a frozen
// wrongLedger closes no ledgers and never rejoins): each failure un-pins
// so checkLedger re-resolves; at the limit it drops to a degraded resync
// so closes resume while recovery continues.
func (e *Engine) OnLedgerAcquireFailed(id consensus.LedgerID) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.mode != consensus.ModeWrongLedger || e.wrongLedgerID != id {
		return
	}

	e.wrongLedgerAcquireFailures++
	// Un-pin so the next checkLedger re-resolves and re-requests.
	e.wrongLedgerID = consensus.LedgerID{}

	if e.wrongLedgerAcquireFailures < wrongLedgerAcquireMaxFailures {
		slog.Warn("wrongLedger acquisition failed; will re-attempt",
			"t", "consensus",
			"event", "wrong-lcl-retry",
			"hash", fmt.Sprintf("%x", id[:8]),
			"failures", e.wrongLedgerAcquireFailures,
		)
		return
	}

	// Persistent clean failure: validated ledger unacquirable.
	e.dropToDegradedResync("acquire-max-failures")
}

// dropToDegradedResync demotes a node that cannot acquire its wrongLedger
// target: ModeObserving keeps rounds advancing while checkLedger retries, so
// closes resume. Reached from both the clean-failure hatch (at its limit) and
// the stuck-acquisition backstop. Caller must hold e.mu.
func (e *Engine) dropToDegradedResync(reason string) {
	slog.Warn("wrongLedger ledger unacquirable; dropping to degraded resync",
		"t", "consensus",
		"event", "wrong-lcl-degraded",
		"reason", reason,
	)
	e.wrongLedgerAcquireFailures = 0
	// Un-pin so the next checkLedger re-resolves and re-requests.
	e.wrongLedgerID = consensus.LedgerID{}
	e.degradedResyncUntil = e.adaptor.Now().Add(degradedResyncCooldown)
	if e.state != nil {
		e.state.HaveCorrectLCL = false
	}
	e.setMode(consensus.ModeObserving)
	if e.adaptor.GetOperatingMode() == consensus.OpModeFull {
		e.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
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

	// Stamp wrongLedgerSince on entry to ModeWrongLedger, clear on exit. A re-pin
	// stays in the same mode, so the stamp survives a churning target.
	switch {
	case newMode == consensus.ModeWrongLedger && oldMode != consensus.ModeWrongLedger:
		e.wrongLedgerSince = e.adaptor.Now()
	case newMode != consensus.ModeWrongLedger:
		e.wrongLedgerSince = time.Time{}
	}

	e.eventBus.Publish(&consensus.ModeChangedEvent{
		OldMode:   oldMode,
		NewMode:   newMode,
		Timestamp: e.adaptor.Now(),
	})

	e.adaptor.OnModeChange(oldMode, newMode)
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
		e.state.Phase = newPhase
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

// closeAgreementReporter is optionally implemented by a prevLedger that can
// report whether its close time was reached by consensus. Ledgers that don't
// (simulation/test ledgers) are treated as having agreed — the normal case.
type closeAgreementReporter interface {
	CloseAgree() bool
	ParentCloseTime() time.Time
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
	proposersClosed = e.proposalTracker.CountTrusted(e.adaptor.IsTrusted)
	if e.prevLedger != nil && e.validationTracker != nil {
		proposersValidated = e.validationTracker.ProposersValidated(e.prevLedger.ID())
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
		if twoRes := 2 * e.adaptor.PrevCloseTimeResolution(); twoRes > idle {
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
	// #422: log when prior proposers + self can't meet quorum (likely stall);
	// skipped before the first completed round.
	if e.consensusCount > 0 {
		quorum := e.adaptor.GetQuorum()
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
				"unl_size", len(e.adaptor.GetTrustedValidators()),
				"seq", seq,
			)
		}
	}

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
			parentVals := e.parentValidations(prev.ParentID())
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
	requested := make(map[consensus.TxSetID]struct{})
	for _, p := range e.proposalTracker.All() {
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

// phaseEstablish re-evaluates convergence each heartbeat. Caller must hold e.mu.
func (e *Engine) phaseEstablish() {
	roundTime := e.now().Sub(e.roundStartTime)

	// Snapshot round time and converge percent each tick (before pause/accept)
	// so consensus_info reports meaningful values between rounds.
	e.currentRoundTime = roundTime
	e.lastConvergePercent = e.convergePercent()

	// Pause before the accept paths if we've run past validated and a
	// quorum-blocking share of validators lags (#451); bounded inside
	// shouldPause so a stuck round still abandons via the ceiling below.
	if e.shouldPause(roundTime) {
		return
	}

	e.establishCounter++
	e.peerUnchangedCounter++

	// Give everyone a chance to take an initial position: rippled
	// phaseEstablish returns before updateOurPositions until roundTime
	// reaches ledgerMIN_CONSENSUS (Consensus.h:1393-1400). Updating our
	// position or tallying close-time votes earlier diverges from the peer
	// majority's timing. Counters above still advance each tick.
	if roundTime < e.timing.LedgerMinConsensus {
		return
	}

	// Prune stale peer proposals every tick regardless of our own mode
	// (rippled updateOurPositions prunes unconditionally, Consensus.h:
	// 1509-1528): a bowed-out observer waiting at the close-time gate must
	// not have its tally diluted forever by a silent peer's stale vote.
	e.pruneStaleProposalsLocked()

	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil {
		e.updatePosition()
	}
	e.updateCloseTimePosition()
	e.checkConvergence()
}

// pruneStaleProposalsLocked drops peer proposals older than the freshness
// window and revokes their dispute votes. Caller must hold e.mu.
func (e *Engine) pruneStaleProposalsLocked() {
	cutoff := e.adaptor.Now().Add(-e.timing.ProposeFreshness)
	for _, nodeID := range e.proposalTracker.PruneStale(cutoff) {
		if e.disputeTracker != nil {
			e.disputeTracker.UnVote(nodeID)
		}
	}
}

// shouldPause returns true when the establish phase should suspend for one
// heartbeat: our prev LCL has run past the fully-validated tip and a
// quorum-blocking share of trusted validators is lagging or offline. A
// paused round skips acceptLedger, so the local closed_ledger doesn't
// drift further past validated (#451). Clears once the round exceeds
// LedgerMaxConsensus or peers catch up. Caller must hold e.mu.
func (e *Engine) shouldPause(roundTime time.Duration) bool {
	if e.prevLedger == nil {
		return false
	}
	// Early-out: not a validator, no validation history, nothing ahead, or
	// past the hard timeout. Skipping with no prior validation lets bootstrap
	// rounds run — pause guards ongoing drift, not startup.
	if !e.adaptor.IsValidator() {
		return false
	}
	if e.ourLastValidatedSeq == 0 {
		return false
	}
	if e.timing.LedgerMaxConsensus > 0 && roundTime > e.timing.LedgerMaxConsensus {
		return false
	}

	prevSeq := e.prevLedger.Seq()
	validatedSeq := e.validatedSeqLocked()
	if validatedSeq >= prevSeq {
		return false
	}
	ahead := prevSeq - validatedSeq
	if ahead == 0 {
		return false
	}

	trusted := e.adaptor.GetTrustedValidators()
	totalValidators := len(trusted)
	if totalValidators == 0 {
		return false
	}
	quorum := e.adaptor.GetQuorum()
	if quorum == 0 {
		return false
	}

	laggards, offline := e.countLaggardsAndOfflineLocked(prevSeq, trusted)
	if laggards == 0 {
		return false
	}

	// Phase-progressive threshold: each ledger we're ahead cycles through 5
	// phases of increasing strictness — phase 0 pauses on a single laggard,
	// maxPausePhase pauses unconditionally.
	const maxPausePhase = 4
	phase := int(ahead-1) % (maxPausePhase + 1)

	switch phase {
	case 0:
		// Pause when laggards+offline exceed quorum slack.
		if laggards+offline > totalValidators-quorum {
			return logPauseLocked(e, ahead, laggards, offline, totalValidators, quorum, phase)
		}
	case maxPausePhase:
		// No tolerance — strictest phase.
		return logPauseLocked(e, ahead, laggards, offline, totalValidators, quorum, phase)
	default:
		// Intermediate: require the non-laggard ratio to clear quorum + a
		// linear share of slack.
		nonLaggards := float64(totalValidators - laggards - offline)
		quorumRatio := float64(quorum) / float64(totalValidators)
		allowedDissent := 1.0 - quorumRatio
		phaseFactor := float64(phase) / float64(maxPausePhase)
		if nonLaggards/float64(totalValidators) < quorumRatio+(allowedDissent*phaseFactor) {
			return logPauseLocked(e, ahead, laggards, offline, totalValidators, quorum, phase)
		}
	}
	return false
}

// validatedSeqLocked returns the most-recently fully-validated seq (0 if
// none), from the adaptor's validated hash+ledger. Caller must hold e.mu.
func (e *Engine) validatedSeqLocked() uint32 {
	vh := e.adaptor.GetValidatedLedgerHash()
	if vh == (consensus.LedgerID{}) {
		return 0
	}
	vl, err := e.adaptor.GetLedger(vh)
	if err != nil || vl == nil {
		return 0
	}
	return vl.Seq()
}

// isBuildCompatibleWithValidatedLocked reports whether the built ledger
// has the validated tip on its ancestry (rippled's areCompatible). Three
// branches by validatedSeq vs builtSeq: walk the higher back to the lower
// via ParentID and compare, or compare hashes at equal seq. Missing
// intermediate ancestors → true (compatible), matching rippled's
// nullopt-hashOfSeq rule. Caller must hold e.mu.
func (e *Engine) isBuildCompatibleWithValidatedLocked(built consensus.Ledger) bool {
	if built == nil {
		return true
	}
	vh := e.adaptor.GetValidatedLedgerHash()
	if vh == (consensus.LedgerID{}) {
		return true
	}
	vl, err := e.adaptor.GetLedger(vh)
	if err != nil || vl == nil {
		return true
	}
	validatedSeq := vl.Seq()
	builtSeq := built.Seq()

	if validatedSeq == builtSeq {
		return built.ID() == vh
	}

	if validatedSeq < builtSeq {
		current := built
		// Walk built back to validatedSeq via parents (first hop is
		// prevLedger, always known; deeper hops may miss → compatible per
		// rippled).
		for current != nil && current.Seq() > validatedSeq {
			parent, err := e.adaptor.GetLedger(current.ParentID())
			if err != nil || parent == nil {
				return true
			}
			current = parent
		}
		if current == nil || current.Seq() != validatedSeq {
			return true
		}
		return current.ID() == vh
	}

	// validatedSeq > builtSeq: walk validated back to builtSeq.
	current := vl
	for current != nil && current.Seq() > builtSeq {
		parent, err := e.adaptor.GetLedger(current.ParentID())
		if err != nil || parent == nil {
			return true
		}
		current = parent
	}
	if current == nil || current.Seq() != builtSeq {
		return true
	}
	return current.ID() == built.ID()
}

// canSwitchToLedgerLocked applies rippled's pre-switch sanity checks to an
// acquired candidate LCL (NetworkOPs.cpp:1953-1962): plausible seq/close time
// via canBeCurrent, and validated-chain ancestry via areCompatible. Peer-LCL
// votes are counted ungated, so this is the safety that keeps a bogus
// gossiped hash from being adopted. Caller must hold e.mu.
func (e *Engine) canSwitchToLedgerLocked(candidate consensus.Ledger) bool {
	if candidate == nil {
		return false
	}
	return e.canBeCurrentLocked(candidate) &&
		e.isBuildCompatibleWithValidatedLocked(candidate)
}

// canBeCurrentLocked mirrors rippled LedgerMaster::canBeCurrent
// (LedgerMaster.cpp:341-407): never rewind behind the validated tip, close
// time within 5 minutes of network time, and seq at most
// validated+10+elapsed/2 ahead. goXRPL's ledger surface has no parent close
// time, so the candidate's own close time stands in (one close interval of
// slack inside a 5-minute window). Caller must hold e.mu.
func (e *Engine) canBeCurrentLocked(candidate consensus.Ledger) bool {
	now := e.adaptor.Now()
	var validated consensus.Ledger
	if vh := e.adaptor.GetValidatedLedgerHash(); vh != (consensus.LedgerID{}) {
		if vl, err := e.adaptor.GetLedger(vh); err == nil {
			validated = vl
		}
	}
	if validated != nil && candidate.Seq() < validated.Seq() {
		return false
	}
	if validated != nil || candidate.Seq() > 10 {
		gap := now.Sub(candidate.CloseTime())
		if gap < 0 {
			gap = -gap
		}
		if gap > 5*time.Minute {
			return false
		}
	}
	if validated != nil {
		maxSeq := validated.Seq() + 10
		if elapsed := now.Sub(validated.CloseTime()); elapsed > 0 {
			maxSeq += uint32(elapsed / (2 * time.Second))
		}
		if candidate.Seq() > maxSeq {
			return false
		}
	}
	return true
}

// validationLaggardFreshness (20s): a validation older than this counts
// the peer as offline, not a laggard. Shorter than the 3m/5m isCurrent
// windows — laggard accounting wants "validated in the last interval".
const validationLaggardFreshness = 20 * time.Second

// countLaggardsAndOfflineLocked partitions trusted validators (except us)
// by their latest fresh validation: laggards have a fresh validation at
// seq < prevSeq (haven't advanced past our prev); offline have none or
// only a stale one. seq >= prevSeq counts as neither. Caller must hold e.mu.
func (e *Engine) countLaggardsAndOfflineLocked(prevSeq uint32, trusted []consensus.NodeID) (laggards, offline int) {
	if e.validationTracker == nil {
		return 0, 0
	}
	self, _ := e.adaptor.GetValidatorKey()
	now := e.adaptor.Now()
	for _, k := range trusted {
		if k == self {
			continue
		}
		v := e.validationTracker.GetLatestValidation(k)
		if v == nil {
			offline++
			continue
		}
		seen := v.SeenTime
		if seen.IsZero() {
			seen = v.SignTime
		}
		if !seen.IsZero() && now.Sub(seen) > validationLaggardFreshness {
			offline++
			continue
		}
		if v.LedgerSeq < prevSeq {
			laggards++
		}
	}
	return laggards, offline
}

// logPauseLocked emits the pause telemetry and returns true so callers can
// `return logPauseLocked(...)`.
func logPauseLocked(e *Engine, ahead uint32, laggards, offline, totalValidators, quorum int, phase int) bool {
	seq := uint32(0)
	if e.prevLedger != nil {
		seq = e.prevLedger.Seq()
	}
	slog.Info("consensus pause — ahead of validated, peers lagging",
		"t", "consensus",
		"event", "consensus-pause",
		"working_seq", seq,
		"ahead", ahead,
		"validators", totalValidators,
		"laggards", laggards,
		"offline", offline,
		"quorum", quorum,
		"phase", phase,
	)
	return true
}

// abandonDeadlineExceeded reports whether the round passed the
// clamp(prevRoundTime*factor, LedgerMaxConsensus, LedgerAbandonConsensus)
// hard deadline. Caller must hold e.mu.
func (e *Engine) abandonDeadlineExceeded(roundTime time.Duration) bool {
	lo := e.timing.LedgerMaxConsensus
	hi := e.timing.LedgerAbandonConsensus
	if hi <= 0 {
		return false
	}
	// clamp(factor×prev, lo, hi); factor 0 disables scaling → absolute ceiling.
	var deadline time.Duration
	if e.timing.LedgerAbandonConsensusFactor > 0 && e.prevRoundTime > 0 {
		deadline = e.prevRoundTime * time.Duration(e.timing.LedgerAbandonConsensusFactor)
	} else {
		deadline = hi
	}
	if lo > 0 && deadline < lo {
		deadline = lo
	}
	if deadline > hi {
		deadline = hi
	}
	return roundTime > deadline
}

// consensusState is checkConsensusState's decision: No, MovedOn, Expired,
// Yes (same ordinal layout as rippled's ConsensusState).
type consensusState int

const (
	consensusStateNo consensusState = iota
	consensusStateMovedOn
	consensusStateExpired
	consensusStateYes
)

// checkConvergence drives the accept gate (rippled's
// phaseEstablish→haveConsensus→checkConsensus flow): maintain the local
// "converged" observability flag, compute consensusState, then dispatch.
// Every accept path — Yes, MovedOn, and Expired — sits behind the
// close-time gate, exactly as in rippled where haveConsensus returns true
// for all three and phaseEstablish then returns on !haveCloseTimeConsensus_
// (Consensus.h:1406-1411). No→retry next heartbeat.
func (e *Engine) checkConvergence() {
	if e.phase != consensus.PhaseEstablish {
		return
	}

	// Gate out wrongLedger: rippled makes this structurally unreachable
	// (result_ null), but go-xrpl's observer fallback in countAgreement would
	// otherwise accept on peer-peer agreement, walk prev past validated, and
	// re-enter wrongLedger every round — a permanent stall (iter27/iter28).
	if e.mode == consensus.ModeWrongLedger {
		return
	}

	roundTime := e.now().Sub(e.roundStartTime)
	agree, disagree := e.countAgreement()
	total := agree + disagree

	// EarlyConvergencePct is a go-xrpl-local observability flag; acceptance
	// uses MinConsensusPct inside checkConsensusState.
	if total > 0 && agree*100 >= total*e.thresholds.EarlyConvergencePct {
		e.converged = true
		e.state.Converged = true
	}

	state := e.checkConsensusState(roundTime, agree, total)

	if state == consensusStateNo {
		return
	}

	// The Expired hard-timeout bows a proposer out (rippled leaveConsensus
	// inside haveConsensus, Consensus.h:1784) once past the per-avalanche
	// minimum dwell (Consensus.h:1765). Acceptance still sits behind the
	// close-time gate below: an expired round without close-time consensus
	// stays in Establish and recovers only via checkLedger resyncing it onto
	// the network's ledger.
	if state == consensusStateExpired {
		minimumCounter := len(e.parms.AvalancheCutoffs) * e.parms.MinRounds
		if e.establishCounter < minimumCounter {
			slog.Warn("consensus expired but inside retry window — continuing",
				"t", "consensus",
				"event", "expired-retry",
				"round", e.state.Round,
				"establish_counter", e.establishCounter,
				"minimum_counter", minimumCounter,
				"round_time", roundTime,
			)
			return
		}
		if !e.roundExpiredReported {
			e.roundExpiredReported = true
			slog.Warn("consensus taken too long, abandoning round",
				"t", "consensus",
				"event", "expired",
				"round", e.state.Round,
				"round_time", roundTime,
				"prev_round_time", e.prevRoundTime,
				"max_consensus", e.timing.LedgerMaxConsensus,
				"abandon_consensus", e.timing.LedgerAbandonConsensus,
			)
			e.eventBus.Publish(&consensus.TimerFiredEvent{
				Timer:     consensus.TimerRoundTimeout,
				Round:     e.state.Round,
				Timestamp: e.adaptor.Now(),
			})
		}
		e.leaveConsensusLocked()
	}

	// Close-time consensus gates every accept path. Re-try once here in case
	// the caller (OnProposal/OnTxSet) skipped phaseEstablish.
	if !e.closeTime.haveConsensus {
		e.updateCloseTimePosition()
		if !e.closeTime.haveConsensus {
			return
		}
	}

	switch state {
	case consensusStateYes:
		e.acceptLedger(consensus.ResultSuccess)
	case consensusStateMovedOn:
		finished := 0
		if e.validationTracker != nil && e.prevLedger != nil {
			finished = e.validationTracker.ProposersFinished(e.prevLedger)
		}
		slog.Info("consensus moved on, accepting",
			"t", "consensus",
			"event", "moved-on",
			"seq", e.state.Round.Seq,
			"finished", finished,
			"current_proposers", total,
			"prev_proposers", e.prevProposers,
			"round_time_ms", roundTime.Milliseconds(),
		)
		e.acceptLedger(consensus.ResultMovedOn)
	case consensusStateExpired:
		e.acceptLedger(consensus.ResultAbandoned)
	}
}

// leaveConsensusLocked bows a proposer out of the round (rippled
// Consensus::leaveConsensus, Consensus.h:1800-1817): stop proposing so the
// next round doesn't count us, without touching phase or prevLedger.
// Idempotent. Caller holds e.mu.
func (e *Engine) leaveConsensusLocked() {
	if e.mode != consensus.ModeProposing {
		return
	}
	// Broadcast a bow-out (seqLeave) so peers immediately unVote and drop
	// our position instead of counting it for up to ProposeFreshness after
	// we've left (rippled leaveConsensus → position.bowOut + propose,
	// Consensus.h:1807-1810).
	if e.state != nil && e.state.OurPosition != nil && e.prevLedger != nil {
		nodeID, err := e.adaptor.GetValidatorKey()
		if err == nil {
			bow := &consensus.Proposal{
				Round:          e.state.Round,
				NodeID:         nodeID,
				Position:       0xFFFFFFFF, // seqLeave
				TxSet:          e.state.OurPosition.TxSet,
				CloseTime:      e.state.OurPosition.CloseTime,
				PreviousLedger: e.prevLedger.ID(),
				Timestamp:      e.adaptor.Now(),
			}
			if err := e.adaptor.SignProposal(bow); err == nil {
				e.state.OurPosition = bow
				e.enqueueProposalBroadcastLocked(bow)
			}
		}
	}
	e.setMode(consensus.ModeObserving)
}

// reproposeCurrentLocked re-emits our current position unchanged with a
// bumped seq and fresh timestamp (rippled's freshness re-proposal). Caller
// holds e.mu; caller guarantees OurPosition and prevLedger are non-nil.
func (e *Engine) reproposeCurrentLocked() {
	cur := e.state.OurPosition
	nodeID, err := e.adaptor.GetValidatorKey()
	if err != nil {
		return
	}
	proposal := &consensus.Proposal{
		Round:          e.state.Round,
		NodeID:         nodeID,
		Position:       cur.Position + 1,
		TxSet:          cur.TxSet,
		CloseTime:      cur.CloseTime,
		PreviousLedger: e.prevLedger.ID(),
		Timestamp:      e.adaptor.Now(),
	}
	if err := e.adaptor.SignProposal(proposal); err == nil {
		e.state.OurPosition = proposal
		e.enqueueProposalBroadcastLocked(proposal)
	}
}

// checkConsensusState mirrors rippled's checkConsensus, returning
// {No, Yes, MovedOn, Expired}. Args are caller-computed so e.converged
// stays on a consistent snapshot. Priority order:
//
//  1. roundTime <= ledgerMIN_CONSENSUS                         → No
//  2. currentProposers < prevProposers*3/4 AND
//     roundTime < prevRoundTime + ledgerMIN_CONSENSUS          → No
//  3. checkConsensusReached(agree, ...)                        → Yes
//  4. checkConsensusReached(finished, ...)                     → MovedOn
//  5. roundTime > clamp(prevRoundTime*factor, MAX, ABANDON)    → Expired
//  6. else                                                     → No
//
// "stalled" requires haveCloseTimeConsensus and every dispute Stalled.
func (e *Engine) checkConsensusState(roundTime time.Duration, agree, currentProposers int) consensusState {
	if roundTime <= e.timing.LedgerMinConsensus {
		return consensusStateNo
	}

	// 3/4 prev-proposers pause: with fewer than 3/4 of last round's proposers
	// present, wait one more MIN_CONSENSUS past prevRoundTime for stragglers.
	// Skipped at prevProposers==0 so a 1-node soak can't freeze.
	if e.prevProposers > 0 && currentProposers < (e.prevProposers*3/4) {
		if roundTime < (e.prevRoundTime + e.timing.LedgerMinConsensus) {
			return consensusStateNo
		}
	}

	reachedMax := e.timing.LedgerMaxConsensus > 0 && roundTime > e.timing.LedgerMaxConsensus
	proposing := e.mode == consensus.ModeProposing

	// agree/currentProposers are PEER-only counts (rippled currPeerPositions_);
	// self joins only inside the Yes check via countSelf=proposing
	// (Consensus.cpp:153-158) — folding it into the shared counts skews the
	// 3/4-proposers pause and MovedOn denominator. stalled needs
	// haveCloseTimeConsensus and a non-empty dispute set all individually
	// stalled.
	stalled := false
	if e.closeTime.haveConsensus && e.disputeTracker != nil {
		stalled = e.disputeTracker.AllStalled(e.parms, proposing, e.peerUnchangedCounter)
	}
	if checkConsensusReached(agree, currentProposers, proposing, e.thresholds.MinConsensusPct, reachedMax, stalled) {
		return consensusStateYes
	}

	// MovedOn denominator is current-round proposers (not prevProposers):
	// peers stop proposing for our round as they advance.
	if e.prevLedger != nil && e.validationTracker != nil {
		finished := e.validationTracker.ProposersFinished(e.prevLedger)
		if checkConsensusReached(finished, currentProposers, false, e.thresholds.MinConsensusPct, reachedMax, false) {
			return consensusStateMovedOn
		}
	}

	if e.timing.LedgerAbandonConsensus > 0 && e.abandonDeadlineExceeded(roundTime) {
		return consensusStateExpired
	}

	return consensusStateNo
}

// checkConsensusReached: true when agreeing/total meets minPct. Empty set
// → true only past ledgerMAX_CONSENSUS (reachedMax, the alone-too-long
// carve-out); a stalled dispute set short-circuits to true.
func checkConsensusReached(agreeing, total int, countSelf bool, minPct int, reachedMax, stalled bool) bool {
	if total == 0 {
		// Alone for too long → consensus by default.
		return reachedMax
	}
	if stalled {
		return true
	}
	if countSelf {
		agreeing++
		total++
	}
	return (agreeing*100)/total >= minPct
}

// countAgreement returns PEER proposers whose position matches ours
// (agree) vs differs (disagree) — self excluded, like rippled's
// currPeerPositions_ tally (Consensus.h:1689-1707); the Yes check adds
// self via countSelf. Caller must hold e.mu.
func (e *Engine) countAgreement() (agree, disagree int) {
	var ourTxSet consensus.TxSetID
	haveOurs := false
	if e.state != nil && e.state.OurPosition != nil {
		ourTxSet = e.state.OurPosition.TxSet
		haveOurs = true
	} else if e.ourTxSet != nil {
		ourTxSet = e.ourTxSet.ID()
		haveOurs = true
	}
	if !haveOurs {
		// Observer without a position: count peer-peer agreement on the most
		// popular tx set so non-proposing nodes still get a convergence signal.
		counts := make(map[consensus.TxSetID]int)
		for nodeID, p := range e.proposalTracker.All() {
			if e.adaptor.IsTrusted(nodeID) {
				counts[p.TxSet]++
			}
		}
		var best int
		for _, c := range counts {
			if c > best {
				best = c
			}
		}
		agree = best
		for _, c := range counts {
			if c != best {
				disagree += c
			}
		}
		return agree, disagree
	}

	for nodeID, p := range e.proposalTracker.All() {
		if !e.adaptor.IsTrusted(nodeID) {
			continue
		}
		if p.TxSet == ourTxSet {
			agree++
		} else {
			disagree++
		}
	}
	return agree, disagree
}

// updatePosition runs the per-tx dispute re-vote and, if any vote flipped,
// rebuilds ourTxSet from the inclusion decisions and rebroadcasts our
// position. Caller must hold e.mu.
func (e *Engine) updatePosition() {
	if e.state == nil {
		return
	}

	if e.disputeTracker == nil || e.ourTxSet == nil {
		return
	}

	// Re-vote each dispute at the current converge percent. Observers run the
	// bookkeeping (avalanche consistency) but only proposers flip positions.
	proposing := e.mode == consensus.ModeProposing
	disputeCount := e.disputeTracker.Count()
	changed := e.disputeTracker.UpdateOurVote(e.convergePercent(), proposing, e.parms)

	if disputeCount > 0 || proposing {
		var ourSetID consensus.TxSetID
		ourSetSize := -1
		if e.ourTxSet != nil {
			ourSetID = e.ourTxSet.ID()
			ourSetSize = e.ourTxSet.Size()
		}
		slog.Info("update position",
			"t", "consensus",
			"event", "update-position",
			"seq", e.state.Round.Seq,
			"mode", e.mode.String(),
			"converge_pct", e.convergePercent(),
			"disputes", disputeCount,
			"flipped", len(changed),
			"our_txset", fmt.Sprintf("%x", ourSetID[:8]),
			"our_tx_count", ourSetSize,
			"acquired_txsets", len(e.acquiredTxSets),
			"peer_proposals", e.proposalTracker.Count(),
		)
	}

	if !proposing {
		return
	}

	// Freshness re-proposal (rippled Consensus.h:1636-1642): when nothing
	// flipped but our position has gone stale (older than ProposeInterval),
	// re-emit it with a bumped seq and fresh timestamp so peers don't prune
	// it at ProposeFreshness during a long round — losing it would drop our
	// vote from every peer's tally exactly when convergence is hardest.
	if len(changed) == 0 {
		if e.state.OurPosition != nil && e.prevLedger != nil &&
			e.adaptor.Now().Sub(e.state.OurPosition.Timestamp) >= e.timing.ProposeInterval {
			e.reproposeCurrentLocked()
		}
		return
	}

	// Rebuild ourTxSet from the dispute decisions: add a tx on a yes vote,
	// drop it on a no vote.
	currentBlobs := e.ourTxSet.Txs()
	currentIDs := e.ourTxSet.TxIDs()
	idSet := make(map[consensus.TxID]int, len(currentIDs))
	for idx, id := range currentIDs {
		idSet[id] = idx
	}

	newBlobs := make([][]byte, 0, len(currentBlobs)+len(changed))
	keep := make(map[consensus.TxID]bool, len(currentIDs))
	for _, id := range currentIDs {
		keep[id] = true
	}
	for _, txID := range changed {
		dispute := e.disputeTracker.GetDispute(txID)
		if dispute == nil {
			continue
		}
		if dispute.OurVote {
			if !keep[txID] {
				keep[txID] = true
			}
		} else {
			keep[txID] = false
		}
	}
	// Preserve original order for txs we keep that were already in
	// ours, then append newly-voted-in disputes.
	for idx, id := range currentIDs {
		if keep[id] {
			newBlobs = append(newBlobs, currentBlobs[idx])
		}
	}
	for _, txID := range changed {
		if _, already := idSet[txID]; already {
			continue
		}
		if !keep[txID] {
			continue
		}
		dispute := e.disputeTracker.GetDispute(txID)
		if dispute == nil || dispute.Tx == nil {
			continue
		}
		newBlobs = append(newBlobs, dispute.Tx)
	}

	newTxSet, err := e.adaptor.BuildTxSet(newBlobs)
	if err != nil || newTxSet == nil {
		slog.Warn("updatePosition: failed to rebuild tx set after dispute re-vote",
			"err", err,
		)
		return
	}

	// No-op if the rebuild produced the same set.
	if newTxSet.ID() == e.ourTxSet.ID() {
		return
	}

	e.ourTxSet = newTxSet
	e.acquiredTxSets[newTxSet.ID()] = newTxSet
	// Emitting needs both OurPosition (for the seq bump) and prevLedger; a
	// test harness without Start() has prevLedger nil — still update ourTxSet,
	// just don't emit.
	if e.state.OurPosition != nil && e.prevLedger != nil {
		nodeID, _ := e.adaptor.GetValidatorKey()
		proposal := &consensus.Proposal{
			Round:          e.state.Round,
			NodeID:         nodeID,
			Position:       e.state.OurPosition.Position + 1,
			TxSet:          newTxSet.ID(),
			CloseTime:      e.state.OurPosition.CloseTime,
			PreviousLedger: e.prevLedger.ID(),
			Timestamp:      e.adaptor.Now(),
		}
		if err := e.adaptor.SignProposal(proposal); err == nil {
			e.state.OurPosition = proposal
			e.enqueueProposalBroadcastLocked(proposal)
		}
	}

	// Refresh per-peer votes for peers whose position matches the new set.
	for nodeID, p := range e.proposalTracker.All() {
		if p.TxSet != newTxSet.ID() {
			continue
		}
		if e.disputeTracker.UpdateDisputes(nodeID, newTxSet) {
			e.peerUnchangedCounter = 0
		}
	}
}

// acceptLedger finalizes consensus and accepts the new ledger. Runs in
// every mode; only validation emission is mode-gated via isCompatible.
func (e *Engine) acceptLedger(result consensus.Result) {
	if e.phase != consensus.PhaseEstablish {
		return
	}

	// Close-time consensus → determineCloseTime + effCloseTime; else a
	// deterministic parentClose+1s fallback (a local-clock fallback diverges
	// across nodes — #401).
	priorClose := e.prevLedger.CloseTime()
	resolution := e.adaptor.CloseTimeResolution()
	var rawCloseTime, closeTime time.Time
	var ctBranch string
	if e.closeTime.haveConsensus {
		rawCloseTime = e.determineCloseTime()
		closeTime = effCloseTime(rawCloseTime, resolution, priorClose)
		ctBranch = "consensus"
	} else {
		closeTime = priorClose.Add(time.Second)
		rawCloseTime = closeTime
		ctBranch = "fallback"
	}

	var ourPosCT int64
	var ourPosSeq uint32
	if e.state != nil && e.state.OurPosition != nil {
		ourPosCT = e.state.OurPosition.CloseTime.Unix() - protocol.RippleEpochUnix
		ourPosSeq = e.state.OurPosition.Position
	}
	slog.Info("close-time decision",
		"t", "consensus",
		"event", "accept-ct",
		"seq", e.prevLedger.Seq()+1,
		"mode", e.mode.String(),
		"have_ct_consensus", e.closeTime.haveConsensus,
		"ct_branch", ctBranch,
		"raw_ct_xrpl", rawCloseTime.Unix()-protocol.RippleEpochUnix,
		"eff_ct_xrpl", closeTime.Unix()-protocol.RippleEpochUnix,
		"prior_ct_xrpl", priorClose.Unix()-protocol.RippleEpochUnix,
		"our_pos_ct_xrpl", ourPosCT,
		"our_pos_seq", ourPosSeq,
		"self_ct_xrpl", e.state.CloseTimes.Self.Unix()-protocol.RippleEpochUnix,
		"resolution_s", int(resolution.Seconds()),
		"peer_ct_count", len(e.state.CloseTimes.Peers),
		"proposer_count", e.proposalTracker.Count(),
	)

	var txSet consensus.TxSet
	if e.ourTxSet != nil {
		txSet = e.ourTxSet
	} else {
		// Find most popular among trusted
		txSetCounts := make(map[consensus.TxSetID]int)
		for nodeID, proposal := range e.proposalTracker.All() {
			if e.adaptor.IsTrusted(nodeID) {
				txSetCounts[proposal.TxSet]++
			}
		}

		bestID, _ := mostPopularTxSet(txSetCounts)

		var err error
		txSet, err = e.adaptor.GetTxSet(bestID)
		if err != nil {
			return
		}
	}

	// prevRoundTime must exclude LCL build time (rippled ConsensusParms.h:
	// "Does not include the time to build the LCL"); capture both durations
	// before the off-lock apply so the converge-percent divisor and abandon
	// clamp track convergence, not the apply.
	roundTime := e.now().Sub(e.roundStartTime)
	roundDuration := e.now().Sub(e.state.StartTime)

	// Apply the LCL off e.mu, mirroring rippled onAccept→addJob(jtACCEPT)
	// ("no lock is held during this job"). Snapshot every build input and
	// freeze the round (PhaseAccepted + buildInProgress) under the lock first:
	// concurrent OnProposal/OnValidation/OnTxSet during the unlocked apply then
	// buffer for the NEXT round instead of mutating this one, and the consensus
	// goroutine parks its round-driving until the commit tail runs.
	prevLedger := e.prevLedger
	closeTimeCorrect := e.closeTime.haveConsensus
	e.buildInProgress = true
	e.setPhase(consensus.PhaseAccepted)

	e.mu.Unlock()
	newLedger, err := e.adaptor.BuildLedger(prevLedger, txSet, closeTime, closeTimeCorrect)
	if err == nil {
		if err = e.adaptor.ValidateLedger(newLedger); err == nil {
			err = e.adaptor.StoreLedger(newLedger)
		}
	}
	e.mu.Lock()
	e.buildInProgress = false

	if err != nil {
		// Build/validate/store failed off-lock; unwind to Establish so the next
		// heartbeat retries (matches the pre-offload early-return).
		e.setPhase(consensus.PhaseEstablish)
		return
	}

	parentID := prevLedger.ID()
	parentClose := prevLedger.CloseTime()
	newID := newLedger.ID()
	txSetID := txSet.ID()
	slog.Info("ledger built",
		"t", "consensus",
		"event", "ledger-built",
		"seq", newLedger.Seq(),
		"hash", fmt.Sprintf("%x", newID[:8]),
		"parent_seq", prevLedger.Seq(),
		"parent_hash", fmt.Sprintf("%x", parentID[:8]),
		"parent_ct_xrpl", parentClose.Unix()-protocol.RippleEpochUnix,
		"close_time_xrpl", closeTime.Unix()-protocol.RippleEpochUnix,
		"close_time_correct", closeTimeCorrect,
		"resolution_s", int(resolution.Seconds()),
		"tx_set", fmt.Sprintf("%x", txSetID[:8]),
		"tx_count", txSet.Size(),
		"result", result.String(),
		"mode", e.mode.String(),
	)

	e.eventBus.Publish(&consensus.ConsensusReachedEvent{
		Round:     e.state.Round,
		TxSet:     txSet.ID(),
		CloseTime: closeTime,
		Proposers: e.proposalTracker.Count(),
		Result:    result,
		// StartTime is wall-clock (see startRoundLocked); paired with e.now()
		// and captured before the off-lock build, like prevRoundTime.
		Duration:  roundDuration,
		Timestamp: e.adaptor.Now(),
	})

	// Emission gate (rippled RCLConsensus.cpp:591-594):
	// validating && !consensusFail && canValidateSeq.
	//   consensusFail = MovedOn ONLY — Expired (hard timeout) still emits, and
	//     peers form quorum on the timeout-built ledger. Lumping Timeout in
	//     with MovedOn silently bowed us out of every timed-out round (#451).
	//   canValidateSeq prevents a second validation for an already-validated
	//     seq (a divergent close + reacquire would flag us Conflicting, #401).
	// Mode is intentionally NOT gated: rippled emits regardless of mode; the
	// Full flag (from mode==ModeProposing) controls whether peers count it
	// toward quorum. Partials in non-proposing modes keep us visible as a
	// liveness signal without affecting quorum; suppressing emission in
	// wrongLedger (the old behaviour) caused permanent quorum stalls (#451).
	// ResultFail is a go-xrpl sentinel mapping to the MovedOn suppress class.
	consensusFail := result == consensus.ResultMovedOn || result == consensus.ResultFail
	isValidator := e.adaptor.IsValidator()
	canValidate := e.peekCanValidateSeqLocked(newLedger.Seq())
	// isCompatible suppresses emission when the build is on a side chain (not
	// just ahead of validated on the same chain). Replaces the coarse
	// wrongLedger-mode gate that blocked the ahead-but-compatible case (#451)
	// while still preventing side-chain emits (#401).
	compatible := e.isBuildCompatibleWithValidatedLocked(newLedger)
	willEmit := isValidator && !consensusFail && canValidate && compatible

	newLedgerID := newLedger.ID()
	hashShort := fmt.Sprintf("%x", newLedgerID[:8])
	slog.Info("validation gate",
		"t", "consensus",
		"event", "validate-gate",
		"seq", newLedger.Seq(),
		"hash", hashShort,
		"result", result.String(),
		"is_validator", isValidator,
		"consensus_fail", consensusFail,
		"wrong_lcl", e.mode == consensus.ModeWrongLedger,
		"compatible", compatible,
		"can_validate_seq", canValidate,
		"our_last_validated_seq", e.ourLastValidatedSeq,
		"mode", e.mode.String(),
		"decision", emitDecision(willEmit, isValidator, consensusFail, canValidate, compatible),
	)

	if willEmit {
		e.sendValidation(newLedger)
	}

	validations := e.proposalTracker.ValidationsFor(newLedger.ID())

	e.adaptor.OnConsensusReached(newLedger, validations, roundTime)

	e.eventBus.Publish(&consensus.LedgerAcceptedEvent{
		LedgerID:    newLedger.ID(),
		LedgerSeq:   newLedger.Seq(),
		TxCount:     txSet.Size(),
		CloseTime:   closeTime,
		Validations: len(validations),
		Timestamp:   e.adaptor.Now(),
	})

	// Adjust our clock toward the network's close-time average.
	if e.mode == consensus.ModeProposing || e.mode == consensus.ModeObserving {
		e.adaptor.AdjustCloseTime(e.state.CloseTimes)
	}

	// Refresh the tracker's trusted set + quorum each accept (amendments /
	// neg-UNL can mutate the UNL across boundaries), and advance the minSeq
	// floor so far-stale validations are rejected at Add() not every pass.
	if e.validationTracker != nil {
		e.validationTracker.SetTrusted(e.adaptor.GetTrustedValidators())
		e.validationTracker.SetQuorum(e.adaptor.GetQuorum())
		// Pull the negative-UNL from the accepted ledger so disabled
		// validators are excluded from quorum.
		e.validationTracker.SetNegativeUNL(e.adaptor.GetNegativeUNL())
		if newLedger.Seq() > 128 {
			// Keep a small history window so late validations for the
			// just-accepted ledger still count.
			e.validationTracker.SetMinSeq(newLedger.Seq() - 128)
		}
	}

	// Track round time for convergePercent calculation
	e.prevRoundTime = roundTime

	// Track trusted proposer count for peer pressure in next round
	e.prevProposers = e.proposalTracker.CountTrusted(e.adaptor.IsTrusted)
	// Publish to the lock-free mirror for GetLastCloseInfo.
	e.storeLastCloseLocked()

	// Update state for next round
	e.prevLedger = newLedger
	e.proposalTracker.ResetValidations()
	e.consensusCount++

	// Phase is already PhaseAccepted (set before the off-lock apply).

	// Auto-advance only in Full mode; otherwise the router re-adopts until
	// caught up and checkAndStartRound takes over.
	if e.adaptor.GetOperatingMode() == consensus.OpModeFull {
		// Preferred-LCL jump: retarget prev to a different preferred LCL we
		// hold locally to skip a handleWrongLedger detour; acquire via
		// handleWrongLedger when not cached.
		nextPrev := newLedger
		if e.validationTracker != nil {
			candidateID, candidateSeq, ok := e.validationTracker.GetPreferred(newLedger.Seq())
			if !ok {
				candidateID, candidateSeq, ok = e.validationTracker.PreferredFromValidations(newLedger.Seq())
			}
			localID := newLedger.ID()
			if ok && candidateID != localID && candidateSeq >= newLedger.Seq() {
				if cached, err := e.adaptor.GetLedger(candidateID); err == nil && cached != nil {
					localBytes := localID
					slog.Info("preferred LCL differs; jumping prev to cached ledger",
						"t", "consensus",
						"event", "preferred-lcl-jump-cached",
						"local_seq", newLedger.Seq(),
						"local_hash", fmt.Sprintf("%x", localBytes[:8]),
						"preferred_seq", candidateSeq,
						"preferred_hash", fmt.Sprintf("%x", candidateID[:8]),
					)
					nextPrev = cached
					e.prevLedger = cached
				} else {
					localBytes := localID
					slog.Info("preferred LCL differs; routing through handleWrongLedger (acquire)",
						"t", "consensus",
						"event", "preferred-lcl-jump-acquire",
						"local_seq", newLedger.Seq(),
						"local_hash", fmt.Sprintf("%x", localBytes[:8]),
						"preferred_seq", candidateSeq,
						"preferred_hash", fmt.Sprintf("%x", candidateID[:8]),
					)
					e.handleWrongLedger(candidateID, nil)
					return
				}
			}
		}

		// Auto-advance.
		proposing := e.adaptor.IsValidator()
		nextRound := consensus.RoundID{
			Seq:        nextPrev.Seq() + 1,
			ParentHash: nextPrev.ID(),
		}
		e.startRoundLocked(nextRound, proposing, false)
	}
}

// updateCloseTimePosition tallies close-time votes, applies avalanche
// thresholds, and bumps our proposal's close time to consensus.
func (e *Engine) updateCloseTimePosition() {
	resolution := e.adaptor.CloseTimeResolution()

	// Tally close-time votes from trusted proposals, rounded via roundCloseTime.
	closeTimeVotes := make(map[time.Time]int)
	participants := 0
	for nodeID, proposal := range e.proposalTracker.All() {
		if e.adaptor.IsTrusted(nodeID) {
			rounded := roundCloseTime(proposal.CloseTime, resolution)
			closeTimeVotes[rounded]++
			participants++
		}
	}

	if participants == 0 {
		e.closeTime.haveConsensus = true // trivially
		return
	}

	// Add our own vote if proposing
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil {
		ourRounded := roundCloseTime(e.state.OurPosition.CloseTime, resolution)
		closeTimeVotes[ourRounded]++
		participants++
	}

	neededWeight := e.closeTime.neededWeight(e.convergePercent(), e.parms)
	threshVote := participantsNeeded(participants, neededWeight)
	threshConsensus := participantsNeeded(participants, 75) // avCT_CONSENSUS_PCT

	consensusCloseTime, winningVotes, haveWinner := mostVotedAscending(closeTimeVotes, threshVote)
	e.closeTime.haveConsensus = haveWinner && winningVotes >= threshConsensus

	votesSummary := summarizeCloseTimeVotes(closeTimeVotes)
	var consensusCT int64
	if !consensusCloseTime.IsZero() {
		consensusCT = consensusCloseTime.Unix() - protocol.RippleEpochUnix
	}
	var ourPosCT int64
	var ourPosSeq uint32
	if e.state.OurPosition != nil {
		ourPosCT = e.state.OurPosition.CloseTime.Unix() - protocol.RippleEpochUnix
		ourPosSeq = e.state.OurPosition.Position
	}
	slog.Debug("close-time avalanche",
		"t", "consensus",
		"event", "ct-avalanche",
		"seq", e.state.Round.Seq,
		"mode", e.mode.String(),
		"converge_pct", e.convergePercent(),
		"avalanche_state", e.closeTime.stateName(),
		"needed_weight", neededWeight,
		"thresh_vote", threshVote,
		"thresh_consensus", threshConsensus,
		"participants", participants,
		"have_consensus", e.closeTime.haveConsensus,
		"consensus_ct_xrpl", consensusCT,
		"our_pos_ct_xrpl", ourPosCT,
		"our_pos_seq", ourPosSeq,
		"votes", votesSummary,
	)

	// Update our proposal if close time changed
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil && !consensusCloseTime.IsZero() {
		ourRounded := roundCloseTime(e.state.OurPosition.CloseTime, resolution)
		if consensusCloseTime != ourRounded {
			oldCT := e.state.OurPosition.CloseTime.Unix() - protocol.RippleEpochUnix
			e.state.OurPosition.CloseTime = consensusCloseTime
			e.state.OurPosition.Position++
			e.state.OurPosition.Timestamp = e.adaptor.Now()
			if err := e.adaptor.SignProposal(e.state.OurPosition); err == nil {
				e.enqueueProposalBroadcastLocked(e.state.OurPosition)
			}
			slog.Info("our close-time bumped",
				"t", "consensus",
				"event", "ct-bump",
				"seq", e.state.Round.Seq,
				"old_ct_xrpl", oldCT,
				"new_ct_xrpl", consensusCT,
				"new_pos_seq", e.state.OurPosition.Position,
			)
		}
	}
}

// convergePercent returns establish-phase progress as a percentage of the
// previous round time (min 5s).
func (e *Engine) convergePercent() int {
	elapsed := e.now().Sub(e.roundStartTime)
	prevRound := max(e.prevRoundTime, avMinConsensusTime)
	return int(elapsed * 100 / prevRound)
}

// determineCloseTime returns the consensus close time: our converged
// position if set, else the most popular peer close time rounded to
// resolution (observers).
func (e *Engine) determineCloseTime() time.Time {
	// Our position is already rounded by updateCloseTimePosition.
	if e.state.OurPosition != nil && !e.state.OurPosition.CloseTime.IsZero() {
		return e.state.OurPosition.CloseTime
	}

	resolution := e.adaptor.CloseTimeResolution()

	// Observers: CloseTimes.Peers holds raw times; round before voting to
	// match rippled's asCloseTime.
	if len(e.state.CloseTimes.Peers) > 0 {
		roundedVotes := make(map[time.Time]int)
		for t, count := range e.state.CloseTimes.Peers {
			rounded := roundCloseTime(t, resolution)
			roundedVotes[rounded] += count
		}

		// Largest time on a tie, matching the proposing path — a different
		// pick would fork.
		bestTime, bestCount, _ := mostVotedAscending(roundedVotes, 0)
		if bestCount > 0 {
			return bestTime
		}
	}

	return roundCloseTime(e.state.CloseTimes.Self, resolution)
}

// peekCanValidateSeqLocked is the non-mutating SeqEnforcer predicate.
// Caller holds e.mu read.
func (e *Engine) peekCanValidateSeqLocked(seq uint32) bool {
	floor := e.ourLastValidatedSeq
	if !e.ourLastValidatedTime.IsZero() &&
		e.adaptor.Now().Sub(e.ourLastValidatedTime) > validationSetExpires {
		floor = 0
	}
	return seq > floor
}

// tryAdvanceValidatedSeqLocked is the mutating SeqEnforcer: idle-reset
// then reject-or-bump. The floor commits before signing so a sign failure
// still consumes the seq. Caller holds e.mu write.
func (e *Engine) tryAdvanceValidatedSeqLocked(seq uint32) bool {
	now := e.adaptor.Now()
	if !e.ourLastValidatedTime.IsZero() &&
		now.Sub(e.ourLastValidatedTime) > validationSetExpires {
		e.ourLastValidatedSeq = 0
	}
	if seq <= e.ourLastValidatedSeq {
		return false
	}
	e.ourLastValidatedSeq = seq
	e.ourLastValidatedTime = now
	return true
}

// sendValidation builds and broadcasts a validation. The Full flag (set
// from mode==ModeProposing) is what makes peers count it toward quorum;
// partials from non-proposing modes are accepted but don't count.
func (e *Engine) sendValidation(ledger consensus.Ledger) {
	// SeqEnforcer guard + bump; defensive so direct test callers can't bypass.
	if !e.tryAdvanceValidatedSeqLocked(ledger.Seq()) {
		return
	}

	nodeID, err := e.adaptor.GetValidatorKey()
	if err != nil {
		return
	}

	full := e.mode == consensus.ModeProposing

	// SignTime under a monotonic floor: a regressing adaptor clock would emit
	// a stale SignTime peers reject, so bump to lastSignTime+1s. SeenTime
	// mirrors SignTime.
	signTime := e.adaptor.Now()
	if !e.lastSignTime.IsZero() && !signTime.After(e.lastSignTime) {
		signTime = e.lastSignTime.Add(1 * time.Second)
	}
	e.lastSignTime = signTime

	validation := &consensus.Validation{
		LedgerID:  ledger.ID(),
		LedgerSeq: ledger.Seq(),
		NodeID:    nodeID,
		SignTime:  signTime,
		SeenTime:  signTime,
		Full:      full,
		// load_fee (sfLoadFee); zero = no load info, serializer omits it.
		LoadFee: e.adaptor.GetLoadFee(),
	}

	// Cookie / ServerVersion are HardenedValidations-only: pre-HV peers reject
	// validations carrying them (their sig preimage omits the fields). Cookie
	// on every HV validation; ServerVersion only on voting ledgers.
	if e.adaptor.IsFeatureEnabled("HardenedValidations") {
		cookie := e.adaptor.GetCookie()
		if cookie == 0 {
			slog.Warn("sendValidation: cookie is zero under HardenedValidations — adaptor must generate one at boot; emitting without cookie")
		}
		validation.Cookie = cookie

		if protocol.IsVotingLedger(ledger.Seq()) {
			serverVersion := e.adaptor.GetServerVersion()
			if serverVersion == 0 {
				slog.Warn("sendValidation: serverVersion is zero on voting ledger under HardenedValidations — adaptor must advertise a build tag; emitting without serverVersion")
			}
			validation.ServerVersion = serverVersion
		}
	}

	// Fee + amendment votes only on voting (flag) ledgers; emitting every
	// ledger inflates bandwidth ~256× and confuses peer aggregators.
	if protocol.IsVotingLedger(ledger.Seq()) {
		// Fee vote: AMOUNT triple under post-XRPFees rules, legacy UINT triple
		// otherwise (never both). Zero = no vote, serializer omits.
		if fv := e.adaptor.GetFeeVote(); fv.BaseFee != 0 || fv.ReserveBase != 0 || fv.ReserveIncrement != 0 {
			if fv.PostXRPFees {
				validation.BaseFeeDrops = fv.BaseFee
				validation.ReserveBaseDrops = fv.ReserveBase
				validation.ReserveIncrementDrops = fv.ReserveIncrement
			} else {
				validation.BaseFee = fv.BaseFee
				validation.ReserveBase = uint32(fv.ReserveBase)
				validation.ReserveIncrement = uint32(fv.ReserveIncrement)
			}
		}

		// Amendment vote (flag ledgers only); nil when there's no vote to cast.
		validation.Amendments = e.adaptor.GetAmendmentVote()
	}

	// Tie to the converged tx-set so peers tie-break concurrent same-seq
	// ledgers; only set when we produced a proposal (observers omit it).
	if e.ourTxSet != nil {
		setID := e.ourTxSet.ID()
		copy(validation.ConsensusHash[:], setID[:])
	}

	// ValidatedHash is HardenedValidations-only (pre-HV peers reject it as
	// malformed). Skip when we haven't crossed quorum (zero hash).
	if e.adaptor.IsFeatureEnabled("HardenedValidations") {
		if vh := e.adaptor.GetValidatedLedgerHash(); vh != (consensus.LedgerID{}) {
			copy(validation.ValidatedHash[:], vh[:])
		}
	}

	if err := e.adaptor.SignValidation(validation); err != nil {
		slog.Warn("validation sign failed",
			"t", "consensus",
			"event", "validate-sign-fail",
			"seq", ledger.Seq(),
			"error", err,
		)
		return
	}

	ledgerID := ledger.ID()
	slog.Info("validation emitted",
		"t", "consensus",
		"event", "validate-emit",
		"seq", ledger.Seq(),
		"hash", fmt.Sprintf("%x", ledgerID[:8]),
		"full", full,
		"sign_time_xrpl", signTime.Unix()-protocol.RippleEpochUnix,
	)

	e.enqueueValidationBroadcastLocked(validation)

	// Feed our own validation into the tracker. Partials steer our trie but
	// don't count toward quorum (Full filter); a 1-validator standalone is
	// always proposing, so Full crosses immediately.
	if e.validationTracker != nil {
		e.validationTracker.Add(validation)
	}
}

// roundCloseTime rounds to the nearest multiple of resolution (up at the
// midpoint). Truncates sub-second precision first so nanosecond-skewed
// validators round identically; does the modulo in XRPL-epoch space to
// match rippled byte-for-byte.
func roundCloseTime(closeTime time.Time, resolution time.Duration) time.Time {
	if closeTime.IsZero() {
		return closeTime
	}
	resSec := int64(resolution.Seconds())
	if resSec <= 0 {
		return closeTime
	}
	xrplSec := closeTime.Unix() - protocol.RippleEpochUnix
	xrplSec += resSec / 2
	xrplSec -= xrplSec % resSec
	return time.Unix(xrplSec+protocol.RippleEpochUnix, 0).UTC()
}

// emitDecision labels which arm of the validation gate fired. wrongLedger
// is intentionally NOT a skip reason (rippled emits a partial there, #451).
func emitDecision(emit, isValidator, consensusFail, canValidate, compatible bool) string {
	if emit {
		return "emit"
	}
	if !isValidator {
		return "skip:not-validator"
	}
	if consensusFail {
		return "skip:consensus-fail"
	}
	if !canValidate {
		return "skip:already-validated-seq"
	}
	if !compatible {
		return "skip:incompatible-with-validated"
	}
	return "skip:unknown"
}

// effCloseTime rounds to resolution, then floors at priorCloseTime + 1s.
func effCloseTime(closeTime time.Time, resolution time.Duration, priorCloseTime time.Time) time.Time {
	if closeTime.IsZero() {
		return closeTime
	}
	rounded := roundCloseTime(closeTime, resolution)
	minTime := priorCloseTime.Add(time.Second)
	if rounded.Before(minTime) {
		return minTime
	}
	return rounded
}
