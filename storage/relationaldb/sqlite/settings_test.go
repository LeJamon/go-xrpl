package sqlite

import (
	"context"
	"testing"
)

func TestSettingsReachPragmas(t *testing.T) {
	rm, err := NewRepositoryManager(context.Background(), t.TempDir(), Settings{
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
	t.Cleanup(func() { _ = rm.Close() })

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

func TestSettingsValidatedByConstructor(t *testing.T) {
	for _, settings := range []Settings{
		{JournalMode: "wal; DROP TABLE ledgers"},
		{Synchronous: "sometimes"},
		{TempStore: "disk"},
		{PageSize: 513},
		{JournalSizeLimit: -1},
	} {
		if rm, err := NewRepositoryManager(context.Background(), t.TempDir(), settings); err == nil {
			_ = rm.Close()
			t.Fatalf("accepted invalid settings: %+v", settings)
		}
	}
}

// TestDefaultSettingsUnchanged pins the zero-value Settings behaviour to
// the rippled-aligned default pragmas (wal / normal / file).
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
