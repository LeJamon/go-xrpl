package shamapstore

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type faultStateFile struct {
	persistedFile
	writeErr error
	syncErr  error
	closeErr error
}

func (f *faultStateFile) Write(data []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.persistedFile.Write(data)
}

func (f *faultStateFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.persistedFile.Sync()
}

func (f *faultStateFile) Close() error {
	if f.closeErr != nil {
		_ = f.persistedFile.Close()
		return f.closeErr
	}
	return f.persistedFile.Close()
}

type faultStateDirectory struct {
	stateDirectory
	syncErr error
}

func (d *faultStateDirectory) Sync() error {
	if d.syncErr != nil {
		return d.syncErr
	}
	return d.stateDirectory.Sync()
}

func TestStore_DisabledAdvisoryDelete(t *testing.T) {
	s, err := New(false, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.AdvisoryDelete() {
		t.Fatal("AdvisoryDelete should be false")
	}
	// setCanDelete is a no-op for the in-memory value when advisory delete is
	// off (mirrors rippled SHAMapStoreImp::setCanDelete).
	got, err := s.SetCanDelete(100)
	if err != nil {
		t.Fatalf("SetCanDelete: %v", err)
	}
	if got != 0 || s.GetCanDelete() != 0 {
		t.Fatalf("canDelete should stay 0 when advisory delete is off, got %d", got)
	}
}

func TestStore_SetGetCanDelete(t *testing.T) {
	s, err := New(true, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !s.AdvisoryDelete() {
		t.Fatal("AdvisoryDelete should be true")
	}
	if got := s.GetCanDelete(); got != 0 {
		t.Fatalf("initial canDelete = %d, want 0", got)
	}
	got, err := s.SetCanDelete(12345)
	if err != nil {
		t.Fatalf("SetCanDelete: %v", err)
	}
	if got != 12345 || s.GetCanDelete() != 12345 {
		t.Fatalf("canDelete = %d, want 12345", s.GetCanDelete())
	}
}

func TestStore_LastRotated(t *testing.T) {
	s, _ := New(true, "")
	if got := s.GetLastRotated(); got != 0 {
		t.Fatalf("initial lastRotated = %d, want 0", got)
	}
	if err := s.SetLastRotated(777); err != nil {
		t.Fatalf("SetLastRotated: %v", err)
	}
	if got := s.GetLastRotated(); got != 777 {
		t.Fatalf("lastRotated = %d, want 777", got)
	}
}

func TestStore_PersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := New(true, dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.SetCanDelete(900); err != nil {
		t.Fatalf("SetCanDelete: %v", err)
	}
	if err := s.SetRotation(800, 501); err != nil {
		t.Fatalf("SetRotation: %v", err)
	}

	// Reopen from the same dir; state must survive.
	reopened, err := New(true, dir)
	if err != nil {
		t.Fatalf("reopen New: %v", err)
	}
	if got := reopened.GetCanDelete(); got != 900 {
		t.Fatalf("reloaded canDelete = %d, want 900", got)
	}
	if got := reopened.GetLastRotated(); got != 800 {
		t.Fatalf("reloaded lastRotated = %d, want 800", got)
	}
	if got := reopened.GetMinimumOnline(); got != 501 {
		t.Fatalf("reloaded minimumOnline = %d, want 501", got)
	}

	// The state file lives under database_path.
	if _, statErr := os.ReadFile(filepath.Join(dir, stateFile)); statErr != nil {
		t.Fatalf("state file not written: %v", statErr)
	}
}

func TestStore_ReloadIgnoresCanDeleteWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	enabled, _ := New(true, dir)
	if _, err := enabled.SetCanDelete(500); err != nil {
		t.Fatalf("SetCanDelete: %v", err)
	}
	if err := enabled.SetLastRotated(400); err != nil {
		t.Fatalf("SetLastRotated: %v", err)
	}

	// A node that reads the same state with advisory delete OFF must not
	// honor the persisted canDelete (mirrors SHAMapStoreImp.cpp:275-276), but
	// lastRotated is still loaded.
	disabled, err := New(false, dir)
	if err != nil {
		t.Fatalf("New disabled: %v", err)
	}
	if got := disabled.GetCanDelete(); got != 0 {
		t.Fatalf("canDelete = %d, want 0 when advisory delete disabled", got)
	}
	if got := disabled.GetLastRotated(); got != 400 {
		t.Fatalf("lastRotated = %d, want 400", got)
	}
}

func TestStore_PersistenceFailureDoesNotPublishState(t *testing.T) {
	s, err := New(true, "")
	if err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("write failed")
	s.persist = func(persistedState) (bool, error) {
		return false, writeErr
	}

	if got, err := s.SetCanDelete(900); !errors.Is(err, writeErr) || got != 0 {
		t.Fatalf("SetCanDelete = (%d, %v), want (0, %v)", got, err, writeErr)
	}
	if got := s.GetCanDelete(); got != 0 {
		t.Fatalf("canDelete published before persistence: %d", got)
	}
	if err := s.SetRotation(800, 501); !errors.Is(err, writeErr) {
		t.Fatalf("SetRotation error = %v, want %v", err, writeErr)
	}
	if got := s.GetLastRotated(); got != 0 {
		t.Fatalf("lastRotated published before persistence: %d", got)
	}
	if got := s.GetMinimumOnline(); got != 0 {
		t.Fatalf("minimumOnline published before persistence: %d", got)
	}
}

func TestStore_PostCommitFailurePublishesState(t *testing.T) {
	s, err := New(true, "")
	if err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("directory sync failed")
	s.persist = func(persistedState) (bool, error) {
		return true, syncErr
	}

	if err := s.SetRotation(800, 501); !errors.Is(err, syncErr) {
		t.Fatalf("SetRotation error = %v, want %v", err, syncErr)
	}
	if got := s.GetLastRotated(); got != 800 {
		t.Fatalf("lastRotated = %d, want committed value 800", got)
	}
	if got := s.GetMinimumOnline(); got != 501 {
		t.Fatalf("minimumOnline = %d, want committed value 501", got)
	}
}

func TestStore_RejectsMalformedPersistedState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(true, dir); err == nil {
		t.Fatal("New accepted malformed persisted state")
	}
}

