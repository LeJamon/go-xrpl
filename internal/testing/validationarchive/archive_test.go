// Package validationarchive holds end-to-end tests for the on-disk
// validation archive (issue #267). These wire a real ValidationTracker,
// a real archive.Archive, and a real SQLite-backed ValidationRepository
// — no mocks — so the tests document the integration contract.
package validationarchive

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/archive"
	"github.com/LeJamon/goXRPLd/internal/consensus/rcl"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
	"github.com/LeJamon/goXRPLd/storage/relationaldb/sqlite"
)

func openArchiveDB(t *testing.T) *sqlite.RepositoryManager {
	t.Helper()
	rm, err := sqlite.NewRepositoryManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rm.Close(context.Background()) })
	return rm
}

func mkValidation(seq uint32, node byte) *consensus.Validation {
	v := &consensus.Validation{
		LedgerSeq: seq,
		Full:      true,
		SignTime:  time.Now(),
		Signature: []byte{0xAB, 0xCD, byte(seq), node},
		Raw:       []byte{0xFE, 0xED, byte(seq >> 8), byte(seq), node},
	}
	v.LedgerID[0] = byte(seq >> 8)
	v.LedgerID[1] = byte(seq)
	v.LedgerID[31] = node
	// SigningPubKey: synthetic 33-byte compressed key (0x02 prefix +
	// node-discriminator). This drives the archive's NodePubKey column;
	// NodeID is the calcNodeID derivation used by trust map keying.
	v.SigningPubKey[0] = 0x02
	v.SigningPubKey[32] = node
	v.NodeID = consensus.CalcNodeID([33]byte(v.SigningPubKey))
	return v
}

