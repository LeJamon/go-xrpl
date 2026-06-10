package sqlite

import (
	"context"
	"testing"
)

// TestSettingsReachPragmas verifies the [sqlite] tuning wiring: settings
// passed to NewRepositoryManagerWithSettings must be observable as live
// PRAGMA values on the opened databases.
func TestSettingsReachPragmas(t *testing.T) {
	rm, err := NewRepositoryManagerWithSettings(t.TempDir(), Settings{
		JournalMode:      "truncate",
		Synchronous:      "full",
		TempStore:        "file",
		PageSize:         8192,
		JournalSizeLimit: 65536,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := rm.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rm.Close(ctx) })

	var journalMode string
	if err := rm.ledgerDB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "truncate" {
		t.Errorf("journal_mode = %q, want truncate", journalMode)
	}

	var synchronous int
	if err := rm.ledgerDB.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 { // FULL
		t.Errorf("synchronous = %d, want 2 (full)", synchronous)
	}

	var tempStore int
	if err := rm.ledgerDB.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&tempStore); err != nil {
		t.Fatal(err)
	}
	if tempStore != 1 { // FILE
		t.Errorf("temp_store = %d, want 1 (file)", tempStore)
	}

	var pageSize int
	if err := rm.ledgerDB.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if pageSize != 8192 {
		t.Errorf("page_size = %d, want 8192", pageSize)
	}

	var journalSizeLimit int
	if err := rm.txDB.QueryRowContext(ctx, "PRAGMA journal_size_limit").Scan(&journalSizeLimit); err != nil {
		t.Fatal(err)
	}
	if journalSizeLimit != 65536 {
		t.Errorf("journal_size_limit = %d, want 65536", journalSizeLimit)
	}
}

// TestDefaultSettingsUnchanged pins the zero-value Settings behaviour to
// the historical hardcoded pragmas (wal / normal / memory).
func TestDefaultSettingsUnchanged(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	var journalMode string
	if err := rm.ledgerDB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var synchronous int
	if err := rm.ledgerDB.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 1 { // NORMAL
		t.Errorf("synchronous = %d, want 1 (normal)", synchronous)
	}
}
