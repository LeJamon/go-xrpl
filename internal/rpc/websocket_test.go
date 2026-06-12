package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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
