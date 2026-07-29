package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeAuxiliaryAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		server      string
		raw         string
		allowUnsafe bool
		want        string
		wantErr     string
	}{
		{name: "empty host", server: "pprof", raw: ":6060", want: "127.0.0.1:6060"},
		{name: "IPv4 metrics wildcard", server: "metrics", raw: "0.0.0.0:9100", want: "127.0.0.1:9100"},
		{name: "IPv6 metrics wildcard", server: "metrics", raw: "[::]:9100", want: "[::1]:9100"},
		{name: "explicit loopback", server: "pprof", raw: "localhost:6060", want: "localhost:6060"},
		{name: "remote metrics", server: "metrics", raw: "192.0.2.1:9100", want: "192.0.2.1:9100"},
		{name: "unsafe pprof", server: "pprof", raw: "192.0.2.1:6060", wantErr: "ALLOW_UNSAFE=true"},
		{name: "opted-in remote pprof", server: "pprof", raw: "192.0.2.1:6060", allowUnsafe: true, want: "192.0.2.1:6060"},
		{name: "pprof wildcard requires opt-in", server: "pprof", raw: "0.0.0.0:6060", wantErr: "ALLOW_UNSAFE=true"},
		{name: "opted-in pprof wildcard", server: "pprof", raw: "0.0.0.0:6060", allowUnsafe: true, want: "0.0.0.0:6060"},
		{name: "missing port", server: "pprof", raw: "localhost", wantErr: "missing port"},
		{name: "non-numeric port", server: "pprof", raw: "localhost:http", wantErr: "port must be a number"},
		{name: "out of range port", server: "pprof", raw: "localhost:65536", wantErr: "port must be a number"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeAuxiliaryAddress(test.server, test.raw, test.allowUnsafe)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("normalizeAuxiliaryAddress() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeAuxiliaryAddress() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("normalizeAuxiliaryAddress() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStartAuxiliaryServersRejectsMalformedUnsafeSetting(t *testing.T) {
	t.Parallel()

	_, err := startAuxiliaryServers(
		context.Background(),
		func(error) {},
		envValues(map[string]string{"GOXRPL_PPROF_ALLOW_UNSAFE": "1"}),
		net.Listen,
	)
	if err == nil || !strings.Contains(err.Error(), `must be "true" or "false"`) {
		t.Fatalf("startAuxiliaryServers() error = %v, want strict boolean error", err)
	}
}

func TestStartAuxiliaryServersUnsafePProfRequiresOptIn(t *testing.T) {
	t.Parallel()

	_, err := startAuxiliaryServers(
		context.Background(),
		func(error) {},
		envValues(map[string]string{"GOXRPL_PPROF": "192.0.2.1:6060"}),
		net.Listen,
	)
	if err == nil || !strings.Contains(err.Error(), "GOXRPL_PPROF_ALLOW_UNSAFE=true") {
		t.Fatalf("startAuxiliaryServers() error = %v, want unsafe pprof error", err)
	}
}

func TestStartAuxiliaryServersBindsAllBeforeServing(t *testing.T) {
	t.Parallel()

	var acceptCalled atomic.Bool
	first := &testListener{
		addr: testAddr("127.0.0.1:6060"),
		accept: func() (net.Conn, error) {
			acceptCalled.Store(true)
			return nil, errors.New("unexpected accept")
		},
	}
	bindErr := errors.New("address already in use")
	listenCalls := 0
	listen := func(_, _ string) (net.Listener, error) {
		listenCalls++
		if listenCalls == 1 {
			return first, nil
		}
		if acceptCalled.Load() {
			t.Fatal("first server began serving before all auxiliary sockets were bound")
		}
		return nil, bindErr
	}

	_, err := startAuxiliaryServers(
		context.Background(),
		func(error) {},
		envValues(map[string]string{
			"GOXRPL_PPROF":   "127.0.0.1:6060",
			"GOXRPL_METRICS": "127.0.0.1:9100",
		}),
		listen,
	)
	if !errors.Is(err, bindErr) {
		t.Fatalf("startAuxiliaryServers() error = %v, want %v", err, bindErr)
	}
	if !first.closed.Load() {
		t.Fatal("first listener was not closed after later bind failed")
	}
	if acceptCalled.Load() {
		t.Fatal("first server served despite later bind failure")
	}
}

func TestStartAuxiliaryServersServeFailureCancelsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	serveErr := errors.New("listener failed")
	listener := &testListener{
		addr: testAddr("127.0.0.1:9100"),
		accept: func() (net.Conn, error) {
			return nil, serveErr
		},
	}
	aux, err := startAuxiliaryServers(
		ctx,
		cancel,
		envValues(map[string]string{"GOXRPL_METRICS": "127.0.0.1:9100"}),
		func(_, _ string) (net.Listener, error) { return listener, nil },
	)
	if err != nil {
		t.Fatalf("startAuxiliaryServers() error = %v", err)
	}
	defer aux.Shutdown()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context was not canceled after auxiliary serve failure")
	}
	if cause := context.Cause(ctx); cause == nil ||
		!strings.Contains(cause.Error(), "metrics server") ||
		!errors.Is(cause, serveErr) {
		t.Fatalf("context cause = %v, want wrapped metrics serve error", cause)
	}
}

func TestStartAuxiliaryServersServesAndShutsDown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(context.Background())
	aux, err := startAuxiliaryServers(
		ctx,
		cancel,
		envValues(map[string]string{
			"GOXRPL_PPROF":   ":0",
			"GOXRPL_METRICS": ":0",
		}),
		net.Listen,
	)
	if err != nil {
		t.Fatalf("startAuxiliaryServers() error = %v", err)
	}
	addresses := aux.Addresses()
	for name, addr := range addresses {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("%s address %q: %v", name, addr, err)
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			t.Fatalf("%s bound to %q, want loopback", name, addr)
		}
	}

	resp, err := http.Get("http://" + addresses["metrics"] + "/metrics")
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	if !strings.Contains(string(body), "goxrpl_build_info") {
		t.Fatalf("metrics body missing goxrpl_build_info:\n%s", body)
	}

	cancel(context.Canceled)
	if err := aux.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	conn, err := net.DialTimeout("tcp", addresses["metrics"], 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("metrics listener still accepted connections after shutdown")
	}
}

func envValues(values map[string]string) func(string) string {
	return func(name string) string {
		return values[name]
	}
}

type testListener struct {
	addr   net.Addr
	accept func() (net.Conn, error)
	closed atomic.Bool
}

func (l *testListener) Accept() (net.Conn, error) {
	return l.accept()
}

func (l *testListener) Close() error {
	l.closed.Store(true)
	return nil
}

func (l *testListener) Addr() net.Addr {
	return l.addr
}

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }
