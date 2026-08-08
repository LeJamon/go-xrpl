package rpc

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// pushOverDropThreshold charges key until the tracker reports it is over the
// per-IP drop threshold, so a single subsequent request is admission-rejected
// deterministically rather than after a probabilistic number of heavy calls.
func pushOverDropThreshold(t *testing.T, manager *resource.Manager, key string) {
	t.Helper()
	require.NoError(t, manager.ImportConsumers("overload-test", resource.Gossip{Items: []resource.GossipItem{{
		Address: key,
		Balance: resource.DropThreshold,
	}}}))
}

// TestHTTPOverloadBeforeForbid pins issue #975: a forbidden admin command from a
// caller already over its per-IP budget is rejected at the overload-admission
// layer (503 "Server is overloaded"), ahead of the role-layer FORBID (403),
// matching rippled's usage.disconnect() running before the FORBID short-circuit
// (ServerHandler.cpp:735 ahead of :750).
func TestHTTPOverloadBeforeForbid(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "stop", &stubHandler{role: types.RoleAdmin})
	srv.resourceManager = resource.NewManager(nil, nil)
	pushOverDropThreshold(t, srv.resourceManager, "203.0.113.5")

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"stop","params":[{}]}`))
	req.RemoteAddr = "203.0.113.5:1234" // non-local peer, no AdminNets → RoleGuest
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	// rippled labels even this plain-text body application/json.
	if ct := rr.Header().Get("Content-Type"); ct != "application/json; charset=UTF-8" {
		t.Fatalf("content-type = %q, want application/json; charset=UTF-8", ct)
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "Server is overloaded" {
		t.Fatalf("body = %q, want \"Server is overloaded\"", got)
	}
	// It must be the overload denial, not the 403 FORBID (which would win if the
	// gate order were wrong) nor the result envelope.
	if strings.Contains(rr.Body.String(), "Forbidden") || strings.Contains(rr.Body.String(), "result") {
		t.Fatalf("overload denial leaked a FORBID/result shape: %s", rr.Body.String())
	}
}

// TestHTTPOverloadOpenMethod confirms the overload gate still fires for an
// ordinary (non-forbidden) method, so moving it ahead of FORBID did not narrow
// it to only the admin path.
func TestHTTPOverloadOpenMethod(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "ping", echoHandler())
	srv.resourceManager = resource.NewManager(nil, nil)
	pushOverDropThreshold(t, srv.resourceManager, "198.51.100.7")

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"ping","params":[{}]}`))
	req.RemoteAddr = "198.51.100.7:1234"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "Server is overloaded" {
		t.Fatalf("body = %q, want \"Server is overloaded\"", got)
	}
}

// TestHTTPOverloadAdminUnlimitedBypass confirms an admin (unlimited) caller
// bypasses gateLoad entirely even when its key is over the threshold, matching
// rippled's isUnlimited() taking the newUnlimitedEndpoint branch that never
// disconnects.
func TestHTTPOverloadAdminUnlimitedBypass(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "stop", &stubHandler{role: types.RoleAdmin})
	srv.resourceManager = resource.NewManager(nil, nil)
	pushOverDropThreshold(t, srv.resourceManager, "127.0.0.1")

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"stop","params":[{}]}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req = withLoopbackAdmin(req)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("admin caller: expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	if result := decodeEnvelope(t, rr.Body.Bytes()); result["status"] != "success" {
		t.Fatalf("admin caller: status = %v, want success", result["status"])
	}
}

