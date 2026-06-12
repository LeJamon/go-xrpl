package list_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/validator/list"
)

// validV1Envelope builds the JSON envelope body a publisher site serves
// for a single accepted v1 list.
func validV1Envelope(t *testing.T, pub *publisherFixture, validators [][33]byte) []byte {
	t.Helper()
	exp := time.Now().Add(24 * time.Hour).Unix()
	blob, sig := pub.signList(t, 1, 0, exp, validators)
	env := map[string]any{
		"manifest":  string(pub.manifestB64),
		"blob":      string(blob),
		"signature": string(sig),
		"version":   1,
	}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return body
}

// TestSitePoller_DoubleStartSingleLoop verifies that a second Start is a
// no-op: a rogue second runLoop would fetch concurrently with the first,
// pushing the observed peak in-flight count above one. The single-goroutine
// design (and the new started guard) keeps it at exactly one.
func TestSitePoller_DoubleStartSingleLoop(t *testing.T) {
	pub := newPublisher(t, 0x63, 0x64)
	v1 := derivedValidatorKey(0x71)
	body := validV1Envelope(t, pub, [][33]byte{v1})

	var inflight, peak int32
	served := make(chan struct{}, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&inflight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
				break
			}
		}
		// Hold long relative to the poll interval so a second runLoop would
		// overlap this fetch and drive peak to 2.
		time.Sleep(25 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		select {
		case served <- struct{}{}:
		default:
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		SiteURIs:      []string{srv.URL},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	poller, err := list.NewSitePoller([]string{srv.URL}, agg, nil)
	if err != nil {
		t.Fatalf("NewSitePoller: %v", err)
	}
	poller.SetInterval(5 * time.Millisecond)

	poller.Start(t.Context())
	poller.Start(t.Context()) // must be a no-op
	defer poller.Stop()

	for range 6 {
		select {
		case <-served:
		case <-time.After(3 * time.Second):
			t.Fatal("poller did not fetch the site")
		}
	}
	if p := atomic.LoadInt32(&peak); p != 1 {
		t.Fatalf("peak concurrent in-flight fetches = %d, want 1 (double Start spawned a second poll loop)", p)
	}
}

// TestSitePoller_RefreshesNextRefreshBeforeFetch verifies that fetchSite
// advances the RPC-visible next_refresh_time before the (possibly slow)
// fetch runs, so the validator_list_sites RPC never reports a stale,
// already-past value while a fetch is in flight.
func TestSitePoller_RefreshesNextRefreshBeforeFetch(t *testing.T) {
	pub := newPublisher(t, 0x61, 0x62)
	v1 := derivedValidatorKey(0x70)
	body := validV1Envelope(t, pub, [][33]byte{v1})

	var agg *list.Aggregator
	observed := make(chan time.Time, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Runs while the fetch is in flight. SetNextRefresh has already
		// run at the top of fetchSite, so the snapshot must show a fresh
		// future time, not the stale past value seeded below.
		if snap := agg.SiteSnapshot(); len(snap) == 1 {
			select {
			case observed <- snap[0].NextRefresh:
			default:
			}
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	a, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		SiteURIs:      []string{srv.URL},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	agg = a

	// Seed a stale, already-past next_refresh_time — the state the bug
	// leaves untouched for the duration of an in-flight fetch.
	agg.SetNextRefresh(srv.URL, time.Now().Add(-time.Hour))

	poller, err := list.NewSitePoller([]string{srv.URL}, agg, nil)
	if err != nil {
		t.Fatalf("NewSitePoller: %v", err)
	}
	poller.SetInterval(5 * time.Second) // future bump, comfortably ahead of now

	poller.Start(t.Context())
	defer poller.Stop()

	select {
	case got := <-observed:
		if !got.After(time.Now()) {
			t.Fatalf("next_refresh_time not refreshed before fetch: got %v, want a future time", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("poller never fetched the site")
	}
}

// TestAggregator_CacheWriteFailureDoesNotBlockIngest verifies that a failing
// cache write (read-only directory) does not change the apply disposition or
// drop the ingested state — the cache write is off the critical path and its
// failure is tolerated, matching rippled.
func TestAggregator_CacheWriteFailureDoesNotBlockIngest(t *testing.T) {
	pub := newPublisher(t, 0x65, 0x66)
	v1 := derivedValidatorKey(0x72)

	parent := t.TempDir()
	dir := filepath.Join(parent, "cache")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := agg.SetCacheDir(dir); err != nil {
		t.Fatalf("SetCacheDir: %v", err)
	}
	// Drop write permission so the deferred flush's WriteFile fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	now := fixedClock()()
	exp := now.Add(24 * time.Hour).Unix()
	blob, sig := pub.signList(t, 1, 0, exp, [][33]byte{v1})
	disp, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "site://")
	if disp != list.Accepted {
		t.Fatalf("disposition with failing cache dir: got %s want Accepted", disp)
	}
	snap := agg.PublisherSnapshot()
	if len(snap) != 1 || snap[0].Status != list.StatusAvailable || snap[0].Sequence != 1 {
		t.Fatalf("ingest state not advanced despite tolerated cache failure: %+v", snap)
	}
}
