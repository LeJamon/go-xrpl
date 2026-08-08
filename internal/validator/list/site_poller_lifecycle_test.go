package list

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func mustURLForTest(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	return u
}

func newPollerForLifecycleTest(t *testing.T, uri string) *SitePoller {
	t.Helper()
	agg, err := New(Config{PublisherKeys: []PublisherKey{{0xed, 1}}, SiteURIs: []string{uri}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	poller, err := NewSitePoller([]string{uri}, agg, nil)
	if err != nil {
		t.Fatalf("NewSitePoller: %v", err)
	}
	return poller
}

func TestSitePollerClonesHTTPClientAndClampsTimeout(t *testing.T) {
	poller := newPollerForLifecycleTest(t, "https://example.com/list")
	caller := &http.Client{Timeout: time.Hour}
	poller.SetHTTPClient(caller)
	if caller.Timeout != time.Hour {
		t.Fatal("SetHTTPClient mutated caller client")
	}
	if poller.client == caller || poller.client.Timeout != defaultRequestTimeout {
		t.Fatalf("client clone/timeout: same=%v timeout=%v", poller.client == caller, poller.client.Timeout)
	}
	if err := poller.client.CheckRedirect(&http.Request{URL: mustURLForTest(t, "file:///tmp/x")}, nil); err == nil {
		t.Fatal("redirect policy allowed non-http scheme")
	}
	short := &http.Client{Timeout: time.Second}
	poller = newPollerForLifecycleTest(t, "https://example.com/list")
	poller.SetHTTPClient(short)
	if poller.client.Timeout != time.Second || short.CheckRedirect != nil {
		t.Fatal("short timeout or caller redirect policy was not preserved/isolated")
	}
}

func TestSitePollerCanceledContextCanRestart(t *testing.T) {
	var served atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		_, _ = w.Write([]byte(`{"version":1,"manifest":"bad","blob":"bad","signature":"bad"}`))
	}))
	defer server.Close()
	poller := newPollerForLifecycleTest(t, server.URL)
	poller.SetInterval(time.Millisecond)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	poller.Start(canceled)
	poller.Stop()
	poller.Start(context.Background())
	defer poller.Stop()
	deadline := time.Now().Add(time.Second)
	for served.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if served.Load() == 0 {
		t.Fatal("poller did not restart after canceled generation")
	}
}

func TestSitePollerConcurrentStartStop(t *testing.T) {
	poller := newPollerForLifecycleTest(t, "file:///definitely/missing")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			poller.Start(ctx)
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		poller.Stop()
		poller.Start(ctx)
	}
	<-done
	poller.Stop()
}
