package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/gorilla/websocket"
)

type blockingWriteConn struct {
	net.Conn
	armed        chan struct{}
	writeStarted chan struct{}
	closed       chan struct{}
	startOnce    sync.Once
	closeOnce    sync.Once
}

func (c *blockingWriteConn) Write(p []byte) (int, error) {
	select {
	case <-c.armed:
		c.startOnce.Do(func() { close(c.writeStarted) })
		<-c.closed
		return 0, net.ErrClosed
	default:
		return c.Conn.Write(p)
	}
}

func (c *blockingWriteConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type blockingWriteListener struct {
	net.Listener
	accepted chan *blockingWriteConn
}

func (l *blockingWriteListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := &blockingWriteConn{
		Conn:         conn,
		armed:        make(chan struct{}),
		writeStarted: make(chan struct{}),
		closed:       make(chan struct{}),
	}
	l.accepted <- wrapped
	return wrapped, nil
}

// TestWebSocketServer_Close_JoinsHandlers verifies that Close blocks until
// all per-connection goroutines (read loop, send pump, ping loop) have exited.
func TestWebSocketServer_Close_JoinsHandlers(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})

	httpSrv := httptest.NewServer(http.HandlerFunc(ws.ServeHTTP))
	defer httpSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	const numConns = 5
	clients := make([]*websocket.Conn, 0, numConns)
	for i := range numConns {
		c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		clients = append(clients, c)
	}

	// Wait until all connections are registered and goroutines are running.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ws.connectionsMutex.RLock()
		n := len(ws.connections)
		ws.connectionsMutex.RUnlock()
		if n == numConns {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	goroutinesBefore := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	closeDone := make(chan error, 1)
	go func() { closeDone <- ws.Close(ctx) }()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5s")
	}

	// After Close returns, goroutine count should drop. Allow runtime slack
	// for unrelated goroutines but assert per-connection goroutines exited.
	// Each connection contributes 3 goroutines (read, send, ping). Allow a
	// small margin for net/http server housekeeping.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= goroutinesBefore-numConns {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > goroutinesBefore-numConns+2 {
		t.Errorf("expected goroutine count to drop after Close; before=%d after=%d", goroutinesBefore, got)
	}

	for _, c := range clients {
		_ = c.Close()
	}
}

// TestWebSocketServer_Close_RespectsContext verifies Close returns promptly
// when the context expires, even if handlers might otherwise linger.
func TestWebSocketServer_Close_RespectsContext(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})

	// Inflate the WaitGroup so it never reaches zero on its own.
	ws.wg.Add(1)
	released := false
	defer func() {
		if !released {
			ws.wg.Done()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := ws.Close(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected Close to return ctx.Err() when wg never drains")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Close took too long despite ctx deadline: %v", elapsed)
	}

	ws.wg.Done()
	released = true
	joinCtx, cancelJoin := context.WithTimeout(context.Background(), time.Second)
	defer cancelJoin()
	if err := ws.Close(joinCtx); err != nil {
		t.Fatalf("second Close did not join the in-flight shutdown: %v", err)
	}
}

func TestWebSocketServer_Close_ContextInterruptsBlockedControlWrite(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	pc := &PortContext{PortName: "wsport", Limit: 1}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wrappedListener := &blockingWriteListener{
		Listener: listener,
		accepted: make(chan *blockingWriteConn, 1),
	}
	httpSrv := httptest.NewUnstartedServer(PortMiddleware(pc, http.HandlerFunc(ws.ServeHTTP)))
	httpSrv.Listener.Close()
	httpSrv.Listener = wrappedListener
	httpSrv.Start()
	defer httpSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	serverConn := <-wrappedListener.accepted

	var wsConn *websocketConnection
	deadline := time.Now().Add(time.Second)
	for wsConn == nil && time.Now().Before(deadline) {
		ws.connectionsMutex.RLock()
		for _, conn := range ws.connections {
			wsConn = conn
			break
		}
		ws.connectionsMutex.RUnlock()
		runtime.Gosched()
	}
	if wsConn == nil {
		t.Fatal("WebSocket connection was not registered")
	}

	close(serverConn.armed)
	if !wsConn.TrySend([]byte("block the server writer")) {
		t.Fatal("failed to queue blocking frame")
	}
	select {
	case <-serverConn.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("server write did not block")
	}

	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- ws.Close(ctx)
	}()
	for {
		ws.connectionsMutex.RLock()
		closing := ws.closing
		ws.connectionsMutex.RUnlock()
		if closing {
			break
		}
		runtime.Gosched()
	}
	cancel()

	select {
	case err = <-closeDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close did not return after context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Close error = %v, want context canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Close took %v with a blocked control write", elapsed)
	}
	select {
	case <-serverConn.closed:
	default:
		t.Fatal("Close did not close the blocked socket")
	}
}

