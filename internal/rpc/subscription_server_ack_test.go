package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

type mptSnapshotLedger struct {
	*mockLedgerService
	gets types.Amount
	pays types.Amount
}

func (m *mptSnapshotLedger) GetBookOffers(_ context.Context, gets, pays types.Amount, _, _ string, _ string, _ uint32, _ string, _ bool) (*types.BookOffersResult, error) {
	m.gets, m.pays = gets, pays
	return &types.BookOffersResult{}, nil
}

func TestServerSubscriptionInitialAcknowledgement(t *testing.T) {
	ledger := newMockLedgerServiceServerInfo()
	ledger.standalone = false
	ledger.serverState = "validating"
	services := &types.ServiceContainer{Ledger: ledger, NodePublicKey: testNodePublicKey()}
	ws := NewWebSocketServer(WebSocketServerOptions{Services: services})
	request := types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubServer}}

	guest := ws.buildSubscribeAck(&types.RpcContext{Services: services}, request)
	require.Regexp(t, regexp.MustCompile(`^[0-9A-F]{64}$`), guest["random"])
	require.Equal(t, "full", guest["server_status"])
	require.IsType(t, uint32(0), guest["load_base"])
	require.IsType(t, uint32(0), guest["load_factor"])
	require.NotEmpty(t, guest["hostid"])
	require.Equal(t, testNodePublicKey(), guest["pubkey_node"])
	require.NotContains(t, guest, "stand_alone")

	admin := ws.buildSubscribeAck(&types.RpcContext{Services: services, IsAdmin: true}, request)
	require.Equal(t, "validating", admin["server_status"])
	require.NotEmpty(t, admin["hostid"])
	require.NotEqual(t, guest["random"], admin["random"])

	ledger.standalone = true
	standalone := ws.buildSubscribeAck(&types.RpcContext{Services: services}, request)
	require.Equal(t, true, standalone["stand_alone"])
	require.Equal(t, "standalone", standalone["server_status"])
}

func TestServerSubscriptionInitialLoadExcludesTxQEscalation(t *testing.T) {
	services := &types.ServiceContainer{
		LoadFactorFees: func() types.LoadFactorFees {
			return types.LoadFactorFees{Local: 256, Net: 256, Cluster: 256}
		},
		TxQMetrics: func() types.TxQServerMetrics {
			return types.TxQServerMetrics{ReferenceFeeLevel: 256, OpenLedgerFeeLevel: 1024}
		},
	}
	ws := NewWebSocketServer(WebSocketServerOptions{Services: services})
	request := types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubServer}}
	ack := ws.buildSubscribeAck(&types.RpcContext{Services: services}, request)
	require.Equal(t, uint32(256), ack["load_factor"])
	require.Equal(t, uint64(1024), handlers.ComputeServerLoad(services).LoadFactor)
}

func TestServerSubscriptionStateIsSampledBeforeRegistration(t *testing.T) {
	for _, transport := range []string{"websocket", "url"} {
		t.Run(transport, func(t *testing.T) {
			services := &types.ServiceContainer{}
			var ws *WebSocketServer
			services.LoadFactorFees = func() types.LoadFactorFees {
				factor := uint32(256)
				if ws != nil && ws.subscriptionManager.GetSubscriberCount(types.SubServer) != 0 {
					factor = 512
				}
				return types.LoadFactorFees{Local: factor, Net: factor, Cluster: factor}
			}
			ws = NewWebSocketServer(WebSocketServerOptions{Services: services})
			ctx := &types.RpcContext{Services: services, IsAdmin: true}
			var ack map[string]any
			if transport == "websocket" {
				connection := subscription.NewConnection("ack-order", make(chan []byte, 1))
				registration, attached := ws.subscriptionManager.Attach(connection)
				require.True(t, attached)
				t.Cleanup(func() { ws.subscriptionManager.Detach(registration) })
				result, rpcErr := ws.executeSubscribe(&websocketConnection{Connection: connection, registration: registration}, ctx, types.WebSocketCommand{
					Params: json.RawMessage(`{"streams":["server"]}`),
				})
				require.Nil(t, rpcErr)
				ack = result.(map[string]any)
			} else {
				target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				t.Cleanup(target.Close)
				t.Cleanup(ws.urlSubs.Close)
				var rpcErr *types.RpcError
				ack, rpcErr = ws.urlSubs.Subscribe(ctx, types.SubscriptionRequest{
					URL: target.URL, Streams: []types.SubscriptionType{types.SubServer},
				})
				require.Nil(t, rpcErr)
			}
			require.Equal(t, 1, ws.subscriptionManager.GetSubscriberCount(types.SubServer))
			require.Equal(t, uint32(256), ack["load_factor"])
		})
	}
}

func TestMPTBookSnapshotPreservesIssuanceID(t *testing.T) {
	ledger := &mptSnapshotLedger{mockLedgerService: newMockLedgerService()}
	services := &types.ServiceContainer{Ledger: ledger}
	ws := NewWebSocketServer(WebSocketServerOptions{Services: services})
	const mptID = "00000001C4F149B6F2A4B6A4C4A01C1570C4A040A3D9B221"
	var request types.SubscriptionRequest
	require.NoError(t, json.Unmarshal([]byte(`{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"mpt_issuance_id":"`+mptID+`"},"snapshot":true}]}`), &request))
	ws.buildSubscribeAck(&types.RpcContext{Services: services}, request)
	require.Equal(t, mptID, ledger.gets.MPTIssuanceID)
	require.Equal(t, "XRP", ledger.pays.Currency)
}

func TestWebSocketMPTBookFlow(t *testing.T) {
	const mptID = "00000001C4F149B6F2A4B6A4C4A01C1570C4A040A3D9B221"
	ws := NewWebSocketServer(WebSocketServerOptions{})
	conn := subscription.NewConnection("mpt-ws", make(chan []byte, 1))
	wsConn := &websocketConnection{Connection: conn}
	registration, attached := ws.subscriptionManager.Attach(conn)
	require.True(t, attached)
	wsConn.registration = registration
	t.Cleanup(func() { ws.subscriptionManager.Detach(registration) })
	_, rpcErr := ws.executeSubscribe(wsConn, &types.RpcContext{}, types.WebSocketCommand{Params: json.RawMessage(`{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"mpt_issuance_id":"` + mptID + `"}}]}`)})
	require.Nil(t, rpcErr)
	NewPublisher(ws.SubscriptionManager()).PublishOrderBookChange(publisherTestTransactionEvent(), []types.OrderBookSpec{{
		TakerPays: types.CurrencySpec{Currency: "XRP"},
		TakerGets: types.CurrencySpec{MPTIssuanceID: mptID},
	}})
	select {
	case <-conn.Outbound():
	default:
		t.Fatal("MPT book event was not delivered to the WebSocket subscriber")
	}
}
