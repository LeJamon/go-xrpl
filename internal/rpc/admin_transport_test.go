package rpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func transportTestPort() *PortContext {
	return &PortContext{
		PortName:       "admin",
		User:           "operator",
		Password:       "secret",
		AllowedOrigins: []string{"https://console.example"},
	}
}

func transportTestServer(t *testing.T, calls *atomic.Int32) *Server {
	t.Helper()
	srv := NewServer(ServerOptions{Timeout: time.Second, Services: types.NewTestServiceGraph(types.NewServiceContainer(nil)), Registry: mustTestMethodRegistry(t, map[string]types.MethodHandler{
		"stop": &stubHandler{
			role: types.RoleGuest,
			handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
				calls.Add(1)
				return map[string]any{"stopped": true}, nil
			},
		}})})
	return srv
}

func transportRequest(origin, user, password string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"method":"stop","params":[{}]}`))
	req.RemoteAddr = "203.0.113.9:1234"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if user != "" || password != "" {
		req.SetBasicAuth(user, password)
	}
	return req
}

func TestHTTPTransportOriginAndBasicAuth(t *testing.T) {
	var calls atomic.Int32
	srv := transportTestServer(t, &calls)
	handler := PortMiddleware(transportTestPort(), srv)

	t.Run("allowed browser preflight does not need credentials", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/", nil)
		req.Header.Set("Origin", "https://console.example")
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "https://console.example", rr.Header().Get("Access-Control-Allow-Origin"))
		require.Equal(t, int32(0), calls.Load())
	})

	t.Run("GET never dispatches an RPC command", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?command=stop", nil)
		req.RemoteAddr = "203.0.113.9:1234"
		req.SetBasicAuth("operator", "secret")
		req.Header.Set("Origin", "https://console.example")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
		require.Equal(t, int32(0), calls.Load())
	})

	t.Run("rejected origin cannot reach stop", func(t *testing.T) {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, transportRequest("https://attacker.example", "operator", "secret"))
		require.Equal(t, http.StatusForbidden, rr.Code)
		require.Equal(t, int32(0), calls.Load())
		require.Equal(t, "close", rr.Header().Get("Connection"))
	})

	t.Run("duplicate origin headers are rejected", func(t *testing.T) {
		req := transportRequest("https://console.example", "operator", "secret")
		req.Header.Add("Origin", "https://attacker.example")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		require.Equal(t, http.StatusForbidden, rr.Code)
		require.Equal(t, int32(0), calls.Load())
		require.Equal(t, "close", rr.Header().Get("Connection"))
	})

	t.Run("allowed origin and credentials reach stop", func(t *testing.T) {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, transportRequest("https://console.example", "operator", "secret"))
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, "https://console.example", rr.Header().Get("Access-Control-Allow-Origin"))
		require.Equal(t, "true", rr.Header().Get("Access-Control-Allow-Credentials"))
		require.Equal(t, int32(1), calls.Load())
	})

	for _, test := range []struct {
		name     string
		user     string
		password string
	}{
		{name: "missing", user: "", password: ""},
		{name: "wrong user", user: "attacker", password: "secret"},
		{name: "wrong password", user: "operator", password: "wrong"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, transportRequest("", test.user, test.password))
			require.Equal(t, http.StatusUnauthorized, rr.Code)
			require.Equal(t, `Basic realm="xrpld"`, rr.Header().Get("WWW-Authenticate"))
			require.Equal(t, int32(1), calls.Load(), "rejected credentials reached the handler")
			require.Equal(t, "close", rr.Header().Get("Connection"))
		})
	}
}

func TestHTTPTransportNoOriginCLI(t *testing.T) {
	var calls atomic.Int32
	srv := transportTestServer(t, &calls)
	port := transportTestPort()
	port.AllowedOrigins = []string{"https://console.example"}
	rr := httptest.NewRecorder()
	PortMiddleware(port, srv).ServeHTTP(rr, transportRequest("", "operator", "secret"))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, int32(1), calls.Load())
}

func wsTransportServer(t *testing.T, pc *PortContext) (*httptest.Server, *WebSocketServer) {
	t.Helper()
	ws := NewWebSocketServer(WebSocketServerOptions{
		Timeout: time.Second,
		Registry: mustTestMethodRegistry(t, map[string]types.MethodHandler{
			"ping": &stubHandler{role: types.RoleGuest},
		}),
	})
	httpSrv := httptest.NewServer(PortMiddleware(pc, http.HandlerFunc(ws.ServeHTTP)))
	t.Cleanup(func() {
		httpSrv.Close()
		_ = ws.Close(context.Background())
	})
	return httpSrv, ws
}

func dialTransport(t *testing.T, httpSrv *httptest.Server, origin, user, password string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	if user != "" || password != "" {
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+password)))
	}
	url := "ws" + strings.TrimPrefix(httpSrv.URL, "http")
	return websocket.DefaultDialer.Dial(url, header)
}

func TestWebSocketTransportOriginAndBasicAuth(t *testing.T) {
	pc := transportTestPort()
	httpSrv, _ := wsTransportServer(t, pc)

	_, response, err := dialTransport(t, httpSrv, "https://attacker.example", "operator", "secret")
	require.Error(t, err)
	require.NotNil(t, response)
	require.Equal(t, http.StatusForbidden, response.StatusCode)

	_, response, err = dialTransport(t, httpSrv, "https://console.example", "", "")
	require.Error(t, err)
	require.NotNil(t, response)
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.Equal(t, `Basic realm="xrpld"`, response.Header.Get("WWW-Authenticate"))

	_, response, err = dialTransport(t, httpSrv, "https://console.example", "operator", "wrong")
	require.Error(t, err)
	require.NotNil(t, response)
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)

	conn, response, err := dialTransport(t, httpSrv, "https://console.example", "operator", "secret")
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
	defer conn.Close()
	require.NoError(t, conn.WriteJSON(map[string]any{"command": "ping", "id": 1}))
	var reply map[string]any
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	require.NoError(t, conn.ReadJSON(&reply))
	require.Equal(t, "success", reply["status"])
}

func TestWebSocketTransportNoOriginCLI(t *testing.T) {
	httpSrv, _ := wsTransportServer(t, transportTestPort())
	conn, response, err := dialTransport(t, httpSrv, "", "operator", "secret")
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
	defer conn.Close()
}
