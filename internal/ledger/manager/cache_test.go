package manager

import (
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
)

// makeLedger builds a *ledger.Ledger whose Sequence() and Hash() are controllable.
// NewFromHeader copies the header verbatim; Sequence() reads header.LedgerIndex
// and Hash() reads header.Hash. The state/tx SHAMaps are unused by the cache, so
// nil is fine.
func makeLedger(seq uint32, hashByte byte) *ledger.Ledger {
	var hash [32]byte
	for i := range hash {
		hash[i] = hashByte
	}
	// Encode the seq into the tail so distinct seqs always yield distinct hashes.
	hash[28] = byte(seq >> 24)
	hash[29] = byte(seq >> 16)
	hash[30] = byte(seq >> 8)
	hash[31] = byte(seq)
	hdr := header.LedgerHeader{LedgerIndex: seq, Hash: hash}
	return ledger.NewFromHeader(hdr, nil, nil, drops.Fees{})
}

func newCache(t *testing.T, maxRecent int) *LedgerCache {
	t.Helper()
	c, err := newLedgerCache(ledgerCacheConfig{MaxRecentLedgers: maxRecent})
	if err != nil {
		t.Fatalf("newLedgerCache error: %v", err)
	}
	return c
}

func TestPutGet(t *testing.T) {
	c := newCache(t, 16)
	l := makeLedger(5, 0xAB)
	c.Put(l)

	got, ok := c.Get(5)
	if !ok {
		t.Fatalf("Get(5) miss after Put")
	}
	if got != l {
		t.Errorf("Get(5) returned a different ledger pointer")
	}

	gotByHash, ok := c.GetByHash(l.Hash())
	if !ok {
		t.Fatalf("GetByHash miss after Put")
	}
	if gotByHash != l {
		t.Errorf("GetByHash returned a different ledger pointer")
	}
}

func TestGetMissAndStats(t *testing.T) {
	c := newCache(t, 16)
	l := makeLedger(1, 0x11)
	c.Put(l)

	// Two hits (Get + GetByHash).
	if _, ok := c.Get(1); !ok {
		t.Fatalf("expected hit on Get(1)")
	}
	if _, ok := c.GetByHash(l.Hash()); !ok {
		t.Fatalf("expected hit on GetByHash")
	}

	// Three misses.
	if _, ok := c.Get(999); ok {
		t.Fatalf("expected miss on Get(999)")
	}
	var absent [32]byte
	absent[0] = 0xFF
	if _, ok := c.GetByHash(absent); ok {
		t.Fatalf("expected miss on GetByHash(absent)")
	}
	if _, ok := c.Get(1000); ok {
		t.Fatalf("expected miss on Get(1000)")
	}

	stats := c.stats()
	if stats.Hits != 2 {
		t.Errorf("Stats.Hits = %d, want 2", stats.Hits)
	}
	if stats.Misses != 3 {
		t.Errorf("Stats.Misses = %d, want 3", stats.Misses)
	}
	wantRate := 2.0 / 5.0
	if stats.HitRate != wantRate {
		t.Errorf("Stats.HitRate = %v, want %v", stats.HitRate, wantRate)
	}
	if stats.SeqCacheLen != 1 {
		t.Errorf("Stats.SeqCacheLen = %d, want 1", stats.SeqCacheLen)
	}
	if stats.HashCacheLen != 1 {
		t.Errorf("Stats.HashCacheLen = %d, want 1", stats.HashCacheLen)
	}
}

func TestStatsHitRateZeroWhenNoOps(t *testing.T) {
	c := newCache(t, 16)
	stats := c.stats()
	if stats.HitRate != 0 {
		t.Errorf("Stats.HitRate = %v, want 0 with no ops", stats.HitRate)
	}
}

func TestRemoveEvictsBothCaches(t *testing.T) {
	c := newCache(t, 16)
	l := makeLedger(3, 0x33)
	c.Put(l)

	c.Remove(3)

	if _, ok := c.Get(3); ok {
		t.Errorf("Get(3) hit after Remove")
	}
	if _, ok := c.GetByHash(l.Hash()); ok {
		t.Errorf("GetByHash hit after Remove — hash cache not evicted")
	}

	stats := c.stats()
	if stats.SeqCacheLen != 0 {
		t.Errorf("SeqCacheLen = %d, want 0 after Remove", stats.SeqCacheLen)
	}
	if stats.HashCacheLen != 0 {
		t.Errorf("HashCacheLen = %d, want 0 after Remove", stats.HashCacheLen)
	}
}

