package list

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSitePollerRedirectTargetByStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		permanent bool
	}{
		{name: "moved permanently", status: http.StatusMovedPermanently, permanent: true},
		{name: "permanent redirect", status: http.StatusPermanentRedirect, permanent: true},
		{name: "found", status: http.StatusFound},
		{name: "temporary redirect", status: http.StatusTemporaryRedirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var targetURL string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					w.Header().Set("Location", targetURL)
					w.WriteHeader(tc.status)
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			targetURL = server.URL + "/target"

			poller := newPollerForLifecycleTest(t, server.URL+"/start")
			permanentTarget := ""
			result, err := poller.fetch(t.Context(), server.URL+"/start", func(target string) {
				permanentTarget = target
			})
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if result.responseURI != targetURL {
				t.Fatalf("response URI = %q, want %q", result.responseURI, targetURL)
			}
			wantTarget := ""
			if tc.permanent {
				wantTarget = targetURL
			}
			if permanentTarget != wantTarget {
				t.Fatalf("permanent target = %q, want %q", permanentTarget, wantTarget)
			}
		})
	}
}

func TestSitePollerRedirectLocationValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		location string
		want     string
	}{
		{name: "relative", location: "/target", want: "URI '/target' cannot be parsed"},
		{name: "unsupported scheme", location: "ftp://example.com/list", want: "Unsupported scheme: 'ftp'"},
		{name: "file scheme", location: "file:///tmp/list", want: "invalid scheme in redirect file"},
		{name: "missing location", want: "missing location"},
		{name: "zero port", location: "http://example.com:0/list", want: "URI 'http://example.com:0/list' cannot be parsed"},
		{name: "overflowing port", location: "http://example.com:65536/list", want: "URI 'http://example.com:65536/list' cannot be parsed"},
		{name: "query without path", location: "http://example.com?list", want: "URI 'http://example.com?list' cannot be parsed"},
		{name: "fragment without path", location: "http://example.com#list", want: "URI 'http://example.com#list' cannot be parsed"},
		{name: "missing authority", location: "http:relative", want: "URI 'http:relative' cannot be parsed"},
		{name: "missing hostname", location: "http:///list", want: "http URI must contain a hostname"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(http.StatusMovedPermanently)
			}))
			defer server.Close()
			poller := newPollerForLifecycleTest(t, server.URL)
			_, err := poller.fetch(t.Context(), server.URL, nil)
			if err == nil {
				t.Fatal("fetch unexpectedly succeeded")
			}
			if got, retry := fetchDiagnostic(err); got != tc.want || retry {
				t.Fatalf("diagnostic = (%q, retry=%v), want (%q, false)", got, retry, tc.want)
			}
		})
	}
}

func TestSitePollerPermanentTargetSurvivesTemporaryRedirectAndFailure(t *testing.T) {
	var initialHits, permanentHits atomic.Int32
	var targetURL string
	var latestURL string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			initialHits.Add(1)
			w.Header().Set("Location", targetURL)
			w.WriteHeader(http.StatusMovedPermanently)
		case "/permanent":
			if permanentHits.Add(1) <= 2 {
				w.Header().Set("Location", server.URL+"/temporary")
				w.WriteHeader(http.StatusTemporaryRedirect)
				return
			}
			w.Header().Set("Location", latestURL)
			w.WriteHeader(http.StatusMovedPermanently)
		case "/temporary":
			_, _ = w.Write([]byte(`{}`))
		case "/latest":
			panic(http.ErrAbortHandler)
		}
	}))
	defer server.Close()
	targetURL = server.URL + "/permanent"
	latestURL = server.URL + "/latest"
	poller := newPollerForLifecycleTest(t, server.URL+"/start")

	for i, wantTarget := range []string{targetURL, targetURL, latestURL, latestURL} {
		poller.fetchSite(t.Context(), poller.sites[0])
		snapshot := poller.aggregator.SiteSnapshot()
		wantURI := server.URL + "/start (redirects to " + wantTarget + ")"
		if len(snapshot) != 1 || snapshot[0].URI != wantURI {
			t.Fatalf("poll %d URI = %#v, want %q", i, snapshot, wantURI)
		}
		wantError := "missing fields"
		if i >= 2 {
			wantError = "fetch error"
		}
		if snapshot[0].LastError != wantError {
			t.Fatalf("poll %d error = %q, want %q", i, snapshot[0].LastError, wantError)
		}
	}
	if initialHits.Load() != 1 || permanentHits.Load() != 3 {
		t.Fatalf("poll targets: initial=%d permanent=%d, want 1 and 3", initialHits.Load(), permanentHits.Load())
	}
}

