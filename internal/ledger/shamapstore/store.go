// Package shamapstore implements goXRPL's advisory-delete state — the subset
// of rippled's SHAMapStore (src/xrpld/app/misc/SHAMapStore.h) that the
// can_delete RPC reads and writes.
//
// It tracks two ledger sequences, gated by the node_db advisory_delete config
// flag and persisted across restarts (mirroring rippled's SavedStateDB):
//
//   - canDelete   the advisory boundary: all ledgers at or below this seq are
//     unprotected and online delete may remove them.
//   - lastRotated the most recent ledger online delete has rotated; 0 until
//     the first rotation.
//
// The background rotation that consumes canDelete to actually delete old
// ledger nodes (rippled SHAMapStoreImp's run loop) is a separate subsystem and
// is intentionally not part of this state layer — can_delete only manages the
// advisory boundary, exactly as rippled's CanDelete.cpp does.
package shamapstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// stateFile is the JSON file holding the persisted advisory-delete state,
// written under the configured database_path. Mirrors the role of rippled's
// SavedStateDB SQLite table.
const stateFile = "advisory_delete_state.json"

// Store holds the advisory-delete state. It is safe for concurrent use.
type Store struct {
	mu             sync.RWMutex
	advisoryDelete bool
	canDelete      uint32
	lastRotated    uint32
	filePath       string
}

type persistedState struct {
	CanDelete   uint32 `json:"can_delete"`
	LastRotated uint32 `json:"last_rotated"`
}

// New constructs the advisory-delete state store. advisoryDelete reflects the
// node_db advisory_delete config flag. dataDir is the database_path used for
// persistence; an empty dataDir disables persistence (in-memory only, e.g.
// standalone / tests). Any previously persisted state is loaded immediately.
func New(advisoryDelete bool, dataDir string) (*Store, error) {
	s := &Store{advisoryDelete: advisoryDelete}
	if dataDir != "" {
		s.filePath = filepath.Join(dataDir, stateFile)
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// AdvisoryDelete reports whether advisory delete is enabled. Mirrors
// SHAMapStore::advisoryDelete().
func (s *Store) AdvisoryDelete() bool { return s.advisoryDelete }

// GetCanDelete returns the current advisory deletion boundary. Mirrors
// SHAMapStore::getCanDelete().
func (s *Store) GetCanDelete() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.canDelete
}

// SetCanDelete records a new advisory deletion boundary, persists it, and
// returns the stored value. Mirrors SHAMapStore::setCanDelete() — the value is
// only retained while advisory delete is enabled.
func (s *Store) SetCanDelete(seq uint32) (uint32, error) {
	s.mu.Lock()
	if s.advisoryDelete {
		s.canDelete = seq
	}
	stored := s.canDelete
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return stored, err
	}
	return stored, nil
}

// GetLastRotated returns the most recently rotated ledger sequence (0 until
// the first rotation). Mirrors SHAMapStore::getLastRotated().
func (s *Store) GetLastRotated() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRotated
}

// SetLastRotated records the last rotated ledger and persists it. Reserved for
// the online-delete rotation subsystem (rippled SHAMapStoreImp); can_delete
// never advances lastRotated.
func (s *Store) SetLastRotated(seq uint32) error {
	s.mu.Lock()
	s.lastRotated = seq
	s.mu.Unlock()
	return s.save()
}

func (s *Store) load() error {
	if s.filePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var ps persistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// canDelete is only honored while advisory delete is enabled, mirroring
	// rippled which loads canDelete_ from the state db only when
	// advisoryDelete_ is set (SHAMapStoreImp.cpp:275-276).
	if s.advisoryDelete {
		s.canDelete = ps.CanDelete
	}
	s.lastRotated = ps.LastRotated
	return nil
}

func (s *Store) save() error {
	if s.filePath == "" {
		return nil
	}
	s.mu.RLock()
	ps := persistedState{CanDelete: s.canDelete, LastRotated: s.lastRotated}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0o644)
}
