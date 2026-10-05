// Package replayfault records replay failures that must stop signing and
// proposal publication until the affected state has been independently
// verified.
package replayfault

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

// Class identifies the kind of replay failure that was observed.
type Class string

const (
	MissingState          Class = "missing_state"
	CorruptState          Class = "corrupt_state"
	ExecutionDisagreement Class = "execution_disagreement"
	TransientAcquisition  Class = "transient_acquisition"
	Unclassified          Class = "unclassified"
)

// RecoveryStrategy selects the operator-approved way to resolve a replay
// fault. The exact transition path is the historical behaviour; a verified
// pivot is a separate path because it supersedes, rather than authenticates,
// the original evidence.
type RecoveryStrategy string

const (
	RecoveryStrategyExactTransition RecoveryStrategy = "exact_transition"
	RecoveryStrategyVerifiedPivot   RecoveryStrategy = "verified_pivot"
)

// RecoveryPhase is persisted before each externally visible recovery step.
// A non-terminal phase is safe to resume after a process restart while the
// replay-fault gate remains closed.
type RecoveryPhase string

const (
	RecoveryPhaseSelectingPivot  RecoveryPhase = "selecting_pivot"
	RecoveryPhaseAuthenticating  RecoveryPhase = "authenticating_evidence"
	RecoveryPhaseAcquiringPivot  RecoveryPhase = "acquiring_pivot"
	RecoveryPhaseCatchingUp      RecoveryPhase = "catching_up"
	RecoveryPhaseReadyToActivate RecoveryPhase = "ready_to_activate"
	RecoveryPhaseActivating      RecoveryPhase = "activating"
	RecoveryPhaseComplete        RecoveryPhase = "complete"
	// RecoveryPhaseExecutionFailed is terminal for the current explicit
	// admission. A process restart may observe this phase, but it must never
	// silently retry the route; a new operator admission is required.
	RecoveryPhaseExecutionFailed RecoveryPhase = "execution_failed"
	// RecoveryPhaseNeedsOperator is reserved for durable publication failures
	// that need an explicit operator decision before another attempt.
	RecoveryPhaseNeedsOperator RecoveryPhase = "needs_operator"
)

// RecoveryOutcome distinguishes direct replay verification from a successor
// selected through a newer trusted pivot.
type RecoveryOutcome string

const (
	RecoveryOutcomeResolvedByReplay  RecoveryOutcome = "resolved_by_replay"
	RecoveryOutcomeSupersededByPivot RecoveryOutcome = "superseded_by_verified_pivot"
)

// Fault is the durable description of an unresolved replay failure.
type Fault struct {
	AcquisitionAttempts int             `json:"acquisition_attempts,omitempty"`
	AcquisitionError    string          `json:"acquisition_error,omitempty"`
	RecoveryError       string          `json:"recovery_error,omitempty"`
	BlockedReason       string          `json:"blocked_reason,omitempty"`
	OperatorAction      string          `json:"operator_action,omitempty"`
	ID                  string          `json:"id"`
	Class               Class           `json:"class"`
	ParentHash          [32]byte        `json:"parent_hash"`
	TargetHash          [32]byte        `json:"target_hash"`
	Sequence            uint32          `json:"sequence"`
	Message             string          `json:"message"`
	Revision            string          `json:"revision"`
	CreatedAt           time.Time       `json:"created_at"`
	Evidence            json.RawMessage `json:"evidence,omitempty"`
	// OriginalEvidence is immutable after the fault is first recorded. Recovery
	// diagnostics may enrich Evidence, but a pivot-based recovery must retain
	// the exact observation that caused the gate.
	OriginalEvidence json.RawMessage `json:"original_evidence,omitempty"`
	// RecoveryTarget is an opaque coordinator proof for the target currently
	// staged by a verified-pivot worker. It is intentionally removed from
	// public summaries but survives a crash between proof and execution.
	RecoveryTarget json.RawMessage `json:"recovery_target,omitempty"`
	RecoveryPivot  json.RawMessage `json:"recovery_pivot,omitempty"`
	// RecoveryFrontier is the proof for the latest derived frontier whose
	// durable journal update completed. It is kept separately from the pending
	// target so a crash after selecting the next successor cannot hide the
	// previous replay parent.
	RecoveryFrontier json.RawMessage   `json:"recovery_frontier,omitempty"`
	Recovery         *RecoveryProgress `json:"recovery,omitempty"`
	Attempts         int               `json:"attempts"`
}

// RecoveryProgress describes the current or most recent explicit
// revalidation attempt and any durable blocker requiring operator action.
type RecoveryProgress struct {
	AcquisitionAttempts int              `json:"acquisition_attempts,omitempty"`
	AcquisitionError    string           `json:"acquisition_error,omitempty"`
	BlockedReason       string           `json:"blocked_reason,omitempty"`
	InFlight            bool             `json:"in_flight"`
	ID                  string           `json:"id,omitempty"`
	OperatorAction      string           `json:"operator_action,omitempty"`
	Generation          uint64           `json:"generation,omitempty"`
	Attempts            int              `json:"attempts,omitempty"`
	StartedAt           time.Time        `json:"started_at,omitempty"`
	LastError           string           `json:"last_error,omitempty"`
	Strategy            RecoveryStrategy `json:"strategy,omitempty"`
	Phase               RecoveryPhase    `json:"phase,omitempty"`
	PivotSequence       uint32           `json:"pivot_sequence,omitempty"`
	PivotHash           [32]byte         `json:"pivot_hash,omitempty"`
	SuccessorSequence   uint32           `json:"successor_sequence,omitempty"`
	SuccessorHash       [32]byte         `json:"successor_hash,omitempty"`
	FrontierSequence    uint32           `json:"frontier_sequence,omitempty"`
	FrontierHash        [32]byte         `json:"frontier_hash,omitempty"`
	// FrontierGeneration is the generation supplied to the retention layer
	// when the latest frontier was staged. UpdateRecovery advances the journal
	// generation after that stage, so restart can address the staged record
	// without weakening the CAS contract.
	FrontierGeneration    uint64          `json:"frontier_generation,omitempty"`
	RouteDigest           [32]byte        `json:"route_digest,omitempty"`
	RetentionSequence     uint32          `json:"retention_sequence,omitempty"`
	Outcome               RecoveryOutcome `json:"outcome,omitempty"`
	FailureFingerprint    string          `json:"failure_fingerprint,omitempty"`
	FailureAttempts       int             `json:"failure_attempts,omitempty"`
	Revision              string          `json:"revision,omitempty"`
	EvidenceAuthenticated bool            `json:"evidence_authenticated,omitempty"`
	StateVerified         bool            `json:"state_verified,omitempty"`
	ReplayLag             uint32          `json:"replay_lag,omitempty"`
	ReusedNodes           uint64          `json:"reused_nodes,omitempty"`
	FetchedNodes          uint64          `json:"fetched_nodes,omitempty"`
	MissingNodes          uint64          `json:"missing_nodes,omitempty"`
}

