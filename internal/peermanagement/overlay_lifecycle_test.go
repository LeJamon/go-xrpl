package peermanagement

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requirePeerTLSSupported(t *testing.T) {
	t.Helper()
	if !peertls.Supported() {
		t.Skip("peer TLS requires CGO")
	}
}

func newLifecycleTestOverlay(t *testing.T, opts ...Option) *Overlay {
	t.Helper()
	base := []Option{
		WithListenAddr("127.0.0.1:0"),
		WithDataDir(t.TempDir()),
		WithMaxPeers(2),
		WithMaxInbound(1),
		WithMaxOutbound(1),
	}
	o, err := New(append(base, opts...)...)
	require.NoError(t, err)
	return o
}

func TestOverlayConnectContextHonorsCallerCancellation(t *testing.T) {
	o := newLifecycleTestOverlay(t)
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	o.lifecycleMu.Lock()
	o.ctx = baseCtx
	o.lifecycleMu.Unlock()
	connectCtx, cancelConnect := context.WithCancel(context.Background())
	cancelConnect()
	err := o.ConnectContext(connectCtx, "127.0.0.1:1")
	require.ErrorIs(t, err, context.Canceled)
}

func TestOverlayRunCancellationClosesListener(t *testing.T) {
	requirePeerTLSSupported(t)
	o, err := New(WithDataDir(t.TempDir()), WithListenAddr("127.0.0.1:0"), WithPrivateMode(true))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(ctx) }()
	select {
	case <-o.ListenerReady():
	case err := <-runDone:
		t.Fatalf("overlay stopped before startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("overlay did not become ready")
	}
	addr := o.ListenAddr()

	cancel()
	select {
	case err := <-runDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("overlay did not stop after cancellation")
	}
	require.NoError(t, o.Stop())

	conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("listener still accepted connections after cancellation")
	}
}

func TestOverlayRunStartupFailureUnwinds(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()), WithListenAddr("127.0.0.1:not-a-port"))
	require.NoError(t, err)
	require.Error(t, o.Run(context.Background()))
	require.NoError(t, o.Stop())
}

func TestOverlayStopPersistsLatePeerMutation(t *testing.T) {
	requirePeerTLSSupported(t)
	dir := t.TempDir()
	o, err := New(WithDataDir(dir), WithListenAddr("127.0.0.1:0"), WithPrivateMode(true))
	require.NoError(t, err)
	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(context.Background()) }()
	select {
	case <-o.ListenerReady():
	case err := <-runDone:
		t.Fatalf("overlay stopped before startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("overlay did not become ready")
	}

	const address = "198.51.100.20:51235"
	release := make(chan struct{})
	o.peerWG.Add(1)
	go func() {
		defer o.peerWG.Done()
		<-release
		o.discovery.MarkConnected(address, PeerID(20))
	}()

	stopDone := make(chan struct{})
	go func() {
		_ = o.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop completed before the in-flight peer producer joined")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not complete after the peer producer joined")
	}
	<-runDone

	reloaded := NewBootCache(filepath.Clean(dir))
	require.NoError(t, reloaded.Load())
	assert.Len(t, reloaded.Endpoints(0), 1)
	assert.Equal(t, address, reloaded.Endpoints(0)[0].Address)
}

func TestOverlayContextCancellationPersistsLatePeerMutation(t *testing.T) {
	requirePeerTLSSupported(t)
	dir := t.TempDir()
	o, err := New(WithDataDir(dir), WithListenAddr("127.0.0.1:0"), WithPrivateMode(true))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(ctx) }()
	select {
	case <-o.ListenerReady():
	case err := <-runDone:
		t.Fatalf("overlay stopped before startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("overlay did not become ready")
	}

	const address = "198.51.100.21:51235"
	release := make(chan struct{})
	o.peerWG.Add(1)
	go func() {
		defer o.peerWG.Done()
		<-release
		o.discovery.MarkConnected(address, PeerID(21))
	}()
	cancel()
	select {
	case <-runDone:
		t.Fatal("Run returned before the in-flight peer producer joined")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not complete after the peer producer joined")
	}

	reloaded := NewBootCache(filepath.Clean(dir))
	require.NoError(t, reloaded.Load())
	assert.Len(t, reloaded.Endpoints(0), 1)
	assert.Equal(t, address, reloaded.Endpoints(0)[0].Address)
}

func TestOverlayStopBeforeRunRejectsRun(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()), WithListenAddr("127.0.0.1:0"))
	require.NoError(t, err)
	require.NoError(t, o.Stop())
	err = o.Run(context.Background())
	assert.ErrorIs(t, err, ErrShutdown)
	assert.Empty(t, o.ListenAddr())
}

func TestOverlayConcurrentStopRejectsStartupBeforeReadiness(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()), WithListenAddr("127.0.0.1:0"))
	require.NoError(t, err)
	o.discovery.cfg.BootstrapPeers = []string{"blocked.example:51235"}
	entered := make(chan struct{})
	o.discovery.lookupIP = func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery startup did not enter the blocking resolver")
	}
	require.NoError(t, o.Stop())
	err = <-runDone
	assert.ErrorIs(t, err, ErrShutdown)
	assert.Empty(t, o.ListenAddr())
	select {
	case <-o.ListenerReady():
		t.Fatal("concurrent Stop allowed startup readiness")
	default:
	}
}

func TestOverlayRejectsDoubleRun(t *testing.T) {
	requirePeerTLSSupported(t)
	o, err := New(WithDataDir(t.TempDir()), WithListenAddr("127.0.0.1:0"), WithPrivateMode(true))
	require.NoError(t, err)
	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(context.Background()) }()
	select {
	case <-o.ListenerReady():
	case err := <-runDone:
		t.Fatalf("overlay stopped before startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("overlay did not become ready")
	}

	assert.ErrorIs(t, o.Run(context.Background()), ErrAlreadyRunning)
	require.NoError(t, o.Stop())
	<-runDone
	assert.ErrorIs(t, o.Run(context.Background()), ErrShutdown)
}
