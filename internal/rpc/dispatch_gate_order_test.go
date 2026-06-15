package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// saturatedShedder returns a ClientLoadShedder whose in-flight count is one
// over MaxJobQueueClients, the threshold RequireNotBusyClient sheds at.
func saturatedShedder() *types.ClientLoadShedder {
	s := types.NewClientLoadShedder()
	for i := int64(0); i <= types.MaxJobQueueClients; i++ {
		s.Begin()
	}
	return s
}

// TestDispatchGateOrder pins the gate sequence of the shared dispatch core
// (used by BOTH the HTTP and WebSocket transports). rippled resolves a role-
// layer FORBID (403) in ServerHandler::processRequest ahead of doCommand, while
// the job-queue busy gate (rpcTOO_BUSY) lives inside fillHandler — so FORBID
// precedes busy, busy precedes the unknown-command failure, and an invalid
// api_version precedes both. A forbidden admin request under queue saturation
// must therefore be denied 403, not rpcTOO_BUSY.
func TestDispatchGateOrder(t *testing.T) {
	reg := types.NewMethodRegistry()
	reg.Register("stop", &stubHandler{role: types.RoleAdmin}) // admin-only
	reg.Register("ping", &stubHandler{role: types.RoleGuest}) // open

	cases := []struct {
		name       string
		method     string
		apiVersion int
		saturated  bool
		wantErr    bool
		wantCode   int
	}{
		{"forbidden admin while saturated → FORBIDDEN, not TOO_BUSY",
			"stop", types.ApiVersion1, true, true, types.RpcFORBIDDEN},
		{"forbidden admin while idle → FORBIDDEN",
			"stop", types.ApiVersion1, false, true, types.RpcFORBIDDEN},
		{"unknown method while saturated → TOO_BUSY (busy before unknown)",
			"nope", types.ApiVersion1, true, true, types.RpcTOO_BUSY},
		{"unknown method while idle → METHOD_NOT_FOUND",
			"nope", types.ApiVersion1, false, true, types.RpcMETHOD_NOT_FOUND},
		{"invalid api_version + forbidden admin → INVALID_API_VERSION (before FORBID)",
			"stop", 99, false, true, types.RpcINVALID_API_VERSION},
		{"invalid api_version + forbidden admin while saturated → INVALID_API_VERSION",
			"stop", 99, true, true, types.RpcINVALID_API_VERSION},
		{"open method while saturated → TOO_BUSY (busy still fires when not forbidden)",
			"ping", types.ApiVersion1, true, true, types.RpcTOO_BUSY},
		{"open method while idle → success",
			"ping", types.ApiVersion1, false, false, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			services := &types.ServiceContainer{}
			if c.saturated {
				services.ClientLoad = saturatedShedder()
			}
			ctx := &types.RpcContext{
				ApiVersion: c.apiVersion,
				Role:       types.RoleGuest, // non-admin caller
				Services:   services,
			}

			result, rpcErr := dispatchMethod(reg, nil, services, ctx, c.method, nil, types.RpcErrorForbidden, rpcLog())

			if !c.wantErr {
				require.Nil(t, rpcErr)
				assert.Equal(t, map[string]any{"ok": true}, result)
				return
			}
			require.NotNil(t, rpcErr)
			assert.Equal(t, c.wantCode, rpcErr.Code)
		})
	}
}

// TestHTTPForbiddenBeatsBusy pins the observable wire-shape divergence the
// resequencing fixes: a forbidden admin request concurrent with a saturated
// job queue returns HTTP 403 "Forbidden" (the role-layer short-circuit), not
// the rpcTOO_BUSY 503 envelope. An unknown method under the same saturation
// stays 503, confirming busy still precedes the unknown-command failure.
func TestHTTPForbiddenBeatsBusy(t *testing.T) {
	services := types.NewServiceContainer(nil)
	srv := NewServer(time.Second, services)
	srv.registry.Register("stop", &stubHandler{role: types.RoleAdmin})

	for i := int64(0); i <= types.MaxJobQueueClients; i++ {
		services.ClientLoad.Begin()
	}

	t.Run("forbidden admin request → 403, not 503", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"stop","params":[{}]}`))
		// 192.0.2.1 (TEST-NET-1) is never localhost → RoleGuest, no admin
		// fallback and not exempt from the shedder.
		req.RemoteAddr = "192.0.2.1:1234"
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		require.Equal(t, http.StatusForbidden, rr.Code,
			"forbidden admin request under saturation must be 403, not 503; body: %s", rr.Body.String())
		assert.Equal(t, "Forbidden", strings.TrimSpace(rr.Body.String()))
		assert.NotContains(t, rr.Body.String(), "tooBusy")
	})

	t.Run("unknown method stays 503 tooBusy", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"nope","params":[{}]}`))
		req.RemoteAddr = "192.0.2.1:1234"
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		require.Equal(t, http.StatusServiceUnavailable, rr.Code,
			"unknown method under saturation must stay 503; body: %s", rr.Body.String())
		result := decodeEnvelope(t, rr.Body.Bytes())
		assert.Equal(t, "tooBusy", result["error"])
	})
}
