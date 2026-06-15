package peermanagement

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrivateMode_SuppressesSelfGossip pins finding 6: peer_private must
// stop the overlay from advertising its own address in TMEndpoints gossip.
func TestPrivateMode_SuppressesSelfGossip(t *testing.T) {
	cfg := Config{PublicIP: net.ParseIP("198.51.100.5"), ListenAddr: ":51235"}
	o := &Overlay{cfg: cfg}

	_, ok := o.localEndpointForGossip()
	assert.True(t, ok, "non-private node with PublicIP+ListenAddr advertises itself")

	o.cfg.PrivateMode = true
	_, ok = o.localEndpointForGossip()
	assert.False(t, ok, "peer_private must suppress self-gossip")
}

// TestBootCache_SaveDirtyHandling pins finding 8: Save clears dirty only
// after a successful write (a failed write must retain the flag so the next
// Save retries instead of dropping the data).
func TestBootCache_SaveDirtyHandling(t *testing.T) {
	dir := t.TempDir()
	bc := NewBootCache(dir)
	assert.False(t, bc.dirty, "fresh cache is not dirty")

	bc.Insert("198.51.100.7:51235", 51235)
	assert.True(t, bc.dirty, "Insert marks the cache dirty")

	require.NoError(t, bc.Save())
	assert.False(t, bc.dirty, "a successful Save clears dirty")

	data, err := os.ReadFile(filepath.Join(dir, DefaultBootCacheFile))
	require.NoError(t, err)
	assert.Contains(t, string(data), "198.51.100.7", "the entry must be persisted")

	// A failed write must retain dirty. Point the cache file under a path
	// whose parent is a regular file so MkdirAll fails.
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	bad := &BootCache{
		cache:    map[string]*CachedEndpoint{},
		filePath: filepath.Join(blocker, "sub", DefaultBootCacheFile),
	}
	bad.Insert("203.0.113.1:51235", 51235)
	require.Error(t, bad.Save(), "writing under a regular file must fail")
	assert.True(t, bad.dirty, "a failed Save must retain dirty for the next retry")
}

// TestDiscovery_MarkConnected_FeedsBootCache pins finding 7: a successful
// (outbound) connect must populate the boot cache so a restart can
// reconnect to known-good peers. Before wiring, the cache was permanently
// empty and GetEndpoints contributed nothing to peer selection.
func TestDiscovery_MarkConnected_FeedsBootCache(t *testing.T) {
	cfg := Config{MaxPeers: 50, MaxInbound: 25, MaxOutbound: 25, DataDir: t.TempDir(), Clock: time.Now}
	d := NewDiscovery(&cfg, make(chan Event, 1))
	require.NotNil(t, d.bootCache, "a DataDir-configured Discovery has a boot cache")

	const addr = "198.51.100.9:51235"
	d.MarkConnected(addr, PeerID(1))

	found := false
	for _, e := range d.bootCache.GetEndpoints(10) {
		if e.Address == addr {
			found = true
		}
	}
	assert.True(t, found, "MarkConnected must feed the boot cache")
}
