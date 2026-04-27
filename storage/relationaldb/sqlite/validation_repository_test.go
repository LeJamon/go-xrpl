package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

func mkValidationRecord(ledgerSeq uint32, nodeByte byte) *relationaldb.ValidationRecord {
	rec := &relationaldb.ValidationRecord{
		LedgerSeq:  relationaldb.LedgerIndex(ledgerSeq),
		InitialSeq: relationaldb.LedgerIndex(ledgerSeq - 1),
		NodePubKey: make([]byte, 33),
		SignTime:   time.Unix(1700000000, 0).UTC(),
		SeenTime:   time.Unix(1700000005, 0).UTC(),
		Flags:      0x80000001,
		// Raw includes the signature in the canonical wire format —
		// the schema has no separate signature column.
		Raw: []byte{0xDE, 0xAD, 0xBE, 0xEF, byte(ledgerSeq), nodeByte},
	}
	rec.LedgerHash[0] = byte(ledgerSeq)
	rec.LedgerHash[31] = nodeByte
	rec.NodePubKey[0] = 0x02
	rec.NodePubKey[32] = nodeByte
	return rec
}

func recordsEqual(a, b *relationaldb.ValidationRecord) bool {
	if a.LedgerSeq != b.LedgerSeq || a.InitialSeq != b.InitialSeq || a.Flags != b.Flags {
		return false
	}
	if a.LedgerHash != b.LedgerHash {
		return false
	}
	if !a.SignTime.Equal(b.SignTime) || !a.SeenTime.Equal(b.SeenTime) {
		return false
	}
	if !byteEq(a.NodePubKey, b.NodePubKey) || !byteEq(a.Raw, b.Raw) {
		return false
	}
	return true
}

func byteEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestValidationRepository_SaveAndGet(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	repo := rm.Validation()
	if repo == nil {
		t.Fatal("Validation() returned nil — repo not wired")
	}

	orig := mkValidationRecord(100, 0x01)
	if err := repo.Save(ctx, orig); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetValidationsForLedger(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if !recordsEqual(orig, got[0]) {
		t.Fatalf("roundtrip mismatch:\n got  %+v\n want %+v", got[0], orig)
	}
}

func TestValidationRepository_DuplicateIsNoop(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	repo := rm.Validation()

	rec := mkValidationRecord(100, 0x01)
	if err := repo.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	// Re-save identical record — must not error, must not double-insert.
	if err := repo.Save(ctx, rec); err != nil {
		t.Fatalf("duplicate save errored: %v", err)
	}

	count, err := repo.GetValidationCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after duplicate save, got %d", count)
	}
}

func TestValidationRepository_SaveBatch(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	repo := rm.Validation()

	batch := []*relationaldb.ValidationRecord{
		mkValidationRecord(100, 0x01),
		mkValidationRecord(100, 0x02),
		mkValidationRecord(101, 0x01),
		mkValidationRecord(100, 0x01), // duplicate in batch
	}

	if err := repo.SaveBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}

	count, err := repo.GetValidationCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected 3 unique rows after batch, got %d", count)
	}
}

func TestValidationRepository_GetByValidator_RespectsLimit(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	repo := rm.Validation()

	for seq := uint32(100); seq < 110; seq++ {
		if err := repo.Save(ctx, mkValidationRecord(seq, 0x01)); err != nil {
			t.Fatal(err)
		}
	}

	key := make([]byte, 33)
	key[0] = 0x02
	key[32] = 0x01

	rows, err := repo.GetValidationsByValidator(ctx, key, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("limit ignored: got %d rows, expected 3", len(rows))
	}
	// Descending order: newest (109) first.
	if rows[0].LedgerSeq != 109 || rows[2].LedgerSeq != 107 {
		t.Fatalf("expected DESC ordering 109..107, got %d..%d", rows[0].LedgerSeq, rows[2].LedgerSeq)
	}

	rows, err = repo.GetValidationsByValidator(ctx, key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 10 {
		t.Fatalf("limit=0 should be unbounded: got %d rows, expected 10", len(rows))
	}
}

func TestValidationRepository_DeleteOlderThanSeq_Bounded(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	repo := rm.Validation()

	// Insert 20 rows at seqs 1..20, distinct validators so they all land.
	for seq := uint32(1); seq <= 20; seq++ {
		rec := mkValidationRecord(seq, byte(seq))
		if err := repo.Save(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	// Bounded sweep: delete up to 5 rows with seq < 15.
	n, err := repo.DeleteOlderThanSeq(ctx, 15, 5)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("expected 5 deletions, got %d", n)
	}

	// Remaining: 20 - 5 = 15 rows.
	count, err := repo.GetValidationCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 15 {
		t.Fatalf("expected 15 rows remaining, got %d", count)
	}

	// Unbounded sweep: remove the rest of the <15 rows (9 more).
	n, err = repo.DeleteOlderThanSeq(ctx, 15, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 9 {
		t.Fatalf("expected 9 deletions in unbounded sweep, got %d", n)
	}

	// All remaining rows should be seqs 15..20.
	count, err = repo.GetValidationCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("expected 6 rows after full sweep, got %d", count)
	}
}
