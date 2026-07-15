package peermanagement

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateIdentityPersistsAcrossRestarts(t *testing.T) {
	dataDir := t.TempDir()

	first, err := loadOrCreateIdentity(dataDir)
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	second, err := loadOrCreateIdentity(dataDir)
	if err != nil {
		t.Fatalf("reload identity: %v", err)
	}

	if first.PrivateKeyHex() != second.PrivateKeyHex() {
		t.Fatal("reloaded identity does not match the persisted identity")
	}
	info, err := os.Stat(filepath.Join(dataDir, "node_identity.key"))
	if err != nil {
		t.Fatalf("stat identity file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("identity file permissions = %o, want 600", got)
	}
}

func TestLoadOrCreateIdentityReturnsPersistenceError(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dataDir, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}

	if _, err := loadOrCreateIdentity(dataDir); err == nil {
		t.Fatal("expected identity persistence error")
	}
}

func TestLoadOrCreateIdentityReplacesCorruptIdentity(t *testing.T) {
	dataDir := t.TempDir()
	keyPath := filepath.Join(dataDir, "node_identity.key")
	want := []byte("not-a-private-key")
	if err := os.WriteFile(keyPath, want, 0o600); err != nil {
		t.Fatalf("write corrupt identity: %v", err)
	}

	id, err := loadOrCreateIdentity(dataDir)
	if err != nil {
		t.Fatalf("replace corrupt identity: %v", err)
	}
	got, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read preserved identity: %v", err)
	}
	if string(got) == string(want) {
		t.Fatal("corrupt identity was not replaced")
	}
	if string(got) != id.PrivateKeyHex() {
		t.Fatalf("persisted identity = %q, want %q", got, id.PrivateKeyHex())
	}
}

func TestLoadOrCreateIdentityPreservesUnreadableIdentityPath(t *testing.T) {
	dataDir := t.TempDir()
	keyPath := filepath.Join(dataDir, "node_identity.key")
	if err := os.Mkdir(keyPath, 0o700); err != nil {
		t.Fatalf("create identity directory: %v", err)
	}

	if _, err := loadOrCreateIdentity(dataDir); err == nil {
		t.Fatal("expected identity read error")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat preserved identity path: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("identity path was replaced")
	}
}
