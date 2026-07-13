package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/gorilla/websocket"
)

// TestWebSocketServer_Close_JoinsHandlers verifies that Close blocks until
// all per-connection goroutines (read loop, send pump, ping loop) have exited.
// Regression test for issue #186.
func TestWebSocketServer_Close_JoinsHandlers(t *testing.T) {
	ws := NewWebSocketServer(30*time.Second, nil)
	ws.RegisterAllMethods()

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
	ws := NewWebSocketServer(30*time.Second, nil)

	// Inflate the WaitGroup so it never reaches zero on its own.
	ws.wg.Add(1)
	defer ws.wg.Done()

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
}

// TestWebSocketServer_Close_NoConnections verifies Close is safe with no
// active connections and returns immediately.
func TestWebSocketServer_Close_NoConnections(t *testing.T) {
	ws := NewWebSocketServer(30*time.Second, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ws.Close(ctx); err != nil {
		t.Fatalf("Close on empty server: %v", err)
	}
}

// TestWebSocketServer_FailedUpgrade_ReleasesSlot verifies that a malformed
// WebSocket upgrade request does not permanently leak its per-port connection
// slot. PortMiddleware acquires a slot and delegates release to closeConnection,
// which never runs when the gorilla upgrade fails — so ServeHTTP must release
// the slot itself. Regression test for issue #598.
func TestWebSocketServer_FailedUpgrade_ReleasesSlot(t *testing.T) {
	ws := NewWebSocketServer(30*time.Second, nil)
	ws.RegisterAllMethods()

	limiter := NewConnLimiter()
	ws.SetConnLimiter(limiter)

	const portName = "wsport"
	pc := &PortContext{PortName: portName, Limit: 1}
	handler := PortMiddleware(pc, limiter, http.HandlerFunc(ws.ServeHTTP))

	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()

	// Send several malformed upgrade requests. Each carries Upgrade: websocket
	// (so PortMiddleware classifies it as WS and skips its own release) but
	// omits Sec-WebSocket-Key, so gorilla rejects the upgrade.
	for i := range 5 {
		req, err := http.NewRequest(http.MethodGet, httpSrv.URL, nil)
		if err != nil {
			t.Fatalf("new request %d: %v", i, err)
		}
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("malformed upgrade %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusServiceUnavailable {
			t.Fatalf("request %d got 503 — slot leaked from a prior failed upgrade", i)
		}
	}

	if got := limiter.Count(portName); got != 0 {
		t.Fatalf("connection slots leaked after failed upgrades: count=%d, want 0", got)
	}

	// A legitimate client must still be able to connect (limit=1).
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("legitimate dial after failed upgrades: %v", err)
	}
	c.Close()
}

// TestWebSocketServer_ConcurrentWrites_NoRace drives the ping path and the
// data-send path against the same gorilla *websocket.Conn at once. pingLoop
// (and Close) must write their control frames via WriteControl so they
// serialize against handleSend's message-frame writes; the old WriteMessage
// calls touched gorilla's unguarded single-writer state and raced handleSend.
// Run under -race to catch a regression. Regression test for issue #746.
func TestWebSocketServer_ConcurrentWrites_NoRace(t *testing.T) {
	ws := NewWebSocketServer(30*time.Second, nil)
	ws.RegisterAllMethods()
	ws.pingInterval = time.Millisecond // hammer the ping path during the test

	httpSrv := httptest.NewServer(http.HandlerFunc(ws.ServeHTTP))
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
	var wsConn *WebSocketConnection
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
	for range 500 {
		select {
		case wsConn.sendChannel <- []byte(`{"type":"race-probe"}`):
		case <-time.After(2 * time.Second):
			t.Fatal("send channel stalled")
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

// Sanity: ensure we can call NewWebSocketServer concurrently without races.
func TestWebSocketServer_New_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = NewWebSocketServer(time.Second, nil)
		}()
	}
	wg.Wait()
}