// MarshalJSON omits the zero start time, which keeps healthy status output
// compact while retaining a concrete time.Time API for callers.
func (p RecoveryProgress) MarshalJSON() ([]byte, error) {
	type progressJSON RecoveryProgress
	var started *time.Time
	if !p.StartedAt.IsZero() {
		value := p.StartedAt.UTC()
		started = &value
	}
	return json.Marshal(struct {
		progressJSON
		StartedAt *time.Time `json:"started_at,omitempty"`
	}{progressJSON(p), started})
}

// Status is a point-in-time view of the replay fault gate.
type Status struct {
	TransitionVerification string           `json:"transition_verification"`
	LastTransitionHash     [32]byte         `json:"last_transition_hash"`
	FollowerMode           bool             `json:"follower_mode"`
	Fault                  *Fault           `json:"fault,omitempty"`
	Blocked                bool             `json:"blocked"`
	Recovery               RecoveryProgress `json:"recovery"`
	PersistenceError       error            `json:"-"`
}

// MarshalJSON keeps errors concise and makes status suitable for diagnostics.
func (s Status) MarshalJSON() ([]byte, error) {
	persistenceError := ""
	if s.PersistenceError != nil {
		persistenceError = s.PersistenceError.Error()
	}
	return json.Marshal(struct {
		TransitionVerification string           `json:"transition_verification"`
		LastTransitionHash     [32]byte         `json:"last_transition_hash"`
		FollowerMode           bool             `json:"follower_mode"`
		Fault                  *Fault           `json:"fault,omitempty"`
		Blocked                bool             `json:"blocked"`
		Recovery               RecoveryProgress `json:"recovery"`
		PersistenceError       string           `json:"persistence_error,omitempty"`
	}{
		TransitionVerification: s.TransitionVerification,
		LastTransitionHash:     s.LastTransitionHash,
		FollowerMode:           s.FollowerMode,
		Fault:                  s.Fault,
		Blocked:                s.Blocked,
		Recovery:               s.Recovery,
		PersistenceError:       persistenceError,
	})
}

var (
	// ErrBlocked is returned when a caller tries to validate or publish while
	// an unresolved fault is present.
	ErrBlocked = errors.New("replay fault store is blocked")
	// ErrReplayInProgress is returned when another transition owns the durable intent.
	ErrReplayInProgress = errors.New("replay transition is already in progress")
	// ErrNoFault is returned when revalidation is requested without a fault.
	ErrNoFault = errors.New("replay fault store has no fault")
	// ErrFaultIDMismatch identifies a stale or unknown fault ID.
	ErrFaultIDMismatch = errors.New("replay fault ID does not match")
	// ErrRevalidationInProgress prevents multiple recovery callbacks from
	// racing to clear one fault.
	ErrRevalidationInProgress = errors.New("replay fault revalidation is already in progress")
	// ErrStaleRevalidation means a fault update intervened in recovery.
	ErrStaleRevalidation = errors.New("replay fault revalidation is stale")
	// ErrVerifierRequired prevents callers from clearing a fault without an
	// independent verification callback.
	ErrVerifierRequired = errors.New("replay fault verifier is required")
	// ErrRecoveryStrategy identifies an operator strategy this store does not
	// understand.
	ErrRecoveryStrategy = errors.New("unsupported replay recovery strategy")
	// ErrRecoveryGeneration means another update intervened in a staged step.
	ErrRecoveryGeneration = errors.New("replay recovery generation is stale")
	// ErrRecoveryActivation prevents an unverified phase from clearing the gate.
	ErrRecoveryActivation = errors.New("replay recovery is not ready for activation")
	// ErrGenerationExhausted prevents a durable revision from wrapping around
	// and allowing an old compare-and-swap token to become valid again.
	ErrGenerationExhausted = errors.New("replay fault generation exhausted")
)

type recoveryState struct {
	RecoveryProgress
}

// Store durably records at most one unresolved replay fault. Its zero value is
// usable as an in-memory store; Open is preferred when loading persisted state.
type Store struct {
	mu sync.Mutex

	// operation serializes validator callbacks with mutations that can gate or
	// clear signing and proposal publication.
	operation      sync.RWMutex
	path           string
	fault          *Fault
	pending        *Fault
	blocked        bool
	generation     uint64
	recovery       recoveryState
	persistenceErr error
}

func (s *Store) advanceGenerationLocked() error {
	if s.generation == ^uint64(0) {
		return ErrGenerationExhausted
	}
	s.generation++
	return nil
}

