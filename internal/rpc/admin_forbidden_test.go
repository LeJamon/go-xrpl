package rpc

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/loadtrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHTTPAdminDenialReturns403 pins the role-layer rejection of an admin-only
// command for a non-admin caller on the HTTP single path: rippled answers
// HTTPReply(403, "Forbidden") before the handler runs, not a 200 + JSON-RPC
// result envelope (ServerHandler.cpp:750-757). An admin caller is unaffected.
func TestHTTPAdminDenialReturns403(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "stop", &stubHandler{role: types.RoleAdmin})

	t.Run("non-admin caller gets 403 Forbidden", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"stop","params":[{}]}`))
		req.RemoteAddr = "203.0.113.5:1234" // non-local peer, no AdminNets → RoleGuest
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d\nbody: %s", rr.Code, rr.Body.String())
		}
		// rippled labels even this plain-text body application/json.
		if ct := rr.Header().Get("Content-Type"); ct != "application/json; charset=UTF-8" {
			t.Fatalf("content-type = %q, want application/json; charset=UTF-8", ct)
		}
		if got := strings.TrimSpace(rr.Body.String()); got != "Forbidden" {
			t.Fatalf("body = %q, want \"Forbidden\"", got)
		}
		// The denial must not ride the XRPL result envelope (the old 200 shape).
		if strings.Contains(rr.Body.String(), "result") || strings.Contains(rr.Body.String(), "noPermission") {
			t.Fatalf("admin denial leaked the result envelope: %s", rr.Body.String())
		}
	})

	t.Run("admin caller is allowed", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"stop","params":[{}]}`))
		req.RemoteAddr = "127.0.0.1:1234" // localhost fallback → RoleAdmin
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("admin caller: expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
		}
		if result := decodeEnvelope(t, rr.Body.Bytes()); result["status"] != "success" {
			t.Fatalf("admin caller: status = %v, want success", result["status"])
		}
	})
}

// TestHTTPAdminDenialChargesFeeMalformed pins that a role-layer FORBID charges
// the caller feeMalformedRPC (rippled ServerHandler.cpp:752), so admin-probing
// is not a cheap probe. An admin (unlimited) caller bypasses the charge.
func TestHTTPAdminDenialChargesFeeMalformed(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "stop", &stubHandler{role: types.RoleAdmin})
	srv.loadTracker = loadtrack.New()

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"stop","params":[{}]}`))
	req.RemoteAddr = "198.51.100.7:5555"
	srv.ServeHTTP(httptest.NewRecorder(), req)

	// The malformed bucket is 100; the reference bucket is 20. Allow for a hair
	// of decay between the charge and this read, but confirm it is the malformed
	// charge, not a cheaper one.
	if got := srv.loadTracker.Balance("198.51.100.7"); got <= float64(loadtrack.ChargeReference) {
		t.Fatalf("forbidden admin denial charged %v, want feeMalformedRPC (~%d)", got, loadtrack.ChargeMalformed)
	}

	// An admin caller is unlimited and accrues nothing.
	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"stop","params":[{}]}`))
	req.RemoteAddr = "127.0.0.1:5555"
	srv.ServeHTTP(httptest.NewRecorder(), req)
	if got := srv.loadTracker.Balance("127.0.0.1"); got != 0 {
		t.Fatalf("admin caller charged %v, want 0 (unlimited)", got)
	}
}

// TestHTTPBatchAdminDenialForbidden pins the batch-element shape for a role-layer
// FORBID: rippled echoes the element's own fields and attaches
// make_json_error(forbidden, "Forbidden") (forbidden = -32605,
// ServerHandler.cpp:758-760), rather than the XRPL result envelope. Sibling
// elements are unaffected.
func TestHTTPBatchAdminDenialForbidden(t *testing.T) {
	srv := &Server{
		registry: types.NewMethodRegistry(),
		timeout:  time.Second,
		services: types.NewServiceContainer(nil),
	}
	srv.registry.Register("stop", &stubHandler{role: types.RoleAdmin})
	srv.registry.Register("ping", echoHandler())

	body := `{"method":"batch","params":[
		{"method":"stop","value":7},
		{"method":"ping","value":1}
	]}`
	rr := postBatch(t, srv, body) // posts from 10.0.0.1 → RoleGuest

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	var replies []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &replies); err != nil {
		t.Fatalf("not a JSON array: %v\nbody: %s", err, rr.Body.String())
	}
	if len(replies) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(replies))
	}

	denied := replies[0]
	if _, hasResult := denied["result"]; hasResult {
		t.Fatalf("forbidden batch element must not carry a result envelope: %v", denied)
	}
	if denied["method"] != "stop" || denied["value"] != float64(7) {
		t.Fatalf("forbidden element did not echo its own fields: %v", denied)
	}
	// make_json_error nests under "error"; rippled assigns the whole object to
	// the element's "error" field, so the wire shape is double-nested.
	errObj, ok := denied["error"].(map[string]any)
	if !ok {
		t.Fatalf("forbidden element missing error object: %v", denied)
	}
	inner, ok := errObj["error"].(map[string]any)
	if !ok {
		t.Fatalf("forbidden error not in make_json_error shape: %v", errObj)
	}
	if inner["code"] != float64(-32605) {
		t.Fatalf("forbidden code = %v, want -32605", inner["code"])
	}
	if inner["message"] != "Forbidden" {
		t.Fatalf("forbidden message = %v, want \"Forbidden\"", inner["message"])
	}

	okReply := replies[1]
	result, ok := okReply["result"].(map[string]any)
	if !ok || result["status"] != "success" {
		t.Fatalf("guest sibling element should succeed in a result envelope: %v", okReply)
	}
}

// TestWSAdminDenialForbidden confirms the WebSocket admin-denial against rippled:
// requestRole → Role::FORBID → rpcError(rpcFORBIDDEN) (ServerHandler.cpp:482-486),
// i.e. the "forbidden" token with code 3. A non-admin role is forced by
// configuring AdminNets that exclude the loopback test peer, disabling the
// localhost-admin fallback.
func TestWSAdminDenialForbidden(t *testing.T) {
	ws := NewWebSocketServer(2*time.Second, nil)
	ws.methodRegistry.Register("stop", &stubHandler{role: types.RoleAdmin})

	_, adminNet, _ := net.ParseCIDR("10.0.0.0/8")
	pc := &PortContext{AdminNets: []net.IPNet{*adminNet}}
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws.ServeHTTP(w, r.WithContext(WithPortContext(r.Context(), pc)))
	}))
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.WriteJSON(map[string]any{"command": "stop", "id": float64(1)}))
	require.NoError(t, c.SetReadDeadline(time.Now().Add(2*time.Second)))
	var resp map[string]any
	require.NoError(t, c.ReadJSON(&resp))

	assert.Equal(t, "error", resp["status"])
	assert.Equal(t, "forbidden", resp["error"])
	assert.Equal(t, float64(types.RpcFORBIDDEN), resp["error_code"])
	// rpcFORBIDDEN's errorInfo message is "Bad credentials." (ErrorCodes.cpp:77),
	// not rpcNO_PERMISSION's "You don't have permission for this command."
	assert.Equal(t, "Bad credentials.", resp["error_message"])
	assert.Equal(t, float64(1), resp["id"])
}