// TestWebSocketSubscribeErrorWireEnvelope asserts the full wire envelope a
// subscribe validation failure produces over a live WebSocket: rippled puts
// the token in `error`, the numeric code in `error_code` and the
// ErrorCodes.cpp default text in `error_message` (issue #828 regression —
// these envelopes previously went out as `"error": ""` with code 31).
func TestWebSocketSubscribeErrorWireEnvelope(t *testing.T) {
	ws := NewWebSocketServer(30*time.Second, nil)
	ws.RegisterAllMethods()

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

func TestWebSocketHandlerPanicWireEnvelope(t *testing.T) {
	const panicCause = "websocket panic must stay private"
	ws := NewWebSocketServer(30*time.Second, nil)
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
	ws := NewWebSocketServer(30*time.Second, nil)
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
	ws := NewWebSocketServer(30*time.Second, nil)
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

	var params map[string]any
	if err := json.Unmarshal(<-received, &params); err != nil {
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

func TestWebSocketSpecialCommandDecodeErrorsAreFixed(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
		invoke  func(*WebSocketServer, *WebSocketConnection, *types.RpcContext, types.WebSocketCommand)
	}{
		{
			name:    "subscribe",
			command: "subscribe",
			want:    `{"error":"invalidParams","error_code":31,"error_message":"Invalid subscription parameters.","id":7,"request":{"command":"subscribe","id":7},"status":"error","type":"response"}`,
			invoke: func(ws *WebSocketServer, conn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
				ws.handleSpecialCommand(conn, ctx, cmd, ws.executeSubscribe)
			},
		},
		{
			name:    "unsubscribe",
			command: "unsubscribe",
			want:    `{"error":"invalidParams","error_code":31,"error_message":"Invalid unsubscription parameters.","id":7,"request":{"command":"unsubscribe","id":7},"status":"error","type":"response"}`,
			invoke: func(ws *WebSocketServer, conn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
				ws.handleSpecialCommand(conn, ctx, cmd, ws.executeUnsubscribe)
			},
		},
		{
			name:    "path find",
			command: "path_find",
			want:    `{"error":"invalidParams","error_code":31,"error_message":"Invalid parameters.","id":7,"request":{"command":"path_find","id":7},"status":"error","type":"response"}`,
			invoke: func(ws *WebSocketServer, conn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
				ws.handleSpecialCommand(conn, ctx, cmd, ws.executePathFind)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ws := NewWebSocketServer(time.Second, nil)
			wsConn := &WebSocketConnection{
				ID:          "decode-test",
				sendChannel: make(chan []byte, 1),
				ctx:         context.Background(),
			}
			cmd := types.WebSocketCommand{
				Command: test.command,
				ID:      int32(7),
				Params:  json.RawMessage(`{"private":"decoder-detail"`),
				Request: map[string]any{"command": test.command, "id": int32(7)},
			}

			test.invoke(ws, wsConn, &types.RpcContext{}, cmd)
			body := <-wsConn.sendChannel
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
	ws := NewWebSocketServer(30*time.Second, nil)
	ws.methodRegistry.Register("ping", &stubHandler{})

	body := wsRawRoundTrip(t, ws, `{"command":"ping","id":7}{"command":"ping","id":8}`)
	const want = `{"error":"jsonInvalid","type":"error","value":"<redacted>"}`
	if got := string(body); got != want {
		t.Fatalf("trailing JSON response = %s, want %s", got, want)
	}
}

func TestWebSocketJSONInvalidWireEnvelope(t *testing.T) {
	ws := NewWebSocketServer(30*time.Second, nil)
	tests := []struct {
		name    string
		request string
		want    string
	}{
		{name: "malformed", request: `{`, want: `{"error":"jsonInvalid","type":"error","value":"<redacted>"}`},
		{name: "oversized", request: strings.Repeat(" ", MaxRequestBytes+1), want: `{"error":"jsonInvalid","type":"error","value":"<redacted>"}`},
		{name: "null", request: `null`, want: `{"error":"jsonInvalid","type":"error","value":"null"}`},
		{name: "scalar", request: `7`, want: `{"error":"jsonInvalid","type":"error","value":"7"}`},
		{name: "redacted array", request: `[{"secret":"private seed","nested":{"Seed":"nested seed"}}]`, want: `{"error":"jsonInvalid","type":"error","value":"[{\\"nested\\":{\\"Seed\\":\\"<masked>\\"},\\"secret\\":\\"<masked>\\"}]"}`},
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
	ws := NewWebSocketServer(30*time.Second, nil)
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
	ws := NewWebSocketServer(30*time.Second, nil)
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

// TestSetPingInterval guards the websocket_ping_frequency wiring: a
// configured cadence must replace the default, and non-positive values
// must be ignored.
func TestSetPingInterval(t *testing.T) {
	ws := NewWebSocketServer(time.Second, nil)
	if ws.pingInterval != 30*time.Second {
		t.Fatalf("default pingInterval = %v, want 30s", ws.pingInterval)
	}

	ws.SetPingInterval(5 * time.Second)
	if ws.pingInterval != 5*time.Second {
		t.Errorf("pingInterval = %v, want 5s", ws.pingInterval)
	}

	ws.SetPingInterval(0)
	if ws.pingInterval != 5*time.Second {
		t.Errorf("pingInterval = %v after SetPingInterval(0), want 5s", ws.pingInterval)
	}
}
