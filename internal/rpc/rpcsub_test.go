package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rpcSubSink is a loopback HTTP endpoint standing in for the remote
// JSON-RPC server a url subscription delivers to.
type rpcSubSink struct {
	srv      *httptest.Server
	received chan rpcSubEvent
}

type rpcSubEvent struct {
	Method        string         `json:"method"`
	Params        map[string]any `json:"params"`
	ID            any            `json:"id"`
	authorization string
	userAgent     string
}

func newRPCSubSink(t *testing.T) *rpcSubSink {
	t.Helper()
	sink := &rpcSubSink{received: make(chan rpcSubEvent, 16)}
	sink.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev rpcSubEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("sink: undecodable body: %v", err)
			return
		}
		ev.authorization = r.Header.Get("Authorization")
		ev.userAgent = r.Header.Get("User-Agent")
		sink.received <- ev
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{},"error":null,"id":1}`))
	}))
	t.Cleanup(sink.srv.Close)
	return sink
}

func (s *rpcSubSink) next(t *testing.T) rpcSubEvent {
	t.Helper()
	select {
	case ev := <-s.received:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for url subscription event")
		return rpcSubEvent{}
	}
}

func (s *rpcSubSink) expectNone(t *testing.T) {
	t.Helper()
	select {
	case ev := <-s.received:
		t.Fatalf("unexpected event delivered: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// newRPCSubTestServer builds a WebSocket server whose service container
// carries the url-subscription registry, plus admin/guest contexts for
// driving the plain JSON-RPC handlers.
func newRPCSubTestServer(t *testing.T) (*WebSocketServer, *types.ServiceContainer) {
	t.Helper()
	services := types.NewServiceContainer(nil)
	ws := NewWebSocketServer(time.Second, services)
	require.NotNil(t, services.URLSubscriptions, "NewWebSocketServer must expose the url registry")
	return ws, services
}

func adminCtx(services *types.ServiceContainer) *types.RpcContext {
	return &types.RpcContext{
		Role:       types.RoleAdmin,
		IsAdmin:    true,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}
}

func subscribeURL(t *testing.T, services *types.ServiceContainer, params string) (any, *types.RpcError) {
	t.Helper()
	method := &handlers.SubscribeMethod{}
	return method.Handle(adminCtx(services), json.RawMessage(params))
}

func unsubscribeURL(t *testing.T, services *types.ServiceContainer, params string) (any, *types.RpcError) {
	t.Helper()
	method := &handlers.UnsubscribeMethod{}
	return method.Handle(adminCtx(services), json.RawMessage(params))
}

// TestRPCSub_DeliversEvents covers the core RPCSub loop: an admin url
// subscription receives broadcasts as outbound JSON-RPC "event" calls with
// per-url sequence numbers starting at 1 and basic auth (sent even with
// empty credentials, like rippled's createHTTPPost).
func TestRPCSub_DeliversEvents(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)

	result, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	assert.NotNil(t, result)

	first := map[string]any{"type": "ledgerClosed", "ledger_index": float64(7)}
	data, err := json.Marshal(first)
	require.NoError(t, err)
	ws.GetSubscriptionManager().BroadcastToStream(types.SubLedger, data, nil)

	ev := sink.next(t)
	assert.Equal(t, "event", ev.Method)
	assert.Equal(t, float64(1), ev.ID)
	assert.Equal(t, "ledgerClosed", ev.Params["type"])
	assert.Equal(t, float64(7), ev.Params["ledger_index"])
	assert.Equal(t, float64(1), ev.Params["seq"], "sequence numbers start at 1")
	// base64(":") — empty username and password.
	assert.Equal(t, "Basic Og==", ev.authorization)
	// rippled posts with this fixed User-Agent (createHTTPPost).
	assert.Equal(t, "ripple-json-rpc/v1", ev.userAgent)

	ws.GetSubscriptionManager().BroadcastToStream(types.SubLedger, data, nil)
	assert.Equal(t, float64(2), sink.next(t).Params["seq"], "sequence increments per event")

	// Streams the url is not subscribed to are not delivered.
	ws.GetSubscriptionManager().BroadcastToStream(types.SubValidations, data, nil)
	sink.expectNone(t)
}

// TestRPCSub_DroppedEventLeavesSeqGap proves the seq is stamped at enqueue
// (mirroring rippled's mSeq++ in send): an event dropped by the bounded
// queue still consumes a number, so the events that do land carry a visible
// gap rather than a silently gapless sequence. Exercises the TrySend
// chokepoint directly with a one-slot, undrained channel so the drop is
// deterministic.
func TestRPCSub_DroppedEventLeavesSeqGap(t *testing.T) {
	sub := &rpcSub{}
	conn := &types.Connection{
		SendChannel:    make(chan []byte, 1),
		EncodeOutbound: sub.encodeOutbound,
	}

	data, _ := json.Marshal(map[string]any{"type": "ledgerClosed"})

	require.True(t, conn.TrySend(data), "first event fits the queue (seq 1)")
	require.False(t, conn.TrySend(data), "queue full → event dropped (seq 2 consumed)")
	require.False(t, conn.TrySend(data), "still full → dropped (seq 3 consumed)")

	// Drain the one landed event: it carries seq 1.
	landed := decodeRPCSubEnvelope(t, <-conn.SendChannel)
	assert.Equal(t, float64(1), landed["seq"])

	// The next event that fits now carries seq 4 — the gap (2,3) is visible.
	require.True(t, conn.TrySend(data))
	next := decodeRPCSubEnvelope(t, <-conn.SendChannel)
	assert.Equal(t, float64(4), next["seq"], "dropped events leave a visible gap")
}

func decodeRPCSubEnvelope(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env struct {
		Params map[string]any `json:"params"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	return env.Params
}

