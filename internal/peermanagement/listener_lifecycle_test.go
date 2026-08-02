package peermanagement

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOverlayStopDuringListenerBindClosesUnpublishedListener(t *testing.T) {
	o, err := New(
		WithListenAddr("127.0.0.1:0"),
		WithDataDir(t.TempDir()),
		WithMaxPeers(1),
		WithMaxInbound(1),
		WithMaxOutbound(0),
	)
	require.NoError(t, err)

	opened := make(chan net.Listener, 1)
	release := make(chan struct{})
	o.listenFunc = func(_ context.Context, network, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err != nil {
			return nil, err
		}
		opened <- listener
		<-release
		return listener, nil
	}

	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(context.Background()) }()
	var bound net.Listener
	select {
	case bound = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("listener bind hook did not run")
	}
	addr := bound.Addr().String()

	stopDone := make(chan error, 1)
	go func() { stopDone <- o.Stop() }()
	select {
	case err := <-stopDone:
		t.Fatalf("Stop returned before listener preparation completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-runDone:
		require.ErrorIs(t, err, ErrShutdown)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not finish after Stop during listener preparation")
	}
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not finish after Run unwound")
	}

	probe, err := net.Listen("tcp", addr)
	require.NoError(t, err, "unpublished listener must be closed on the Stop race path")
	require.NoError(t, probe.Close())
}