// Open loads a fault store. An empty path creates an in-memory store. A
// malformed or unreadable durable file returns both a blocked store and the
// load error so callers cannot accidentally proceed in a healthy state.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if path == "" {
		return s, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		s.blocked = true
		s.persistenceErr = fmt.Errorf("load replay fault store: %w", err)
		return s, s.persistenceErr
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return s, nil
	}

	var completion struct {
		Completed bool             `json:"completed"`
		Recovery  RecoveryProgress `json:"recovery"`
	}
	if err := json.Unmarshal(data, &completion); err == nil && completion.Completed {
		if completion.Recovery.ID == "" || (completion.Recovery.Outcome != RecoveryOutcomeResolvedByReplay && completion.Recovery.Outcome != RecoveryOutcomeSupersededByPivot) {
			s.blocked = true
			s.persistenceErr = errors.New("decode replay fault store: invalid recovery completion")
			return s, s.persistenceErr
		}
		s.recovery.RecoveryProgress = completion.Recovery
		s.recovery.InFlight = false
		s.generation = completion.Recovery.Generation
		return s, nil
	}
	var fault Fault
	if err := json.Unmarshal(data, &fault); err != nil {
		s.blocked = true
		s.persistenceErr = fmt.Errorf("decode replay fault store: %w", err)
		return s, s.persistenceErr
	}
	if fault.ID == "" {
		s.blocked = true
		s.persistenceErr = errors.New("decode replay fault store: missing fault ID")
		return s, s.persistenceErr
	}
	if fault.Class == "" {
		fault.Class = Unclassified
	}
	fault.Evidence = cloneRaw(fault.Evidence)
	if fault.OriginalEvidence == nil {
		fault.OriginalEvidence = cloneRaw(fault.Evidence)
	} else {
		fault.OriginalEvidence = cloneRaw(fault.OriginalEvidence)
	}
	fault.Recovery = cloneRecovery(fault.Recovery)
	fault.RecoveryTarget = cloneRaw(fault.RecoveryTarget)
	fault.RecoveryPivot = cloneRaw(fault.RecoveryPivot)
	fault.RecoveryFrontier = cloneRaw(fault.RecoveryFrontier)
	s.fault = &fault
	s.recovery.LastError = fault.RecoveryError
	s.recovery.Attempts = fault.Attempts
	if fault.Recovery != nil {
		s.recovery.RecoveryProgress = *cloneRecovery(fault.Recovery)
		// A process can die after the durable phase write and before the worker
		// clears InFlight. The phase is the resume point; stale in-flight state
		// must not block the explicit retry.
		s.recovery.InFlight = false
	}
	s.blocked = true
	s.generation = 1
	if fault.Recovery != nil && fault.Recovery.Generation > s.generation {
		s.generation = fault.Recovery.Generation
	}
	return s, nil
}

// BeginReplay persists the transition before execution. An interrupted replay
// loads as an unresolved fault; a failed write prevents execution from starting.
func (s *Store) BeginReplay(fault Fault) (string, error) {
	if s == nil {
		return "", ErrBlocked
	}
	s.operation.Lock()
	defer s.operation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocked {
		return "", ErrBlocked
	}
	if s.pending != nil {
		return "", ErrReplayInProgress
	}
	if s.generation == ^uint64(0) {
		return "", ErrGenerationExhausted
	}
	normalized, err := normalizeFault(fault)
	if err != nil {
		return "", err
	}
	normalized.Class = Unclassified
	normalized.Message = "replay transition interrupted before verification completed"
	normalized.OriginalEvidence = cloneRaw(normalized.Evidence)
	normalized.Recovery = nil
	normalized.RecoveryTarget = nil
	normalized.RecoveryPivot = nil
	normalized.RecoveryFrontier = nil
	if err := s.persistLocked(&normalized); err != nil {
		s.fault = &normalized
		s.recovery = recoveryState{}
		s.blocked = true
		s.persistenceErr = err
		if generationErr := s.advanceGenerationLocked(); generationErr != nil {
			return "", errors.Join(err, generationErr)
		}
		return "", err
	}
	s.pending = &normalized
	if err := s.advanceGenerationLocked(); err != nil {
		return "", err
	}
	return normalized.ID, nil
}

// FailReplay latches the gate before enriching an already durable intent.
func (s *Store) FailReplay(id string, fault Fault) error {
	if s == nil {
		return ErrBlocked
	}
	s.operation.Lock()
	defer s.operation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil || id == "" || s.pending.ID != id {
		return ErrFaultIDMismatch
	}
	if s.generation == ^uint64(0) {
		return ErrGenerationExhausted
	}
	intent := s.pending
	fault.ID = intent.ID
	fault.ParentHash = intent.ParentHash
	fault.TargetHash = intent.TargetHash
	fault.Sequence = intent.Sequence
	fault.CreatedAt = intent.CreatedAt
	fault.Revision = intent.Revision
	if fault.Class == "" {
		fault.Class = Unclassified
	}
	if fault.Evidence == nil {
		fault.Evidence = intent.Evidence
	}
	// The final failure observation may contain diagnosis/detail added after
	// BeginReplay. Preserve that complete observation in the archive.
	if fault.Evidence != nil {
		fault.OriginalEvidence = cloneRaw(fault.Evidence)
	} else {
		fault.OriginalEvidence = cloneRaw(intent.OriginalEvidence)
		if fault.OriginalEvidence == nil {
			fault.OriginalEvidence = cloneRaw(intent.Evidence)
		}
	}
	fault.Recovery = nil
	fault.RecoveryTarget = nil
	fault.RecoveryPivot = nil
	fault.RecoveryFrontier = nil
	s.fault = cloneFault(&fault)
	s.recovery = recoveryState{}
	s.pending = nil
	s.blocked = true
	if err := s.advanceGenerationLocked(); err != nil {
		return err
	}
	s.persistenceErr = s.persistLocked(s.fault)
	return s.persistenceErr
}

// CompleteReplay clears an intent only after its transition was verified.
func (s *Store) CompleteReplay(id string) error {
	return s.clearReplayIntent(id)
}

// CancelReplay clears an intent after its owner has discarded the private
// replay without publishing it. A failed durable clear keeps validation blocked.
func (s *Store) CancelReplay(id string) error {
	return s.clearReplayIntent(id)
}

func (s *Store) clearReplayIntent(id string) error {
	if s == nil {
		return ErrBlocked
	}
	s.operation.Lock()
	defer s.operation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocked {
		return ErrBlocked
	}
	if s.pending == nil || id == "" || s.pending.ID != id {
		return ErrFaultIDMismatch
	}
	if s.generation == ^uint64(0) {
		return ErrGenerationExhausted
	}
	if err := clearDurable(s.path, *s.pending); err != nil {
		s.fault = s.pending
		s.recovery = recoveryState{}
		s.pending = nil
		s.blocked = true
		s.persistenceErr = err
		if generationErr := s.advanceGenerationLocked(); generationErr != nil {
			return errors.Join(err, generationErr)
		}
		return err
	}
	s.pending = nil
	if err := s.advanceGenerationLocked(); err != nil {
		return err
	}
	return nil
}