func TestRemoveAbsentIsNoOp(t *testing.T) {
	c := newCache(t, 16)
	c.Put(makeLedger(1, 0x01))
	c.Remove(999) // absent — must not panic or evict the present entry
	if _, ok := c.Get(1); !ok {
		t.Errorf("Get(1) miss after Remove(999) — unrelated entry evicted")
	}
}

func TestLRUEviction(t *testing.T) {
	const maxRecent = 3
	c := newCache(t, maxRecent)

	l1 := makeLedger(1, 0x01)
	l2 := makeLedger(2, 0x02)
	l3 := makeLedger(3, 0x03)
	l4 := makeLedger(4, 0x04)

	c.Put(l1)
	c.Put(l2)
	c.Put(l3)
	// Inserting the 4th distinct ledger evicts the least-recently-used (seq 1).
	c.Put(l4)

	if stats := c.stats(); stats.SeqCacheLen != maxRecent {
		t.Errorf("SeqCacheLen = %d, want %d", stats.SeqCacheLen, maxRecent)
	}
	if _, ok := c.Get(1); ok {
		t.Errorf("Get(1) hit — LRU entry should have been evicted")
	}
	for _, seq := range []uint32{2, 3, 4} {
		if _, ok := c.Get(seq); !ok {
			t.Errorf("Get(%d) miss — should still be cached", seq)
		}
	}
}

func TestClearPreservesCompleteness(t *testing.T) {
	c := newCache(t, 16)
	c.Put(makeLedger(1, 0x01))
	c.Put(makeLedger(2, 0x02))
	c.MarkComplete(1)

	c.Clear()

	if stats := c.stats(); stats.SeqCacheLen != 0 || stats.HashCacheLen != 0 {
		t.Errorf("after Clear, caches not empty: seq=%d hash=%d", stats.SeqCacheLen, stats.HashCacheLen)
	}
	if !c.IsComplete(1) {
		t.Errorf("IsComplete(1) = false after Clear — completeness must be preserved")
	}
}

func TestCompletenessDelegation(t *testing.T) {
	c := newCache(t, 16)

	c.MarkComplete(5)
	if !c.IsComplete(5) {
		t.Errorf("IsComplete(5) = false after MarkComplete(5)")
	}
	if c.IsComplete(6) {
		t.Errorf("IsComplete(6) = true, want false")
	}

	c.MarkCompleteRange(10, 15)
	for seq := uint32(10); seq <= 15; seq++ {
		if !c.IsComplete(seq) {
			t.Errorf("IsComplete(%d) = false after MarkCompleteRange(10,15)", seq)
		}
	}

	min, max, hasAny := c.GetCompleteRange()
	if !hasAny || min != 5 || max != 15 {
		t.Errorf("GetCompleteRange() = (%d, %d, %v), want (5, 15, true)", min, max, hasAny)
	}

	missing := c.FindMissingInRange(5, 15)
	want := []uint32{6, 7, 8, 9}
	if !reflect.DeepEqual(missing, want) {
		t.Errorf("FindMissingInRange(5,15) = %v, want %v", missing, want)
	}
}

func TestClearCompleteness(t *testing.T) {
	c := newCache(t, 16)
	c.MarkCompleteRange(1, 10)
	if !c.IsComplete(5) {
		t.Fatalf("setup: IsComplete(5) should be true")
	}

	c.ClearCompleteness()

	if c.IsComplete(5) {
		t.Errorf("IsComplete(5) = true after ClearCompleteness")
	}
	if _, _, hasAny := c.GetCompleteRange(); hasAny {
		t.Errorf("GetCompleteRange() reports hasAny=true after ClearCompleteness")
	}
}

func TestNewLedgerCacheDefaultsMaxRecent(t *testing.T) {
	// MaxRecentLedgers <= 0 must default to 256 and still work.
	for _, n := range []int{0, -1} {
		c, err := newLedgerCache(ledgerCacheConfig{MaxRecentLedgers: n})
		if err != nil {
			t.Fatalf("newLedgerCache(MaxRecentLedgers=%d) error: %v", n, err)
		}
		c.Put(makeLedger(1, 0x01))
		if _, ok := c.Get(1); !ok {
			t.Errorf("cache with default capacity (from n=%d) failed to store a ledger", n)
		}
	}
}
