package peermanagement

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

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
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return o
}

func waitRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("overlay Run did not return")
		return nil
	}
}

func TestOverlayRunCancellationOwnsResources(t *testing.T) {
	o := newLifecycleTestOverlay(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.Run(ctx) }()

	select {
	case <-o.ListenerReady():
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not become ready")
	}
	addr := o.ListenAddr()
	cancel()
	if err := waitRun(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if err := o.Stop(); err != nil {
		t.Fatalf("Stop after Run: %v", err)
	}

	conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("listener still accepted connections after cancellation")
	}
}

func TestOverlayRunIsOneShot(t *testing.T) {
	o := newLifecycleTestOverlay(t, WithListenAddr(""))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.Run(ctx) }()

	select {
	case <-o.ListenerReady():
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not finish startup")
	}
	if err := o.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("concurrent Run error = %v, want ErrAlreadyRunning", err)
	}
	cancel()
	if err := waitRun(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run error = %v, want context.Canceled", err)
	}
	if err := o.Run(context.Background()); !errors.Is(err, ErrShutdown) {
		t.Fatalf("second Run error = %v, want ErrShutdown", err)
	}
}

func TestOverlayRunAfterStop(t *testing.T) {
	o := newLifecycleTestOverlay(t, WithListenAddr(""))
	if err := o.Stop(); err != nil {
		t.Fatalf("Stop before Run: %v", err)
	}
	if err := o.Stop(); err != nil {
		t.Fatalf("repeated Stop before Run: %v", err)
	}
	if err := o.Run(context.Background()); !errors.Is(err, ErrShutdown) {
		t.Fatalf("Run after Stop error = %v, want ErrShutdown", err)
	}
}

func TestOverlayRunStartupFailureUnwinds(t *testing.T) {
	o := newLifecycleTestOverlay(t, WithListenAddr("127.0.0.1:not-a-port"))
	done := make(chan error, 1)
	go func() { done <- o.Run(context.Background()) }()
	if err := waitRun(t, done); err == nil {
		t.Fatal("Run unexpectedly succeeded with an invalid listener address")
	}
	if err := o.Stop(); err != nil {
		t.Fatalf("Stop after startup failure: %v", err)
	}
}
