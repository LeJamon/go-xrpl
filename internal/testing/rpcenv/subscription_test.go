package rpcenv_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/testing/rpcenv"
)

const subscriptionTestMPT = "000000000000000000000000000000000000000000000001"

func openSubscriptionWebSocket(t *testing.T) (*rpc.WebSocketServer, *websocket.Conn) {
	t.Helper()
	env := rpcenv.New(t)
	ws := rpc.NewWebSocketServer(rpc.WebSocketServerOptions{
		Timeout:  time.Second,
		Services: types.NewTestServiceGraph(env.Services()),
	})
	httpServer := httptest.NewServer(http.HandlerFunc(ws.ServeHTTP))
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = ws.Close(ctx)
		httpServer.Close()
	})
	return ws, client
}

func websocketJSON(t *testing.T, client *websocket.Conn, request string) map[string]any {
	t.Helper()
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(request)))
	require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
	_, payload, err := client.ReadMessage()
	require.NoError(t, err)
	var response map[string]any
	require.NoError(t, json.Unmarshal(payload, &response))
	return response
}

func TestSubscriptionServerAcknowledgementUsesStandaloneFlag(t *testing.T) {
	_, client := openSubscriptionWebSocket(t)
	response := websocketJSON(t, client, `{"command":"subscribe","id":1,"streams":["server"]}`)
	require.Equal(t, "success", response["status"])
	result, ok := response["result"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, result["stand_alone"])
	require.Equal(t, "full", result["server_status"])
}

func TestSubscriptionCanonicalMPTBookFanout(t *testing.T) {
	ws, client := openSubscriptionWebSocket(t)
	response := websocketJSON(t, client, `{"command":"subscribe","id":2,"books":[{"taker_pays":{"mpt_issuance_id":"`+subscriptionTestMPT+`"},"taker_gets":{"currency":"XRP"}}]}`)
	require.Equal(t, "success", response["status"])

	payload := []byte(`{"type":"transaction","marker":"mpt-book"}`)
	ws.SubscriptionManager().BroadcastToOrderBooksVersioned(payload, payload, []types.OrderBookSpec{{
		TakerPays: types.CurrencySpec{MPTIssuanceID: subscriptionTestMPT},
		TakerGets: types.CurrencySpec{Currency: "XRP"},
	}})
	require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
	_, delivered, err := client.ReadMessage()
	require.NoError(t, err)
	require.JSONEq(t, string(payload), string(delivered))
}

func TestSubscriptionBookWireCoercionAndHexIssuer(t *testing.T) {
	_, client := openSubscriptionWebSocket(t)
	malformed := websocketJSON(t, client, `{"command":"subscribe","id":3,"books":[{"taker_pays":{"currency":0.0},"taker_gets":{"currency":"USD","issuer":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}}]}`)
	require.Equal(t, "error", malformed["status"])
	require.Equal(t, "srcCurMalformed", malformed["error"])

	valid := websocketJSON(t, client, `{"command":"subscribe","id":4,"books":[{"taker_pays":{"currency":"XRP","issuer":"0"},"taker_gets":{"currency":"USD","issuer":"B5F762798A53D543A014CAF8B297CFF8F2F937E8"}}]}`)
	require.Equal(t, "success", valid["status"])
}
