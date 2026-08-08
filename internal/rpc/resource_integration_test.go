package rpc

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnifiedResourceManagerGossipsWebSocketLoadIntoHTTPAdmission(t *testing.T) {
	const clientIP = "203.0.113.80"
	managerA := resource.NewManager(nil, nil)
	ws := NewWebSocketServer(WebSocketServerOptions{
		Timeout:         time.Second,
		ResourceManager: managerA,
	})
	ws.methodRegistry.Register("heavy", &heavyStub{stubHandler: stubHandler{}})

	_, gateway, err := net.ParseCIDR("127.0.0.0/8")
	require.NoError(t, err)
	portCtx := &PortContext{SecureGatewayNets: []net.IPNet{*gateway}}
	wsHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws.ServeHTTP(w, r.WithContext(WithPortContext(r.Context(), portCtx)))
	}))
	defer wsHTTP.Close()

	headers := http.Header{"X-Real-IP": []string{clientIP}}
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(wsHTTP.URL, "http"), headers)
	require.NoError(t, err)
	defer client.Close()

	for i := range 60 {
		require.NoError(t, client.WriteJSON(map[string]any{"command": "heavy", "id": i}))
		var response map[string]any
		require.NoError(t, client.ReadJSON(&response))
		assert.Equal(t, "success", response["status"])
	}

	snapshot := managerA.ExportConsumers()
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, clientIP, snapshot.Items[0].Address)
	assert.GreaterOrEqual(t, snapshot.Items[0].Balance, uint32(resource.WarningThreshold))

	peer := managerA.NewInboundEndpoint(clientIP + ":51235")
	require.NotNil(t, peer)
	before := peer.Balance()
	peer.Charge(resource.FeeInvalidData, "peer work")
	assert.Greater(t, peer.Balance(), before, "peer and RPC work must share one endpoint balance")
	peer.Release()

	managerB := resource.NewManager(nil, nil)
	require.NoError(t, managerB.ImportConsumers("node-a", managerA.ExportConsumers()))
	httpServer := newHardeningServer(t, time.Second, "ping", &stubHandler{})
	httpServer.resourceManager = managerB
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"method":"ping","params":[{}]}`))
	request.RemoteAddr = clientIP + ":1234"
	response := httptest.NewRecorder()
	httpServer.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	result := decodeEnvelope(t, response.Body.Bytes())
	assert.Equal(t, "load", result["warning"], "imported load must affect real HTTP admission")

	require.NoError(t, client.Close())
	deadline := time.Now().Add(time.Second)
	for len(managerA.ExportConsumers().Items) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	assert.Empty(t, managerA.ExportConsumers().Items, "released WebSocket consumers must not be gossiped")
}
