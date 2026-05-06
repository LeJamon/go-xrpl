package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement"
)

func waitForBroadcasts(sender *fakeManifestSender, n int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sender.mu.Lock()
		got := len(sender.bcasts)
		sender.mu.Unlock()
		if got >= n {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return len(sender.bcasts)
}

func TestPeriodicManifestBroadcast_FiresOnInterval(t *testing.T) {
	sender := &fakeManifestSender{
		peers: []peermanagement.PeerInfo{{}, {}},
	}
	router, _, _ := routerWithCache(t, sender, 0xa1, 1)
	c := &Components{Router: router}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.runPeriodicManifestBroadcast(ctx, 50*time.Millisecond)

	// Two ticks at 50ms must fit comfortably in 500ms even on a
	// loaded CI runner. Larger budget would mask a regression where
	// the ticker stops firing.
	if got := waitForBroadcasts(sender, 2, 500*time.Millisecond); got < 2 {
		t.Fatalf("expected >=2 broadcasts, got %d", got)
	}
}

func TestPeriodicManifestBroadcast_StopsOnContextCancel(t *testing.T) {
	sender := &fakeManifestSender{
		peers: []peermanagement.PeerInfo{{}},
	}
	router, _, _ := routerWithCache(t, sender, 0xa2, 1)
	c := &Components{Router: router}

	ctx, cancel := context.WithCancel(context.Background())
	go c.runPeriodicManifestBroadcast(ctx, 50*time.Millisecond)

	if got := waitForBroadcasts(sender, 1, 500*time.Millisecond); got < 1 {
		t.Fatalf("expected first broadcast within 500ms, got %d", got)
	}
	cancel()

	sender.mu.Lock()
	frozen := len(sender.bcasts)
	sender.mu.Unlock()

	time.Sleep(150 * time.Millisecond)

	sender.mu.Lock()
	after := len(sender.bcasts)
	sender.mu.Unlock()
	if after != frozen {
		t.Fatalf("broadcast continued after cancel: before=%d after=%d", frozen, after)
	}
}

// Empty cache (observer / seed-only mode where nothing was applied to
// the manifest cache) must produce no broadcasts even though the
// ticker fires.
func TestPeriodicManifestBroadcast_EmptyCacheSilent(t *testing.T) {
	sender := &fakeManifestSender{
		peers: []peermanagement.PeerInfo{{}},
	}
	// seq=0 → routerWithCache skips seeding the cache.
	router, _, _ := routerWithCache(t, sender, 0, 0)
	c := &Components{Router: router}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.runPeriodicManifestBroadcast(ctx, 20*time.Millisecond)

	time.Sleep(120 * time.Millisecond)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.bcasts) != 0 {
		t.Errorf("empty-cache mode emitted %d broadcasts", len(sender.bcasts))
	}
}
