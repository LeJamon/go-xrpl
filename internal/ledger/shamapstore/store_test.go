package shamapstore

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if err := s.SetLastRotated(800); err != nil {
		t.Fatalf("SetLastRotated: %v", err)
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
