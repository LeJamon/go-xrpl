// Package shamapstore implements go-xrpl's online-delete subsystem — the
// go-xrpl equivalent of rippled's SHAMapStore (src/xrpld/app/misc/SHAMapStore.h).
//
// Two pieces live here:
//
//   - Store, the advisory-delete state the can_delete RPC reads and writes. It
//     tracks retention state, gated by the node_db advisory_delete config
//     flag and persisted across restarts (mirroring rippled's SavedStateDB):
//     canDelete (the advisory boundary: ledgers at or below it are unprotected
//     and online delete may remove them) and lastRotated (the most recent
//     ledger online delete has rotated; 0 until the first rotation), and the
//     minimum online ledger retained after the most recent deletion.
//   - Rotator, the background job that consumes those boundaries to actually
//     reclaim disk: every node_db online_delete validated ledgers it deletes
//     complete ledgers below the rotation boundary from the node store and the
//     relational index, advancing lastRotated (rippled SHAMapStoreImp's run
//     loop).
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
	saveMu         sync.Mutex
	advisoryDelete bool
	canDelete      uint32
	lastRotated    uint32
	minimumOnline  uint32
	filePath       string
	persist        func(persistedState) (bool, error)
	fileOps        stateFileOps
}

type persistedState struct {
	CanDelete     uint32 `json:"can_delete"`
	LastRotated   uint32 `json:"last_rotated"`
	MinimumOnline uint32 `json:"minimum_online,omitempty"`
}

type persistedFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
	Name() string
}

type stateDirectory interface {
	Sync() error
	Close() error
}

type stateFileOps struct {
	mkdirAll  func(string, os.FileMode) error
	createTmp func(string, string) (persistedFile, error)
	rename    func(string, string) error
	openDir   func(string) (stateDirectory, error)
	remove    func(string) error
}

func defaultStateFileOps() stateFileOps {
	return stateFileOps{
		mkdirAll: os.MkdirAll,
		createTmp: func(dir, pattern string) (persistedFile, error) {
			return os.CreateTemp(dir, pattern)
		},
		rename: os.Rename,
		openDir: func(path string) (stateDirectory, error) {
			return os.Open(path)
		},
		remove: os.Remove,
	}
}

// New constructs the advisory-delete state store. advisoryDelete reflects the
// node_db advisory_delete config flag. dataDir is the filesystem directory
// used for persistence; an empty dataDir disables persistence (in-memory only, e.g.
// standalone / tests). Any previously persisted state is loaded immediately.
func New(advisoryDelete bool, dataDir string) (*Store, error) {
	s := &Store{
		advisoryDelete: advisoryDelete,
		fileOps:        defaultStateFileOps(),
	}
	if dataDir != "" {
		s.filePath = filepath.Join(dataDir, stateFile)
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	s.persist = s.persistState
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
	var stored uint32
	err := s.update(func(next *persistedState) {
		if s.advisoryDelete {
			next.CanDelete = seq
		}
		stored = next.CanDelete
	})
	if err != nil {
		stored = s.GetCanDelete()
	}
	return stored, err
}

// GetLastRotated returns the most recently rotated ledger sequence (0 until
// the first rotation). Mirrors SHAMapStore::getLastRotated().
func (s *Store) GetLastRotated() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRotated
}

// SetLastRotated records the last rotated ledger and persists it. Advanced by
// the online-delete rotation subsystem (see Rotator); can_delete never advances
// lastRotated.
func (s *Store) SetLastRotated(seq uint32) error {
	return s.update(func(next *persistedState) {
		if seq > next.LastRotated {
			next.LastRotated = seq
		}
	})
}

func (s *Store) GetMinimumOnline() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.minimumOnline
}

// SetMinimumOnline durably advances the lowest retained ledger before online
// deletion starts removing records below it.
func (s *Store) SetMinimumOnline(seq uint32) error {
	return s.update(func(next *persistedState) {
		if seq > next.MinimumOnline {
			next.MinimumOnline = seq
		}
	})
}

func (s *Store) SetRotation(lastRotated, minimumOnline uint32) error {
	return s.update(func(next *persistedState) {
		if lastRotated > next.LastRotated {
			next.LastRotated = lastRotated
		}
		minimumOnline = max(minimumOnline, rotationMinimum(next.LastRotated))
		if minimumOnline > next.MinimumOnline {
			next.MinimumOnline = minimumOnline
		}
	})
}

func rotationMinimum(lastRotated uint32) uint32 {
	if lastRotated == 0 {
		return 0
	}
	if lastRotated == ^uint32(0) {
		return lastRotated
	}
	return lastRotated + 1
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
	s.minimumOnline = max(ps.MinimumOnline, rotationMinimum(ps.LastRotated))
	return nil
}

func (s *Store) update(change func(*persistedState)) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	s.mu.RLock()
	next := persistedState{
		CanDelete:     s.canDelete,
		LastRotated:   s.lastRotated,
		MinimumOnline: s.minimumOnline,
	}
	s.mu.RUnlock()
	change(&next)

	committed, err := s.persist(next)
	if !committed {
		return err
	}
	s.mu.Lock()
	s.canDelete = next.CanDelete
	s.lastRotated = next.LastRotated
	s.minimumOnline = next.MinimumOnline
	s.mu.Unlock()
	return err
}

func (s *Store) persistState(next persistedState) (bool, error) {
	if s.filePath == "" {
		return true, nil
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return false, err
	}
	if err := s.fileOps.mkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		return false, err
	}
	dir := filepath.Dir(s.filePath)
	tmp, err := s.fileOps.createTmp(dir, ".advisory-delete-*")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer s.fileOps.remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := s.fileOps.rename(tmpPath, s.filePath); err != nil {
		return false, err
	}
	dirFile, err := s.fileOps.openDir(dir)
	if err != nil {
		return true, err
	}
	defer dirFile.Close()
	return true, dirFile.Sync()
}
