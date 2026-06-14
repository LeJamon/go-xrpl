package adaptor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTxSetCache_TTLEviction pins A3: the cache evicts entries older than
// txSetCacheTTL on Put, so a long-running node's cache stays bounded
// instead of retaining one full SHAMap per round forever.
func TestTxSetCache_TTLEviction(t *testing.T) {
	c := NewTxSetCache()
	clock := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return clock }

	old, err := NewTxSet([][]byte{makeBlob(1)})
	require.NoError(t, err)
	c.Put(old)

	if _, ok := c.Get(old.ID()); !ok {
		t.Fatal("freshly Put set should be present")
	}

	// Advance past the TTL and Put a second set: the sweep on Put must
	// evict the now-stale first entry while keeping the fresh one.
	clock = clock.Add(txSetCacheTTL + time.Second)
	fresh, err := NewTxSet([][]byte{makeBlob(2)})
	require.NoError(t, err)
	c.Put(fresh)

	if _, ok := c.Get(old.ID()); ok {
		t.Error("stale entry should have been evicted by the TTL sweep")
	}
	if _, ok := c.Get(fresh.ID()); !ok {
		t.Error("fresh entry should remain after the sweep")
	}
}

// TestTxSetCache_Remove covers explicit removal.
func TestTxSetCache_Remove(t *testing.T) {
	c := NewTxSetCache()
	ts, err := NewTxSet([][]byte{makeBlob(3)})
	require.NoError(t, err)
	c.Put(ts)
	c.Remove(ts.ID())
	if _, ok := c.Get(ts.ID()); ok {
		t.Error("entry should be gone after Remove")
	}
}
