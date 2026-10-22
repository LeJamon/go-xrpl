package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/observability"
	"github.com/LeJamon/go-xrpl/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestMetricsRegistry(t *testing.T) {
	cleanup := observability.SetLedgerServeMetrics(func() observability.LedgerServeMetricsSnapshot {
		return observability.LedgerServeMetricsSnapshot{}
	})
	defer cleanup()
	stopIngress := observability.SetTransactionIngressMetrics(func() observability.TransactionIngressMetricsSnapshot {
		return observability.TransactionIngressMetricsSnapshot{}
	})
	defer stopIngress()

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
	for _, name := range []string{
		"goxrpl_build_info",
		"goxrpl_sched_latency_milliseconds",
		"goxrpl_io_latency_events_total",
		"goxrpl_replay_fault_duration_seconds",
		"goxrpl_router_serve_queued",
		"goxrpl_router_serve_queue_wait_seconds",
		"goxrpl_transaction_ingress_frames_total",
		"goxrpl_transaction_ingress_inputs_total",
		"goxrpl_transaction_ingress_queue_wait_seconds",
	} {
		if !strings.Contains(out, name) {
			t.Errorf("metrics output missing %s:\n%s", name, out)
		}
	}
	if want := "version=\"" + version.Version + "\""; !strings.Contains(out, want) {
		t.Errorf("goxrpl_build_info missing label %s:\n%s", want, out)
	}
	for _, class := range []string{"ledger", "txset"} {
		if want := "goxrpl_router_serve_queued{class=\"" + class + "\"}"; !strings.Contains(out, want) {
			t.Errorf("router serving metrics missing class %s:\n%s", class, out)
		}
	}
}
