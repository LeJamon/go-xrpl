package list

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
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
			result := poller.fetchWithRedirect(t.Context(), server.URL+"/start", nil)
			if result.err != nil {
				t.Fatalf("fetchWithRedirect: %v", result.err)
			}
			if result.responseURI != targetURL {
				t.Fatalf("response URI = %q, want %q", result.responseURI, targetURL)
			}
			wantTarget := server.URL + "/start"
			if tc.permanent {
				wantTarget = targetURL
			}
			if result.targetURI != wantTarget || result.permanentRedirected != tc.permanent {
				t.Fatalf("redirect state = (%q, %v), want (%q, %v)", result.targetURI, result.permanentRedirected, wantTarget, tc.permanent)
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
			result := poller.fetchWithRedirect(t.Context(), server.URL, nil)
			if result.err == nil {
				t.Fatal("fetchWithRedirect unexpectedly succeeded")
			}
			if got, _ := fetchDiagnostic(result.err); got != tc.want {
				t.Fatalf("diagnostic = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSitePollerPermanentTargetSurvivesTemporaryRedirectAndFailure(t *testing.T) {
	var permanentHits atomic.Int32
	var targetURL string
	var latestURL string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			w.Header().Set("Location", targetURL)
			w.WriteHeader(http.StatusMovedPermanently)
		case "/permanent":
			if permanentHits.Add(1) == 1 {
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

	result := poller.fetchWithRedirect(t.Context(), server.URL+"/start", nil)
	if result.err != nil || !result.permanentRedirected || result.targetURI != targetURL {
		t.Fatalf("initial redirect state = (%q, %v), err=%v", result.targetURI, result.permanentRedirected, result.err)
	}
	result = poller.fetchWithRedirect(t.Context(), result.targetURI, nil)
	if result.err != nil || result.permanentRedirected || result.targetURI != targetURL {
		t.Fatalf("temporary redirect state = (%q, %v), err=%v", result.targetURI, result.permanentRedirected, result.err)
	}
	result = poller.fetchWithRedirect(t.Context(), result.targetURI, nil)
	if result.err == nil || !result.permanentRedirected || result.targetURI != latestURL {
		t.Fatalf("failed permanent redirect state = (%q, %v), err=%v", result.targetURI, result.permanentRedirected, result.err)
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
	<-entered

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
		targetHits[r.URL.Path]++
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
			name: "transport",
			uri:  func(*testing.T) string { return "file:///definitely/missing-validator-list" },
			want: "fetch error",
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
