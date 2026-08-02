package node

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBindRPCTransportsValidatesAllPortsBeforeBinding(t *testing.T) {
	cfg := &config.Config{Ports: map[string]config.PortConfig{
		"a_ws": {
			IP: "127.0.0.1", Port: 10001, Protocol: "ws",
		},
		"b_http": {
			IP: "127.0.0.1", Port: 10002, Protocol: "http", Admin: []string{"invalid network"},
		},
	}}
	var calls atomic.Int32

	bound, err := bindRPCTransports(
		context.Background(),
		xrpllog.Discard(),
		cfg,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		rpc.NewWebSocketServer(time.Second, nil),
		nil,
		func(context.Context, string, string) (net.Listener, error) {
			calls.Add(1)
			return nil, errors.New("must not bind")
		},
	)
	require.ErrorContains(t, err, "parse admin nets")
	require.Nil(t, bound)
	require.Zero(t, calls.Load())
}

func TestBindRPCTransportsClosesEarlierListenersOnLaterFailure(t *testing.T) {
	cfg := &config.Config{Ports: map[string]config.PortConfig{
		"a_http": {IP: "127.0.0.1", Port: 10001, Protocol: "http"},
		"b_http": {IP: "127.0.0.1", Port: 10002, Protocol: "http"},
	}}
	wantErr := errors.New("occupied")
	var first net.Listener
	var calls atomic.Int32

	bound, err := bindRPCTransports(
		context.Background(),
		xrpllog.Discard(),
		cfg,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		rpc.NewWebSocketServer(time.Second, nil),
		nil,
		func(context.Context, string, string) (net.Listener, error) {
			if calls.Add(1) == 2 {
				return nil, wantErr
			}
			listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
			first = listener
			return listener, listenErr
		},
	)
	require.ErrorIs(t, err, wantErr)
	require.Nil(t, bound)
	require.NotNil(t, first)
	_, acceptErr := first.Accept()
	require.ErrorIs(t, acceptErr, net.ErrClosed)
}

func TestBindRPCTransportsClosesHTTPWhenGRPCBindFails(t *testing.T) {
	cfg := &config.Config{Ports: map[string]config.PortConfig{
		"a_http": {IP: "127.0.0.1", Port: 10001, Protocol: "http"},
		"b_grpc": {IP: "127.0.0.1", Port: 10002, Protocol: "grpc"},
	}}
	wantErr := errors.New("grpc occupied")
	var first net.Listener
	var calls atomic.Int32

	bound, err := bindRPCTransports(
		context.Background(),
		xrpllog.Discard(),
		cfg,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		rpc.NewWebSocketServer(time.Second, nil),
		&stubLookup{},
		func(context.Context, string, string) (net.Listener, error) {
			if calls.Add(1) == 2 {
				return nil, wantErr
			}
			listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
			first = listener
			return listener, listenErr
		},
	)
	require.ErrorIs(t, err, wantErr)
	require.Nil(t, bound)
	require.NotNil(t, first)
	_, acceptErr := first.Accept()
	require.ErrorIs(t, acceptErr, net.ErrClosed)
}

type trackingListener struct {
	net.Listener
	accepts atomic.Int32
}

func (l *trackingListener) Accept() (net.Conn, error) {
	l.accepts.Add(1)
	return l.Listener.Accept()
}

