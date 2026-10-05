package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/shamap"
)

var (
	// ErrRecoveryStorageInvalidLedger means that a recovery candidate is not a
	// detached, immutable closed ledger suitable for durable staging.
	ErrRecoveryStorageInvalidLedger = errors.New("recovery storage candidate must be an immutable closed ledger")
	// ErrRecoveryStorageRetired means that a stage no longer owns its durable
	// admission lease and cannot be installed again.
	ErrRecoveryStorageRetired = errors.New("recovery storage stage is retired")
	// ErrRecoveryStorageActivationStale means that the recovery worker no longer
	// owns the fault generation for which it is trying to install nodes.
	ErrRecoveryStorageActivationStale = errors.New("recovery storage activation is stale")
	// ErrRecoveryStorageGateInactive means that activation was attempted outside
	// the in-flight blocked replay-fault verification callback.
	ErrRecoveryStorageGateInactive = errors.New("recovery storage replay gate is not active")
	// ErrRecoveryStorageWrongService means that a stage belongs to another
	// ledger service and cannot be installed here.
	ErrRecoveryStorageWrongService = errors.New("recovery storage stage belongs to another service")
	// ErrRecoveryStorageFrontierNotFound means that a staged detached frontier
	// has no durable ledger header yet.
	ErrRecoveryStorageFrontierNotFound = errors.New("replay recovery frontier not found")
)

// RecoveryStorageActivation identifies the replay-fault verification attempt
// that owns a recovery storage install. Generation is the value captured when
// the fault store entered its in-flight verification state.
//
// The token never clears the replay gate. The recovery worker must complete its
// durable journal and normal eligibility checks before it archives the fault
// and publishes the canonical history/frontier.
type RecoveryStorageActivation struct {
	FaultID    string
	Generation uint64
}

// RecoveryStorageStage owns a verified detached recovery ledger and the durable
// generation admission that protects all nodes reachable from it. A stage is
// intentionally absent from service caches, history, indexes, completion sets,
// and the persisted validated tip until the recovery worker performs its final
// canonical handoff.
type RecoveryStorageStage struct {
	service   *Service
	candidate *ledger.Ledger

	mu               sync.Mutex
	installed        bool
	retired          bool
	releaseAdmission func()
}