// TestWebSocketServer_Close_NoConnections verifies Close is safe with no
// active connections and returns immediately.
func TestWebSocketServer_Close_NoConnections(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ws.Close(ctx); err != nil {
		t.Fatalf("Close on empty server: %v", err)
	}
}

func TestWebSocketConnectionCloseSocketUnblocksRead(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	httpSrv := httptest.NewServer(http.HandlerFunc(ws.ServeHTTP))
	defer httpSrv.Close()

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpSrv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	var serverConn *websocketConnection
	requireDeadline := time.Now().Add(time.Second)
	for serverConn == nil && time.Now().Before(requireDeadline) {
		ws.connectionsMutex.RLock()
		for _, candidate := range ws.connections {
			serverConn = candidate
			break
		}
		ws.connectionsMutex.RUnlock()
		if serverConn == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if serverConn == nil {
		t.Fatal("server connection was not registered")
	}

	readDone := make(chan error, 1)
	go func() {
		_, _, readErr := client.ReadMessage()
		readDone <- readErr
	}()

	var closers sync.WaitGroup
	for range 16 {
		closers.Add(1)
		go func() {
			defer closers.Done()
			serverConn.closeSocket()
		}()
	}
	closers.Wait()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("closing the canonical connection did not unblock the client read")
	}
	select {
	case <-serverConn.Done():
	default:
		t.Fatal("canonical connection remained active after closeSocket")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ws.Close(ctx); err != nil {
		t.Fatalf("close server: %v", err)
	}
}

func TestWebSocketServer_Close_RejectsNewConnections(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	pc := &PortContext{PortName: "wsport", Limit: 1}
	httpSrv := httptest.NewServer(PortMiddleware(pc, http.HandlerFunc(ws.ServeHTTP)))
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ws.Close(ctx); err != nil {
		t.Fatalf("Close on empty server: %v", err)
	}

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if conn != nil {
		conn.Close()
		t.Fatal("connection accepted after Close")
	}
	if err == nil {
		t.Fatal("expected connection attempt after Close to fail")
	}
	if resp == nil {
		t.Fatal("expected HTTP response for rejected connection")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
	if got := resp.Header.Get("Content-Type"); got != jsonContentType {
		t.Fatalf("Content-Type = %q, want %q", got, jsonContentType)
	}
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("read rejection body: %v", readErr)
	}
	if got := string(body); got != "server shutting down\r\n" {
		t.Fatalf("rejection body = %q, want exact plain HTTP error", got)
	}
}

func TestWebSocketServer_Close_WaitsForInFlightUpgrade(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	upgradeStarted := make(chan struct{})
	continueUpgrade := make(chan struct{})
	ws.upgrader.CheckOrigin = func(*http.Request) bool {
		close(upgradeStarted)
		<-continueUpgrade
		return true
	}

	httpSrv := httptest.NewServer(http.HandlerFunc(ws.ServeHTTP))
	defer httpSrv.Close()
	defer func() {
		select {
		case <-continueUpgrade:
		default:
			close(continueUpgrade)
		}
	}()

	type dialResult struct {
		conn *websocket.Conn
		resp *http.Response
	}
	dialDone := make(chan dialResult, 1)
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")
	go func() {
		conn, resp, _ := websocket.DefaultDialer.Dial(wsURL, nil)
		dialDone <- dialResult{conn: conn, resp: resp}
	}()

	select {
	case <-upgradeStarted:
	case <-time.After(time.Second):
		t.Fatal("WebSocket upgrade did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- ws.Close(ctx)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		ws.connectionsMutex.RLock()
		closing := ws.closing
		ws.connectionsMutex.RUnlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close did not begin shutdown")
		}
		runtime.Gosched()
	}

	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before the admitted upgrade finished: %v", err)
	default:
	}

	close(continueUpgrade)

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the upgrade finished")
	}

	result := <-dialDone
	if result.conn != nil {
		result.conn.Close()
	}
	if result.resp != nil {
		result.resp.Body.Close()
	}

	ws.connectionsMutex.RLock()
	connectionCount := len(ws.connections)
	ws.connectionsMutex.RUnlock()
	if connectionCount != 0 {
		t.Fatalf("registered connections = %d, want 0", connectionCount)
	}
}