// ReserveAcquisition persists the retry budget before network work begins.
func (s *Store) ReserveAcquisition(id string, limit int) error {
	s.operation.Lock()
	defer s.operation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault == nil {
		return ErrNoFault
	}
	if s.fault.ID != id {
		return ErrFaultIDMismatch
	}
	if s.fault.AcquisitionAttempts >= limit {
		return errors.New("replay acquisition budget exhausted")
	}
	if err := s.advanceGenerationLocked(); err != nil {
		return err
	}
	s.fault.AcquisitionAttempts++
	if err := s.persistLocked(s.fault); err != nil {
		s.persistenceErr = err
		return err
	}
	s.persistenceErr = nil
	return nil
}

// Update enriches the current fault without changing its identity, creation
// time, or recovery attempt count. It invalidates any revalidation in flight.
func (s *Store) Update(id string, fault Fault) error {
	if s == nil {
		return ErrNoFault
	}
	s.operation.Lock()
	defer s.operation.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault == nil {
		if s.blocked {
			return ErrBlocked
		}
		return ErrNoFault
	}
	if id == "" || s.fault.ID != id {
		return fmt.Errorf("%w: %q", ErrFaultIDMismatch, id)
	}
	if s.generation == ^uint64(0) {
		return ErrGenerationExhausted
	}

	next := fault
	next.ID = s.fault.ID
	next.ParentHash = s.fault.ParentHash
	next.TargetHash = s.fault.TargetHash
	next.Sequence = s.fault.Sequence
	next.CreatedAt = s.fault.CreatedAt
	next.Attempts = s.fault.Attempts
	next.AcquisitionAttempts = max(next.AcquisitionAttempts, s.fault.AcquisitionAttempts)
	next.OriginalEvidence = cloneRaw(s.fault.OriginalEvidence)
	if next.OriginalEvidence == nil {
		next.OriginalEvidence = cloneRaw(s.fault.Evidence)
	}
	next.Recovery = cloneRecovery(s.fault.Recovery)
	next.RecoveryTarget = cloneRaw(s.fault.RecoveryTarget)
	next.RecoveryPivot = cloneRaw(s.fault.RecoveryPivot)
	next.RecoveryFrontier = cloneRaw(s.fault.RecoveryFrontier)
	if next.BlockedReason == "" {
		next.BlockedReason = s.fault.BlockedReason
	}
	if next.OperatorAction == "" {
		next.OperatorAction = s.fault.OperatorAction
	}
	if next.Class == "" {
		next.Class = s.fault.Class
	}
	if next.Revision == "" {
		next.Revision = s.fault.Revision
	}
	next.Evidence = cloneRaw(next.Evidence)
	s.fault = cloneFault(&next)
	s.blocked = true
	if next.Recovery != nil {
		s.recovery = recoveryState{RecoveryProgress: *cloneRecovery(next.Recovery)}
		if next.Recovery.Strategy == RecoveryStrategyVerifiedPivot {
			s.recovery.InFlight = false
		}
	} else {
		s.recovery.LastError = next.RecoveryError
	}
	if err := s.advanceGenerationLocked(); err != nil {
		return err
	}
	if s.fault.Recovery != nil {
		s.recovery.Generation = s.generation
		s.fault.Recovery = cloneRecovery(&s.recovery.RecoveryProgress)
	}
	if err := s.persistLocked(s.fault); err != nil {
		s.persistenceErr = err
		return err
	}
	s.persistenceErr = nil
	return nil
}

// BeginRecovery durably admits one explicit staged recovery. Reusing the same
// strategy resumes its persisted phase; choosing a different strategy starts a
// fresh phase while retaining the original fault evidence.
func (s *Store) BeginRecovery(id string, strategy RecoveryStrategy) (RecoveryProgress, error) {
	if s == nil {
		return RecoveryProgress{}, ErrBlocked
	}
	if strategy != RecoveryStrategyExactTransition && strategy != RecoveryStrategyVerifiedPivot {
		return RecoveryProgress{}, fmt.Errorf("%w: %q", ErrRecoveryStrategy, strategy)
	}
	s.operation.Lock()
	defer s.operation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault == nil {
		if s.blocked {
			return RecoveryProgress{}, ErrBlocked
		}
		return RecoveryProgress{}, ErrNoFault
	}
	if id == "" || s.fault.ID != id {
		return RecoveryProgress{}, fmt.Errorf("%w: %q", ErrFaultIDMismatch, id)
	}
	if s.recovery.InFlight {
		return RecoveryProgress{}, ErrRevalidationInProgress
	}

	next := cloneFault(s.fault)
	progress := RecoveryProgress{Strategy: strategy, Phase: RecoveryPhaseSelectingPivot}
	resume := next.Recovery != nil && next.Recovery.Strategy == strategy && next.Recovery.Phase != "" &&
		next.Recovery.Phase != RecoveryPhaseExecutionFailed && next.Recovery.Phase != RecoveryPhaseNeedsOperator &&
		next.Recovery.LastError == ""
	if resume {
		progress = *cloneRecovery(next.Recovery)
	} else if next.Recovery != nil {
		// A new explicit admission starts a new route after a failed attempt,
		// while retaining the diagnostic counters and latest fingerprint.
		progress.FailureFingerprint = next.Recovery.FailureFingerprint
		progress.FailureAttempts = next.Recovery.FailureAttempts
		progress.RetentionSequence = next.Recovery.RetentionSequence
	}
	if !resume {
		next.RecoveryTarget = nil
		next.RecoveryPivot = nil
		next.RecoveryFrontier = nil
	}
	if next.Attempts < int(^uint(0)>>1) {
		next.Attempts++
	}
	next.RecoveryError = ""
	progress.ID = id
	progress.InFlight = true
	progress.Attempts = next.Attempts
	progress.Revision = buildRevision()
	progress.StartedAt = time.Now().UTC()
	progress.LastError = ""
	progress.BlockedReason = "recovery in progress"
	progress.OperatorAction = "wait for recovery verification"
	if err := s.advanceGenerationLocked(); err != nil {
		return RecoveryProgress{}, err
	}
	progress.Generation = s.generation
	next.Recovery = cloneRecovery(&progress)
	if err := s.persistLocked(next); err != nil {
		progress.InFlight = false
		progress.LastError = err.Error()
		next.Recovery = cloneRecovery(&progress)
		s.fault = next
		s.recovery = recoveryState{RecoveryProgress: progress}
		s.persistenceErr = err
		return RecoveryProgress{}, err
	}
	s.fault = next
	s.blocked = true
	s.recovery = recoveryState{RecoveryProgress: progress}
	s.persistenceErr = nil
	return progress, nil
}

