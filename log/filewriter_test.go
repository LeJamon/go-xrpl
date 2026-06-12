package log

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileWriterRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	fw, err := NewFileWriter(path)
	if err != nil {
		t.Fatalf("NewFileWriter: %v", err)
	}
	if _, err := fw.Write([]byte("first\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Simulate external rotation tooling renaming the active file, then ask
	// the writer to reopen the original path.
	rotated := path + ".1"
	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := fw.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := fw.Write([]byte("second\n")); err != nil {
		t.Fatalf("Write after rotate: %v", err)
	}

	newData, _ := os.ReadFile(path)
	oldData, _ := os.ReadFile(rotated)
	if !strings.Contains(string(newData), "second") || strings.Contains(string(newData), "first") {
		t.Errorf("reopened file = %q, want only the post-rotate write", newData)
	}
	if !strings.Contains(string(oldData), "first") {
		t.Errorf("rotated file = %q, want the pre-rotate write", oldData)
	}
}

func TestFileWriterRotateReopenFailure(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "logs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	fw, err := NewFileWriter(filepath.Join(sub, "app.log"))
	if err != nil {
		t.Fatalf("NewFileWriter: %v", err)
	}

	// Drop the parent directory so the reopen during Rotate cannot succeed.
	if err := os.RemoveAll(sub); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := fw.Rotate(); err == nil {
		t.Fatal("Rotate() = nil, want error when the reopen fails")
	}

	// A write after a failed reopen must drop silently (mirroring rippled's
	// null-stream guard), never panic on a nil descriptor.
	if n, err := fw.Write([]byte("dropped\n")); err != nil || n != len("dropped\n") {
		t.Errorf("Write after failed rotate = (%d, %v), want (%d, nil)", n, err, len("dropped\n"))
	}
}

func TestFileWriterSync(t *testing.T) {
	dir := t.TempDir()
	fw, err := NewFileWriter(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatalf("NewFileWriter: %v", err)
	}
	if _, err := fw.Write([]byte("data\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := fw.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// After a failed reopen the descriptor is nil; Sync must no-op, not panic.
	fw.mu.Lock()
	_ = fw.f.Close()
	fw.f = nil
	fw.mu.Unlock()
	if err := fw.Sync(); err != nil {
		t.Errorf("Sync with no descriptor = %v, want nil", err)
	}
}

func TestPackageSync(t *testing.T) {
	prev := rootCfg.Load()
	t.Cleanup(func() { rootCfg.Store(prev) })

	// Unset root config → no-op.
	rootCfg.Store(nil)
	if err := Sync(); err != nil {
		t.Errorf("Sync() with no root config = %v, want nil", err)
	}

	// Non-file output → no-op (stdout/stderr are synced via their own fds).
	rootCfg.Store(&Config{Output: os.Stdout})
	if err := Sync(); err != nil {
		t.Errorf("Sync() with stdout = %v, want nil", err)
	}

	// File-backed output → flushes cleanly.
	fw, err := NewFileWriter(filepath.Join(t.TempDir(), "root.log"))
	if err != nil {
		t.Fatalf("NewFileWriter: %v", err)
	}
	rootCfg.Store(&Config{Output: fw})
	if err := Sync(); err != nil {
		t.Errorf("Sync() with file output = %v, want nil", err)
	}
}

func TestRotateRootConfig(t *testing.T) {
	prev := rootCfg.Load()
	t.Cleanup(func() { rootCfg.Store(prev) })

	// Not file-backed → ErrLogNotRotatable.
	rootCfg.Store(&Config{Output: os.Stdout})
	if err := Rotate(); !errors.Is(err, ErrLogNotRotatable) {
		t.Errorf("Rotate() with stdout = %v, want ErrLogNotRotatable", err)
	}

	// File-backed → rotates cleanly.
	fw, err := NewFileWriter(filepath.Join(t.TempDir(), "root.log"))
	if err != nil {
		t.Fatalf("NewFileWriter: %v", err)
	}
	rootCfg.Store(&Config{Output: fw})
	if err := Rotate(); err != nil {
		t.Errorf("Rotate() with file output = %v, want nil", err)
	}
}