// TestWebSocketServer_ConcurrentWrites_NoRace drives the ping path and the
// data-send path against the same gorilla *websocket.Conn at once. pingLoop
// and Close write control frames via WriteControl so they serialize against
// handleSend's message-frame writes. Run under -race to enforce gorilla's
// single-writer invariant.
func TestWebSocketServer_ConcurrentWrites_NoRace(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	ws.pingInterval = time.Millisecond // hammer the ping path during the test

	httpSrv := httptest.NewServer(PortMiddleware(&PortContext{SendQueue: 256}, http.HandlerFunc(ws.ServeHTTP)))
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Drain every frame the server sends (data frames, plus gorilla
	// auto-responds to pings) so handleSend never blocks on a full buffer.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Locate the server-side connection so we can push data frames through it.
	var wsConn *websocketConnection
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ws.connectionsMutex.RLock()
		for _, c := range ws.connections {
			wsConn = c
		}
		ws.connectionsMutex.RUnlock()
		if wsConn != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if wsConn == nil {
		t.Fatal("server connection never registered")
	}

	// Feed handleSend a steady stream of data frames while pingLoop fires.
	for range 100 {
		deadline := time.Now().Add(time.Second)
		for !wsConn.TrySend([]byte(`{"type":"race-probe"}`)) {
			if time.Now().After(deadline) {
				t.Fatal("send channel stalled")
			}
			time.Sleep(time.Millisecond)
		}
	}

	// Close writes a control close frame, again concurrently with handleSend.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	client.Close()
	<-readDone
}

func TestWebSocketRegistrationRejectsDuplicateAndStaleOwner(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{})
	first := &websocketConnection{Connection: subscription.NewConnection("same-id", make(chan []byte, 1))}
	second := &websocketConnection{Connection: subscription.NewConnection("same-id", make(chan []byte, 1))}
	if !ws.attachConnection(first) {
		t.Fatal("first owner was rejected")
	}
	if ws.attachConnection(second) {
		t.Fatal("duplicate owner was accepted")
	}

	ws.detachConnection(first)
	if !ws.attachConnection(second) {
		t.Fatal("replacement owner was rejected")
	}
	ws.detachConnection(first)
	ws.connectionsMutex.RLock()
	current := ws.connections[second.ID()]
	ws.connectionsMutex.RUnlock()
	if current != second {
		t.Fatal("stale owner removed its replacement")
	}
	if second.registration == nil {
		t.Fatal("replacement registration is nil")
	}
	if second.registration.Snapshot().ItemCount() != 0 {
		t.Fatal("unexpected replacement subscriptions")
	}
	ws.detachConnection(second)
}

// Sanity: ensure we can call NewWebSocketServer concurrently without races.
func TestWebSocketServer_New_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second})
		}()
	}
	wg.Wait()
}

