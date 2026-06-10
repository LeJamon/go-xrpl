package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/gorilla/websocket"
)

// versionEchoServer builds an HTTP server whose single "ping" method echoes the
// resolved api_version, so tests can assert default resolution and beta gating
// end-to-end through ServeHTTP. The handler advertises support for all three
// versions; the dispatch-layer cap, not the handler set, is what gates v3.
func versionEchoServer(t *testing.T, beta bool) *Server {
	t.Helper()
	srv := &Server{
		registry: types.NewMethodRegistry(),
		timeout:  time.Second,
		services: types.NewServiceContainer(nil),
	}
	srv.services.BetaRPCAPI = beta
	srv.registry.Register("ping", &stubHandler{
		apiVers: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		handle: func(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
			return map[string]any{"api_version": ctx.ApiVersion}, nil
		},
	})
	return srv
}

func postJSON(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// TestApiVersion_UnspecifiedDefaultsToV1 verifies a request that omits
// api_version is served as v1, matching rippled apiVersionIfUnspecified = 1.
func TestApiVersion_UnspecifiedDefaultsToV1(t *testing.T) {
	srv := versionEchoServer(t, false)

	rr := postJSON(t, srv, `{"method":"ping","params":[{}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	result := decodeEnvelope(t, rr.Body.Bytes())
	if got := result["api_version"]; got != float64(types.ApiVersion1) {
		t.Fatalf("unspecified api_version resolved to %v, want %d", got, types.ApiVersion1)
	}
}

// TestApiVersion_ExplicitV2 verifies an explicit api_version:2 is honoured.
func TestApiVersion_ExplicitV2(t *testing.T) {
	srv := versionEchoServer(t, false)

	rr := postJSON(t, srv, `{"method":"ping","params":[{"api_version":2}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	result := decodeEnvelope(t, rr.Body.Bytes())
	if got := result["api_version"]; got != float64(types.ApiVersion2) {
		t.Fatalf("api_version resolved to %v, want %d", got, types.ApiVersion2)
	}
}

// TestApiVersion_V3RejectedWithoutBeta verifies api_version:3 is rejected with
// invalidApiVersion when beta_rpc_api is off, mirroring rippled
// getAPIVersionNumber capping maxVersion at apiMaximumSupportedVersion.
func TestApiVersion_V3RejectedWithoutBeta(t *testing.T) {
	srv := versionEchoServer(t, false)

	rr := postJSON(t, srv, `{"method":"ping","params":[{"api_version":3}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	result := decodeEnvelope(t, rr.Body.Bytes())
	if got := result["error"]; got != "invalidApiVersion" {
		t.Fatalf("v3 without beta error = %v, want invalidApiVersion\nbody: %s", got, rr.Body.String())
	}
}

// TestApiVersion_V3AcceptedWithBeta verifies api_version:3 is accepted when
// beta_rpc_api is configured.
func TestApiVersion_V3AcceptedWithBeta(t *testing.T) {
	srv := versionEchoServer(t, true)

	rr := postJSON(t, srv, `{"method":"ping","params":[{"api_version":3}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	result := decodeEnvelope(t, rr.Body.Bytes())
	if got := result["error"]; got != nil {
		t.Fatalf("v3 with beta unexpectedly errored: %v\nbody: %s", got, rr.Body.String())
	}
	if got := result["api_version"]; got != float64(types.ApiVersion3) {
		t.Fatalf("v3 with beta resolved to %v, want %d", got, types.ApiVersion3)
	}
}

// TestApiVersion_BatchV3RejectedWithoutBeta verifies the same cap applies to a
// batch element: rippled rejects an over-max version per element with
// invalid_API_version (ServerHandler.cpp:692-697).
func TestApiVersion_BatchV3RejectedWithoutBeta(t *testing.T) {
	srv := versionEchoServer(t, false)

	body := `{"method":"batch","params":[
		{"method":"ping","api_version":3},
		{"method":"ping","api_version":2}
	]}`
	rr := postJSON(t, srv, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}

	var replies []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &replies); err != nil {
		t.Fatalf("batch reply is not a JSON array: %v\nbody: %s", err, rr.Body.String())
	}
	if len(replies) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(replies))
	}

	el0 := replies[0]["result"].(map[string]any)
	if el0["error"] != "invalidApiVersion" {
		t.Fatalf("batch element 0 error = %v, want invalidApiVersion", el0["error"])
	}
	el1 := replies[1]["result"].(map[string]any)
	if got := el1["api_version"]; got != float64(types.ApiVersion2) {
		t.Fatalf("batch element 1 api_version = %v, want %d", got, types.ApiVersion2)
	}
}

// versionEchoWSServer registers a ping echo on a WebSocket server with the
// given beta flag.
func versionEchoWSServer(t *testing.T, beta bool) *WebSocketServer {
	t.Helper()
	ws := NewWebSocketServer(30*time.Second, types.NewServiceContainer(nil))
	ws.services.BetaRPCAPI = beta
	ws.methodRegistry.Register("ping", &stubHandler{
		apiVers: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		handle: func(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
			return map[string]any{"api_version": ctx.ApiVersion}, nil
		},
	})
	return ws
}

func wsRoundTrip(t *testing.T, ws *WebSocketServer, request string) types.WebSocketResponse {
	t.Helper()
	httpSrv := httptest.NewServer(http.HandlerFunc(ws.ServeHTTP))
	defer httpSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp types.WebSocketResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, string(raw))
	}
	return resp
}

// TestApiVersion_WS_V3GatedByBeta verifies the WebSocket dispatch path enforces
// the same beta-gated version cap as the HTTP path: v3 is rejected with
// invalidApiVersion when beta is off and accepted when it is on.
func TestApiVersion_WS_V3GatedByBeta(t *testing.T) {
	t.Run("rejected_without_beta", func(t *testing.T) {
		ws := versionEchoWSServer(t, false)
		resp := wsRoundTrip(t, ws, `{"command":"ping","api_version":3}`)
		if resp.Error != "invalidApiVersion" {
			t.Fatalf("WS v3 without beta error = %q, want invalidApiVersion", resp.Error)
		}
	})

	t.Run("accepted_with_beta", func(t *testing.T) {
		ws := versionEchoWSServer(t, true)
		resp := wsRoundTrip(t, ws, `{"command":"ping","api_version":3}`)
		if resp.Error != "" {
			t.Fatalf("WS v3 with beta unexpectedly errored: %q", resp.Error)
		}
		if resp.Status != "success" {
			t.Fatalf("WS v3 with beta status = %q, want success", resp.Status)
		}
	})
}

// TestApiVersion_WS_UnspecifiedDefaultsToV1 verifies a WS command without
// api_version resolves to v1.
func TestApiVersion_WS_UnspecifiedDefaultsToV1(t *testing.T) {
	ws := versionEchoWSServer(t, false)
	resp := wsRoundTrip(t, ws, `{"command":"ping"}`)
	if resp.Status != "success" {
		t.Fatalf("WS ping status = %q, want success", resp.Status)
	}
	if resp.ApiVersion != types.ApiVersion1 {
		t.Fatalf("WS unspecified api_version resolved to %d, want %d", resp.ApiVersion, types.ApiVersion1)
	}
}
