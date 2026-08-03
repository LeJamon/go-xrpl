package node

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newConnectionLimitTestTransports(t *testing.T, protocol string, limit int) *boundRPCTransports {
	t.Helper()
	ports := map[string]config.PortConfig{
		protocol: {IP: "127.0.0.1", Port: 0, Protocol: protocol, Limit: limit},
	}
	if protocol == "ws" {
		ports["http"] = config.PortConfig{IP: "127.0.0.1", Port: 0, Protocol: "http"}
	}
	bound, err := bindRPCTransports(
		t.Context(),
		xrpllog.Discard(),
		&config.Config{Ports: ports},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		}),
		rpc.NewWebSocketServer(time.Second, nil),
		nil,
		systemListen,
	)
	require.NoError(t, err)
	require.NoError(t, bound.serve(xrpllog.Discard()))
	t.Cleanup(func() {
		for _, server := range append(append([]*boundHTTPServer(nil), bound.ws...), bound.http...) {
			_ = server.server.Close()
		}
		_ = bound.closeListeners()
		bound.wait()
	})
	return bound
}

func TestRPCConnectionLimitCountsIdleTCPConnections(t *testing.T) {
	bound := newConnectionLimitTestTransports(t, "http", 1)
	address := bound.http[0].address

	first, err := net.Dial("tcp", address)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	require.Eventually(t, func() bool {
		bound.limiter.mu.Lock()
		defer bound.limiter.mu.Unlock()
		return bound.limiter.counts["http"] == 1
	}, time.Second, time.Millisecond)

	second, err := net.Dial("tcp", address)
	require.NoError(t, err)
	require.NoError(t, second.SetDeadline(time.Now().Add(time.Second)))
	_, _ = io.WriteString(second, "GET /health HTTP/1.1\r\nHost: localhost\r\n\r\n")
	_, err = bufio.NewReader(second).ReadString('\n')
	require.Error(t, err)
	_ = second.Close()

	require.NoError(t, first.Close())
	require.Eventually(t, func() bool {
		bound.limiter.mu.Lock()
		defer bound.limiter.mu.Unlock()
		return bound.limiter.counts["http"] == 0
	}, time.Second, time.Millisecond)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Get("http://" + address + "/health")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
}

func TestRPCConnectionLimitCountsKeepAliveOnce(t *testing.T) {
	bound := newConnectionLimitTestTransports(t, "http", 1)
	transport := &http.Transport{MaxConnsPerHost: 1}
	client := &http.Client{Transport: transport}
	t.Cleanup(transport.CloseIdleConnections)

	for range 2 {
		response, err := client.Get("http://" + bound.http[0].address + "/health")
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode)
	}

	bound.limiter.mu.Lock()
	require.Equal(t, 1, bound.limiter.counts["http"])
	bound.limiter.mu.Unlock()
}

func TestRPCConnectionLimitOwnsWebSocketLifetime(t *testing.T) {
	bound := newConnectionLimitTestTransports(t, "ws", 1)
	url := "ws://" + bound.ws[0].address + "/"

	first, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)

	_, response, err := websocket.DefaultDialer.Dial(url, nil)
	require.Error(t, err)
	if response != nil {
		_ = response.Body.Close()
	}

	require.NoError(t, first.Close())
	require.Eventually(t, func() bool {
		third, _, dialErr := websocket.DefaultDialer.Dial(url, nil)
		if dialErr != nil {
			return false
		}
		_ = third.Close()
		return true
	}, time.Second, 10*time.Millisecond)
}

func TestRPCConnectionLimitIgnoresUpgradeShapedHTTPHeaders(t *testing.T) {
	bound := newConnectionLimitTestTransports(t, "http", 1)
	address := bound.http[0].address
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/health", nil)
	require.NoError(t, err)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Close = true

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	require.Eventually(t, func() bool {
		response, getErr := http.Get("http://" + address + "/health")
		if getErr != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == http.StatusOK
	}, time.Second, 10*time.Millisecond)
}