// TestWebSocketSubscribeErrorWireEnvelope asserts the full wire envelope a
// subscribe validation failure produces over a live WebSocket: rippled puts
// the token in `error`, the numeric code in `error_code` and the
// ErrorCodes.cpp default text in `error_message`; the token and numeric code
// are part of the wire contract.
func TestWebSocketSubscribeErrorWireEnvelope(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})

	httpSrv := httptest.NewServer(http.HandlerFunc(ws.ServeHTTP))
	defer httpSrv.Close()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ws.Close(ctx)
	}()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	tests := []struct {
		name        string
		request     map[string]any
		wantError   string
		wantCode    float64
		wantMessage string
	}{
		{
			name:        "unknown stream",
			request:     map[string]any{"id": 1, "command": "subscribe", "streams": []string{"bogus"}},
			wantError:   "malformedStream",
			wantCode:    71,
			wantMessage: "Stream malformed.",
		},
		{
			name:        "malformed account",
			request:     map[string]any{"id": 2, "command": "subscribe", "accounts": []string{"nope"}},
			wantError:   "actMalformed",
			wantCode:    35,
			wantMessage: "Account malformed.",
		},
		{
			name: "IOU taker_pays without issuer",
			request: map[string]any{"id": 3, "command": "subscribe", "books": []map[string]any{{
				"taker_pays": map[string]any{"currency": "USD"},
				"taker_gets": map[string]any{"currency": "XRP"},
			}}},
			wantError:   "srcIsrMalformed",
			wantCode:    70,
			wantMessage: "Source issuer is malformed.",
		},
		{
			name: "same-asset book",
			request: map[string]any{"id": 4, "command": "subscribe", "books": []map[string]any{{
				"taker_pays": map[string]any{"currency": "XRP"},
				"taker_gets": map[string]any{"currency": "XRP"},
			}}},
			wantError:   "badMarket",
			wantCode:    42,
			wantMessage: "No such market.",
		},
		{
			name:        "unsubscribe unknown stream",
			request:     map[string]any{"id": 5, "command": "unsubscribe", "streams": []string{"bogus"}},
			wantError:   "malformedStream",
			wantCode:    71,
			wantMessage: "Stream malformed.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := client.WriteJSON(tc.request); err != nil {
				t.Fatalf("write: %v", err)
			}
			client.SetReadDeadline(time.Now().Add(5 * time.Second))
			var resp map[string]any
			if err := client.ReadJSON(&resp); err != nil {
				t.Fatalf("read: %v", err)
			}
			if got := resp["status"]; got != "error" {
				t.Fatalf("status = %v, want error (resp %v)", got, resp)
			}
			if got := resp["error"]; got != tc.wantError {
				t.Errorf("error = %v, want %q", got, tc.wantError)
			}
			if got := resp["error_code"]; got != tc.wantCode {
				t.Errorf("error_code = %v, want %v", got, tc.wantCode)
			}
			if got := resp["error_message"]; got != tc.wantMessage {
				t.Errorf("error_message = %v, want %q", got, tc.wantMessage)
			}
		})
	}
}

func TestWebSocketErrorProjectorPreservesExtrasAndCanonicalFields(t *testing.T) {
	outbound := make(chan []byte, 3)
	wsConn := &websocketConnection{Connection: subscription.NewConnection("error-projector", outbound)}
	ws := &WebSocketServer{}
	fields := map[string]any{
		"error":           "spoofed",
		"status":          "spoofed",
		"error_code":      999,
		"error_message":   "spoofed",
		"error_exception": "spoofed",
		"code":            999,
		"message":         "spoofed",
		"type":            "spoofed",
		"index":           7,
	}
	ws.sendErrorResponse(wsConn, types.RpcErrorInvalidParams("").WithExtra(fields), 1, nil, nil)

	var response map[string]any
	if err := json.Unmarshal(<-outbound, &response); err != nil {
		t.Fatal(err)
	}
	if response["status"] != "error" || response["error"] != "invalidParams" || response["error_code"] != float64(types.RpcINVALID_PARAMS) || response["error_message"] != "Invalid parameters." {
		t.Fatalf("canonical WS error = %#v", response)
	}
	if response["index"] != float64(7) {
		t.Fatalf("non-reserved extra missing: %#v", response)
	}
	if response["type"] != "response" {
		t.Fatalf("response type = %v, want response", response["type"])
	}
	for _, key := range []string{"error_exception", "code", "message"} {
		if _, ok := response[key]; ok {
			t.Errorf("reserved extra %q was projected: %#v", key, response)
		}
	}

	bare := types.RpcErrorEntryNotFoundBare("").WithExtra(map[string]any{
		"error_code":    999,
		"error_message": "spoofed",
		"index":         9,
	})
	ws.sendErrorResponse(wsConn, bare, 2, nil, nil)
	response = nil
	if err := json.Unmarshal(<-outbound, &response); err != nil {
		t.Fatal(err)
	}
	if response["error"] != "entryNotFound" || response["index"] != float64(9) {
		t.Fatalf("bare WS error = %#v", response)
	}
	for _, key := range []string{"error_code", "error_message"} {
		if _, ok := response[key]; ok {
			t.Errorf("bare error projected %q: %#v", key, response)
		}
	}

	exception := types.RpcErrorInvalidTransaction("decode failed").WithExtra(map[string]any{
		"error":           "spoofed",
		"error_code":      999,
		"error_message":   "spoofed",
		"error_exception": "spoofed",
		"index":           10,
	})
	ws.sendErrorResponse(wsConn, exception, 3, nil, nil)
	response = nil
	if err := json.Unmarshal(<-outbound, &response); err != nil {
		t.Fatal(err)
	}
	if response["error"] != "invalidTransaction" || response["error_exception"] != "decode failed" || response["index"] != float64(10) {
		t.Fatalf("exception WS error = %#v", response)
	}
	for _, key := range []string{"error_code", "error_message"} {
		if _, ok := response[key]; ok {
			t.Errorf("exception error projected %q: %#v", key, response)
		}
	}
}

