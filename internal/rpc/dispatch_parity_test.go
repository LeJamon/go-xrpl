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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// condStubHandler is a stub method handler with a configurable precondition,
// used to exercise the shared dispatch core's conditionMet gate.
type condStubHandler struct {
	cond types.Condition
}

func (h *condStubHandler) Handle(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
	return map[string]any{"ok": true}, nil
}
func (h *condStubHandler) RequiredRole() types.Role           { return types.RoleGuest }
func (h *condStubHandler) SupportedApiVersions() []int        { return nil }
func (h *condStubHandler) RequiredCondition() types.Condition { return h.cond }

// TestDispatchMethodEnforcesConditionMet pins H3: the shared dispatch core
// (used by BOTH the HTTP and WebSocket transports) runs conditionMet, so a
// not-synced node refuses a condition-requiring method on either transport.
func TestDispatchMethodEnforcesConditionMet(t *testing.T) {
	reg := types.NewMethodRegistry()
	reg.Register("gated", &condStubHandler{cond: types.NeedsNetworkConnection})

	t.Run("not synced is refused", func(t *testing.T) {
		ctx := &types.RpcContext{
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: newMockLedgerService()}, // zero serverInfo: disconnected
		}
		_, rpcErr := dispatchMethod(reg, nil, ctx.Services, ctx, "gated", nil, types.RpcErrorNoPermission, rpcLog())
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNO_NETWORK, rpcErr.Code)
		assert.Equal(t, "noNetwork", rpcErr.ErrorString)
	})

	t.Run("synced passes", func(t *testing.T) {
		ctx := &types.RpcContext{
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: syncedStandalone()},
		}
		result, rpcErr := dispatchMethod(reg, nil, ctx.Services, ctx, "gated", nil, types.RpcErrorNoPermission, rpcLog())
		require.Nil(t, rpcErr)
		assert.Equal(t, map[string]any{"ok": true}, result)
	})
}

// TestResolveWSCommand pins M4's command resolution: `method` is accepted as an
// alias for `command`, and the request is malformed (→ missingCommand) only
// when neither field is present, a supplied field is not a string, or both
// fields are present and disagree. An empty string reaches normal dispatch.
func TestResolveWSCommand(t *testing.T) {
	cases := []struct {
		name   string
		in     map[string]any
		want   string
		wantOK bool
	}{
		{"command only", map[string]any{"command": "ping"}, "ping", true},
		{"method alias", map[string]any{"method": "ping"}, "ping", true},
		{"both agree", map[string]any{"command": "ping", "method": "ping"}, "ping", true},
		{"both disagree", map[string]any{"command": "ping", "method": "pong"}, "", false},
		{"neither", map[string]any{"id": 1}, "", false},
		{"empty command", map[string]any{"command": ""}, "", true},
		{"non-string command", map[string]any{"command": 7}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := resolveWSCommand(c.in)
			assert.Equal(t, c.wantOK, ok)
			assert.Equal(t, c.want, got)
		})
	}
}

// TestSingleRequestParseFailuresReturn400 pins M3: a malformed single request
// gets a plain-text HTTP 400, matching rippled (ServerHandler.cpp:629-635,
// :769, :826), not a 200 + JSON-RPC envelope.
func TestSingleRequestParseFailuresReturn400(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "ping", &stubHandler{})

	cases := []struct {
		name string
		body string
		want string
	}{
		{"unparseable json", `{not json`, "Unable to parse request:"},
		{"missing method", `{"params":[{}]}`, "Null method"},
		{"null method", `{"method":null}`, "Null method"},
		{"non-string method", `{"method":7}`, "method is not string"},
		{"empty method", `{"method":""}`, "method is empty"},
		{"non-array params", `{"method":"ping","params":{"x":1}}`, "params unparseable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
			req.RemoteAddr = "10.0.0.1:1234"
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusBadRequest, rr.Code)
			assert.Contains(t, rr.Body.String(), c.want)
			// rippled's HTTPReply labels even these bare-string 400 bodies
			// application/json, not Go's http.Error text/plain (M2).
			assert.Equal(t, "application/json; charset=UTF-8", rr.Header().Get("Content-Type"))
		})
	}
}