// TestRPCSub_BasicAuthCredentials checks url_username/url_password are sent
// as basic auth, and that on reuse only the deprecated username/password
// members update credentials (doSubscribe's reuse branch ignores
// url_username/url_password).
func TestRPCSub_BasicAuthCredentials(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	urlParam := `"url":"` + sink.srv.URL + `"`

	_, rpcErr := subscribeURL(t, services,
		`{`+urlParam+`,"url_username":"alice","url_password":"secret","streams":["ledger"]}`)
	require.Nil(t, rpcErr)

	data, _ := json.Marshal(map[string]any{"type": "ledgerClosed"})
	ws.GetSubscriptionManager().BroadcastToStream(types.SubLedger, data, nil)
	// base64("alice:secret")
	assert.Equal(t, "Basic YWxpY2U6c2VjcmV0", sink.next(t).authorization)

	// url_username on an existing subscription is ignored.
	_, rpcErr = subscribeURL(t, services, `{`+urlParam+`,"url_username":"mallory"}`)
	require.Nil(t, rpcErr)
	ws.GetSubscriptionManager().BroadcastToStream(types.SubLedger, data, nil)
	assert.Equal(t, "Basic YWxpY2U6c2VjcmV0", sink.next(t).authorization)

	// The deprecated username/password members do update credentials.
	_, rpcErr = subscribeURL(t, services, `{`+urlParam+`,"username":"bob","password":"hunter2"}`)
	require.Nil(t, rpcErr)
	ws.GetSubscriptionManager().BroadcastToStream(types.SubLedger, data, nil)
	// base64("bob:hunter2")
	assert.Equal(t, "Basic Ym9iOmh1bnRlcjI=", sink.next(t).authorization)
}