func TestWebSocketHandlerPanicWireEnvelope(t *testing.T) {
	const panicCause = "websocket panic must stay private"
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	ws.methodRegistry.Register("panic", &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
			panic(panicCause)
		},
	})

	httpSrv := httptest.NewServer(http.HandlerFunc(ws.ServeHTTP))
	defer httpSrv.Close()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ws.Close(ctx)
	}()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	request := `{"command":"panic","id":7,"jsonrpc":"2.0","ripplerpc":"1.0","api_version":2,"secret":"private seed","transaction":{"Seed":"nested private seed"}}`
	if err := client.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, body, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	const want = `{"api_version":2,"error":"internal","error_code":73,"error_message":"Internal error.","id":7,"jsonrpc":"2.0","request":{"api_version":2,"command":"panic","id":7,"jsonrpc":"2.0","ripplerpc":"1.0","secret":"<masked>","transaction":{"Seed":"<masked>"}},"ripplerpc":"1.0","status":"error","type":"response"}`
	if got := string(body); got != want {
		t.Fatalf("panic response = %s, want %s", got, want)
	}
	for _, private := range []string{panicCause, "private seed", "nested private seed"} {
		if strings.Contains(string(body), private) {
			t.Fatalf("panic response leaked %q: %s", private, body)
		}
	}
}

func TestWebSocketOrdinaryErrorWireEnvelope(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	ws.methodRegistry.Register("fail", &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
			return nil, rpcInternalError()
		},
	})

	body := wsRawRoundTrip(t, ws, `{"command":"fail","id":7,"jsonrpc":"2.0","ripplerpc":"1.0","api_version":2,"secret":"private seed"}`)
	const want = `{"api_version":2,"error":"internal","error_code":73,"error_message":"Internal error.","id":7,"jsonrpc":"2.0","request":{"api_version":2,"command":"fail","id":7,"jsonrpc":"2.0","ripplerpc":"1.0","secret":"<masked>"},"ripplerpc":"1.0","status":"error","type":"response"}`
	if got := string(body); got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
}

func TestWebSocketRedactsIDAndPreservesItInHandlerParams(t *testing.T) {
	received := make(chan json.RawMessage, 1)
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	ws.methodRegistry.Register("capture", &stubHandler{
		handle: func(_ *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
			received <- append(json.RawMessage(nil), params...)
			return map[string]any{"ok": true}, nil
		},
	})

	body := wsRawRoundTrip(t, ws, `{"command":"capture","method":"capture","api_version":1,"id":{"SeCrEt":"private-id"},"payload":"kept"}`)
	const want = `{"api_version":1,"id":{"SeCrEt":"<masked>"},"result":{"ok":true},"status":"success","type":"response"}`
	if got := string(body); got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
	if strings.Contains(string(body), "private-id") {
		t.Fatalf("response leaked id credential: %s", body)
	}

	receivedParams := <-received
	var params map[string]any
	if err := json.Unmarshal(receivedParams, &params); err != nil {
		t.Fatalf("decode handler params: %v", err)
	}
	id, ok := params["id"].(map[string]any)
	if !ok || len(id) != 1 || id["SeCrEt"] != maskedValue {
		t.Fatalf("handler id = %v, want redacted id", params["id"])
	}
	if params["payload"] != "kept" {
		t.Fatalf("handler payload = %v, want kept", params["payload"])
	}
	for _, stripped := range []string{"command", "method", "api_version"} {
		if _, exists := params[stripped]; exists {
			t.Fatalf("handler params retained %q: %v", stripped, params)
		}
	}
}