func TestRPCConnectionLimitReleasesAllSlotsOnShutdown(t *testing.T) {
	bound := newConnectionLimitTestTransports(t, "http", 2)
	first, err := net.Dial("tcp", bound.http[0].address)
	require.NoError(t, err)
	second, err := net.Dial("tcp", bound.http[0].address)
	require.NoError(t, err)
	defer first.Close()
	defer second.Close()

	require.Eventually(t, func() bool {
		bound.limiter.mu.Lock()
		defer bound.limiter.mu.Unlock()
		return bound.limiter.total == 2
	}, time.Second, time.Millisecond)
	require.NoError(t, bound.http[0].server.Close())
	require.Eventually(t, func() bool {
		bound.limiter.mu.Lock()
		defer bound.limiter.mu.Unlock()
		return bound.limiter.total == 0
	}, time.Second, time.Millisecond)
}

func TestRPCConnectionLimitReleasesRejectedHTTPTransportConnections(t *testing.T) {
	bound, err := bindRPCTransports(
		t.Context(),
		xrpllog.Discard(),
		&config.Config{Ports: map[string]config.PortConfig{
			"http": {
				IP:             "127.0.0.1",
				Port:           0,
				Protocol:       "http",
				Limit:          1,
				User:           "operator",
				Password:       "secret",
				AllowedOrigins: []string{"https://console.example"},
			},
		}},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		rpc.NewWebSocketServer(time.Second, nil),
		nil,
		systemListen,
	)
	require.NoError(t, err)
	require.NoError(t, bound.serve(xrpllog.Discard()))
	t.Cleanup(func() {
		_ = bound.http[0].server.Close()
		_ = bound.closeListeners()
		bound.wait()
	})

	url := "http://" + bound.http[0].address + "/health"
	for _, test := range []struct {
		name     string
		origin   string
		user     string
		password string
		status   int
	}{
		{name: "missing credentials", status: http.StatusUnauthorized},
		{name: "wrong credentials", user: "operator", password: "wrong", status: http.StatusUnauthorized},
		{name: "rejected origin", origin: "https://attacker.example", user: "operator", password: "secret", status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, requestErr := http.NewRequest(http.MethodGet, url, nil)
			require.NoError(t, requestErr)
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if test.user != "" || test.password != "" {
				req.SetBasicAuth(test.user, test.password)
			}
			response, requestErr := http.DefaultClient.Do(req)
			require.NoError(t, requestErr)
			_, requestErr = io.Copy(io.Discard, response.Body)
			require.NoError(t, requestErr)
			require.NoError(t, response.Body.Close())
			require.Equal(t, test.status, response.StatusCode)
			require.Eventually(t, func() bool {
				bound.limiter.mu.Lock()
				defer bound.limiter.mu.Unlock()
				return bound.limiter.counts["http"] == 0
			}, time.Second, time.Millisecond)
		})
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.SetBasicAuth("operator", "secret")
	req.Close = true
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
}

func TestRPCConnectionLimitReleasesRejectedWebSocketConnections(t *testing.T) {
	wsServer := rpc.NewWebSocketServer(time.Second, nil)
	bound, err := bindRPCTransports(
		t.Context(),
		xrpllog.Discard(),
		&config.Config{Ports: map[string]config.PortConfig{
			"http": {IP: "127.0.0.1", Port: 0, Protocol: "http"},
			"ws": {
				IP:             "127.0.0.1",
				Port:           0,
				Protocol:       "ws",
				Limit:          1,
				User:           "operator",
				Password:       "secret",
				AllowedOrigins: []string{"https://console.example"},
			},
		}},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		wsServer,
		nil,
		systemListen,
	)
	require.NoError(t, err)
	require.NoError(t, bound.serve(xrpllog.Discard()))
	t.Cleanup(func() {
		for _, server := range append(append([]*boundHTTPServer(nil), bound.ws...), bound.http...) {
			_ = server.server.Close()
		}
		_ = bound.closeListeners()
		bound.wait()
		_ = wsServer.Close(context.Background())
	})

	url := "ws://" + bound.ws[0].address + "/"
	for _, test := range []struct {
		name   string
		header http.Header
		status int
	}{
		{name: "missing credentials", header: http.Header{}, status: http.StatusUnauthorized},
		{name: "rejected origin", header: http.Header{
			"Origin":        []string{"https://attacker.example"},
			"Authorization": []string{"Basic b3BlcmF0b3I6c2VjcmV0"},
		}, status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, response, dialErr := websocket.DefaultDialer.Dial(url, test.header)
			require.Error(t, dialErr)
			require.NotNil(t, response)
			_ = response.Body.Close()
			require.Equal(t, test.status, response.StatusCode)
			require.Eventually(t, func() bool {
				bound.limiter.mu.Lock()
				defer bound.limiter.mu.Unlock()
				return bound.limiter.counts["ws"] == 0
			}, time.Second, time.Millisecond)
		})
	}

	authorized := http.Header{
		"Origin":        []string{"https://console.example"},
		"Authorization": []string{"Basic b3BlcmF0b3I6c2VjcmV0"},
	}
	plainRequest, err := http.NewRequest(http.MethodGet, "http://"+bound.ws[0].address+"/", nil)
	require.NoError(t, err)
	plainRequest.Header = authorized.Clone()
	response, err := http.DefaultClient.Do(plainRequest)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Eventually(t, func() bool {
		bound.limiter.mu.Lock()
		defer bound.limiter.mu.Unlock()
		return bound.limiter.counts["ws"] == 0
	}, time.Second, time.Millisecond)

	conn, response, err := websocket.DefaultDialer.Dial(url, authorized)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
	require.NoError(t, conn.Close())
}

