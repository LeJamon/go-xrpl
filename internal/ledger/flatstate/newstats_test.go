package flatstate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStoreStatsCachesOutboxAndTransitionState(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0).UTC()
	store, err := Open(t.TempDir(), Options{
		CacheBytes:      1 << 20,
		MemTableSize:    1 << 20,
		MaxBatchRecords: 1,
		Clock:           func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first := testDescriptor(1, Hash{}, testHash(1), testHash(2), testHash(3))
	importInitial(t, store, first, nil)
	stats := store.Stats()
	if stats.OutboxCount != 1 || stats.OutboxBytes <= 0 || stats.OutboxAge != 0 || stats.Advancing || stats.Failures != 0 {
		t.Fatalf("initial stats = %+v", stats)
	}

	if err := store.AckOutbox(context.Background(), SemanticCommitID(first.LedgerHash, "flat-state")); err != nil {
		t.Fatal(err)
	}
	stats = store.Stats()
	if stats.OutboxCount != 0 || stats.OutboxBytes != 0 || stats.OutboxAge != 0 {
		t.Fatalf("acked stats = %+v", stats)
	}

	second := testDescriptor(2, first.LedgerHash, testHash(4), testHash(5), testHash(6))
	err = store.Advance(context.Background(), AdvanceRequest{
		Expected: &first,
		Target:   second,
		Changes: NewSliceChangeIterator([]Change{
			{Key: testHash(3), Value: []byte("three")},
			{Key: testHash(4), Value: []byte("four")},
			{Key: testHash(2), Value: []byte("two")},
		}),
		Authorization: testAuthorization(first, second),
	})
	if !errors.Is(err, ErrInvalidChangeOrder) {
		t.Fatalf("unsorted advance error = %v", err)
	}
	stats = store.Stats()
	if !stats.Advancing || stats.Failures != 1 {
		t.Fatalf("failed transition stats = %+v", stats)
	}
}