// TestHTTPApiVersionPrecedesOverload pins that the api_version check (400) still
// runs ahead of the overload gate, mirroring getAPIVersionNumber preceding
// usage.disconnect() (ServerHandler.cpp:685 ahead of :735): an over-budget caller
// supplying an out-of-range api_version gets invalid_API_version, not 503.
func TestHTTPApiVersionPrecedesOverload(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "ping", echoHandler())
	srv.resourceManager = resource.NewManager(nil, nil)
	pushOverDropThreshold(t, srv.resourceManager, "198.51.100.7")

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"ping","params":[{"api_version":99}]}`))
	req.RemoteAddr = "198.51.100.7:1234"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (api-version before overload), got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	if got := strings.TrimSpace(rr.Body.String()); got != types.InvalidApiVersionToken {
		t.Fatalf("body = %q, want %q", got, types.InvalidApiVersionToken)
	}
}

// TestHTTPBatchOverloadElement pins the batch-element shape for the overload gate:
// rippled echoes the element's own fields and attaches
// make_json_error(server_overloaded, "Server is overloaded") (server_overloaded =
// -32604, ServerHandler.cpp:742-746), ahead of FORBID. So a batch from an
// over-budget caller renders the overload object for BOTH a forbidden admin
// element and an ordinary element — never the forbidden (-32605) object.
func TestHTTPBatchOverloadElement(t *testing.T) {
	srv := &Server{
		registry:        types.NewMethodRegistry(),
		timeout:         time.Second,
		services:        types.NewServiceContainer(nil),
		resourceManager: resource.NewManager(nil, nil),
	}
	srv.registry.Register("stop", &stubHandler{role: types.RoleAdmin})
	srv.registry.Register("ping", echoHandler())
	pushOverDropThreshold(t, srv.resourceManager, "10.0.0.1") // postBatch posts from 10.0.0.1

	body := `{"method":"batch","params":[
		{"method":"stop","value":7},
		{"method":"ping","value":1}
	]}`
	rr := postBatch(t, srv, body)

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

	for i, want := range []float64{7, 1} {
		el := replies[i]
		if _, hasResult := el["result"]; hasResult {
			t.Fatalf("element %d must not carry a result envelope: %v", i, el)
		}
		if el["value"] != want {
			t.Fatalf("element %d did not echo its own fields: %v", i, el)
		}
		errObj, ok := el["error"].(map[string]any)
		if !ok {
			t.Fatalf("element %d missing error object: %v", i, el)
		}
		inner, ok := errObj["error"].(map[string]any)
		if !ok {
			t.Fatalf("element %d error not in make_json_error shape: %v", i, errObj)
		}
		if inner["code"] != float64(-32604) {
			t.Fatalf("element %d code = %v, want -32604 (server_overloaded)", i, inner["code"])
		}
		if inner["message"] != "Server is overloaded" {
			t.Fatalf("element %d message = %v, want \"Server is overloaded\"", i, inner["message"])
		}
	}
}

func TestWSOverloadClosesBeforeRequestValidation(t *testing.T) {
	tests := []string{
		`{"command":"stop","id":1}`,
		`{"command":"subscribe","streams":["ledger"]}`,
		`{"command":"unsubscribe","streams":["ledger"]}`,
		`{"command":"path_find","subcommand":"status"}`,
		`{"command":7,"api_version":99}`,
	}
	for _, request := range tests {
		t.Run(request, func(t *testing.T) {
			manager := resource.NewManager(nil, nil)
			ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 2 * time.Second, ResourceManager: manager})
			ws.methodRegistry.Register("stop", &stubHandler{role: types.RoleAdmin})
			pushOverDropThreshold(t, manager, "127.0.0.1")

			_, adminNet, _ := net.ParseCIDR("10.0.0.0/8")
			pc := &PortContext{AdminNets: []net.IPNet{*adminNet}}
			httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws.ServeHTTP(w, r.WithContext(WithPortContext(r.Context(), pc)))
			}))
			defer httpSrv.Close()

			client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpSrv.URL, "http"), nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer client.Close()
			if err := client.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatalf("set read deadline: %v", err)
			}
			_, body, err := client.ReadMessage()
			if err == nil {
				t.Fatalf("received an application response before close: %s", body)
			}
			closeErr, ok := err.(*websocket.CloseError)
			if !ok {
				t.Fatalf("read error = %T %v, want WebSocket close error", err, err)
			}
			if closeErr.Code != websocket.ClosePolicyViolation || closeErr.Text != "threshold exceeded" {
				t.Fatalf("close = %d %q, want %d %q", closeErr.Code, closeErr.Text, websocket.ClosePolicyViolation, "threshold exceeded")
			}
		})
	}
}

func TestWSEarlyMalformedResponsesChargeWithoutLoadWarning(t *testing.T) {
	tests := []struct {
		name    string
		request string
		want    string
	}{
		{
			name:    "invalid API version",
			request: `{"command":"ping","api_version":99,"id":1}`,
			want:    `{"api_version":99,"error":"invalid_API_version","id":1,"request":{"api_version":99,"command":"ping","id":1},"status":"error","type":"response"}`,
		},
		{
			name:    "missing command",
			request: `{"id":2}`,
			want:    `{"error":"missingCommand","id":2,"request":{"id":2},"status":"error","type":"response"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 2 * time.Second})
			require.NoError(t, ws.resourceManager.ImportConsumers("warning-peer", resource.Gossip{Items: []resource.GossipItem{{
				Address: "127.0.0.1",
				Balance: resource.WarningThreshold - uint32(resource.FeeMalformedRPC.Cost()/resource.DecayWindowSeconds),
			}}}))

			pc := &PortContext{AdminNets: []net.IPNet{mustParseCIDR("10.0.0.0/8")}}
			httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws.ServeHTTP(w, r.WithContext(WithPortContext(r.Context(), pc)))
			}))
			defer httpSrv.Close()

			client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpSrv.URL, "http"), nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer client.Close()
			if err := client.WriteMessage(websocket.TextMessage, []byte(test.request)); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatalf("set read deadline: %v", err)
			}
			_, body, err := client.ReadMessage()
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got := string(body); got != test.want {
				t.Fatalf("response = %s, want %s", got, test.want)
			}
			if got, want := resourceLocalBalance(t, ws.resourceManager, "127.0.0.1"), uint32((resource.FeeMalformedRPC.Cost()+resource.FeeWarning.Cost())/resource.DecayWindowSeconds); got != want {
				t.Fatalf("local charge = %v, want %v", got, want)
			}
		})
	}
}
