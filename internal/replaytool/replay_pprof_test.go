package replaytool

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReplayRangeCommandUsesCommandContext(t *testing.T) {
	t.Parallel()

	wantCause := errors.New("stop replay")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(wantCause)

	var gotContext context.Context
	cmd := newReplayRangeCmdWithRun(func(ctx context.Context, _ *replayRangeRunner) error {
		gotContext = ctx
		return nil
	})
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"--from", "1", "--to", "2"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if cause := context.Cause(gotContext); !errors.Is(cause, wantCause) {
		t.Fatalf("command context cause = %v, want %v", cause, wantCause)
	}
}

func TestReplayRangeRunHonorsCanceledContext(t *testing.T) {
	t.Setenv("GOXRPL_PPROF", "")
	wantCause := errors.New("stop replay")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(wantCause)

	runner := &replayRangeRunner{
		out:  io.Discard,
		from: 1,
		to:   2,
	}
	if err := runner.run(ctx); !errors.Is(err, wantCause) {
		t.Fatalf("run() error = %v, want %v", err, wantCause)
	}
}

func TestNormalizeReplayPProfAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         string
		allowUnsafe bool
		want        string
		wantErr     string
	}{
		{name: "empty host", raw: ":6060", want: "127.0.0.1:6060"},
		{name: "IPv4 wildcard", raw: "0.0.0.0:6060", want: "127.0.0.1:6060"},
		{name: "IPv6 wildcard", raw: "[::]:6060", want: "[::1]:6060"},
		{name: "unsafe IPv4 wildcard", raw: "0.0.0.0:6060", allowUnsafe: true, want: "0.0.0.0:6060"},
		{name: "unsafe IPv6 wildcard", raw: "[::]:6060", allowUnsafe: true, want: "[::]:6060"},
		{name: "localhost", raw: "localhost:6060", want: "localhost:6060"},
		{name: "IPv4 loopback", raw: "127.0.0.2:6060", want: "127.0.0.2:6060"},
		{name: "remote rejected", raw: "192.0.2.1:6060", wantErr: "ALLOW_UNSAFE=true"},
		{name: "remote opted in", raw: "192.0.2.1:6060", allowUnsafe: true, want: "192.0.2.1:6060"},
		{name: "missing port", raw: "localhost", wantErr: "missing port"},
		{name: "named port", raw: "localhost:http", wantErr: "port must be a number"},
		{name: "out of range port", raw: "localhost:65536", wantErr: "port must be a number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeReplayPProfAddress(test.raw, test.allowUnsafe)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("normalizeReplayPProfAddress() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeReplayPProfAddress() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("normalizeReplayPProfAddress() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStartReplayPProfRejectsMalformedUnsafeSetting(t *testing.T) {
	t.Parallel()

	_, err := startReplayPProfWithDependencies(
		func(error) {},
		replayPProfEnv(map[string]string{
			"GOXRPL_PPROF":              ":6060",
			"GOXRPL_PPROF_ALLOW_UNSAFE": "1",
		}),
		net.Listen,
		func() {},
	)
	if err == nil || !strings.Contains(err.Error(), `must be "true" or "false"`) {
		t.Fatalf("startReplayPProfWithDependencies() error = %v, want strict boolean error", err)
	}
}

func TestStartReplayPProfBindsBeforeEnabling(t *testing.T) {
	t.Parallel()

	bindErr := errors.New("address already in use")
	var enabled atomic.Bool
	_, err := startReplayPProfWithDependencies(
		func(error) {},
		replayPProfEnv(map[string]string{"GOXRPL_PPROF": ":6060"}),
		func(_, _ string) (net.Listener, error) {
			return nil, bindErr
		},
		func() {
			enabled.Store(true)
		},
	)
	if !errors.Is(err, bindErr) {
		t.Fatalf("startReplayPProfWithDependencies() error = %v, want %v", err, bindErr)
	}
	if enabled.Load() {
		t.Fatal("profiling was enabled before the listener bound successfully")
	}
}

func TestReplayRangeRunReturnsPProfBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()
	t.Setenv("GOXRPL_PPROF", listener.Addr().String())
	t.Setenv("GOXRPL_PPROF_ALLOW_UNSAFE", "false")

	runner := &replayRangeRunner{
		out:  io.Discard,
		from: 1,
		to:   2,
	}
	err = runner.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "bind pprof server") {
		t.Fatalf("run() error = %v, want pprof bind failure", err)
	}
}

func TestStartReplayPProfServeFailureCancelsAndJoins(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	serveErr := errors.New("listener failed")
	listener := &replayPProfTestListener{
		addr: replayPProfTestAddr("127.0.0.1:6060"),
		accept: func() (net.Conn, error) {
			return nil, serveErr
		},
	}
	profiler, err := startReplayPProfWithDependencies(
		cancel,
		replayPProfEnv(map[string]string{"GOXRPL_PPROF": ":6060"}),
		func(_, _ string) (net.Listener, error) {
			return listener, nil
		},
		func() {},
	)
	if err != nil {
		t.Fatalf("startReplayPProfWithDependencies() error = %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context was not canceled after pprof serve failure")
	}
	if cause := context.Cause(ctx); cause == nil ||
		!strings.Contains(cause.Error(), "pprof server") ||
		!errors.Is(cause, serveErr) {
		t.Fatalf("context cause = %v, want wrapped pprof serve error", cause)
	}
	if err := profiler.Shutdown(); !errors.Is(err, serveErr) {
		t.Fatalf("Shutdown() error = %v, want wrapped %v", err, serveErr)
	}
}

func TestStartReplayPProfServesOnLoopbackAndShutsDown(t *testing.T) {
	t.Parallel()

	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	profiler, err := startReplayPProfWithDependencies(
		cancel,
		replayPProfEnv(map[string]string{"GOXRPL_PPROF": ":0"}),
		net.Listen,
		func() {},
	)
	if err != nil {
		t.Fatalf("startReplayPProfWithDependencies() error = %v", err)
	}

	host, _, err := net.SplitHostPort(profiler.Addr())
	if err != nil {
		t.Fatalf("profile address %q: %v", profiler.Addr(), err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("profile server bound to %q, want loopback", profiler.Addr())
	}
	if err := profiler.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := profiler.Shutdown(); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
	conn, err := net.DialTimeout("tcp", profiler.Addr(), 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("pprof listener still accepted connections after shutdown")
	}
}

func replayPProfEnv(values map[string]string) func(string) string {
	return func(name string) string {
		return values[name]
	}
}

type replayPProfTestListener struct {
	addr   net.Addr
	accept func() (net.Conn, error)
}

func (l *replayPProfTestListener) Accept() (net.Conn, error) {
	return l.accept()
}

func (l *replayPProfTestListener) Close() error {
	return nil
}

func (l *replayPProfTestListener) Addr() net.Addr {
	return l.addr
}

type replayPProfTestAddr string

func (a replayPProfTestAddr) Network() string { return "tcp" }
func (a replayPProfTestAddr) String() string  { return string(a) }
