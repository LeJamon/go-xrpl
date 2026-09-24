// Package replayfault records replay failures that must stop signing and
// proposal publication until the affected state has been independently
// verified.
package replayfault

import (
	"bytes"
	"context"
	"crypto/rand"
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

// Fault is the durable description of an unresolved replay failure.
type Fault struct {
	ID         string          `json:"id"`
	Class      Class           `json:"class"`
	ParentHash [32]byte        `json:"parent_hash"`
	TargetHash [32]byte        `json:"target_hash"`
	Sequence   uint32          `json:"sequence"`
	Message    string          `json:"message"`
	Revision   string          `json:"revision"`
	CreatedAt  time.Time       `json:"created_at"`
	Evidence   json.RawMessage `json:"evidence,omitempty"`
	Attempts   int             `json:"attempts"`
}

// RecoveryProgress describes the current or most recent explicit
// revalidation attempt.
type RecoveryProgress struct {
	InFlight   bool      `json:"in_flight"`
	ID         string    `json:"id,omitempty"`
	Generation uint64    `json:"generation,omitempty"`
	Attempts   int       `json:"attempts,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

// MarshalJSON omits the zero start time, which keeps healthy status output
// compact while retaining a concrete time.Time API for callers.
func (p RecoveryProgress) MarshalJSON() ([]byte, error) {
	var startedAt *time.Time
	if !p.StartedAt.IsZero() {
		started := p.StartedAt
		startedAt = &started
	}
	return json.Marshal(struct {
		InFlight   bool       `json:"in_flight"`
		ID         string     `json:"id,omitempty"`
		Generation uint64     `json:"generation,omitempty"`
		Attempts   int        `json:"attempts,omitempty"`
		StartedAt  *time.Time `json:"started_at,omitempty"`
		LastError  string     `json:"last_error,omitempty"`
	}{
		InFlight:   p.InFlight,
		ID:         p.ID,
		Generation: p.Generation,
		Attempts:   p.Attempts,
		StartedAt:  startedAt,
		LastError:  p.LastError,
	})
}

// Status is a point-in-time view of the replay fault gate.
type Status struct {
	Fault            *Fault           `json:"fault,omitempty"`
	Blocked          bool             `json:"blocked"`
	Recovery         RecoveryProgress `json:"recovery"`
	PersistenceError error            `json:"-"`
}

// MarshalJSON keeps errors concise and makes status suitable for diagnostics.
func (s Status) MarshalJSON() ([]byte, error) {
	persistenceError := ""
	if s.PersistenceError != nil {
		persistenceError = s.PersistenceError.Error()
	}
	return json.Marshal(struct {
		Fault            *Fault           `json:"fault,omitempty"`
		Blocked          bool             `json:"blocked"`
		Recovery         RecoveryProgress `json:"recovery"`
		PersistenceError string           `json:"persistence_error,omitempty"`
	}{
		Fault:            s.Fault,
		Blocked:          s.Blocked,
		Recovery:         s.Recovery,
		PersistenceError: persistenceError,
	})
}

var (
	// ErrBlocked is returned when a caller tries to validate or publish while
	// an unresolved fault is present.
	ErrBlocked = errors.New("replay fault store is blocked")
	// ErrNoFault is returned when revalidation is requested without a fault.
	ErrNoFault = errors.New("replay fault store has no fault")
	// ErrFaultIDMismatch identifies a stale or unknown fault ID.
	ErrFaultIDMismatch = errors.New("replay fault ID does not match")
	// ErrRevalidationInProgress prevents multiple recovery callbacks from
	// racing to clear one fault.
	ErrRevalidationInProgress = errors.New("replay fault revalidation is already in progress")
	// ErrStaleRevalidation means a Record or Update intervened in recovery.
	ErrStaleRevalidation = errors.New("replay fault revalidation is stale")
	// ErrVerifierRequired prevents callers from clearing a fault without an
	// independent verification callback.
	ErrVerifierRequired = errors.New("replay fault verifier is required")
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
	blocked        bool
	generation     uint64
	recovery       recoveryState
	persistenceErr error
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
	s.fault = &fault
	s.blocked = true
	s.generation = 1
	return s, nil
}

// Record gates immediately on the first unresolved fault. Later records do
// not replace that fault; Update is the explicit enrichment operation.
func (s *Store) Record(fault Fault) error {
	if s == nil {
		return ErrNoFault
	}
	s.operation.Lock()
	defer s.operation.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	if s.fault != nil {
		if s.persistenceErr == nil {
			return nil
		}
		err := s.persistLocked(s.fault)
		s.persistenceErr = err
		return err
	}

	normalized, err := normalizeFault(fault)
	s.fault = cloneFault(&normalized)
	s.blocked = true
	if err != nil {
		s.persistenceErr = err
		return err
	}
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

	next := fault
	next.ID = s.fault.ID
	next.CreatedAt = s.fault.CreatedAt
	next.Attempts = s.fault.Attempts
	if next.Class == "" {
		next.Class = s.fault.Class
	}
	if next.Revision == "" {
		next.Revision = s.fault.Revision
	}
	next.Evidence = cloneRaw(next.Evidence)
	s.fault = cloneFault(&next)
	s.blocked = true
	s.generation++
	if err := s.persistLocked(s.fault); err != nil {
		s.persistenceErr = err
		return err
	}
	s.persistenceErr = nil
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
	return Status{
		Fault:            cloneFault(s.fault),
		Blocked:          s.blocked,
		Recovery:         s.recovery.RecoveryProgress,
		PersistenceError: s.persistenceErr,
	}
}

// WithValidator runs fn while preventing Record, Update, and a successful
// revalidation clear from overlapping it. A blocked store rejects fn.
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

// Revalidate runs one explicit verification attempt. The callback is always
// required and runs without either store mutex held. A successful callback
// only clears the gate when no Record or Update intervened and the durable
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

	s.fault.Attempts++
	generation := s.generation
	fault := *cloneFault(s.fault)
	s.recovery = recoveryState{RecoveryProgress: RecoveryProgress{
		InFlight:   true,
		ID:         id,
		Generation: generation,
		Attempts:   fault.Attempts,
		StartedAt:  time.Now().UTC(),
	}}
	if err := s.persistLocked(s.fault); err != nil {
		s.persistenceErr = err
	}
	s.mu.Unlock()

	err := verify(ctx, fault)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		s.mu.Lock()
		s.recovery.InFlight = false
		s.recovery.LastError = err.Error()
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
	if err := clearDurable(s.path); err != nil {
		s.persistenceErr = err
		s.recovery.InFlight = false
		s.recovery.LastError = err.Error()
		return err
	}
	s.fault = nil
	s.blocked = false
	s.persistenceErr = nil
	s.recovery.InFlight = false
	s.recovery.LastError = ""
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
	return &copyOfFault
}

func (s *Store) persistLocked(fault *Fault) error {
	if s.path == "" {
		return nil
	}
	return writeAtomic(s.path, fault)
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
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync replay fault directory: %w", err)
	}
	return nil
}

func clearDurable(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove replay fault: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync replay fault directory after clear: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}
