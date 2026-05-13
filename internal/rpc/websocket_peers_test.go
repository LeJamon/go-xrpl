package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
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
	services := &types.ServiceContainer{Ledger: ledger}

	ws := NewWebSocketServer(30*time.Second, services)
	ws.RegisterAllMethods()
	ws.SetPeerSource(src)

	// Admin port context so peers (admin-only) is reachable.
	pc := &PortContext{PortName: "test_admin", AdminNets: nil}
	httpSrv := httptest.NewServer(PortMiddleware(pc, nil, ws))
	defer httpSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	peers := wsCall(t, conn, map[string]any{"command": "peers", "id": 1})
	peersList, ok := peers["result"].(map[string]any)["peers"].([]any)
	require.True(t, ok, "peers result must contain a `peers` array")
	assert.Len(t, peersList, len(src.peers),
		"`peers` RPC over WS must return one entry per overlay peer (issue #419)")

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

func TestWebSocket_SetPeerSource_NilDetaches(t *testing.T) {
	src := &stubPeerSource{peers: []map[string]any{{"address": "192.0.2.1:51235"}}}
	ws := NewWebSocketServer(30*time.Second, nil)
	ws.SetPeerSource(src)
	require.NotNil(t, ws.loadPeerSource())
	ws.SetPeerSource(nil)
	require.Nil(t, ws.loadPeerSource())
}
