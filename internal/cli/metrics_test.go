package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestMetricsRegistry(t *testing.T) {
	reg := newMetricsRegistry()
	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("scrape metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("metrics content-type = %q, want Prometheus text exposition", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	out := string(body)

	// Standard Go runtime collector (always present, platform-independent).
	if !strings.Contains(out, "go_goroutines") {
		t.Errorf("metrics output missing go_goroutines:\n%s", out)
	}
	// Node-level collectors registered by newMetricsRegistry.
	for _, name := range []string{
		"goxrpl_build_info",
		"goxrpl_sched_latency_milliseconds",
		"goxrpl_io_latency_events_total",
	} {
		if !strings.Contains(out, name) {
			t.Errorf("metrics output missing %s:\n%s", name, out)
		}
	}
	// build_info must carry the running version as a label.
	if want := "version=\"" + version.Version + "\""; !strings.Contains(out, want) {
		t.Errorf("goxrpl_build_info missing label %s:\n%s", want, out)
	}
}