// UpdateRecovery persists one staged phase using the generation returned by
// BeginRecovery or a previous UpdateRecovery. It is a compare-and-swap so a
// stale worker cannot advance a newer operator attempt.
func (s *Store) UpdateRecovery(id string, expectedGeneration uint64, progress RecoveryProgress) (RecoveryProgress, error) {
	return s.updateRecovery(id, expectedGeneration, progress, nil, false)
}

// UpdateRecoveryWithFrontier advances the recovery journal and records the
// latest durably staged frontier in one atomic fault-file replacement. The
// pending RecoveryTarget is deliberately retained, so a crash after selecting
// a successor still has both the last usable parent and the next target.
func (s *Store) UpdateRecoveryWithFrontier(id string, expectedGeneration uint64, progress RecoveryProgress, frontier json.RawMessage) (RecoveryProgress, error) {
	if len(frontier) == 0 {
		return RecoveryProgress{}, errors.New("replay recovery frontier proof is required")
	}
	return s.updateRecovery(id, expectedGeneration, progress, frontier, true)
}

func (s *Store) updateRecovery(id string, expectedGeneration uint64, progress RecoveryProgress, frontier json.RawMessage, setFrontier bool) (RecoveryProgress, error) {
	if s == nil {
		return RecoveryProgress{}, ErrBlocked
	}
	s.operation.Lock()
	defer s.operation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault == nil {
		return RecoveryProgress{}, ErrNoFault
	}
	if id == "" || s.fault.ID != id || s.recovery.ID != id {
		return RecoveryProgress{}, fmt.Errorf("%w: %q", ErrFaultIDMismatch, id)
	}
	if expectedGeneration != s.generation || expectedGeneration != s.recovery.Generation {
		return RecoveryProgress{}, ErrRecoveryGeneration
	}
	if progress.Strategy == "" || progress.Phase == "" {
		return RecoveryProgress{}, errors.New("replay recovery strategy and phase are required")
	}
	if err := validateRecoveryProgress(s.recovery.RecoveryProgress, progress); err != nil {
		return RecoveryProgress{}, err
	}
	next := cloneFault(s.fault)
	next.RecoveryError = ""
	if setFrontier {
		next.RecoveryFrontier = cloneRaw(frontier)
	}
	previousGeneration := s.generation
	if err := s.advanceGenerationLocked(); err != nil {
		return RecoveryProgress{}, err
	}
	progress.ID = id
	progress.Generation = s.generation
	progress.Attempts = next.Attempts
	if progress.StartedAt.IsZero() {
		progress.StartedAt = s.recovery.StartedAt
	}
	next.Recovery = cloneRecovery(&progress)
	if !progress.InFlight {
		next.RecoveryError = progress.LastError
	}
	if err := s.persistLocked(next); err != nil {
		s.generation = previousGeneration
		s.persistenceErr = err
		return RecoveryProgress{}, err
	}
	s.fault = next
	s.recovery = recoveryState{RecoveryProgress: progress}
	s.blocked = true
	s.persistenceErr = nil
	return progress, nil
}

func validateRecoveryProgress(previous, next RecoveryProgress) error {
	if previous.Strategy != "" && previous.Strategy != next.Strategy {
		return fmt.Errorf("%w: recovery strategy changed", ErrRecoveryGeneration)
	}
	terminal := func(phase RecoveryPhase) bool {
		return phase == RecoveryPhaseExecutionFailed || phase == RecoveryPhaseNeedsOperator
	}
	if terminal(next.Phase) {
		if terminal(previous.Phase) {
			return fmt.Errorf("%w: recovery terminal phase cannot advance", ErrRecoveryGeneration)
		}
		if next.PivotSequence != 0 && next.PivotHash == ([32]byte{}) {
			return errors.New("replay recovery pivot identity is incomplete")
		}
		if next.SuccessorSequence != 0 && next.SuccessorHash == ([32]byte{}) {
			return errors.New("replay recovery successor identity is incomplete")
		}
		if next.FrontierSequence != 0 && next.FrontierHash == ([32]byte{}) {
			return errors.New("replay recovery frontier identity is incomplete")
		}
		return nil
	}
	rank := func(phase RecoveryPhase) int {
		switch phase {
		case RecoveryPhaseSelectingPivot:
			return 0
		case RecoveryPhaseAuthenticating:
			return 1
		case RecoveryPhaseAcquiringPivot:
			return 2
		case RecoveryPhaseCatchingUp:
			return 3
		case RecoveryPhaseReadyToActivate:
			return 4
		case RecoveryPhaseActivating:
			return 5
		default:
			return -1
		}
	}
	if terminal(previous.Phase) {
		return fmt.Errorf("%w: recovery terminal phase cannot advance", ErrRecoveryGeneration)
	}
	previousRank, nextRank := rank(previous.Phase), rank(next.Phase)
	if nextRank < 0 || previousRank < 0 {
		return fmt.Errorf("%w: invalid recovery phase", ErrRecoveryGeneration)
	}
	// Publication can race a newly authenticated successor becoming available.
	// The gate remains closed, so a ready or activating route may safely return
	// to catch-up and continue from its durable frontier.
	resumeCatchup := next.Phase == RecoveryPhaseCatchingUp && (previous.Phase == RecoveryPhaseReadyToActivate || previous.Phase == RecoveryPhaseActivating)
	if nextRank > previousRank+1 {
		return fmt.Errorf("%w: recovery phase skipped", ErrRecoveryGeneration)
	}
	if previous.FrontierSequence > next.FrontierSequence || (previous.FrontierSequence != 0 && previous.FrontierSequence == next.FrontierSequence && previous.FrontierHash != next.FrontierHash) {
		return fmt.Errorf("%w: verified frontier regressed or changed", ErrRecoveryGeneration)
	}
	if nextRank < previousRank && !resumeCatchup {
		return fmt.Errorf("%w: recovery phase regressed", ErrRecoveryGeneration)
	}
	if previous.PivotSequence != 0 && (next.PivotSequence != previous.PivotSequence || next.PivotHash != previous.PivotHash) {
		return fmt.Errorf("%w: pivot identity changed", ErrRecoveryGeneration)
	}
	if previous.SuccessorSequence != 0 && (next.SuccessorSequence != previous.SuccessorSequence || next.SuccessorHash != previous.SuccessorHash) {
		return fmt.Errorf("%w: successor identity changed", ErrRecoveryGeneration)
	}
	if next.PivotSequence == 0 || next.PivotHash == ([32]byte{}) {
		if next.PivotSequence != 0 || next.PivotHash != ([32]byte{}) {
			return errors.New("replay recovery pivot identity is incomplete")
		}
	}
	if next.SuccessorSequence == 0 || next.SuccessorHash == ([32]byte{}) {
		if next.SuccessorSequence != 0 || next.SuccessorHash != ([32]byte{}) {
			return errors.New("replay recovery successor identity is incomplete")
		}
	}
	if next.FrontierSequence != 0 && next.FrontierHash == ([32]byte{}) {
		return errors.New("replay recovery frontier identity is incomplete")
	}
	if next.PivotSequence != 0 && next.FrontierSequence != 0 && next.FrontierSequence < next.PivotSequence {
		return errors.New("replay recovery frontier precedes pivot")
	}
	if nextRank >= 4 && (next.SuccessorSequence == 0 || next.SuccessorHash == ([32]byte{}) || next.FrontierSequence == 0 || next.FrontierHash == ([32]byte{}) || next.SuccessorSequence > next.FrontierSequence) {
		return errors.New("replay recovery activation requires a verified successor frontier")
	}
	return nil
}