// TestValidationArchive_StaleValidationWrittenOnPrune is the first of
// the three acceptance tests called out in issue #267.
func TestValidationArchive_StaleValidationWrittenOnPrune(t *testing.T) {
	rm := openArchiveDB(t)
	repo := rm.Validation()

	a := archive.New(repo, archive.Config{
		BatchSize:     1,
		FlushInterval: 10 * time.Millisecond,
		DeleteBatch:   1000,
	}, nil)
	defer a.Close(context.Background())

	tracker := rcl.NewValidationTracker(1, 5*time.Minute)
	tracker.SetOnStale(a.OnStale)

	v := mkValidation(100, 0x01)
	if !tracker.Add(v) {
		t.Fatal("Add returned false; precondition broken")
	}

	tracker.ExpireOld(200) // v becomes stale

	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.GetValidationsForLedger(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("archive missing the stale validation: got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.LedgerSeq != 100 {
		t.Errorf("LedgerSeq=%d, want 100", got.LedgerSeq)
	}
	if got.LedgerHash != relationaldb.Hash(v.LedgerID) {
		t.Errorf("LedgerHash mismatch:\n got  %x\n want %x", got.LedgerHash, v.LedgerID)
	}
	if string(got.NodePubKey) != string(v.SigningPubKey[:]) {
		t.Errorf("NodePubKey mismatch")
	}
	if string(got.Raw) != string(v.Raw) {
		t.Errorf("Raw bytes mismatch")
	}
}

// slowRepo wraps a real repo to inject latency on SaveBatch — exercises
// the channel-buffered, non-blocking property of OnStale.
type slowRepo struct {
	relationaldb.ValidationRepository
	wait time.Duration
}

func (s *slowRepo) SaveBatch(ctx context.Context, vs []*relationaldb.ValidationRecord) error {
	time.Sleep(s.wait)
	return s.ValidationRepository.SaveBatch(ctx, vs)
}

// TestValidationArchive_BatchedWriter_DoesNotBlockOnReceive is the second
// acceptance test from issue #267. OnStale must return in well under the
// SaveBatch latency budget — proving the writer is decoupled from the
// receive path via a buffered channel.
func TestValidationArchive_BatchedWriter_DoesNotBlockOnReceive(t *testing.T) {
	rm := openArchiveDB(t)
	repo := &slowRepo{
		ValidationRepository: rm.Validation(),
		wait:                 50 * time.Millisecond, // each SaveBatch is glacial
	}

	const enqueues = 1000
	a := archive.New(repo, archive.Config{
		BatchSize:     128,
		FlushInterval: 5 * time.Millisecond,
		DeleteBatch:   1,
	}, nil)
	defer a.Close(context.Background())

	// 1000 OnStale calls into a channel of capacity BatchSize*8=1024.
	// Even with the slow repo, the loop must complete near-instantly.
	deadline := 200 * time.Millisecond
	start := time.Now()
	for i := 0; i < enqueues; i++ {
		a.OnStale(mkValidation(uint32(i+1), byte(i&0xFF)))
	}
	if elapsed := time.Since(start); elapsed > deadline {
		t.Fatalf("OnStale loop blocked: enqueued %d in %v (deadline %v)", enqueues, elapsed, deadline)
	}

	// All enqueued rows must eventually land in the repo (Flush blocks
	// until the writer drains everything that was queued before the
	// flush request).
	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	count, err := rm.Validation().GetValidationCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != int64(enqueues) {
		t.Fatalf("after Flush, archive has %d rows, want %d", count, enqueues)
	}
}

// TestValidationArchive_RetentionRespected is the third acceptance test
// from issue #267. Archive 20 rows at seqs 1..20 with retention=10 and a
// fully-validated pivot of 20: rows below seq 10 must be deleted; rows
// 10..20 must remain.
func TestValidationArchive_RetentionRespected(t *testing.T) {
	rm := openArchiveDB(t)
	repo := rm.Validation()

	a := archive.New(repo, archive.Config{
		BatchSize:        1,
		FlushInterval:    time.Hour, // size-only flush
		RetentionLedgers: 10,
		DeleteBatch:      1000,
	}, nil)
	defer a.Close(context.Background())

	for seq := uint32(1); seq <= 20; seq++ {
		a.OnStale(mkValidation(seq, byte(seq)))
	}
	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	a.NoteFullyValidated(20)
	if _, err := a.ApplyRetention(context.Background()); err != nil {
		t.Fatal(err)
	}

	count, err := repo.GetValidationCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// cutoff = 20 - 10 = 10 → DELETE WHERE seq < 10 → seqs 1..9 gone,
	// seqs 10..20 remain (11 rows).
	if count != 11 {
		t.Fatalf("post-retention rows = %d, want 11 (seqs 10..20)", count)
	}

	gone, err := repo.GetValidationsForLedger(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Errorf("seq=5 should be retained-out: got %d rows", len(gone))
	}

	keep, err := repo.GetValidationsForLedger(context.Background(), 15)
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != 1 {
		t.Errorf("seq=15 should remain: got %d rows", len(keep))
	}
}

// TestValidationArchive_EngineDrivenFlow validates the full end-to-end
// path: engine wires the archive, drives ExpireOld via the fully-
// validated callback, and the archive captures evicted validations.
func TestValidationArchive_EngineDrivenFlow(t *testing.T) {
	rm := openArchiveDB(t)
	repo := rm.Validation()

	a := archive.New(repo, archive.Config{
		BatchSize:     8,
		FlushInterval: 10 * time.Millisecond,
		DeleteBatch:   1000,
	}, nil)
	defer a.Close(context.Background())

	tracker := rcl.NewValidationTracker(2, 5*time.Minute)
	trustedNodes := []consensus.NodeID{{1}, {2}, {3}}
	tracker.SetTrusted(trustedNodes)
	tracker.SetOnStale(a.OnStale)

	// Pretend the engine wired its fully-validated callback to drive
	// ExpireOld with an in-memory window of 50.
	const inMemoryLedgers = uint32(50)
	var fired atomic.Int32
	tracker.SetFullyValidatedCallback(func(_ consensus.LedgerID, seq uint32) {
		fired.Add(1)
		a.NoteFullyValidated(seq)
		if seq > inMemoryLedgers {
			tracker.ExpireOld(seq - inMemoryLedgers)
		}
	})

	// Seed an old validation at seq 100 — well below the cutoff that
	// the seq=300 fully-validated event will compute (300-50=250).
	old := mkValidation(100, 0x01)
	if !tracker.Add(old) {
		t.Fatal("seed Add returned false")
	}

	// Drive quorum at seq 300: two trusted validations for the SAME
	// ledger hash (mkValidation's hash varies by node, so we pin a
	// deterministic LedgerID here and only swap NodeID).
	sharedLedger := consensus.LedgerID{0xCA, 0xFE, 0xBA, 0xBE}
	for _, n := range []consensus.NodeID{{1}, {2}} {
		v := mkValidation(300, n[0])
		v.LedgerID = sharedLedger
		v.NodeID = n
		if !tracker.Add(v) {
			t.Fatalf("Add at seq=300 returned false for node %v", n)
		}
	}

	if got := fired.Load(); got != 1 {
		t.Fatalf("fully-validated callback fired %d times, want 1", got)
	}

	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.GetValidationsForLedger(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("archive expected to contain the seq-100 evictee: got %d rows", len(rows))
	}
}
