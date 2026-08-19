package sqlite

import (
	"context"
	"testing"
)

func TestSchemaFingerprintBindsInstanceAndVersions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rm, err := NewRepositoryManager(ctx, dir, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := rm.SchemaFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewRepositoryManager(ctx, dir, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	reopenedFingerprint, err := reopened.SchemaFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedFingerprint != first {
		t.Fatal("schema fingerprint changed after reopening the same repository")
	}
	if _, err := reopened.ledgerDB.ExecContext(ctx, "PRAGMA user_version = 4"); err != nil {
		t.Fatal(err)
	}
	changedVersion, err := reopened.SchemaFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changedVersion == first {
		t.Fatal("schema version change did not alter fingerprint")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	replacement, err := NewRepositoryManager(ctx, t.TempDir(), Settings{})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	replacementFingerprint, err := replacement.SchemaFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if replacementFingerprint == first {
		t.Fatal("replacement repository reused the persisted instance identity")
	}
}