// TestLoadWarningNestedInResultOnHTTP pins M1: the HTTP envelope places
// warning:"load" INSIDE result (rippled ServerHandler.cpp:919-920 → :938/:971),
// not at the top level (which is the WS placement, :519).
func TestLoadWarningNestedInResultOnHTTP(t *testing.T) {
	body := buildXrplResponseBody(nil, map[string]any{"foo": "bar"}, nil, &JsonRpcResponseOptions{Warning: "load"})
	result, ok := body["result"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "load", result["warning"], "HTTP load warning belongs inside result")
	_, topLevel := body["warning"]
	assert.False(t, topLevel, "HTTP load warning must not be emitted at the top level")
}

// TestWSCommandAliasAndMissingCommand pins M4 on the wire: `method` works as an
// alias for `command`, and an unresolvable command yields a bare missingCommand
// token that echoes the request and id (ServerHandler.cpp:446-468).
func TestWSCommandAliasAndMissingCommand(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 2 * time.Second})
	ws.methodRegistry.Register("ping", &stubHandler{})

	httpSrv := httptest.NewServer(http.HandlerFunc(ws.ServeHTTP))
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer c.Close()

	roundtrip := func(send map[string]any) map[string]any {
		require.NoError(t, c.WriteJSON(send))
		require.NoError(t, c.SetReadDeadline(time.Now().Add(2*time.Second)))
		var resp map[string]any
		require.NoError(t, c.ReadJSON(&resp))
		return resp
	}

	t.Run("method alias dispatches", func(t *testing.T) {
		resp := roundtrip(map[string]any{"method": "ping", "id": float64(1)})
		assert.Equal(t, "success", resp["status"])
		assert.Equal(t, float64(1), resp["id"])
		assert.Nil(t, resp["error"])
	})

	t.Run("missing command echoes request as bare token", func(t *testing.T) {
		resp := roundtrip(map[string]any{"id": float64(2)})
		assert.Equal(t, "response", resp["type"])
		assert.Equal(t, "error", resp["status"])
		assert.Equal(t, "missingCommand", resp["error"])
		assert.Equal(t, float64(2), resp["id"])
		assert.Equal(t, map[string]any{"id": float64(2)}, resp["request"])
		// Bare token: no numeric code / message.
		assert.NotContains(t, resp, "error_code")
		assert.NotContains(t, resp, "error_message")
	})

	t.Run("missing command redacts credential-shaped metadata", func(t *testing.T) {
		resp := roundtrip(map[string]any{
			"id": map[string]any{"secret": "private seed"},
		})
		assert.Equal(t, "missingCommand", resp["error"])
		assert.Equal(t, map[string]any{"secret": maskedValue}, resp["id"])
		encoded, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), "private seed")
	})

	t.Run("command/method mismatch is missingCommand", func(t *testing.T) {
		resp := roundtrip(map[string]any{"command": "ping", "method": "pong", "id": float64(3)})
		assert.Equal(t, "error", resp["status"])
		assert.Equal(t, "missingCommand", resp["error"])
	})

	t.Run("non-string command is not rescued by method", func(t *testing.T) {
		resp := roundtrip(map[string]any{"command": float64(7), "method": "ping", "id": float64(4)})
		assert.Equal(t, "error", resp["status"])
		assert.Equal(t, "missingCommand", resp["error"])
	})

	t.Run("non-string method is not rescued by command", func(t *testing.T) {
		resp := roundtrip(map[string]any{"command": "ping", "method": float64(7), "id": float64(5)})
		assert.Equal(t, "error", resp["status"])
		assert.Equal(t, "missingCommand", resp["error"])
	})

	t.Run("empty command reaches unknown method", func(t *testing.T) {
		resp := roundtrip(map[string]any{"command": "", "id": float64(6)})
		assert.Equal(t, "error", resp["status"])
		assert.Equal(t, "unknownCmd", resp["error"])
		assert.Equal(t, float64(types.RpcMETHOD_NOT_FOUND), resp["error_code"])
		assert.Equal(t, "Unknown method.", resp["error_message"])
	})
}