// SetRecoveryTarget durably records the coordinator proof before any network
// acquisition or local replay consumes it. The opaque payload is retained only
// in the private journal; Status strips it from the public fault summary.
func (s *Store) SetRecoveryTarget(id string, expectedGeneration uint64, target json.RawMessage) (RecoveryProgress, error) {
	return s.setRecoveryProof(id, expectedGeneration, target, false)
}

// SetRecoveryPivot records the authenticated pivot proof separately from the
// currently executing successor target so a restart never has to select a new
// pivot after a route has been admitted.
func (s *Store) SetRecoveryPivot(id string, expectedGeneration uint64, target json.RawMessage) (RecoveryProgress, error) {
	return s.setRecoveryProof(id, expectedGeneration, target, true)
}

func (s *Store) setRecoveryProof(id string, expectedGeneration uint64, target json.RawMessage, pivot bool) (RecoveryProgress, error) {
	if s == nil {
		return RecoveryProgress{}, ErrBlocked
	}
	if len(target) == 0 {
		return RecoveryProgress{}, errors.New("replay recovery target proof is required")
	}
	s.operation.Lock()
	defer s.operation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault == nil {
		return RecoveryProgress{}, ErrNoFault
	}
	if id == "" || s.fault.ID != id || s.recovery.ID != id {
		return RecoveryProgress{}, fmt.Errorf("%w: %q", ErrFaultIDMismatch, id)
	}
	if expectedGeneration != s.generation || expectedGeneration != s.recovery.Generation {
		return RecoveryProgress{}, ErrRecoveryGeneration
	}
	next := cloneFault(s.fault)
	previousGeneration := s.generation
	if err := s.advanceGenerationLocked(); err != nil {
		return RecoveryProgress{}, err
	}
	progress := s.recovery.RecoveryProgress
	progress.Generation = s.generation
	next.Recovery = cloneRecovery(&progress)
	if pivot {
		next.RecoveryPivot = cloneRaw(target)
	} else {
		next.RecoveryTarget = cloneRaw(target)
	}
	if err := s.persistLocked(next); err != nil {
		s.generation = previousGeneration
		s.persistenceErr = err
		return RecoveryProgress{}, err
	}
	s.fault = next
	s.recovery = recoveryState{RecoveryProgress: progress}
	s.persistenceErr = nil
	return progress, nil
}

// FailRecovery records a stable failure for the current explicit admission.
// A later process restart must leave this phase blocked until an operator
// explicitly creates a fresh generation.
func (s *Store) SuspendRecovery(id string, expectedGeneration uint64, cause error) error {
	s.mu.Lock()
	progress := s.recovery.RecoveryProgress
	s.mu.Unlock()
	progress.InFlight = false
	progress.LastError = ""
	progress.BlockedReason = "recovery interrupted; verified progress retained"
	progress.OperatorAction = "wait for recovery to resume after restart"
	if cause != nil {
		progress.AcquisitionError = cause.Error()
	}
	_, err := s.UpdateRecovery(id, expectedGeneration, progress)
	return err
}

func (s *Store) HaltRecovery(id string, expectedGeneration uint64, cause error) error {
	s.mu.Lock()
	progress := s.recovery.RecoveryProgress
	s.mu.Unlock()
	progress.InFlight = false
	progress.Phase = RecoveryPhaseNeedsOperator
	progress.LastError = cause.Error()
	progress.BlockedReason = cause.Error()
	progress.OperatorAction = "diagnose and correct the recovery failure, then explicitly admit recovery"
	_, err := s.UpdateRecovery(id, expectedGeneration, progress)
	return err
}

func (s *Store) FailRecovery(id string, expectedGeneration uint64, cause error) error {
	if cause == nil {
		cause = errors.New("replay recovery failed")
	}
	s.mu.Lock()
	progress := s.recovery.RecoveryProgress
	s.mu.Unlock()
	progress.InFlight = false
	progress.Phase = RecoveryPhaseExecutionFailed
	progress.LastError = cause.Error()
	digest := sha256.Sum256([]byte(cause.Error()))
	progress.FailureFingerprint = hex.EncodeToString(digest[:])
	progress.BlockedReason = "reproduced execution disagreement"
	progress.OperatorAction = "correct the execution failure, then explicitly admit recovery"
	if progress.FailureAttempts < int(^uint(0)>>1) {
		progress.FailureAttempts++
	}
	_, err := s.UpdateRecovery(id, expectedGeneration, progress)
	return err
}