func TestBoundRPCTransportsDoNotServeBeforeCommit(t *testing.T) {
	cfg := &config.Config{Ports: map[string]config.PortConfig{
		"http": {IP: "127.0.0.1", Port: 10001, Protocol: "http"},
	}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tracked := &trackingListener{Listener: listener}

	bound, err := bindRPCTransports(
		context.Background(),
		xrpllog.Discard(),
		cfg,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		rpc.NewWebSocketServer(time.Second, nil),
		nil,
		func(context.Context, string, string) (net.Listener, error) { return tracked, nil },
	)
	require.NoError(t, err)
	require.Zero(t, tracked.accepts.Load())

	require.NoError(t, bound.serve(xrpllog.Discard()))
	require.Eventually(t, func() bool { return tracked.accepts.Load() > 0 }, time.Second, time.Millisecond)
	require.NoError(t, bound.http[0].server.Close())
	require.NoError(t, bound.closeListeners())
	bound.wait()
}

func TestBoundRPCTransportsServeAndJoinAllProtocols(t *testing.T) {
	cfg := &config.Config{Ports: map[string]config.PortConfig{
		"http": {IP: "127.0.0.1", Port: 0, Protocol: "http"},
		"ws":   {IP: "127.0.0.1", Port: 0, Protocol: "ws"},
		"grpc": {IP: "127.0.0.1", Port: 0, Protocol: "grpc"},
	}}
	bound, err := bindRPCTransports(
		context.Background(),
		xrpllog.Discard(),
		cfg,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		rpc.NewWebSocketServer(time.Second, nil),
		&stubLookup{},
		systemListen,
	)
	require.NoError(t, err)
	require.NoError(t, bound.serve(xrpllog.Discard()))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, server := range append(append([]*boundHTTPServer(nil), bound.ws...), bound.http...) {
		require.NoError(t, server.server.Shutdown(ctx))
	}
	bound.grpc.server.Stop()
	require.NoError(t, bound.closeListeners())
	done := make(chan struct{})
	go func() {
		bound.wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transport Serve goroutines did not stop")
	}
	select {
	case err := <-bound.errors:
		t.Fatalf("graceful transport stop reported a fatal error: %v", err)
	default:
	}
}

func TestBoundRPCTransportsPreStoppedServersDoNotBlockServe(t *testing.T) {
	cfg := &config.Config{Ports: map[string]config.PortConfig{
		"http": {IP: "127.0.0.1", Port: 0, Protocol: "http"},
		"grpc": {IP: "127.0.0.1", Port: 0, Protocol: "grpc"},
	}}
	bound, err := bindRPCTransports(
		context.Background(),
		xrpllog.Discard(),
		cfg,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		rpc.NewWebSocketServer(time.Second, nil),
		&stubLookup{},
		systemListen,
	)
	require.NoError(t, err)
	require.NoError(t, bound.http[0].server.Close())
	bound.grpc.server.Stop()

	done := make(chan error, 1)
	go func() { done <- bound.serve(xrpllog.Discard()) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("serve blocked after transports were stopped before serving")
	}
	require.NoError(t, bound.closeListeners())
	bound.wait()
}

func TestShutdownTransportsForceClosesStuckHTTPHandler(t *testing.T) {
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	defer cancelRuntime()
	started := make(chan struct{})
	release := make(chan struct{})
	bound, err := bindRPCTransports(
		runtimeCtx,
		xrpllog.Discard(),
		&config.Config{Ports: map[string]config.PortConfig{
			"http": {IP: "127.0.0.1", Port: 0, Protocol: "http"},
		}},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(started)
			<-release
		}),
		rpc.NewWebSocketServer(time.Second, nil),
		nil,
		systemListen,
	)
	require.NoError(t, err)
	require.NoError(t, bound.serve(xrpllog.Discard()))
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + bound.http[0].address + "/")
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not start")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 50*time.Millisecond)
	startedAt := time.Now()
	complete, err := shutdownTransports(shutdownCtx, bound, nil, xrpllog.Discard())
	cancelShutdown()
	require.False(t, complete)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(startedAt), time.Second)
	close(release)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("forced transport close did not release the client")
	}
}

func TestShutdownTransportsDoesNotCompleteWithStuckGRPCHandler(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := &boundGRPCServer{
		listener: listener,
		server:   googlegrpc.NewServer(),
	}
	bound := &boundRPCTransports{grpc: grpcServer}
	started := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		_, _ = grpcServer.trackUnary(
			context.Background(),
			nil,
			nil,
			func(context.Context, any) (any, error) {
				close(started)
				<-release
				return nil, nil
			},
		)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("gRPC handler did not start")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 50*time.Millisecond)
	complete, err := shutdownTransports(shutdownCtx, bound, nil, xrpllog.Discard())
	cancelShutdown()
	require.False(t, complete)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, admissionErr := grpcServer.trackUnary(
		context.Background(),
		nil,
		nil,
		func(context.Context, any) (any, error) { return nil, nil },
	)
	require.Equal(t, codes.Unavailable, status.Code(admissionErr))

	close(release)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("gRPC handler did not exit after release")
	}
}