// TestRPCSub_URLValidation mirrors RPCSub's constructor errors, surfaced as
// rpcINVALID_PARAMS with rippled's exact messages.
func TestRPCSub_URLValidation(t *testing.T) {
	tests := []struct {
		name    string
		params  string
		message string
	}{
		{"unsupported scheme", `{"url":"ftp://example.com/events"}`, "Only http and https is supported."},
		{"empty url member", `{"url":""}`, "Failed to parse url."},
		{"port out of range", `{"url":"http://example.com:99999/x"}`, "Failed to parse url."},
		{"not a url", `{"url":"::not a url::"}`, "Failed to parse url."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, services := newRPCSubTestServer(t)
			result, rpcErr := subscribeURL(t, services, tc.params)
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Equal(t, tc.message, rpcErr.Message)
		})
	}
}

// TestRPCSub_EmptyHostAcceptedAtSubscribe mirrors rippled's parseUrl host
// group matching the empty string: "http://" registers successfully and a
// delivery only fails (harmlessly) at connect time, fire-and-forget.
func TestRPCSub_EmptyHostAcceptedAtSubscribe(t *testing.T) {
	ws, services := newRPCSubTestServer(t)

	result, rpcErr := subscribeURL(t, services, `{"url":"http://","streams":["ledger"]}`)
	require.Nil(t, rpcErr, "empty-host url must register, like rippled")
	assert.NotNil(t, result)
	assert.Equal(t, 1, ws.GetSubscriptionManager().ConnectionCount())

	// A broadcast to the unconnectable endpoint must not panic or block —
	// the delivery goroutine logs and drops it.
	data, _ := json.Marshal(map[string]any{"type": "ledgerClosed"})
	ws.GetSubscriptionManager().BroadcastToStream(types.SubLedger, data, nil)
}