// ActivateRecovery archives the unresolved evidence and clears the gate only
// after the caller has durably published the verified successor. Verified-pivot
// outcomes use a distinct archive suffix so operators can tell that the old
// evidence was superseded rather than directly revalidated.
func (s *Store) ActivateRecovery(id string, expectedGeneration uint64) error {
	if s == nil {
		return ErrBlocked
	}
	s.operation.Lock()
	defer s.operation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault == nil {
		return ErrNoFault
	}
	if id == "" || s.fault.ID != id {
		return fmt.Errorf("%w: %q", ErrFaultIDMismatch, id)
	}
	if expectedGeneration != s.generation || expectedGeneration != s.recovery.Generation {
		return ErrRecoveryGeneration
	}
	if !s.recovery.InFlight || s.fault.Recovery == nil || s.fault.Recovery.Phase != RecoveryPhaseActivating {
		return ErrRecoveryActivation
	}
	if s.generation == ^uint64(0) {
		return ErrGenerationExhausted
	}
	faultToArchive := *cloneFault(s.fault)
	faultToArchive.Recovery.Generation = s.generation + 1
	suffix := ".resolved"
	if faultToArchive.Recovery.Strategy == RecoveryStrategyVerifiedPivot {
		suffix = ".superseded"
		faultToArchive.Recovery.Outcome = RecoveryOutcomeSupersededByPivot
	} else {
		faultToArchive.Recovery.Outcome = RecoveryOutcomeResolvedByReplay
	}
	if err := archiveDurableSuffix(s.path, faultToArchive, suffix); err != nil {
		s.persistenceErr = err
		s.recovery.LastError = err.Error()
		return err
	}
	if err := clearDurable(s.path, faultToArchive); err != nil {
		s.persistenceErr = err
		s.recovery.LastError = err.Error()
		return err
	}
	s.fault = nil
	s.blocked = false
	s.persistenceErr = nil
	s.recovery = recoveryState{RecoveryProgress: *cloneRecovery(faultToArchive.Recovery)}
	s.recovery.InFlight = false
	s.recovery.Phase = RecoveryPhaseComplete
	s.recovery.BlockedReason = ""
	s.recovery.OperatorAction = ""
	s.recovery.LastError = ""
	if err := s.advanceGenerationLocked(); err != nil {
		return err
	}
	return nil
}

// Snapshot returns a deep copy of the unresolved fault, or nil when none is
// recorded.
func (s *Store) Snapshot() *Fault {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneFault(s.fault)
}

// Blocked reports whether signing and proposal publication must stop.
func (s *Store) Blocked() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blocked
}

// Status returns a deep point-in-time view of the gate and recovery state.
func (s *Store) Status() Status {
	if s == nil {
		return Status{Blocked: true, PersistenceError: ErrBlocked}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	recovery := s.recovery.RecoveryProgress
	if s.fault != nil {
		if recovery.LastError == "" {
			recovery.LastError = s.fault.RecoveryError
		}
		if recovery.BlockedReason == "" {
			recovery.BlockedReason = s.fault.BlockedReason
		}
		if recovery.OperatorAction == "" {
			recovery.OperatorAction = s.fault.OperatorAction
		}
	}
	return Status{
		Fault:            faultSummary(s.fault),
		Blocked:          s.blocked,
		Recovery:         recovery,
		PersistenceError: s.persistenceErr,
	}
}

// WithValidator serializes fn with fault-journal mutations. A blocked store
// rejects fn; a pending replay has not established a fault yet.
func (s *Store) WithValidator(fn func() error) error {
	if s == nil {
		return ErrBlocked
	}
	if fn == nil {
		return ErrVerifierRequired
	}
	s.operation.RLock()
	defer s.operation.RUnlock()

	s.mu.Lock()
	blocked := s.blocked
	s.mu.Unlock()
	if blocked {
		return ErrBlocked
	}
	return fn()
}

// WithRecoveryActivation serializes the canonical publication of one admitted
// recovery with every durable journal mutation. The callback runs without the
// store mutex held, but while the operation write lock is held; it must not call
// a store mutation directly or it will wait for this guard to release.
func (s *Store) WithRecoveryActivation(id string, expectedGeneration uint64, fn func() error) error {
	if s == nil {
		return ErrBlocked
	}
	if fn == nil {
		return ErrVerifierRequired
	}

	s.operation.Lock()
	defer s.operation.Unlock()

	s.mu.Lock()
	if s.fault == nil {
		s.mu.Unlock()
		return ErrNoFault
	}
	if id == "" || s.fault.ID != id || s.recovery.ID != id {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrFaultIDMismatch, id)
	}
	if expectedGeneration != s.generation || expectedGeneration != s.recovery.Generation {
		s.mu.Unlock()
		return ErrRecoveryGeneration
	}
	if s.fault.Recovery == nil || s.fault.Recovery.ID != id ||
		s.fault.Recovery.Generation != expectedGeneration || !s.recovery.InFlight ||
		s.fault.Recovery.Phase != RecoveryPhaseActivating {
		s.mu.Unlock()
		return ErrRecoveryActivation
	}
	if s.generation == ^uint64(0) {
		s.mu.Unlock()
		return ErrGenerationExhausted
	}
	s.mu.Unlock()

	return fn()
}