func TestRPCConnectionLimitIsGlobalAcrossHTTPAndWebSocketPorts(t *testing.T) {
	wsServer := rpc.NewWebSocketServer(time.Second, nil)
	bound, err := bindRPCTransports(
		t.Context(),
		xrpllog.Discard(),
		&config.Config{
			Server: config.ServerConfig{MaxConnections: 1},
			Ports: map[string]config.PortConfig{
				"http": {IP: "127.0.0.1", Port: 0, Protocol: "http"},
				"ws":   {IP: "127.0.0.1", Port: 0, Protocol: "ws"},
			},
		},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		wsServer,
		nil,
		systemListen,
	)
	require.NoError(t, err)
	require.NoError(t, bound.serve(xrpllog.Discard()))
	t.Cleanup(func() {
		for _, server := range append(append([]*boundHTTPServer(nil), bound.ws...), bound.http...) {
			_ = server.server.Close()
		}
		_ = bound.closeListeners()
		bound.wait()
		_ = wsServer.Close(context.Background())
	})

	idleHTTP, err := net.Dial("tcp", bound.http[0].address)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		bound.limiter.mu.Lock()
		defer bound.limiter.mu.Unlock()
		return bound.limiter.total == 1
	}, time.Second, time.Millisecond)

	url := "ws://" + bound.ws[0].address + "/"
	_, response, err := websocket.DefaultDialer.Dial(url, nil)
	require.Error(t, err)
	if response != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, idleHTTP.Close())

	require.Eventually(t, func() bool {
		conn, _, dialErr := websocket.DefaultDialer.Dial(url, nil)
		if dialErr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, time.Second, 10*time.Millisecond)
}

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

func TestBoundRPCTransportsHealthUsesTransportBasicAuth(t *testing.T) {
	bound, err := bindRPCTransports(
		context.Background(),
		xrpllog.Discard(),
		&config.Config{Ports: map[string]config.PortConfig{
			"http": {
				IP:       "127.0.0.1",
				Port:     0,
				Protocol: "http",
				User:     "operator",
				Password: "transport-secret",
			},
		}},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		rpc.NewWebSocketServer(time.Second, nil),
		nil,
		systemListen,
	)
	require.NoError(t, err)
	require.NoError(t, bound.serve(xrpllog.Discard()))
	defer func() {
		_ = bound.http[0].server.Shutdown(context.Background())
		_ = bound.closeListeners()
		bound.wait()
	}()

	healthURL := "http://" + bound.http[0].address + "/health"
	for _, test := range []struct {
		name     string
		user     string
		password string
		want     int
	}{
		{name: "correct credentials", user: "operator", password: "transport-secret", want: http.StatusOK},
		{name: "missing credentials", want: http.StatusUnauthorized},
		{name: "incorrect credentials", user: "operator", password: "wrong", want: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, requestErr := http.NewRequest(http.MethodGet, healthURL, nil)
			require.NoError(t, requestErr)
			if test.user != "" || test.password != "" {
				req.SetBasicAuth(test.user, test.password)
			}
			response, doErr := http.DefaultClient.Do(req)
			require.NoError(t, doErr)
			defer response.Body.Close()
			require.Equal(t, test.want, response.StatusCode)
		})
	}
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