func TestSitePollerPublishesPermanentTargetBeforeFetchCompletes(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var targetURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			w.Header().Set("Location", targetURL)
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		close(entered)
		<-release
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	targetURL = server.URL + "/target"

	agg, err := New(Config{SiteURIs: []string{server.URL + "/start"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	poller, err := NewSitePoller([]string{server.URL + "/start"}, agg, nil)
	if err != nil {
		t.Fatalf("NewSitePoller: %v", err)
	}
	done := make(chan struct{})
	go func() {
		poller.fetchSite(t.Context(), poller.sites[0])
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("redirected request did not arrive")
	}

	snapshot := agg.SiteSnapshot()
	wantURI := server.URL + "/start (redirects to " + targetURL + ")"
	if len(snapshot) != 1 || snapshot[0].URI != wantURI {
		t.Fatalf("in-flight site URI = %#v, want %q", snapshot, wantURI)
	}
	close(release)
	<-done
}

func TestSitePollerDuplicateSitesKeepRedirectTargetsIsolated(t *testing.T) {
	var initialHits atomic.Int32
	targetHits := map[string]int{}
	var targetMu sync.Mutex
	var targetOne, targetTwo string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if initialHits.Add(1) == 1 {
				w.Header().Set("Location", targetOne)
			} else {
				w.Header().Set("Location", targetTwo)
			}
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		targetMu.Lock()
		targetHits[r.URL.Path]++
		targetMu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	targetOne = server.URL + "/one"
	targetTwo = server.URL + "/two"

	agg, err := New(Config{SiteURIs: []string{server.URL, server.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	poller, err := NewSitePoller([]string{server.URL, server.URL}, agg, nil)
	if err != nil {
		t.Fatalf("NewSitePoller: %v", err)
	}
	poller.fetchSite(t.Context(), poller.sites[0])
	poller.fetchSite(t.Context(), poller.sites[1])

	wantOne := server.URL + " (redirects to " + targetOne + ")"
	wantTwo := server.URL + " (redirects to " + targetTwo + ")"
	snapshot := agg.SiteSnapshot()
	if len(snapshot) != 2 || snapshot[0].URI != wantOne || snapshot[1].URI != wantTwo {
		t.Fatalf("site snapshot URIs = %#v, want [%q, %q]", snapshot, wantOne, wantTwo)
	}
	if sites := NewRPCReader(agg).Sites(); len(sites) != 2 || sites[0].URI != wantOne || sites[1].URI != wantTwo {
		t.Fatalf("RPC site URIs = %#v, want [%q, %q]", sites, wantOne, wantTwo)
	}

	poller.fetchSite(t.Context(), poller.sites[0])
	poller.fetchSite(t.Context(), poller.sites[1])
	targetMu.Lock()
	defer targetMu.Unlock()
	if initialHits.Load() != 2 || targetHits["/one"] != 2 || targetHits["/two"] != 2 {
		t.Fatalf("poll targets = initial %d, one %d, two %d; want initial 2 and each target 2", initialHits.Load(), targetHits["/one"], targetHits["/two"])
	}
}

func TestSitePollerFetchDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name string
		uri  func(*testing.T) string
		want string
	}{
		{
			name: "file transport",
			uri:  func(*testing.T) string { return "file:///definitely/missing-validator-list" },
			want: "fetch error",
		},
		{
			name: "http transport",
			uri: func(t *testing.T) string {
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					panic(http.ErrAbortHandler)
				}))
				t.Cleanup(server.Close)
				return server.URL
			},
			want: "fetch error",
		},
		{
			name: "non-OK success status",
			uri: func(t *testing.T) string {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				}))
				t.Cleanup(server.Close)
				return server.URL
			},
			want: "bad result code",
		},
		{
			name: "http status",
			uri: func(t *testing.T) string {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				t.Cleanup(server.Close)
				return server.URL
			},
			want: "bad result code",
		},
		{
			name: "unsupported redirect status",
			uri: func(t *testing.T) string {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Location", "/ignored")
					w.WriteHeader(http.StatusSeeOther)
				}))
				t.Cleanup(server.Close)
				return server.URL
			},
			want: "bad result code",
		},
		{
			name: "bad json",
			uri: func(t *testing.T) string {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`{`))
				}))
				t.Cleanup(server.Close)
				return server.URL
			},
			want: "bad json",
		},
		{
			name: "missing fields",
			uri: func(t *testing.T) string {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`{}`))
				}))
				t.Cleanup(server.Close)
				return server.URL
			},
			want: "missing fields",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uri := tc.uri(t)
			agg, err := New(Config{SiteURIs: []string{uri}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			poller, err := NewSitePoller([]string{uri}, agg, nil)
			if err != nil {
				t.Fatalf("NewSitePoller: %v", err)
			}
			poller.fetchSite(t.Context(), poller.sites[0])
			snapshot := agg.SiteSnapshot()
			if len(snapshot) != 1 || snapshot[0].LastError != tc.want {
				t.Fatalf("last error = %#v, want %q", snapshot, tc.want)
			}
		})
	}
}

func TestSitePollerRedirectLimitPreservesSameURITarget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		redirects int32
		want      string
	}{
		{name: "three redirects", redirects: 3, want: "missing fields"},
		{name: "four redirects", redirects: 4, want: "max redirects"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if hits.Add(1) <= tc.redirects {
					w.Header().Set("Location", server.URL)
					w.WriteHeader(http.StatusMovedPermanently)
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			poller := newPollerForLifecycleTest(t, server.URL)
			poller.fetchSite(t.Context(), poller.sites[0])
			snapshot := poller.aggregator.SiteSnapshot()
			wantURI := server.URL + " (redirects to " + server.URL + ")"
			if len(snapshot) != 1 || snapshot[0].URI != wantURI || snapshot[0].LastError != tc.want {
				t.Fatalf("redirect metadata = %#v, want URI %q and error %q", snapshot, wantURI, tc.want)
			}
			if hits.Load() != 4 {
				t.Fatalf("requests = %d, want 4", hits.Load())
			}
		})
	}
}