// Revalidate runs one explicit verification attempt. The callback is always
// required and runs without either store mutex held. A successful callback
// only clears the gate when no fault update intervened and the durable
// clear completed.
func (s *Store) Revalidate(ctx context.Context, id string, verify func(context.Context, Fault) error) error {
	if s == nil {
		return ErrBlocked
	}
	if verify == nil {
		return ErrVerifierRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.fault == nil {
		if s.blocked {
			s.mu.Unlock()
			return ErrBlocked
		}
		s.mu.Unlock()
		return ErrNoFault
	}
	if s.recovery.InFlight {
		s.mu.Unlock()
		return ErrRevalidationInProgress
	}
	if id == "" || s.fault.ID != id {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrFaultIDMismatch, id)
	}

	if s.fault.Attempts < int(^uint(0)>>1) {
		s.fault.Attempts++
	}
	s.fault.RecoveryError = ""
	generation := s.generation
	fault := *cloneFault(s.fault)
	s.recovery = recoveryState{RecoveryProgress: RecoveryProgress{
		Strategy:   RecoveryStrategyExactTransition,
		Revision:   buildRevision(),
		InFlight:   true,
		ID:         id,
		Generation: generation,
		Attempts:   fault.Attempts,
		StartedAt:  time.Now().UTC(),
	}}
	if previous := s.fault.Recovery; previous != nil {
		s.recovery.FailureFingerprint = previous.FailureFingerprint
		s.recovery.FailureAttempts = previous.FailureAttempts
	}
	s.fault.Recovery = cloneRecovery(&s.recovery.RecoveryProgress)
	if err := s.persistLocked(s.fault); err != nil {
		s.fault.RecoveryError = err.Error()
		s.recovery.InFlight = false
		s.recovery.LastError = err.Error()
		s.persistenceErr = err
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	err := runVerifier(ctx, fault, verify)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		s.mu.Lock()
		s.recovery.InFlight = false
		s.recovery.LastError = err.Error()
		if s.fault != nil && s.fault.ID == fault.ID {
			s.fault.RecoveryError = err.Error()
			s.fault.Recovery = cloneRecovery(&s.recovery.RecoveryProgress)
			if persistErr := s.persistLocked(s.fault); persistErr != nil {
				s.persistenceErr = persistErr
			}
		}
		s.mu.Unlock()
		return err
	}

	// Take the operation write lock before checking generation and clearing so
	// a validator callback cannot overlap the durable unblock.
	s.operation.Lock()
	defer s.operation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault == nil || s.fault.ID != id || s.generation != generation {
		err := ErrStaleRevalidation
		s.recovery.InFlight = false
		s.recovery.LastError = err.Error()
		return err
	}
	faultToArchive := *cloneFault(s.fault)
	faultToArchive.Recovery = cloneRecovery(&s.recovery.RecoveryProgress)
	faultToArchive.Recovery.Outcome = RecoveryOutcomeResolvedByReplay
	faultToArchive.Recovery.InFlight = false
	if err := archiveDurable(s.path, faultToArchive); err != nil {
		s.persistenceErr = err
		s.recovery.InFlight = false
		s.recovery.LastError = err.Error()
		return err
	}
	if err := clearDurable(s.path, faultToArchive); err != nil {
		s.persistenceErr = err
		s.recovery.InFlight = false
		s.recovery.LastError = err.Error()
		return err
	}
	s.fault = nil
	s.blocked = false
	s.persistenceErr = nil
	s.recovery = recoveryState{RecoveryProgress: *cloneRecovery(faultToArchive.Recovery)}
	s.recovery.InFlight = false
	s.recovery.LastError = ""
	s.recovery.Phase = RecoveryPhaseComplete
	return nil
}

func normalizeFault(fault Fault) (Fault, error) {
	if fault.Class == "" {
		fault.Class = Unclassified
	}
	if fault.ID == "" {
		id, err := newID()
		if err != nil {
			return fault, fmt.Errorf("generate replay fault ID: %w", err)
		}
		fault.ID = id
	}
	if fault.CreatedAt.IsZero() {
		fault.CreatedAt = time.Now().UTC()
	}
	if fault.Revision == "" {
		fault.Revision = buildRevision()
	}
	fault.Evidence = cloneRaw(fault.Evidence)
	if fault.OriginalEvidence == nil {
		fault.OriginalEvidence = cloneRaw(fault.Evidence)
	} else {
		fault.OriginalEvidence = cloneRaw(fault.OriginalEvidence)
	}
	fault.Recovery = cloneRecovery(fault.Recovery)
	return fault, nil
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}
	return "unknown"
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	copyOfRaw := make(json.RawMessage, len(raw))
	copy(copyOfRaw, raw)
	return copyOfRaw
}

func cloneFault(fault *Fault) *Fault {
	if fault == nil {
		return nil
	}
	copyOfFault := *fault
	copyOfFault.Evidence = cloneRaw(fault.Evidence)
	copyOfFault.OriginalEvidence = cloneRaw(fault.OriginalEvidence)
	copyOfFault.RecoveryTarget = cloneRaw(fault.RecoveryTarget)
	copyOfFault.RecoveryPivot = cloneRaw(fault.RecoveryPivot)
	copyOfFault.RecoveryFrontier = cloneRaw(fault.RecoveryFrontier)
	copyOfFault.Recovery = cloneRecovery(fault.Recovery)
	return &copyOfFault
}

func cloneRecovery(progress *RecoveryProgress) *RecoveryProgress {
	if progress == nil {
		return nil
	}
	copyOfProgress := *progress
	return &copyOfProgress
}

func (s *Store) persistLocked(fault *Fault) error {
	if s.path == "" {
		return nil
	}
	return writeAtomic(s.path, fault)
}

func archiveDurable(path string, fault Fault) error {
	return archiveDurableSuffix(path, fault, ".resolved")
}

func archiveDurableSuffix(path string, fault Fault, suffix string) error {
	if path == "" {
		return nil
	}
	return writeAtomic(path+"."+fault.ID+suffix, &fault)
}

func writeAtomic(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create replay fault directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create replay fault temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set replay fault temporary file mode: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode replay fault: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write replay fault: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync replay fault: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close replay fault temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish replay fault: %w", err)
	}
	committed = true
	if err := syncDirectoryFn(dir); err != nil {
		return fmt.Errorf("sync replay fault directory: %w", err)
	}
	return nil
}

func clearDurable(path string, fault Fault) error {
	if path == "" {
		return nil
	}
	var completion any
	if fault.Recovery != nil && fault.Recovery.Outcome != "" {
		progress := *fault.Recovery
		progress.InFlight = false
		progress.Phase = RecoveryPhaseComplete
		progress.LastError = ""
		progress.BlockedReason = ""
		progress.OperatorAction = ""
		completion = struct {
			Completed bool             `json:"completed"`
			Recovery  RecoveryProgress `json:"recovery"`
		}{true, progress}
	}
	if err := writeAtomic(path, completion); err != nil {
		restoreErr := writeAtomic(path, &fault)
		if restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore replay fault after clear failure: %w", restoreErr))
		}
		return err
	}
	return nil
}

var syncDirectoryFn = syncDirectory

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}

func runVerifier(ctx context.Context, fault Fault, verify func(context.Context, Fault) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("unclassified recovery panic: %v", recovered)
		}
	}()
	return verify(ctx, fault)
}

func faultSummary(fault *Fault) *Fault {
	if fault == nil {
		return nil
	}
	summary := *fault
	summary.Recovery = cloneRecovery(fault.Recovery)
	summary.Evidence = nil
	summary.OriginalEvidence = nil
	summary.RecoveryTarget = nil
	summary.RecoveryPivot = nil
	summary.RecoveryFrontier = nil
	return &summary
}