// TestRPCSub_UnsubscribeRemovesEntry verifies the tryRemoveRpcSub
// semantics: the registry entry is dropped once no stream subscriptions
// remain, and an unknown url unsubscribes as silent success.
func TestRPCSub_UnsubscribeRemovesEntry(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	urlParam := `"url":"` + sink.srv.URL + `"`

	_, rpcErr := subscribeURL(t, services, `{`+urlParam+`,"streams":["ledger","transactions"]}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, 1, ws.GetSubscriptionManager().ConnectionCount())

	// A stream remains subscribed → entry kept.
	_, rpcErr = unsubscribeURL(t, services, `{`+urlParam+`,"streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, 1, ws.GetSubscriptionManager().ConnectionCount())
	ws.urlSubs.mu.Lock()
	assert.Len(t, ws.urlSubs.subs, 1)
	ws.urlSubs.mu.Unlock()

	// Last stream gone → entry and manager connection removed.
	_, rpcErr = unsubscribeURL(t, services, `{`+urlParam+`,"streams":["transactions"]}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, 0, ws.GetSubscriptionManager().ConnectionCount())
	ws.urlSubs.mu.Lock()
	assert.Empty(t, ws.urlSubs.subs)
	ws.urlSubs.mu.Unlock()

	// Unknown url is silent success (Unsubscribe.cpp:52-53).
	result, rpcErr := unsubscribeURL(t, services, `{"url":"http://example.com/none","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, map[string]any{}, result)
}

// TestRPCSub_AccountsDontBlockRemoval mirrors NetworkOPs::tryRemoveRpcSub
// only scanning the stream maps: account subscriptions alone don't keep the
// registry entry alive — like rippled, where dropping the registry's strong
// reference destroys the subscriber, account subscriptions and all.
func TestRPCSub_AccountsDontBlockRemoval(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	urlParam := `"url":"` + sink.srv.URL + `"`

	_, rpcErr := subscribeURL(t, services,
		`{`+urlParam+`,"streams":["ledger"],"accounts":["rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"]}`)
	require.Nil(t, rpcErr)

	_, rpcErr = unsubscribeURL(t, services, `{`+urlParam+`,"streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, 0, ws.GetSubscriptionManager().ConnectionCount(),
		"entry must be removed when only account subscriptions remain")
}

// TestRPCSub_SubscribeAckCarriesLedgerInfo verifies the url path returns
// the same subscribe ack the WebSocket path builds, including rippled's
// field gating: network_id is always present (even 0) and fee_ref appears
// only while XRPFees is disabled.
func TestRPCSub_SubscribeAckCarriesLedgerInfo(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	ws.SetLedgerInfoProvider(stubLedgerInfoProvider{})
	sink := newRPCSubSink(t)

	result, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	ack, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, uint32(42), ack["ledger_index"])
	assert.Equal(t, "ABCD", ack["ledger_hash"])
	assert.Equal(t, uint64(10), ack["fee_base"])
	// network_id is emitted unconditionally, even when zero.
	require.Contains(t, ack, "network_id")
	assert.Equal(t, uint32(0), ack["network_id"])
	// XRPFees disabled → deprecated fee_ref present.
	assert.Equal(t, uint64(10), ack["fee_ref"])
}

// TestRPCSub_SubscribeAckOmitsFeeRefUnderXRPFees verifies fee_ref is dropped
// from the ack once the XRPFees amendment is enabled, mirroring rippled's
// subLedger gate.
func TestRPCSub_SubscribeAckOmitsFeeRefUnderXRPFees(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	ws.SetLedgerInfoProvider(stubLedgerInfoProvider{xrpFees: true})
	sink := newRPCSubSink(t)

	result, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	ack, ok := result.(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, ack, "fee_ref", "fee_ref must be omitted while XRPFees is enabled")
	require.Contains(t, ack, "network_id")
}

type stubLedgerInfoProvider struct{ xrpFees bool }

func (s stubLedgerInfoProvider) GetCurrentLedgerInfo() *types.LedgerSubscribeInfo {
	return &types.LedgerSubscribeInfo{
		LedgerIndex:    42,
		LedgerHash:     "ABCD",
		LedgerTime:     735000000,
		FeeBase:        10,
		FeeRef:         10,
		ReserveBase:    10000000,
		ReserveInc:     2000000,
		XRPFeesEnabled: s.xrpFees,
	}
}

// TestRPCSub_ReuseSharesSubscriber verifies the find-or-create semantics:
// subscribing the same url twice extends one subscriber instead of creating
// a second, so events are not duplicated.
func TestRPCSub_ReuseSharesSubscriber(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	urlParam := `"url":"` + sink.srv.URL + `"`

	_, rpcErr := subscribeURL(t, services, `{`+urlParam+`,"streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	_, rpcErr = subscribeURL(t, services, `{`+urlParam+`,"streams":["validations"]}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, 1, ws.GetSubscriptionManager().ConnectionCount())

	data, _ := json.Marshal(map[string]any{"type": "ledgerClosed"})
	ws.GetSubscriptionManager().BroadcastToStream(types.SubLedger, data, nil)
	assert.Equal(t, float64(1), sink.next(t).Params["seq"])
	sink.expectNone(t)
}

// TestRPCSub_MalformedStreamKeepsEntry mirrors doSubscribe creating the
// registry entry before parsing streams: a bad stream name errors but the
// url remains registered for reuse.
func TestRPCSub_MalformedStreamKeepsEntry(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)

	_, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["nonsense"]}`)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcSTREAM_MALFORMED, rpcErr.Code)
	assert.Equal(t, 1, ws.GetSubscriptionManager().ConnectionCount(),
		"failed stream parse leaves the freshly created url entry, like rippled")
}

// TestRPCSub_CloseStopsDelivery verifies registry shutdown through
// WebSocketServer.Close tears down url subscriptions.
func TestRPCSub_CloseStopsDelivery(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)

	_, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)

	require.NoError(t, ws.Close(t.Context()))
	assert.Equal(t, 0, ws.GetSubscriptionManager().ConnectionCount())

	data, _ := json.Marshal(map[string]any{"type": "ledgerClosed"})
	ws.GetSubscriptionManager().BroadcastToStream(types.SubLedger, data, nil)
	sink.expectNone(t)
}
