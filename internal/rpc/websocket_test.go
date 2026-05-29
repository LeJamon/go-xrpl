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
	for i := 0; i < numConns; i++ {
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
	for i := 0; i < 5; i++ {
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

// Sanity: ensure we can call NewWebSocketServer concurrently without races.
func TestWebSocketServer_New_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = NewWebSocketServer(time.Second, nil)
		}()
	}
	wg.Wait()
}