func TestWebSocketCommandParamsPreservesRawReal(t *testing.T) {
	message := []byte(`{"command":"subscribe","api_version":1,"id":{"SeCrEt":"private-id"},"real":0.0}`)
	params, err := websocketCommandParams(message, map[string]any{
		"id": map[string]any{"SeCrEt": maskedValue},
	})
	if err != nil {
		t.Fatalf("extract handler params: %v", err)
	}

	var rawParams map[string]json.RawMessage
	if err := json.Unmarshal(params, &rawParams); err != nil {
		t.Fatalf("decode handler params: %v", err)
	}
	if got := string(rawParams["real"]); got != "0.0" {
		t.Fatalf("handler real = %s, want preserved JSON real", got)
	}
	var id map[string]any
	if err := json.Unmarshal(rawParams["id"], &id); err != nil {
		t.Fatalf("decode handler id: %v", err)
	}
	if len(id) != 1 || id["SeCrEt"] != maskedValue {
		t.Fatalf("handler id = %v, want redacted id", id)
	}
	for _, stripped := range []string{"command", "api_version"} {
		if _, exists := rawParams[stripped]; exists {
			t.Fatalf("handler params retained %q: %s", stripped, params)
		}
	}
}

func TestWebSocketSpecialCommandDecodeErrorsAreFixed(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
		invoke  func(*WebSocketServer, *websocketConnection, *types.RpcContext, types.WebSocketCommand)
	}{
		{
			name:    "subscribe",
			command: "subscribe",
			want:    `{"error":"invalidParams","error_code":31,"error_message":"Invalid subscription parameters.","id":7,"request":{"command":"subscribe","id":7},"status":"error","type":"response"}`,
			invoke: func(ws *WebSocketServer, conn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
				ws.handleSpecialCommand(conn, ctx, cmd, ws.executeSubscribe)
			},
		},
		{
			name:    "unsubscribe",
			command: "unsubscribe",
			want:    `{"error":"invalidParams","error_code":31,"error_message":"Invalid unsubscription parameters.","id":7,"request":{"command":"unsubscribe","id":7},"status":"error","type":"response"}`,
			invoke: func(ws *WebSocketServer, conn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
				ws.handleSpecialCommand(conn, ctx, cmd, ws.executeUnsubscribe)
			},
		},
		{
			name:    "path find",
			command: "path_find",
			want:    `{"error":"invalidParams","error_code":31,"error_message":"Invalid parameters.","id":7,"request":{"command":"path_find","id":7},"status":"error","type":"response"}`,
			invoke: func(ws *WebSocketServer, conn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
				ws.handleSpecialCommand(conn, ctx, cmd, ws.executePathFind)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ws := NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second})
			wsConn := &websocketConnection{Connection: subscription.NewConnectionWithContext(context.Background(), "decode-test", make(chan []byte, 1))}
			cmd := types.WebSocketCommand{
				Command: test.command,
				ID:      int32(7),
				Params:  json.RawMessage(`{"private":"decoder-detail"`),
				Request: map[string]any{"command": test.command, "id": int32(7)},
			}

			test.invoke(ws, wsConn, &types.RpcContext{
				ApiVersion: types.DefaultApiVersion,
				Services:   &types.ServiceContainer{Capabilities: types.RPCCapabilities{PathSearchMax: 3}},
			}, cmd)
			body := <-wsConn.Outbound()
			if got := string(body); got != test.want {
				t.Fatalf("response = %s, want %s", got, test.want)
			}
			if strings.Contains(string(body), "decoder-detail") || strings.Contains(string(body), "unexpected end") {
				t.Fatalf("response leaked decoder detail: %s", body)
			}
		})
	}
}

