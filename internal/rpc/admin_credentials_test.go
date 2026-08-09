package rpc

import (
	"encoding/json"
	"fmt"
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

func credentialedAdminPort() *PortContext {
	return &PortContext{
		AdminNets:     []net.IPNet{mustParseCIDR("127.0.0.0/8")},
		AdminUser:     "root",
		AdminPassword: "secret",
	}
}

func TestHTTPAdminCredentials(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "stop", &stubHandler{role: types.RoleAdmin})
	tests := []struct {
		name       string
		params     string
		remoteAddr string
		wantStatus int
	}{
		{name: "missing", params: `{}`, remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusForbidden},
		{name: "wrong", params: `{"admin_user":"root","admin_password":"wrong"}`, remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusForbidden},
		{name: "non-string", params: `{"admin_user":"root","admin_password":7}`, remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusForbidden},
		{name: "exact outside admin net", params: `{"admin_user":"root","admin_password":"secret"}`, remoteAddr: "192.0.2.1:1234", wantStatus: http.StatusForbidden},
		{name: "exact", params: `{"admin_user":"root","admin_password":"secret"}`, remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"method":"stop","params":[%s]}`, test.params)
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			req.RemoteAddr = test.remoteAddr
			req = req.WithContext(WithPortContext(req.Context(), credentialedAdminPort()))
			rr := httptest.NewRecorder()

			srv.ServeHTTP(rr, req)

			require.Equal(t, test.wantStatus, rr.Code, rr.Body.String())
		})
	}
}

func TestHTTPBatchAdminCredentialsArePerElement(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "stop", &stubHandler{role: types.RoleAdmin})
	body := `{"method":"batch","params":[
		{"method":"stop","params":[{"admin_user":"root","admin_password":"secret"}]},
		{"method":"stop","params":[{}]}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req = req.WithContext(WithPortContext(req.Context(), credentialedAdminPort()))
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var replies []map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &replies))
	require.Len(t, replies, 2)
	require.Contains(t, replies[0], "result")
	require.Contains(t, replies[1], "error")
}

func TestWebSocketAdminCredentials(t *testing.T) {
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 2 * time.Second})
	ws.methodRegistry.Register("stop", &stubHandler{role: types.RoleAdmin})
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws.ServeHTTP(w, r.WithContext(WithPortContext(r.Context(), credentialedAdminPort())))
	}))
	defer httpSrv.Close()

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpSrv.URL, "http"), nil)
	require.NoError(t, err)
	defer c.Close()

	tests := []struct {
		name        string
		credentials map[string]any
		wantStatus  string
	}{
		{name: "missing", credentials: map[string]any{}, wantStatus: "error"},
		{name: "wrong", credentials: map[string]any{"admin_user": "root", "admin_password": "wrong"}, wantStatus: "error"},
		{name: "non-string", credentials: map[string]any{"admin_user": false, "admin_password": "secret"}, wantStatus: "error"},
		{name: "exact", credentials: map[string]any{"admin_user": "root", "admin_password": "secret"}, wantStatus: "success"},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := map[string]any{"command": "stop", "id": i + 1}
			for key, value := range test.credentials {
				request[key] = value
			}
			require.NoError(t, c.WriteJSON(request))
			require.NoError(t, c.SetReadDeadline(time.Now().Add(2*time.Second)))
			var response map[string]any
			require.NoError(t, c.ReadJSON(&response))
			require.Equal(t, test.wantStatus, response["status"])
		})
	}
}
