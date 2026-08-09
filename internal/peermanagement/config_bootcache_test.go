package peermanagement

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

func TestBootCacheAtomicSavePreservesPreviousFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	bc := NewBootCache(dir)
	bc.Insert("198.51.100.10:51235", 51235)
	require.NoError(t, bc.Save())
	want, err := os.ReadFile(filepath.Join(dir, DefaultBootCacheFile))
	require.NoError(t, err)

	blocker := filepath.Join(dir, "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, []byte("block"), 0o600))
	bc.filePath = filepath.Join(blocker, "nested", DefaultBootCacheFile)
	bc.Insert("198.51.100.11:51235", 51235)
	require.Error(t, bc.Save())
	assert.True(t, bc.dirty, "failed durable save must retain dirty state")

	got, err := os.ReadFile(filepath.Join(dir, DefaultBootCacheFile))
	require.NoError(t, err)
	assert.Equal(t, want, got, "failed save must not replace the previous snapshot")
}

func TestBootCachePostRenameErrorKeepsCommittedSnapshot(t *testing.T) {
	dir := t.TempDir()
	bc := NewBootCache(dir)
	bc.Insert("198.51.100.13:51235", 51235)
	bc.writeFile = func(path string, data []byte, mode os.FileMode) (bool, error) {
		committed, err := writeAtomicFile(path, data, mode)
		require.NoError(t, err)
		return committed, errors.New("directory durability uncertain")
	}

	err := bc.Save()
	require.EqualError(t, err, "directory durability uncertain")
	assert.False(t, bc.dirty, "rename committed the snapshot despite the durability error")

	reloaded := NewBootCache(dir)
	require.NoError(t, reloaded.Load())
	assert.Len(t, reloaded.Endpoints(0), 1)
	assert.Equal(t, "198.51.100.13:51235", reloaded.Endpoints(0)[0].Address)
}

func TestOverlayStopWaitsForRunCompletionBeforeFinalCacheSave(t *testing.T) {
	dir := t.TempDir()
	o, err := New(WithDataDir(dir), WithPrivateMode(true))
	require.NoError(t, err)
	require.NotNil(t, o.discovery.bootCache)
	o.discovery.bootCache.Insert("198.51.100.14:51235", 51235)

	runComplete := make(chan struct{})
	o.lifecycleMu.Lock()
	o.runComplete = runComplete
	o.lifecycleMu.Unlock()
	stopDone := make(chan struct{})
	go func() {
		_ = o.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop saved the cache before Run completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(runComplete)
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not complete after Run completion")
	}

	reloaded := NewBootCache(dir)
	require.NoError(t, reloaded.Load())
	assert.Len(t, reloaded.Endpoints(0), 1)
	assert.Equal(t, "198.51.100.14:51235", reloaded.Endpoints(0)[0].Address)
}

func TestOverlayRunRejectsCorruptReservationBeforeReadiness(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultReservationFile)
	require.NoError(t, os.WriteFile(path, []byte(`[{"node_id":"not-a-node-public"}]`), 0o600))
	o, err := New(
		WithDataDir(dir),
		WithListenAddr("127.0.0.1:0"),
		WithPrivateMode(true),
	)
	require.NoError(t, err)
	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(t.Context()) }()
	select {
	case err := <-runDone:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "discovery error")
	case <-o.ListenerReady():
		t.Fatal("corrupt persisted state was reported ready")
	case <-time.After(5 * time.Second):
		t.Fatal("overlay did not reject corrupt persisted state")
	}
	require.NoError(t, o.Stop())
}

func TestBootCacheLoadRejectsMalformedAndNullEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultBootCacheFile)
	for _, raw := range []string{
		"null",
		"[null]",
		`[{"address":"not-an-endpoint","port":51235,"last_seen":"2026-08-02T00:00:00Z"}]`,
	} {
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
		bc := NewBootCache(dir)
		require.Error(t, bc.Load(), "malformed boot-cache payload %q must be rejected", raw)
	}
}

func TestBootCacheConcurrentMutationsAndSaves(t *testing.T) {
	dir := t.TempDir()
	bc := NewBootCache(dir)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			address := "198.51.100." + strconv.Itoa(i+1) + ":51235"
			bc.Insert(address, 51235)
			bc.MarkSuccess(address)
			_ = bc.Save()
		}(i)
	}
	wg.Wait()
	require.NoError(t, bc.Save())

	reloaded := NewBootCache(dir)
	require.NoError(t, reloaded.Load())
	assert.Len(t, reloaded.Endpoints(0), 32)
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
	for _, e := range d.bootCache.Endpoints(10) {
		if e.Address == addr {
			found = true
		}
	}
	assert.True(t, found, "MarkConnected must feed the boot cache")
}

func TestDiscoveryStopPersistsFinalBootCacheMutation(t *testing.T) {
	dir := t.TempDir()
	d := NewDiscovery(&Config{DataDir: dir, Clock: time.Now}, make(chan Event, 1))
	require.NoError(t, d.Start(t.Context()))
	d.MarkConnected("198.51.100.12:51235", PeerID(2))
	d.Stop()

	reloaded := NewBootCache(dir)
	require.NoError(t, reloaded.Load())
	assert.Len(t, reloaded.Endpoints(0), 1)
	assert.Equal(t, "198.51.100.12:51235", reloaded.Endpoints(0)[0].Address)
}
