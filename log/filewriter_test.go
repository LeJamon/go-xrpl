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
