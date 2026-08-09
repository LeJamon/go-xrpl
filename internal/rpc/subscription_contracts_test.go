package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	historyAccountA = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	historyAccountB = "rPEPPER7kfTD9w2To4CQk6UCfuHM9c6GDY"
)

type historySubscriptionProvider struct {
	mu            sync.Mutex
	subscriptions map[types.AccountHistorySubscriptionSink]map[string]bool
	removed       map[types.AccountHistorySubscriptionSink]bool
	subscribeErr  *types.RpcError
	streamContext context.Context
}

func newHistorySubscriptionProvider() *historySubscriptionProvider {
	return &historySubscriptionProvider{
		subscriptions: make(map[types.AccountHistorySubscriptionSink]map[string]bool),
		removed:       make(map[types.AccountHistorySubscriptionSink]bool),
	}
}

func (p *historySubscriptionProvider) ValidateSubscribe(conn types.AccountHistorySubscriptionSink, account string) *types.RpcError {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.subscribeErr != nil {
		return p.subscribeErr
	}
	if _, exists := p.subscriptions[conn][account]; exists {
		return types.RpcErrorInvalidParams("Invalid parameters.")
	}
	return nil
}

func (p *historySubscriptionProvider) Subscribe(conn types.AccountHistorySubscriptionSink, account string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streamContext = conn.Context()
	accounts := p.subscriptions[conn]
	if accounts == nil {
		accounts = make(map[string]bool)
		p.subscriptions[conn] = accounts
	}
	accounts[account] = true
}

func (p *historySubscriptionProvider) Unsubscribe(conn types.AccountHistorySubscriptionSink, account string, historyOnly bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	accounts := p.subscriptions[conn]
	if historyOnly {
		if _, exists := accounts[account]; exists {
			accounts[account] = false
		}
		return
	}
	delete(accounts, account)
	if len(accounts) == 0 {
		delete(p.subscriptions, conn)
	}
}

func (p *historySubscriptionProvider) RemoveConnection(conn types.AccountHistorySubscriptionSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.subscriptions, conn)
	p.removed[conn] = true
}

func (p *historySubscriptionProvider) HasSubscriptions(conn types.AccountHistorySubscriptionSink) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.subscriptions[conn]) != 0
}

func (p *historySubscriptionProvider) state(conn types.AccountHistorySubscriptionSink, account string) (present, replaying bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	replaying, present = p.subscriptions[conn][account]
	return present, replaying
}

func (p *historySubscriptionProvider) wasRemoved(conn types.AccountHistorySubscriptionSink) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.removed[conn]
}

func (p *historySubscriptionProvider) streamDone() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streamContext == nil {
		return true
	}
	select {
	case <-p.streamContext.Done():
		return true
	default:
		return false
	}
}