// StageRecoveryLedger verifies and durably stages candidate's state and
// transaction trees while keeping it detached from service publication. The
// admission lease remains until Retire so online deletion cannot reclaim it.
//
// A node-store-less service still gets detached verification and lifecycle
// protection; Install is then a durable-storage no-op because there is no
// backing store to write.
func (s *Service) StageRecoveryLedger(ctx context.Context, candidate *ledger.Ledger) (*RecoveryStorageStage, error) {
	if s == nil {
		return nil, errors.New("recovery storage: nil service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRecoveryStorageCandidate(candidate); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	release, err := s.AcquireStateAdmission(ctx)
	if err != nil {
		return nil, fmt.Errorf("admit recovery storage: %w", err)
	}
	stage := &RecoveryStorageStage{
		service:          s,
		candidate:        candidate,
		releaseAdmission: release,
	}
	if err := stage.Install(ctx); err != nil {
		_ = stage.Retire()
		return nil, err
	}
	return stage, nil
}

// StageReplayRecoveryFrontier is the default durable frontier store used by a
// recovery worker. It writes only detached nodes and keeps them out of service
// history until ActivateRecoveryStorage performs the canonical handoff.
func (s *Service) StageReplayRecoveryFrontier(
	ctx context.Context,
	faultID string,
	generation uint64,
	candidate *ledger.Ledger,
) error {
	if s == nil {
		return errors.New("recovery storage: nil service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.validateRecoveryFrontierAdmission(faultID, generation); err != nil {
		return err
	}
	stage, err := s.StageRecoveryLedger(ctx, candidate)
	if err != nil {
		return err
	}
	return stage.Retire()
}

// LoadReplayRecoveryFrontier reconstructs a staged ledger directly from the
// node store. It never consults service history or cache state, and performs a
// fresh durable tree walk before returning the detached ledger.
func (s *Service) LoadReplayRecoveryFrontier(
	ctx context.Context,
	faultID string,
	generation uint64,
	expected header.LedgerHeader,
) (*ledger.Ledger, error) {
	if s == nil {
		return nil, errors.New("recovery storage: nil service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if expected.LedgerIndex == 0 || expected.Hash == ([32]byte{}) || header.CalculateHash(expected) != expected.Hash {
		return nil, fmt.Errorf("%w: frontier header identity is invalid", ErrRecoveryStorageInvalidLedger)
	}
	if err := s.validateRecoveryFrontierAdmission(faultID, generation); err != nil {
		return nil, err
	}
	status := s.ReplayFaultStatus()
	journal := status.Recovery
	if journal.Strategy != replayfault.RecoveryStrategyVerifiedPivot ||
		journal.FrontierSequence != expected.LedgerIndex || journal.FrontierHash != expected.Hash {
		return nil, fmt.Errorf("%w: journal does not mark the expected frontier as locally executed", ErrRecoveryStorageFrontierNotFound)
	}
	if s.nodeStore == nil || s.shamapFamily == nil {
		return nil, ErrRecoveryStorageFrontierNotFound
	}
	release, err := s.AcquireStateAdmission(ctx)
	if err != nil {
		return nil, fmt.Errorf("admit replay recovery frontier load: %w", err)
	}
	defer release()
	loaded, err := s.loadStoredLedgerByHash(ctx, expected.Hash)
	if err != nil {
		return nil, fmt.Errorf("load replay recovery frontier: %w", err)
	}
	if loaded == nil {
		return nil, ErrRecoveryStorageFrontierNotFound
	}
	if loaded.Sequence() != expected.LedgerIndex || loaded.Hash() != expected.Hash || !loaded.IsClosed() {
		return nil, fmt.Errorf("%w: durable frontier identity does not match its journal", ErrRecoveryStorageInvalidLedger)
	}
	if err := s.verifyRecoveryDetachedLedger(ctx, loaded); err != nil {
		return nil, fmt.Errorf("verify replay recovery frontier: %w", err)
	}
	if err := verifyRecoveryStorageHeader(ctx, loaded); err != nil {
		return nil, err
	}
	return loaded, nil
}

func (s *Service) validateRecoveryFrontierAdmission(faultID string, generation uint64) error {
	if faultID == "" || generation == 0 {
		return fmt.Errorf("%w: fault ID and non-zero generation are required", ErrRecoveryStorageActivationStale)
	}
	status := s.ReplayFaultStatus()
	if !status.Blocked || status.Fault == nil || !status.Recovery.InFlight {
		return ErrRecoveryStorageGateInactive
	}
	if status.Fault.ID != faultID || status.Recovery.ID != faultID || status.Recovery.Generation != generation {
		return fmt.Errorf("%w: active fault generation does not match", ErrRecoveryStorageActivationStale)
	}
	return nil
}

// AcquireRecoveryRetention reconstructs the durable admission held for a
// replay-fault journal entry. The recovery worker passes the sequence directly
// below the fault target, so a restarted worker cannot pin an unrelated
// generation. The caller owns the returned release function and must retain it
// until the journal has either abandoned the recovery or completed canonical
// handoff.
func (s *Service) AcquireRecoveryRetention(ctx context.Context, faultID string, sequence uint32) (func(), error) {
	if s == nil {
		return nil, errors.New("recovery storage: nil service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	status := s.ReplayFaultStatus()
	if !status.Blocked || status.Fault == nil {
		return nil, ErrRecoveryStorageGateInactive
	}
	if status.Fault.Sequence == 0 {
		return nil, fmt.Errorf("%w: fault sequence is invalid", ErrRecoveryStorageActivationStale)
	}
	retentionSequence := status.Fault.Sequence - 1
	if faultID == "" || sequence != retentionSequence || status.Fault.ID != faultID {
		return nil, fmt.Errorf("%w: fault identity does not match retention request", ErrRecoveryStorageActivationStale)
	}
	release, err := s.AcquireStateAdmission(ctx)
	if err != nil {
		return nil, fmt.Errorf("admit replay recovery retention: %w", err)
	}
	return release, nil
}

// Install re-verifies the candidate and writes only its detached SHAMap nodes
// and header. It is idempotent and never updates canonical service state.
func (r *RecoveryStorageStage) Install(ctx context.Context) error {
	if r == nil {
		return ErrRecoveryStorageRetired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired {
		return ErrRecoveryStorageRetired
	}
	if r.installed {
		return nil
	}
	if r.service.nodeStore != nil {
		if err := r.service.persistToNodeStore(ctx, r.candidate, r.candidate.Sequence()); err != nil {
			return fmt.Errorf("persist recovery storage candidate: %w", err)
		}
	}
	if err := r.service.verifyRecoveryDetachedLedger(ctx, r.candidate); err != nil {
		return fmt.Errorf("verify recovery storage candidate: %w", err)
	}
	if err := verifyRecoveryStorageHeader(ctx, r.candidate); err != nil {
		return err
	}
	r.installed = true
	return nil
}

// Retire releases the durable admission lease. It never deletes content-
// addressed nodes or publishes the detached candidate.
func (r *RecoveryStorageStage) Retire() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.retired {
		r.mu.Unlock()
		return nil
	}
	r.retired = true
	release := r.releaseAdmission
	r.releaseAdmission = nil
	r.mu.Unlock()
	if release != nil {
		release()
	}
	return nil
}

// ActivateRecoveryStorage authorizes one exact in-flight replay-fault
// verification attempt, then installs the fully replayed successor as the
// canonical closed/open/validated frontier. The replay gate remains blocked;
// the recovery journal archives and clears it only after this method and the
// coordinator handoff return successfully. A stale token is rejected before
// any canonical metadata, cache, index, completion set, or validated tip is
// changed.
func (s *Service) ActivateRecoveryStorage(
	ctx context.Context,
	stage *RecoveryStorageStage,
	activation RecoveryStorageActivation,
) error {
	if s == nil {
		return errors.New("recovery storage: nil service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if stage == nil {
		return ErrRecoveryStorageRetired
	}
	if stage.service != s {
		return ErrRecoveryStorageWrongService
	}
	frontier := stage.candidate.Header()
	if err := s.validateRecoveryStorageActivation(activation, &frontier); err != nil {
		return err
	}
	if err := stage.Install(ctx); err != nil {
		return err
	}
	var afterPublish func()
	if err := s.replayFaults.WithRecoveryActivation(activation.FaultID, activation.Generation, func() error {
		if err := s.validateRecoveryStorageActivation(activation, &frontier); err != nil {
			return err
		}
		var err error
		afterPublish, err = s.installRecoveryFrontier(ctx, stage.candidate)
		return err
	}); err != nil {
		return err
	}
	if afterPublish != nil {
		afterPublish()
	}
	return nil
}

// installRecoveryFrontier publishes a fully replayed successor after its
// detached nodes have been admitted. The replay-fault store remains blocked;
// the caller clears it only after this method returns and its own durable
// handoff checkpoint succeeds.
func (s *Service) installRecoveryFrontier(ctx context.Context, candidate *ledger.Ledger) (func(), error) {
	if err := validateRecoveryStorageCandidate(candidate); err != nil {
		return nil, err
	}
	if err := verifyRecoveryStorageHeader(ctx, candidate); err != nil {
		return nil, err
	}
	if !candidate.IsValidated() {
		if err := candidate.SetValidated(); err != nil {
			return nil, fmt.Errorf("validate recovery frontier: %w", err)
		}
	}

	stagedResults, err := stageTransactionResultsContext(ctx, candidate, candidate.Sequence(), candidate.Hash())
	if err != nil {
		return nil, fmt.Errorf("collect recovery frontier transaction results: %w", err)
	}
	newOpen, err := ledger.NewOpen(candidate, time.Now())
	if err != nil {
		return nil, fmt.Errorf("prepare recovery frontier open ledger: %w", err)
	}
	// Reject an obsolete or conflicting candidate before touching the durable
	// validated tip. The worker normally serializes this check, but keeping it
	// here makes a direct stale handoff side-effect free.
	s.mu.RLock()
	if s.closedLedger != nil {
		if candidate.Sequence() < s.closedLedger.Sequence() {
			s.mu.RUnlock()
			return nil, errors.New("recovery frontier is older than the closed ledger")
		}
		if candidate.Sequence() == s.closedLedger.Sequence() && candidate.Hash() != s.closedLedger.Hash() {
			s.mu.RUnlock()
			return nil, errors.New("recovery frontier conflicts with the closed ledger")
		}
	}
	if s.validatedLedger != nil && candidate.Sequence() < s.validatedLedger.Sequence() {
		s.mu.RUnlock()
		return nil, errors.New("recovery frontier is older than the validated ledger")
	}
	s.mu.RUnlock()
	if err := s.persistValidatedLedger(ctx, candidate, true); err != nil {
		return nil, fmt.Errorf("persist recovery frontier: %w", err)
	}
	if err := s.lockOpenLedgerIfRunning(openLedgerPreferredSwitch); err != nil {
		return nil, err
	}

	var notification validatedLedgerNotification
	s.mu.Lock()
	s.historyComponent.mu.Lock()
	previousValidated := s.validatedLedger
	if s.closedLedger != nil {
		if candidate.Sequence() < s.closedLedger.Sequence() {
			s.historyComponent.mu.Unlock()
			s.mu.Unlock()
			s.openLedgerMu.Unlock()
			return nil, errors.New("recovery frontier is older than the closed ledger")
		}
		if candidate.Sequence() == s.closedLedger.Sequence() && candidate.Hash() != s.closedLedger.Hash() {
			s.historyComponent.mu.Unlock()
			s.mu.Unlock()
			s.openLedgerMu.Unlock()
			return nil, errors.New("recovery frontier conflicts with the closed ledger")
		}
	}
	if s.validatedLedger != nil && candidate.Sequence() < s.validatedLedger.Sequence() {
		s.historyComponent.mu.Unlock()
		s.mu.Unlock()
		s.openLedgerMu.Unlock()
		return nil, errors.New("recovery frontier is older than the validated ledger")
	}
	if err := s.acceptPreferredOpenLedgerLocked(candidate); err != nil {
		s.historyComponent.mu.Unlock()
		s.mu.Unlock()
		s.openLedgerMu.Unlock()
		return nil, fmt.Errorf("install recovery frontier open ledger: %w", err)
	}
	s.fixMismatchLocked(candidate)
	// Reuse the normal fork cleanup boundary. It removes only history and
	// completion state at or beyond the adopted sequence, preserving verified
	// history below the recovery frontier.
	s.purgeConflictingHistoryLocked(candidate)
	s.invalidateAbandonedRecoveryCachesLocked(candidate)
	s.putHistoryLocked(candidate)
	s.cachePersistedLedgerLocked(candidate)
	s.closedLedger = candidate
	s.openLedger = newOpen
	s.validatedLedger = candidate
	s.validatedSignTime = candidate.CloseTime()
	s.replayVerifiedHash = candidate.Hash()
	s.confirmFastLoadLocked(candidate.Sequence(), candidate.Hash())
	s.completeInitialSyncLocked()
	s.tickLoadFeeLocked()
	s.commitTransactionResultsLocked(stagedResults)
	event := s.validatedLedgerEventLocked(candidate)
	notification = s.validatedLedgerNotificationLocked(previousValidated)
	s.dispatchLedgerEvent(event)
	s.historyComponent.mu.Unlock()
	s.mu.Unlock()
	s.openLedgerMu.Unlock()
	return func() {
		notification.notify()
		s.syncTable(candidate)
	}, nil
}

func (s *Service) invalidateAbandonedRecoveryCachesLocked(candidate *ledger.Ledger) {
	cutoff := candidate.Sequence()
	for hash, cached := range s.persistedLedgers {
		if cached == nil || cached.Sequence() < cutoff || hash == candidate.Hash() {
			continue
		}
		delete(s.persistedLedgers, hash)
	}
	if len(s.persistedLedgerFIFO) == 0 {
		return
	}
	filtered := s.persistedLedgerFIFO[:0]
	for _, hash := range s.persistedLedgerFIFO {
		if _, ok := s.persistedLedgers[hash]; ok {
			filtered = append(filtered, hash)
		}
	}
	s.persistedLedgerFIFO = filtered
}

// RecoveryFrontierReady acknowledges an already-installed recovery frontier to
// the coordinator without invoking the ordinary preferred-ledger switch. The
// caller must have durably advanced the verified-pivot journal to its activating
// phase; this method independently checks the live blocked fault, current
// generation, locally replayed hash, and canonical frontiers before Handoff.
func (s *Service) RecoveryFrontierReady(h header.LedgerHeader) bool {
	if s == nil {
		return false
	}
	status := s.ReplayFaultStatus()
	return s.RecoveryFrontierReadyAt(h, RecoveryStorageActivation{
		FaultID:    status.Recovery.ID,
		Generation: status.Recovery.Generation,
	})
}

// RecoveryFrontierReadyAt is the generation-bound form used by a worker that
// retained its activation token across the network handoff.
func (s *Service) RecoveryFrontierReadyAt(h header.LedgerHeader, activation RecoveryStorageActivation) bool {
	if s == nil || h.Hash == ([32]byte{}) || header.CalculateHash(h) != h.Hash {
		return false
	}
	if err := s.validateRecoveryStorageActivation(activation, &h); err != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.replayVerifiedHash != h.Hash || s.closedLedger == nil || s.validatedLedger == nil {
		return false
	}
	return s.closedLedger.Sequence() == h.LedgerIndex &&
		s.closedLedger.Hash() == h.Hash &&
		s.validatedLedger.Sequence() == h.LedgerIndex &&
		s.validatedLedger.Hash() == h.Hash &&
		s.validatedLedger.IsValidated()
}

func (s *Service) validateRecoveryStorageActivation(activation RecoveryStorageActivation, frontier *header.LedgerHeader) error {
	if activation.FaultID == "" || activation.Generation == 0 {
		return fmt.Errorf("%w: fault ID and non-zero generation are required", ErrRecoveryStorageActivationStale)
	}
	status := s.ReplayFaultStatus()
	if !status.Blocked || status.Fault == nil || !status.Recovery.InFlight {
		return ErrRecoveryStorageGateInactive
	}
	if status.Fault.ID != activation.FaultID || status.Recovery.ID != activation.FaultID || status.Recovery.Generation != activation.Generation {
		return fmt.Errorf("%w: active fault generation does not match", ErrRecoveryStorageActivationStale)
	}
	journal := status.Recovery
	if journal.Strategy != replayfault.RecoveryStrategyVerifiedPivot || journal.Phase != replayfault.RecoveryPhaseActivating {
		return fmt.Errorf("%w: recovery journal is not an activating verified pivot", ErrRecoveryStorageActivationStale)
	}
	if frontier == nil || frontier.LedgerIndex == 0 || frontier.Hash == ([32]byte{}) ||
		journal.FrontierSequence != frontier.LedgerIndex || journal.FrontierHash != frontier.Hash {
		return fmt.Errorf("%w: recovery journal frontier does not match the candidate", ErrRecoveryStorageActivationStale)
	}
	return nil
}

func validateRecoveryStorageCandidate(candidate *ledger.Ledger) error {
	if candidate == nil || !candidate.IsClosed() || candidate.IsOpen() || !candidate.IsImmutable() {
		return ErrRecoveryStorageInvalidLedger
	}
	if candidate.Hash() == ([32]byte{}) {
		return fmt.Errorf("%w: candidate hash is empty", ErrRecoveryStorageInvalidLedger)
	}
	return nil
}

// verifyRecoveryDetachedLedger rechecks a recovery candidate from durable
// bytes. The normal detached verifier deliberately reuses the service's
// FullBelow cache for ordinary acquisition; recovery cannot trust those
// proofs because they may describe an abandoned or reclaimed frontier.
func (s *Service) verifyRecoveryDetachedLedger(ctx context.Context, candidate *ledger.Ledger) error {
	if candidate == nil {
		return errors.New("recovery storage: nil ledger")
	}
	stateMap, err := candidate.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot recovery state map: %w", err)
	}
	txMap, err := candidate.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot recovery transaction map: %w", err)
	}
	if err := s.verifyRecoveryDetachedMap(ctx, "state", stateMap); err != nil {
		return err
	}
	return s.verifyRecoveryDetachedMap(ctx, "transaction", txMap)
}

func (s *Service) verifyRecoveryDetachedMap(ctx context.Context, name string, source *shamap.SHAMap) error {
	if source == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot, err := source.SnapshotImmutableContext(ctx)
	if err != nil {
		return fmt.Errorf("snapshot recovery %s map: %w", name, err)
	}
	if snapshot.IsBacked() {
		if s == nil || s.shamapFamily == nil {
			return fmt.Errorf("recovery storage: durable family unavailable for %s map", name)
		}
		root, err := snapshot.Hash()
		if err != nil {
			return fmt.Errorf("hash recovery %s map: %w", name, err)
		}
		if root == ([32]byte{}) {
			return nil
		}
		durable, ok := s.shamapFamily.(interface {
			FetchDurable(context.Context, [32]byte) ([]byte, error)
		})
		if !ok {
			return fmt.Errorf("recovery storage: durable fetch unavailable for %s map", name)
		}
		// Leave cache nil so NewFromRootHashContext allocates a fresh cache;
		// no completeness proof from the suspect service family is reused.
		snapshot, err = shamap.NewFromRootHashContext(ctx, snapshot.Type(), root, durableAdmissionFamily{
			Family:  s.shamapFamily,
			durable: durable,
		})
		if err != nil {
			return fmt.Errorf("load durable recovery %s root: %w", name, err)
		}
	}
	return verifyRecoveryMap(ctx, name, snapshot, recoveryVerificationVisits)
}

func verifyRecoveryStorageHeader(ctx context.Context, candidate *ledger.Ledger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h := candidate.Header()
	stateRoot, err := candidate.StateMapHash()
	if err != nil {
		return fmt.Errorf("hash recovery state map: %w", err)
	}
	if stateRoot != h.AccountHash {
		return fmt.Errorf("recovery storage state root does not match ledger header: got %x, want %x", stateRoot, h.AccountHash)
	}
	txRoot, err := candidate.TxMapHash()
	if err != nil {
		return fmt.Errorf("hash recovery transaction map: %w", err)
	}
	if txRoot != h.TxHash {
		return fmt.Errorf("recovery storage transaction root does not match ledger header: got %x, want %x", txRoot, h.TxHash)
	}
	if header.CalculateHash(h) != h.Hash {
		return fmt.Errorf("recovery storage ledger header hash is invalid")
	}
	return nil
}
