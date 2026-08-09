package rpc

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestRolePrivilegeMatrix(t *testing.T) {
	tests := []struct {
		name      string
		role      types.Role
		isAdmin   bool
		unlimited bool
	}{
		{name: "guest", role: types.RoleGuest},
		{name: "user", role: types.RoleUser},
		{name: "admin", role: types.RoleAdmin, isAdmin: true, unlimited: true},
		{name: "identified", role: types.RoleIdentified, unlimited: true},
		{name: "proxy", role: types.RoleProxy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.isAdmin, test.role.IsAdmin())
			require.Equal(t, test.unlimited, test.role.IsUnlimited())
		})
	}
}

func TestHTTPRoleContextCarriesDerivedPrivileges(t *testing.T) {
	tests := []struct {
		name      string
		port      *PortContext
		xUser     string
		want      types.Role
		isAdmin   bool
		unlimited bool
	}{
		{
			name: "guest",
			want: types.RoleGuest,
		},
		{
			name: "admin",
			port: &PortContext{AdminNets: []net.IPNet{mustParseCIDR("127.0.0.0/8")}},
			want: types.RoleAdmin, isAdmin: true, unlimited: true,
		},
		{
			name:      "identified",
			port:      &PortContext{SecureGatewayNets: []net.IPNet{mustParseCIDR("127.0.0.0/8")}},
			xUser:     "alice",
			want:      types.RoleIdentified,
			unlimited: true,
		},
		{
			name: "proxy",
			port: &PortContext{SecureGatewayNets: []net.IPNet{mustParseCIDR("127.0.0.0/8")}},
			want: types.RoleProxy,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan *types.RpcContext, 1)
			srv := newHardeningServer(t, time.Second, "ping", &stubHandler{
				handle: func(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
					observed <- ctx
					return map[string]any{"ok": true}, nil
				},
			})
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"method":"ping","params":[{}]}`))
			req.RemoteAddr = "127.0.0.1:1234"
			if test.xUser != "" {
				req.Header.Set("X-User", test.xUser)
			}
			if test.port != nil {
				req = req.WithContext(WithPortContext(req.Context(), test.port))
			}
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

			ctx := <-observed
			require.Equal(t, test.want, ctx.Role)
			require.Equal(t, test.isAdmin, ctx.Role.IsAdmin())
			require.Equal(t, test.unlimited, ctx.Role.IsUnlimited())
		})
	}
}

func TestWebSocketRoleContextCarriesDerivedPrivileges(t *testing.T) {
	tests := []struct {
		name      string
		port      *PortContext
		xUser     string
		want      types.Role
		isAdmin   bool
		unlimited bool
	}{
		{
			name:      "admin",
			port:      &PortContext{AdminNets: []net.IPNet{mustParseCIDR("127.0.0.0/8")}},
			want:      types.RoleAdmin,
			isAdmin:   true,
			unlimited: true,
		},
		{
			name:      "identified",
			port:      &PortContext{SecureGatewayNets: []net.IPNet{mustParseCIDR("127.0.0.0/8")}},
			xUser:     "alice",
			want:      types.RoleIdentified,
			unlimited: true,
		},
		{
			name: "proxy",
			port: &PortContext{SecureGatewayNets: []net.IPNet{mustParseCIDR("127.0.0.0/8")}},
			want: types.RoleProxy,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan *types.RpcContext, 1)
			ws := NewWebSocketServer(WebSocketServerOptions{
				Timeout: time.Second,
				Registry: mustTestMethodRegistry(t, map[string]types.MethodHandler{
					"ping": &stubHandler{
						handle: func(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
							observed <- ctx
							return map[string]any{"ok": true}, nil
						},
					},
				}),
			})
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws.ServeHTTP(w, r.WithContext(WithPortContext(r.Context(), test.port)))
			}))
			t.Cleanup(func() {
				httpServer.Close()
				_ = ws.Close(t.Context())
			})

			headers := http.Header{}
			if test.xUser != "" {
				headers.Set("X-User", test.xUser)
			}
			conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), headers)
			require.NoError(t, err)
			require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
			t.Cleanup(func() { _ = conn.Close() })
			require.NoError(t, conn.WriteJSON(map[string]any{"command": "ping", "id": 1}))
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
			var responseBody map[string]any
			require.NoError(t, conn.ReadJSON(&responseBody))
			require.Equal(t, "success", responseBody["status"])

			select {
			case ctx := <-observed:
				require.Equal(t, test.want, ctx.Role)
				require.Equal(t, test.isAdmin, ctx.Role.IsAdmin())
				require.Equal(t, test.unlimited, ctx.Role.IsUnlimited())
			case <-time.After(time.Second):
				t.Fatal("WebSocket handler did not observe a request context")
			}
		})
	}
}
