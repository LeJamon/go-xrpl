package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebSocket_PeersRPC_UsesPeerSource(t *testing.T) {
	src := &stubPeerSource{
		peers: []map[string]any{
			{"address": "192.0.2.1:51235", "public_key": "nHB1"},
			{"address": "192.0.2.2:51235", "public_key": "nHB2"},
		},
	}

	ledger := &mockLedgerService{}
	services := types.NewTestServiceGraph(&types.ServiceContainer{Ledger: ledger})

	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second, Services: services, PeerSource: src})

	pc := loopbackAdminPortContext()
	pc.PortName = "test_admin"
	httpSrv := httptest.NewServer(PortMiddleware(pc, ws))
	defer httpSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	peers := wsCall(t, conn, map[string]any{"command": "peers", "id": 1})
	peersList, ok := peers["result"].(map[string]any)["peers"].([]any)
	require.True(t, ok, "peers result must contain a `peers` array")
	assert.Len(t, peersList, len(src.peers),
		"`peers` RPC over WS must return one entry per overlay peer")

	info := wsCall(t, conn, map[string]any{"command": "server_info", "id": 2})
	infoMap := info["result"].(map[string]any)["info"].(map[string]any)
	assert.Equal(t, float64(len(src.peers)), infoMap["peers"],
		"server_info.peers over WS must equal len(peers RPC result)")
}

func wsCall(t *testing.T, conn *websocket.Conn, req map[string]any) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	require.NoError(t, conn.WriteJSON(req))
	_, raw, err := conn.ReadMessage()
	require.NoError(t, err)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(raw, &resp))
	return resp
}

func TestWebSocketPeerSourceDefaultsToNil(t *testing.T) {
	src := &stubPeerSource{peers: []map[string]any{{"address": "192.0.2.1:51235"}}}
	withSource := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second, PeerSource: src})
	require.NotNil(t, withSource.loadPeerSource())
	withoutSource := NewWebSocketServer(WebSocketServerOptions{Timeout: 30 * time.Second})
	require.Nil(t, withoutSource.loadPeerSource())
}