func TestStore_PreCommitFailuresPreserveMemoryAndRestartState(t *testing.T) {
	writeErr := errors.New("injected persistence failure")
	tests := []struct {
		name   string
		inject func(*Store)
	}{
		{
			name: "mkdir",
			inject: func(s *Store) {
				s.fileOps.mkdirAll = func(string, os.FileMode) error { return writeErr }
			},
		},
		{
			name: "create temp",
			inject: func(s *Store) {
				s.fileOps.createTmp = func(string, string) (persistedFile, error) {
					return nil, writeErr
				}
			},
		},
		{
			name: "write",
			inject: func(s *Store) {
				createTmp := s.fileOps.createTmp
				s.fileOps.createTmp = func(dir, pattern string) (persistedFile, error) {
					file, err := createTmp(dir, pattern)
					if err != nil {
						return nil, err
					}
					return &faultStateFile{persistedFile: file, writeErr: writeErr}, nil
				}
			},
		},
		{
			name: "file sync",
			inject: func(s *Store) {
				createTmp := s.fileOps.createTmp
				s.fileOps.createTmp = func(dir, pattern string) (persistedFile, error) {
					file, err := createTmp(dir, pattern)
					if err != nil {
						return nil, err
					}
					return &faultStateFile{persistedFile: file, syncErr: writeErr}, nil
				}
			},
		},
		{
			name: "file close",
			inject: func(s *Store) {
				createTmp := s.fileOps.createTmp
				s.fileOps.createTmp = func(dir, pattern string) (persistedFile, error) {
					file, err := createTmp(dir, pattern)
					if err != nil {
						return nil, err
					}
					return &faultStateFile{persistedFile: file, closeErr: writeErr}, nil
				}
			},
		},
		{
			name: "rename",
			inject: func(s *Store) {
				s.fileOps.rename = func(string, string) error { return writeErr }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := New(true, dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetCanDelete(100); err != nil {
				t.Fatal(err)
			}
			if err := s.SetRotation(200, 101); err != nil {
				t.Fatal(err)
			}

			test.inject(s)
			if err := s.SetRotation(800, 501); !errors.Is(err, writeErr) {
				t.Fatalf("SetRotation error = %v, want %v", err, writeErr)
			}
			if got := s.GetLastRotated(); got != 200 {
				t.Fatalf("in-memory lastRotated = %d, want 200", got)
			}
			if got := s.GetMinimumOnline(); got != 101 {
				t.Fatalf("in-memory minimumOnline = %d, want 101", got)
			}

			reopened, err := New(true, dir)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			if got := reopened.GetCanDelete(); got != 100 {
				t.Fatalf("reopened canDelete = %d, want 100", got)
			}
			if got := reopened.GetLastRotated(); got != 200 {
				t.Fatalf("reopened lastRotated = %d, want 200", got)
			}
			if got := reopened.GetMinimumOnline(); got != 101 {
				t.Fatalf("reopened minimumOnline = %d, want 101", got)
			}
		})
	}
}

func TestStore_PostCommitFailuresPublishRestartState(t *testing.T) {
	syncErr := errors.New("injected directory persistence failure")
	tests := []struct {
		name   string
		inject func(*Store)
	}{
		{
			name: "open directory",
			inject: func(s *Store) {
				s.fileOps.openDir = func(string) (stateDirectory, error) {
					return nil, syncErr
				}
			},
		},
		{
			name: "sync directory",
			inject: func(s *Store) {
				openDir := s.fileOps.openDir
				s.fileOps.openDir = func(path string) (stateDirectory, error) {
					dir, err := openDir(path)
					if err != nil {
						return nil, err
					}
					return &faultStateDirectory{stateDirectory: dir, syncErr: syncErr}, nil
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := New(true, dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetRotation(200, 101); err != nil {
				t.Fatal(err)
			}

			test.inject(s)
			if err := s.SetRotation(800, 501); !errors.Is(err, syncErr) {
				t.Fatalf("SetRotation error = %v, want %v", err, syncErr)
			}
			if got := s.GetLastRotated(); got != 800 {
				t.Fatalf("in-memory lastRotated = %d, want 800", got)
			}
			if got := s.GetMinimumOnline(); got != 501 {
				t.Fatalf("in-memory minimumOnline = %d, want 501", got)
			}

			reopened, err := New(true, dir)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			if got := reopened.GetLastRotated(); got != 800 {
				t.Fatalf("reopened lastRotated = %d, want 800", got)
			}
			if got := reopened.GetMinimumOnline(); got != 501 {
				t.Fatalf("reopened minimumOnline = %d, want 501", got)
			}
		})
	}
}

func TestStore_ConcurrentSettersPersistPublishedState(t *testing.T) {
	dir := t.TempDir()
	s, err := New(true, dir)
	if err != nil {
		t.Fatal(err)
	}

	const writers = 32
	errs := make(chan error, writers*2)
	var wg sync.WaitGroup
	for i := range writers {
		seq := uint32(i + 1)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := s.SetCanDelete(seq)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			errs <- s.SetRotation(1000+seq, 500+seq)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	reopened, err := New(true, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reopened.GetCanDelete(), s.GetCanDelete(); got != want {
		t.Fatalf("reopened canDelete = %d, want published %d", got, want)
	}
	if got, want := reopened.GetLastRotated(), s.GetLastRotated(); got != want {
		t.Fatalf("reopened lastRotated = %d, want published %d", got, want)
	}
	if got, want := reopened.GetMinimumOnline(), s.GetMinimumOnline(); got != want {
		t.Fatalf("reopened minimumOnline = %d, want published %d", got, want)
	}
}
