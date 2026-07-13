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