func TestWebSocketRejectsTrailingJSON(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	ws.methodRegistry.Register("ping", &stubHandler{})

	body := wsRawRoundTrip(t, ws, `{"command":"ping","id":7}{"command":"ping","id":8}`)
	const want = `{"error":"jsonInvalid","type":"error","value":"<redacted>"}`
	if got := string(body); got != want {
		t.Fatalf("trailing JSON response = %s, want %s", got, want)
	}
}

func TestWebSocketJSONInvalidWireEnvelope(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	tests := []struct {
		name    string
		request string
		want    string
	}{
		{name: "malformed", request: `{`, want: `{"error":"jsonInvalid","type":"error","value":"<redacted>"}`},
		{name: "oversized", request: strings.Repeat(" ", maxRequestBytes+1), want: `{"error":"jsonInvalid","type":"error","value":"<redacted>"}`},
		{name: "null", request: `null`, want: `{"error":"jsonInvalid","type":"error","value":"null"}`},
		{name: "scalar", request: `7`, want: `{"error":"jsonInvalid","type":"error","value":"7"}`},
		{name: "redacted array", request: `[{"secret":"private seed","nested":{"Seed":"nested seed"}}]`, want: `{"error":"jsonInvalid","type":"error","value":"[{\"nested\":{\"Seed\":\"<masked>\"},\"secret\":\"<masked>\"}]"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := wsRawRoundTrip(t, ws, test.request)
			if got := string(body); got != test.want {
				t.Fatalf("response = %s, want %s", got, test.want)
			}
			if strings.Contains(string(body), "private seed") || strings.Contains(string(body), "nested seed") {
				t.Fatalf("response leaked a credential: %s", body)
			}
		})
	}
}

func TestWebSocketJSONIntegerBounds(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	ws.methodRegistry.Register("ping", &stubHandler{})

	for _, request := range []string{
		`{"command":"ping","id":4294967296}`,
		`{"command":"ping","id":-2147483649}`,
	} {
		body := wsRawRoundTrip(t, ws, request)
		const want = `{"error":"jsonInvalid","type":"error","value":"<redacted>"}`
		if got := string(body); got != want {
			t.Fatalf("response for %s = %s, want %s", request, got, want)
		}
	}

	tests := []struct {
		request string
		want    string
	}{
		{
			request: `{"command":"ping","id":4294967295}`,
			want:    `{"id":4294967295,"result":{"ok":true},"status":"success","type":"response"}`,
		},
		{
			request: `{"command":"ping","id":-2147483648}`,
			want:    `{"id":-2147483648,"result":{"ok":true},"status":"success","type":"response"}`,
		},
		{
			request: `{"command":"ping","id":1e20}`,
			want:    `{"id":1e+20,"result":{"ok":true},"status":"success","type":"response"}`,
		},
	}
	for _, test := range tests {
		body := wsRawRoundTrip(t, ws, test.request)
		if got := string(body); got != test.want {
			t.Fatalf("response for %s = %s, want %s", test.request, got, test.want)
		}
	}
}

func TestWebSocketErrorExceptionWireEnvelope(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	ws.methodRegistry.Register("simulate", &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
			return nil, types.RpcErrorInvalidTransaction("invalid transaction detail")
		},
	})

	body := wsRawRoundTrip(t, ws, `{"command":"simulate","id":9}`)
	const want = `{"error":"invalidTransaction","error_exception":"invalid transaction detail","id":9,"request":{"command":"simulate","id":9},"status":"error","type":"response"}`
	if got := string(body); got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
	if strings.Contains(string(body), "error_code") || strings.Contains(string(body), "error_message") {
		t.Fatalf("manual error gained injected fields: %s", body)
	}
}

func TestWebSocketServerOptionsPingInterval(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second})
	if ws.pingInterval != 30*time.Second {
		t.Fatalf("default pingInterval = %v, want 30s", ws.pingInterval)
	}

	configured := NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second, PingInterval: 5 * time.Second})
	if configured.pingInterval != 5*time.Second {
		t.Errorf("configured pingInterval = %v, want 5s", configured.pingInterval)
	}

	zero := NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second, PingInterval: 0})
	if zero.pingInterval != 30*time.Second {
		t.Errorf("zero pingInterval = %v, want default 30s", zero.pingInterval)
	}
}