func TestSubscriptionRequestPreservesMissingWireFields(t *testing.T) {
	var request types.SubscriptionRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"rt_accounts":null,
		"account_history_tx_stream":{"account":"`+historyAccountA+`","stop_history_tx_only":false}
	}`), &request))

	wire := request.WireArrays()
	require.True(t, wire.Present)
	assert.JSONEq(t, `null`, string(wire.RTAccounts))
	assert.JSONEq(t, `{"account":"`+historyAccountA+`","stop_history_tx_only":false}`, string(wire.AccountHistory))
	require.NotNil(t, request.AccountHistory)
	assert.Equal(t, historyAccountA, request.AccountHistory.Account)
}

func TestRTAccountsAliasAndCanonicalPrecedence(t *testing.T) {
	manager := subscription.NewManager()

	t.Run("deprecated alias subscribes proposed accounts", func(t *testing.T) {
		conn := subscription.NewConnection("rt-alias", make(chan []byte, 1))
		var request types.SubscriptionRequest
		require.NoError(t, json.Unmarshal([]byte(`{"rt_accounts":["`+historyAccountA+`"]}`), &request))
		require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, conn), request, false))
		registration := testRegistration(t, manager, conn)
		assert.Equal(t, []string{historyAccountA}, registration.Snapshot().Accounts(types.SubAccountsProposed))

		var unsubscribe types.SubscriptionRequest
		require.NoError(t, json.Unmarshal([]byte(`{"rt_accounts":["`+historyAccountA+`"]}`), &unsubscribe))
		require.Nil(t, manager.HandleUnsubscribe(testRegistration(t, manager, conn), unsubscribe))
		assert.False(t, registration.Snapshot().Has(types.SubAccountsProposed))
	})

	t.Run("canonical member wins even when alias is valid", func(t *testing.T) {
		conn := subscription.NewConnection("canonical", make(chan []byte, 1))
		var request types.SubscriptionRequest
		require.NoError(t, json.Unmarshal([]byte(`{
			"accounts_proposed":["`+historyAccountB+`"],
			"rt_accounts":["`+historyAccountA+`"]
		}`), &request))
		require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, conn), request, true))
		assert.Equal(t, []string{historyAccountB}, testRegistration(t, manager, conn).Snapshot().Accounts(types.SubAccountsProposed))
	})

	t.Run("present malformed canonical member does not fall back", func(t *testing.T) {
		conn := subscription.NewConnection("canonical-malformed", make(chan []byte, 1))
		var request types.SubscriptionRequest
		require.NoError(t, json.Unmarshal([]byte(`{
			"accounts_proposed":[],
			"rt_accounts":["`+historyAccountA+`"]
		}`), &request))
		rpcErr := manager.HandleSubscribe(testRegistration(t, manager, conn), request, false)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "actMalformed", rpcErr.ErrorString)
		assert.False(t, testRegistration(t, manager, conn).Snapshot().Has(types.SubAccountsProposed))
	})
}

func TestAccountHistorySubscribeCapabilityAndValidation(t *testing.T) {
	t.Run("transaction table gate precedes nested validation", func(t *testing.T) {
		ws, conn, ctx := newSpecialDispatchHarness(t)
		ctx.Services.Ledger = &txTablesOffLedger{newMockLedgerService()}
		ctx.Services.AccountHistorySubscriptions = newHistorySubscriptionProvider()
		ctx.LoadCost = uint32(resource.FeeReferenceRPC().Cost())

		_, rpcErr := ws.executeSubscribe(conn, ctx, types.WebSocketCommand{
			Params: json.RawMessage(`{"streams":["ledger"],"account_history_tx_stream":false}`),
		})
		require.NotNil(t, rpcErr)
		assert.Equal(t, "notEnabled", rpcErr.ErrorString)
		assert.Equal(t, uint32(resource.FeeReferenceRPC().Cost()), ctx.LoadCost)
		assert.True(t, conn.registration.Snapshot().Has(types.SubLedger))
	})

	t.Run("missing replay provider fails closed", func(t *testing.T) {
		ws, conn, ctx := newSpecialDispatchHarness(t)
		ctx.Services.Ledger = newMockLedgerService()
		_, rpcErr := ws.executeSubscribe(conn, ctx, types.WebSocketCommand{
			Params: json.RawMessage(`{"account_history_tx_stream":{"account":"` + historyAccountA + `"}}`),
		})
		require.NotNil(t, rpcErr)
		assert.Equal(t, "notEnabled", rpcErr.ErrorString)
	})

	t.Run("success sets medium load warning and rejects duplicates", func(t *testing.T) {
		ws, conn, ctx := newSpecialDispatchHarness(t)
		provider := newHistorySubscriptionProvider()
		ctx.Services.Ledger = newMockLedgerService()
		ctx.Services.AccountHistorySubscriptions = provider
		ctx.LoadCost = uint32(resource.FeeReferenceRPC().Cost())
		command := types.WebSocketCommand{
			Params: json.RawMessage(`{"account_history_tx_stream":{"account":"` + historyAccountA + `"}}`),
		}

		result, rpcErr := ws.executeSubscribe(conn, ctx, command)
		require.Nil(t, rpcErr)
		assert.Equal(t, accountHistoryWarning, result.(map[string]any)["warning"])
		assert.Equal(t, uint32(resource.FeeMediumBurdenRPC().Cost()), ctx.LoadCost)
		present, replaying := provider.state(conn.Connection, historyAccountA)
		assert.True(t, present)
		assert.True(t, replaying)

		_, rpcErr = ws.executeSubscribe(conn, ctx, command)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("bad account is invalidParams after medium charge", func(t *testing.T) {
		ws, conn, ctx := newSpecialDispatchHarness(t)
		ctx.Services.Ledger = newMockLedgerService()
		ctx.Services.AccountHistorySubscriptions = newHistorySubscriptionProvider()
		ctx.LoadCost = uint32(resource.FeeReferenceRPC().Cost())

		_, rpcErr := ws.executeSubscribe(conn, ctx, types.WebSocketCommand{
			Params: json.RawMessage(`{"account_history_tx_stream":{"account":"not-an-account"}}`),
		})
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
		assert.Equal(t, uint32(resource.FeeMediumBurdenRPC().Cost()), ctx.LoadCost)
	})
}

func TestAccountHistoryUnsubscribeModesAndCleanup(t *testing.T) {
	ws, conn, ctx := newSpecialDispatchHarness(t)
	provider := newHistorySubscriptionProvider()
	ctx.Services.Ledger = newMockLedgerService()
	ctx.Services.AccountHistorySubscriptions = provider

	subscribe := types.WebSocketCommand{
		Params: json.RawMessage(`{"account_history_tx_stream":{"account":"` + historyAccountA + `"}}`),
	}
	_, rpcErr := ws.executeSubscribe(conn, ctx, subscribe)
	require.Nil(t, rpcErr)

	_, rpcErr = ws.executeUnsubscribe(conn, ctx, types.WebSocketCommand{
		Params: json.RawMessage(`{"account_history_tx_stream":{"account":"` + historyAccountA + `","stop_history_tx_only":true}}`),
	})
	require.Nil(t, rpcErr)
	present, replaying := provider.state(conn.Connection, historyAccountA)
	assert.True(t, present)
	assert.False(t, replaying)

	_, rpcErr = ws.executeUnsubscribe(conn, ctx, types.WebSocketCommand{
		Params: json.RawMessage(`{"account_history_tx_stream":{"account":"` + historyAccountA + `","stop_history_tx_only":"true"}}`),
	})
	require.NotNil(t, rpcErr)
	assert.Equal(t, "invalidParams", rpcErr.ErrorString)

	_, rpcErr = ws.executeUnsubscribe(conn, ctx, types.WebSocketCommand{
		Params: json.RawMessage(`{"account_history_tx_stream":{"account":"` + historyAccountA + `","stop_history_tx_only":null}}`),
	})
	require.NotNil(t, rpcErr)
	assert.Equal(t, "invalidParams", rpcErr.ErrorString)

	_, rpcErr = ws.executeUnsubscribe(conn, ctx, types.WebSocketCommand{
		Params: json.RawMessage(`{"account_history_tx_stream":{"account":"` + historyAccountA + `"}}`),
	})
	require.Nil(t, rpcErr)
	present, _ = provider.state(conn.Connection, historyAccountA)
	assert.False(t, present)

	_, rpcErr = ws.executeSubscribe(conn, ctx, subscribe)
	require.Nil(t, rpcErr)
	ws.detachConnection(conn)
	assert.False(t, provider.HasSubscriptions(conn.Connection))
	assert.True(t, provider.wasRemoved(conn.Connection))
}

func TestAccountHistoryUnsubscribeWithoutProviderIsNoOp(t *testing.T) {
	ws, conn, ctx := newSpecialDispatchHarness(t)
	ctx.Services.Ledger = &txTablesOffLedger{newMockLedgerService()}

	_, rpcErr := ws.executeUnsubscribe(conn, ctx, types.WebSocketCommand{
		Params: json.RawMessage(`{"account_history_tx_stream":{"account":"not-an-account"}}`),
	})
	require.NotNil(t, rpcErr)
	assert.Equal(t, "invalidParams", rpcErr.ErrorString)

	result, rpcErr := ws.executeUnsubscribe(conn, ctx, types.WebSocketCommand{
		Params: json.RawMessage(`{"account_history_tx_stream":{"account":"` + historyAccountA + `"}}`),
	})
	require.Nil(t, rpcErr)
	assert.Empty(t, result.(map[string]any))
}

func TestAccountHistoryURLSubscriptionRetention(t *testing.T) {
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer sink.Close()

	provider := newHistorySubscriptionProvider()
	services := &types.ServiceContainer{
		Ledger:                      newMockLedgerService(),
		AccountHistorySubscriptions: provider,
	}
	ws := NewWebSocketServer(WebSocketServerOptions{Services: services})
	defer ws.urlSubs.Close()
	requestContext, cancelRequest := context.WithCancel(context.Background())
	ctx := &types.RpcContext{
		Context:    requestContext,
		Role:       types.RoleAdmin,
		ApiVersion: types.DefaultApiVersion,
		Services:   services,
	}
	request := types.SubscriptionRequest{
		URL:            sink.URL,
		AccountHistory: &types.AccountHistorySubscriptionRequest{Account: historyAccountA},
	}

	result, rpcErr := ws.urlSubs.Subscribe(ctx, request)
	require.Nil(t, rpcErr)
	cancelRequest()
	assert.False(t, provider.streamDone())
	assert.Equal(t, accountHistoryWarning, result["warning"])
	require.Len(t, ws.urlSubs.subs, 1)
	var conn *subscription.Connection
	for _, sub := range ws.urlSubs.subs {
		conn = sub.conn
	}
	require.NotNil(t, conn)

	services.Ledger = &txTablesOffLedger{newMockLedgerService()}
	request.AccountHistory.StopHistoryTxOnly = true
	_, rpcErr = ws.urlSubs.Unsubscribe(ctx, request)
	require.Nil(t, rpcErr)
	assert.Len(t, ws.urlSubs.subs, 1)
	present, replaying := provider.state(conn, historyAccountA)
	assert.True(t, present)
	assert.False(t, replaying)

	request.AccountHistory.StopHistoryTxOnly = false
	_, rpcErr = ws.urlSubs.Unsubscribe(ctx, request)
	require.Nil(t, rpcErr)
	assert.Empty(t, ws.urlSubs.subs)
	assert.False(t, provider.HasSubscriptions(conn))
	assert.True(t, provider.wasRemoved(conn))
}

func TestAccountHistoryURLValidationIsOrderedAndAtomic(t *testing.T) {
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer sink.Close()
	provider := newHistorySubscriptionProvider()
	services := &types.ServiceContainer{
		Ledger:                      newMockLedgerService(),
		AccountHistorySubscriptions: provider,
	}
	ws := NewWebSocketServer(WebSocketServerOptions{Services: services})
	defer ws.urlSubs.Close()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.DefaultApiVersion,
		Services:   services,
	}

	_, rpcErr := ws.urlSubs.Subscribe(ctx, types.SubscriptionRequest{
		URL:     sink.URL,
		Streams: []types.SubscriptionType{types.SubTransactions},
	})
	require.Nil(t, rpcErr)
	var conn *subscription.Connection
	var registration *subscription.Registration
	for _, sub := range ws.urlSubs.subs {
		conn = sub.conn
		registration = sub.registration
	}
	require.NotNil(t, conn)

	provider.subscribeErr = types.RpcErrorInternal()
	_, rpcErr = ws.urlSubs.Subscribe(ctx, types.SubscriptionRequest{
		URL:            sink.URL,
		Streams:        []types.SubscriptionType{types.SubLedger},
		AccountHistory: &types.AccountHistorySubscriptionRequest{Account: historyAccountA},
	})
	require.NotNil(t, rpcErr)
	assert.True(t, registration.Snapshot().Has(types.SubTransactions))
	assert.True(t, registration.Snapshot().Has(types.SubLedger))

	provider.subscribeErr = nil
	ctx.LoadCost = uint32(resource.FeeReferenceRPC().Cost())
	var malformed types.SubscriptionRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"url":"`+sink.URL+`",
		"streams":["not_a_stream"],
		"account_history_tx_stream":false
	}`), &malformed))
	_, rpcErr = ws.urlSubs.Subscribe(ctx, malformed)
	require.NotNil(t, rpcErr)
	assert.Equal(t, "malformedStream", rpcErr.ErrorString)
	assert.Equal(t, uint32(resource.FeeReferenceRPC().Cost()), ctx.LoadCost)

	_, rpcErr = ws.urlSubs.Subscribe(ctx, types.SubscriptionRequest{
		URL:            sink.URL,
		Streams:        []types.SubscriptionType{types.SubLedger},
		AccountHistory: &types.AccountHistorySubscriptionRequest{Account: historyAccountA},
	})
	require.Nil(t, rpcErr)
	var invalidUnsubscribe types.SubscriptionRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"url":"`+sink.URL+`",
		"streams":["ledger"],
		"account_history_tx_stream":{"account":"`+historyAccountA+`","stop_history_tx_only":null}
	}`), &invalidUnsubscribe))
	_, rpcErr = ws.urlSubs.Unsubscribe(ctx, invalidUnsubscribe)
	require.NotNil(t, rpcErr)
	assert.False(t, registration.Snapshot().Has(types.SubLedger))
	present, _ := provider.state(conn, historyAccountA)
	assert.True(t, present)
}

func TestAccountHistoryHTTPURLMethods(t *testing.T) {
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer sink.Close()
	provider := newHistorySubscriptionProvider()
	services := &types.ServiceContainer{
		Ledger:                      newMockLedgerService(),
		AccountHistorySubscriptions: provider,
	}
	ws := NewWebSocketServer(WebSocketServerOptions{Services: services})
	defer ws.urlSubs.Close()
	services.URLSubscriptions = ws.urlSubs
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.DefaultApiVersion,
		Services:   services,
	}
	params := json.RawMessage(`{
		"url":"` + sink.URL + `",
		"account_history_tx_stream":{"account":"` + historyAccountA + `"}
	}`)

	result, rpcErr := (&handlers.SubscribeMethod{}).Handle(ctx, params)
	require.Nil(t, rpcErr)
	assert.Equal(t, accountHistoryWarning, result.(map[string]any)["warning"])
	require.Len(t, ws.urlSubs.subs, 1)
	var conn *subscription.Connection
	for _, sub := range ws.urlSubs.subs {
		conn = sub.conn
	}
	present, replaying := provider.state(conn, historyAccountA)
	assert.True(t, present)
	assert.True(t, replaying)

	result, rpcErr = (&handlers.UnsubscribeMethod{}).Handle(ctx, params)
	require.Nil(t, rpcErr)
	assert.Empty(t, result.(map[string]any))
	assert.Empty(t, ws.urlSubs.subs)
}
