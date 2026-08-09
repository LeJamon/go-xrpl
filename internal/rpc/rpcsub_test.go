package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
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

type blockingRPCSubTransport struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (t *blockingRPCSubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.once.Do(func() { close(t.started) })
	<-t.release
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    req,
	}, nil
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

type rpcSubTestServices struct {
	graph *types.ServiceGraph
	url   types.URLSubscriptionService
}

func newRPCSubTestServer(t *testing.T) (*WebSocketServer, *rpcSubTestServices) {
	return newRPCSubTestServerWithProvider(t, nil)
}

func newRPCSubTestServerWithProvider(t *testing.T, provider types.LedgerInfoProvider) (*WebSocketServer, *rpcSubTestServices) {
	t.Helper()
	graph := types.NewTestServiceGraph(types.NewServiceContainer(nil))
	ws := NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second, Services: graph, LedgerInfoProvider: provider})
	url := ws.URLSubscriptionService()
	require.NotNil(t, url, "composition must expose the url registry explicitly")
	return ws, &rpcSubTestServices{graph: graph, url: url}
}

func adminCtx(services *rpcSubTestServices) *types.RpcContext {
	return &types.RpcContext{
		Role:             types.RoleAdmin,
		ApiVersion:       types.ApiVersion1,
		Services:         services.graph,
		URLSubscriptions: services.url,
	}
}

func subscribeURL(t *testing.T, services *rpcSubTestServices, params string) (any, *rpcerrors.RpcError) {
	t.Helper()
	method := &handlers.SubscribeMethod{}
	return method.Handle(adminCtx(services), json.RawMessage(params))
}

func unsubscribeURL(t *testing.T, services *rpcSubTestServices, params string) (any, *rpcerrors.RpcError) {
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
	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)

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

	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)
	assert.Equal(t, float64(2), sink.next(t).Params["seq"], "sequence increments per event")

	// Streams the url is not subscribed to are not delivered.
	ws.SubscriptionManager().BroadcastToStream(types.SubValidations, data)
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
	conn := subscription.NewConnection("sequence-gap", make(chan []byte, 1))
	conn.SetEncodeOutbound(sub.encodeOutbound)

	data, _ := json.Marshal(map[string]any{"type": "ledgerClosed"})

	require.True(t, conn.TrySend(data), "first event fits the queue (seq 1)")
	require.False(t, conn.TrySend(data), "queue full → event dropped (seq 2 consumed)")
	require.False(t, conn.TrySend(data), "still full → dropped (seq 3 consumed)")

	// Drain the one landed event: it carries seq 1.
	landed := decodeRPCSubEnvelope(t, <-conn.Outbound())
	assert.Equal(t, float64(1), landed["seq"])

	// The next event that fits now carries seq 4 — the gap (2,3) is visible.
	require.True(t, conn.TrySend(data))
	next := decodeRPCSubEnvelope(t, <-conn.Outbound())
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
	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)
	// base64("alice:secret")
	assert.Equal(t, "Basic YWxpY2U6c2VjcmV0", sink.next(t).authorization)

	// url_username on an existing subscription is ignored.
	_, rpcErr = subscribeURL(t, services, `{`+urlParam+`,"url_username":"mallory"}`)
	require.Nil(t, rpcErr)
	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)
	assert.Equal(t, "Basic YWxpY2U6c2VjcmV0", sink.next(t).authorization)

	// The deprecated username/password members do update credentials.
	_, rpcErr = subscribeURL(t, services, `{`+urlParam+`,"username":"bob","password":"hunter2"}`)
	require.Nil(t, rpcErr)
	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)
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
		{"empty host", `{"url":"http://"}`, "Failed to parse url."},
		{"embedded credentials", `{"url":"http://alice:secret@example.com/events"}`, "Failed to parse url."},
		{"fragment", `{"url":"http://example.com/events#fragment"}`, "Failed to parse url."},
		{"outer whitespace", `{"url":" http://example.com/events"}`, "Failed to parse url."},
		{"port out of range", `{"url":"http://example.com:99999/x"}`, "Failed to parse url."},
		{"not a url", `{"url":"::not a url::"}`, "Failed to parse url."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, services := newRPCSubTestServer(t)
			result, rpcErr := subscribeURL(t, services, tc.params)
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Equal(t, tc.message, rpcErr.Message)
		})
	}
}

func TestRPCSubBooksApplyIncrementally(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	ctx := adminCtx(services)
	method := &handlers.SubscribeMethod{}
	params := `{"url":"` + sink.srv.URL + `","books":[` +
		`{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"USD","issuer":"` + account + `"},"snapshot":true},` +
		`{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"XRP"}}]}`

	_, rpcErr := method.Handle(ctx, json.RawMessage(params))
	require.NotNil(t, rpcErr)
	assert.Equal(t, uint32(resource.FeeMediumBurdenRPC().Cost()), ctx.LoadCost, "the accepted earlier snapshot is charged before a later book fails")

	ws.urlSubs.mu.Lock()
	sub := ws.urlSubs.subs[sink.srv.URL]
	ws.urlSubs.mu.Unlock()
	require.NotNil(t, sub, "the URL subscriber remains after a later book fails")
	require.Equal(t, 1, sub.registration.Snapshot().BookCount())
	assert.Equal(t, uint64(1), ws.SubscriptionManager().Metrics().Connections)
}

func TestRPCSubBookUnsubscribeAppliesIncrementally(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	urlParam := `"url":"` + sink.srv.URL + `"`
	first := `{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"USD","issuer":"` + account + `"}}`
	second := `{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"EUR","issuer":"` + account + `"}}`

	_, rpcErr := subscribeURL(t, services, `{`+urlParam+`,"books":[`+first+`,`+second+`]}`)
	require.Nil(t, rpcErr)
	_, rpcErr = unsubscribeURL(t, services, `{`+urlParam+`,"books":[`+first+`,{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"XRP"}}]}`)
	require.NotNil(t, rpcErr)

	ws.urlSubs.mu.Lock()
	sub := ws.urlSubs.subs[sink.srv.URL]
	ws.urlSubs.mu.Unlock()
	require.NotNil(t, sub)
	require.Equal(t, 1, sub.registration.Snapshot().BookCount(), "an earlier book removal remains applied when a later book fails")
}

func TestRPCSubMPTBookFlow(t *testing.T) {
	const mptID = "00000001C4F149B6F2A4B6A4C4A01C1570C4A040A3D9B221"
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	_, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"mpt_issuance_id":"`+mptID+`"}}]}`)
	require.Nil(t, rpcErr)
	NewPublisher(ws.SubscriptionManager()).PublishOrderBookChange(publisherTestTransactionEvent(), []types.OrderBookSpec{{
		TakerPays: types.CurrencySpec{Currency: "XRP"},
		TakerGets: types.CurrencySpec{MPTIssuanceID: strings.ToLower(mptID)},
	}})
	event := sink.next(t)
	require.Equal(t, "event", event.Method)
}

func TestRPCSubTypedBookFlow(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	request := types.SubscriptionRequest{
		URL: sink.srv.URL,
		Books: []types.BookRequest{{
			TakerPays: json.RawMessage(`{"currency":"XRP"}`),
			TakerGets: json.RawMessage(`{"currency":"USD","issuer":"` + account + `"}`),
		}},
	}

	_, rpcErr := ws.urlSubs.Subscribe(adminCtx(services), request)
	require.Nil(t, rpcErr)
	NewPublisher(ws.SubscriptionManager()).PublishOrderBookChange(publisherTestTransactionEvent(), []types.OrderBookSpec{{
		TakerPays: types.CurrencySpec{Currency: "XRP"},
		TakerGets: types.CurrencySpec{Currency: "USD", Issuer: account},
	}})
	event := sink.next(t)
	require.Equal(t, "event", event.Method)

	_, rpcErr = ws.urlSubs.Unsubscribe(adminCtx(services), request)
	require.Nil(t, rpcErr)
	ws.urlSubs.mu.Lock()
	require.Empty(t, ws.urlSubs.subs)
	ws.urlSubs.mu.Unlock()
}

func TestRPCSub_EmptyHostRejectedAtSubscribe(t *testing.T) {
	ws, services := newRPCSubTestServer(t)

	result, rpcErr := subscribeURL(t, services, `{"url":"http://","streams":["ledger"]}`)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Zero(t, ws.SubscriptionManager().Metrics().Connections)
}

// TestRPCSub_UnsubscribeRemovesEntry verifies the tryRemoveRPCSub
// semantics: the registry entry is dropped once no stream subscriptions
// remain, and an unknown url unsubscribes as silent success.
func TestRPCSub_UnsubscribeRemovesEntry(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	urlParam := `"url":"` + sink.srv.URL + `"`

	_, rpcErr := subscribeURL(t, services, `{`+urlParam+`,"streams":["ledger","transactions"]}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, uint64(1), ws.SubscriptionManager().Metrics().Connections)

	// A stream remains subscribed → entry kept.
	_, rpcErr = unsubscribeURL(t, services, `{`+urlParam+`,"streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, uint64(1), ws.SubscriptionManager().Metrics().Connections)
	ws.urlSubs.mu.Lock()
	assert.Len(t, ws.urlSubs.subs, 1)
	ws.urlSubs.mu.Unlock()

	// Last stream gone → entry and manager connection removed.
	_, rpcErr = unsubscribeURL(t, services, `{`+urlParam+`,"streams":["transactions"]}`)
	require.Nil(t, rpcErr)
	assert.Zero(t, ws.SubscriptionManager().Metrics().Connections)
	ws.urlSubs.mu.Lock()
	assert.Empty(t, ws.urlSubs.subs)
	ws.urlSubs.mu.Unlock()

	// Unknown url is silent success (Unsubscribe.cpp:52-53).
	result, rpcErr := unsubscribeURL(t, services, `{"url":"http://example.com/none","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, map[string]any{}, result)
}

func TestRPCSub_UnsubscribeMalformedUnknownURLsAreSilent(t *testing.T) {
	_, services := newRPCSubTestServer(t)
	for _, params := range []string{
		`{"url":""}`,
		`{"url":"::not a url::"}`,
		`{"url":"ftp://example.com/events"}`,
		`{"url":"http://"}`,
		`{"url":" http://example.com/events"}`,
	} {
		result, rpcErr := unsubscribeURL(t, services, params)
		assert.Nil(t, rpcErr, "params=%s", params)
		assert.Equal(t, map[string]any{}, result, "params=%s", params)
	}
}

// TestRPCSub_AccountsDontBlockRemoval mirrors NetworkOPs::tryRemoveRPCSub
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
	assert.Zero(t, ws.SubscriptionManager().Metrics().Connections,
		"entry must be removed when only account subscriptions remain")
}

// TestRPCSub_SubscribeAckCarriesLedgerInfo verifies the url path returns
// the same subscribe ack the WebSocket path builds, including rippled's
// field gating: network_id is always present (even 0) and fee_ref appears
// only while XRPFees is disabled.
func TestRPCSub_SubscribeAckCarriesLedgerInfo(t *testing.T) {
	_, services := newRPCSubTestServerWithProvider(t, stubLedgerInfoProvider{ledgerAvailable: true})
	sink := newRPCSubSink(t)

	result, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	ack, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, uint32(42), ack["ledger_index"])
	assert.Equal(t, "ABCD", ack["ledger_hash"])
	assert.Equal(t, int32(10), ack["fee_base"])
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
	_, services := newRPCSubTestServerWithProvider(t, stubLedgerInfoProvider{ledgerAvailable: true, xrpFees: true})
	sink := newRPCSubSink(t)

	result, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	ack, ok := result.(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, ack, "fee_ref", "fee_ref must be omitted while XRPFees is enabled")
	require.Contains(t, ack, "network_id")
}

func TestSubLedgerWireMatrix(t *testing.T) {
	states := []string{"", "disconnected", "connected", "syncing", "tracking", "full", "proposing", "validating"}
	for _, xrpFees := range []bool{false, true} {
		for _, state := range states {
			for _, needsNetworkLedger := range []bool{false, true} {
				for _, networkID := range []uint32{0, 42} {
					for _, complete := range []string{"", "1-2,4"} {
						name := fmt.Sprintf("xrp_fees_%t/%s/needs_%t/network_%d/complete_%t", xrpFees, state, needsNetworkLedger, networkID, complete != "")
						t.Run(name, func(t *testing.T) {
							validatedLedgers := ""
							validatedLedgersPresent := publishesValidatedLedgers(state) && !needsNetworkLedger
							if validatedLedgersPresent {
								validatedLedgers = complete
							}
							ws, _ := newRPCSubTestServerWithProvider(t, stubLedgerInfoProvider{
								ledgerAvailable:         true,
								xrpFees:                 xrpFees,
								validatedLedgers:        validatedLedgers,
								validatedLedgersPresent: validatedLedgersPresent,
								networkID:               networkID,
							})
							ack := ws.buildSubscribeAck(&types.RpcContext{}, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}})
							got, err := json.Marshal(ack)
							require.NoError(t, err)

							want := `{"fee_base":10`
							if !xrpFees {
								want += `,"fee_ref":10`
							}
							want += fmt.Sprintf(`,"ledger_hash":"ABCD","ledger_index":42,"ledger_time":735000000,"network_id":%d,"reserve_base":10000000,"reserve_inc":2000000`, networkID)
							if validatedLedgersPresent {
								want += fmt.Sprintf(`,"validated_ledgers":"%s"`, validatedLedgers)
							}
							want += `}`
							if string(got) != want {
								t.Fatalf("subLedger JSON = %s, want %s", got, want)
							}
						})
					}
				}
			}
		}
	}
}

func TestSubLedgerWireMatrixWithoutValidatedLedger(t *testing.T) {
	states := []string{"", "disconnected", "connected", "syncing", "tracking", "full", "proposing", "validating"}
	for _, state := range states {
		for _, needsNetworkLedger := range []bool{false, true} {
			for _, networkID := range []uint32{0, 42} {
				for _, complete := range []string{"", "1-2,4"} {
					name := fmt.Sprintf("%s/needs_%t/network_%d/complete_%t", state, needsNetworkLedger, networkID, complete != "")
					t.Run(name, func(t *testing.T) {
						validatedLedgers := ""
						validatedLedgersPresent := publishesValidatedLedgers(state) && !needsNetworkLedger
						if validatedLedgersPresent {
							validatedLedgers = complete
						}
						ws, _ := newRPCSubTestServerWithProvider(t, stubLedgerInfoProvider{
							validatedLedgers:        validatedLedgers,
							validatedLedgersPresent: validatedLedgersPresent,
							networkID:               networkID,
						})
						ack := ws.buildSubscribeAck(&types.RpcContext{}, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}})
						got, err := json.Marshal(ack)
						require.NoError(t, err)

						want := "{}"
						if validatedLedgersPresent {
							want = fmt.Sprintf(`{"validated_ledgers":"%s"}`, validatedLedgers)
						}
						if string(got) != want {
							t.Fatalf("subLedger JSON without validated ledger = %s, want %s", got, want)
						}
					})
				}
			}
		}
	}
}

func publishesValidatedLedgers(state string) bool {
	switch state {
	case "syncing", "tracking", "full", "proposing", "validating":
		return true
	default:
		return false
	}
}

type stubLedgerInfoProvider struct {
	ledgerAvailable         bool
	xrpFees                 bool
	validatedLedgers        string
	validatedLedgersPresent bool
	networkID               uint32
}

func (s stubLedgerInfoProvider) GetCurrentLedgerInfo() *types.LedgerSubscribeInfo {
	return &types.LedgerSubscribeInfo{
		LedgerAvailable:         s.ledgerAvailable,
		LedgerIndex:             42,
		LedgerHash:              "ABCD",
		LedgerTime:              735000000,
		FeeBase:                 10,
		FeeRef:                  10,
		ReserveBase:             10000000,
		ReserveInc:              2000000,
		ValidatedLedgers:        s.validatedLedgers,
		ValidatedLedgersPresent: s.validatedLedgersPresent,
		NetworkID:               s.networkID,
		XRPFeesEnabled:          s.xrpFees,
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
	assert.Equal(t, uint64(1), ws.SubscriptionManager().Metrics().Connections)

	data, _ := json.Marshal(map[string]any{"type": "ledgerClosed"})
	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)
	assert.Equal(t, float64(1), sink.next(t).Params["seq"])
	sink.expectNone(t)
}

func TestRPCSub_ExistingSubscribeKeepsEarlierMutations(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	urlParam := `"url":"` + sink.srv.URL + `"`

	_, rpcErr := subscribeURL(t, services, `{`+urlParam+`,"url_username":"before","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	ws.urlSubs.mu.Lock()
	sub := ws.urlSubs.subs[sink.srv.URL]
	require.NotNil(t, sub)
	before := sub.registration.Snapshot()
	ws.urlSubs.mu.Unlock()

	_, rpcErr = subscribeURL(t, services, `{`+urlParam+`,"username":"after","streams":["transactions","invalid"]}`)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcSTREAM_MALFORMED, rpcErr.Code)

	after := sub.registration.Snapshot()
	assert.True(t, before.Has(types.SubLedger))
	assert.True(t, after.Has(types.SubTransactions))
	username, _ := sub.credentials()
	assert.Equal(t, "after", username, "credential mutation precedes stream validation")
}

func TestRPCSub_CanonicalURLReuseAndUnsubscribe(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	rawURL := sink.srv.URL
	canonicalVariant := strings.ToUpper("http") + strings.TrimPrefix(rawURL, "http") + "/"

	_, rpcErr := subscribeURL(t, services, `{"url":"`+rawURL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	_, rpcErr = subscribeURL(t, services, `{"url":"`+canonicalVariant+`","streams":["transactions"]}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, uint64(1), ws.SubscriptionManager().Metrics().Connections)
	ws.urlSubs.mu.Lock()
	assert.Len(t, ws.urlSubs.subs, 1)
	ws.urlSubs.mu.Unlock()

	_, rpcErr = unsubscribeURL(t, services, `{"url":"`+canonicalVariant+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	_, rpcErr = unsubscribeURL(t, services, `{"url":"`+rawURL+`","streams":["transactions"]}`)
	require.Nil(t, rpcErr)
	assert.Zero(t, ws.SubscriptionManager().Metrics().Connections)
	ws.urlSubs.mu.Lock()
	assert.Empty(t, ws.urlSubs.subs)
	ws.urlSubs.mu.Unlock()
}

func TestRPCSub_IPLiteralCanonicalReuseAndUnsubscribe(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	defer ws.urlSubs.Close()
	ctx := adminCtx(services)
	expanded := "http://[0:0:0:0:0:0:0:1]:18080/events?token=one"
	compressed := "http://[::1]:18080/events?token=one"

	_, rpcErr := ws.urlSubs.Subscribe(ctx, types.SubscriptionRequest{
		URL: expanded, Streams: []types.SubscriptionType{types.SubLedger},
	})
	require.Nil(t, rpcErr)
	_, rpcErr = ws.urlSubs.Subscribe(ctx, types.SubscriptionRequest{
		URL: compressed, Streams: []types.SubscriptionType{types.SubTransactions},
	})
	require.Nil(t, rpcErr)
	assert.Equal(t, uint64(1), ws.SubscriptionManager().Metrics().Connections)

	_, rpcErr = ws.urlSubs.Unsubscribe(ctx, types.SubscriptionRequest{
		URL: expanded, Streams: []types.SubscriptionType{types.SubLedger},
	})
	require.Nil(t, rpcErr)
	_, rpcErr = ws.urlSubs.Unsubscribe(ctx, types.SubscriptionRequest{
		URL: compressed, Streams: []types.SubscriptionType{types.SubTransactions},
	})
	require.Nil(t, rpcErr)
	assert.Zero(t, ws.SubscriptionManager().Metrics().Connections)
}

func TestRPCSub_SubscribeAfterClosePrecedesURLValidation(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	ws.urlSubs.Close()

	for _, params := range []string{
		`{"url":"not a url"}`,
		`{"url":"ftp://example.com/events"}`,
	} {
		result, rpcErr := subscribeURL(t, services, params)
		assert.Nil(t, result, "params=%s", params)
		require.NotNil(t, rpcErr, "params=%s", params)
		assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code, "params=%s", params)
	}
}

func TestRPCSub_BoundsGlobalAndPerPrincipal(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	first := newRPCSubSink(t)
	second := newRPCSubSink(t)
	third := newRPCSubSink(t)
	ws.urlSubs.maxEntries = 1
	ws.urlSubs.maxWorkers = 1
	ws.urlSubs.maxPerPrincipal = 100

	_, rpcErr := subscribeURL(t, services, `{"url":"`+first.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	_, rpcErr = subscribeURL(t, services, `{"url":"`+second.srv.URL+`","streams":["ledger"]}`)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcTOO_BUSY, rpcErr.Code)
	assert.Equal(t, uint64(1), ws.SubscriptionManager().Metrics().Connections)
	ws.urlSubs.mu.Lock()
	assert.Len(t, ws.urlSubs.subs, 1)
	ws.urlSubs.mu.Unlock()
	assert.Equal(t, uint64(1), ws.urlSubs.metricsSnapshot().CapacityRejects)

	_, rpcErr = unsubscribeURL(t, services, `{"url":"`+first.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	assert.Eventually(t, func() bool {
		ws.urlSubs.mu.Lock()
		defer ws.urlSubs.mu.Unlock()
		return ws.urlSubs.workers == 0
	}, time.Second, time.Millisecond)
	_, rpcErr = subscribeURL(t, services, `{"url":"`+second.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)

	ws.urlSubs.maxEntries = 0
	ws.urlSubs.maxWorkers = 0
	ws.urlSubs.maxPerPrincipal = 1
	ctx := adminCtx(services)
	ctx.ClientIP = "principal-a"
	_, rpcErr = ws.urlSubs.Subscribe(ctx, types.SubscriptionRequest{URL: third.srv.URL, Streams: []types.SubscriptionType{types.SubLedger}})
	require.Nil(t, rpcErr)
	fourth := newRPCSubSink(t)
	_, rpcErr = ws.urlSubs.Subscribe(ctx, types.SubscriptionRequest{URL: fourth.srv.URL, Streams: []types.SubscriptionType{types.SubLedger}})
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcTOO_BUSY, rpcErr.Code)
	ctx.ClientIP = "principal-b"
	_, rpcErr = ws.urlSubs.Subscribe(ctx, types.SubscriptionRequest{URL: fourth.srv.URL, Streams: []types.SubscriptionType{types.SubLedger}})
	require.Nil(t, rpcErr)
}

func TestRPCSub_EquivalentIPv6PrincipalsShareCapacity(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	first := newRPCSubSink(t)
	second := newRPCSubSink(t)
	ws.urlSubs.maxEntries = 4
	ws.urlSubs.maxWorkers = 4
	ws.urlSubs.maxPerPrincipal = 1

	firstCtx := adminCtx(services)
	firstCtx.ClientIP = "2001:0db8:0:0:0:0:0:1"
	_, rpcErr := ws.urlSubs.Subscribe(firstCtx, types.SubscriptionRequest{
		URL: first.srv.URL, Streams: []types.SubscriptionType{types.SubLedger},
	})
	require.Nil(t, rpcErr)

	secondCtx := adminCtx(services)
	secondCtx.ClientIP = "2001:db8::1"
	_, rpcErr = ws.urlSubs.Subscribe(secondCtx, types.SubscriptionRequest{
		URL: second.srv.URL, Streams: []types.SubscriptionType{types.SubLedger},
	})
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcTOO_BUSY, rpcErr.Code)

	ws.urlSubs.mu.Lock()
	defer ws.urlSubs.mu.Unlock()
	assert.Equal(t, 1, ws.urlSubs.principalCounts["2001:db8::1"])
	assert.Equal(t, 1, ws.urlSubs.principalWorkers["2001:db8::1"])
	assert.Len(t, ws.urlSubs.principalCounts, 1)
	assert.Len(t, ws.urlSubs.principalWorkers, 1)
}

func TestRPCSub_WorkerCapacityReclaimedBeforeUnsubscribeReturns(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	first := newRPCSubSink(t)
	second := newRPCSubSink(t)
	ws.urlSubs.maxWorkers = 1

	_, rpcErr := subscribeURL(t, services, `{"url":"`+first.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	_, rpcErr = unsubscribeURL(t, services, `{"url":"`+first.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)

	ws.urlSubs.mu.Lock()
	workers := ws.urlSubs.workers
	ws.urlSubs.mu.Unlock()
	assert.Zero(t, workers, "unsubscribe must not return before worker-cap accounting is released")

	_, rpcErr = subscribeURL(t, services, `{"url":"`+second.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr, "an immediate replacement must fit the reclaimed worker slot")
}

func TestRPCSub_UnsubscribeCancelsConnection(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)

	_, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	ws.urlSubs.mu.Lock()
	sub := ws.urlSubs.subs[sink.srv.URL]
	ws.urlSubs.mu.Unlock()
	require.NotNil(t, sub)

	_, rpcErr = unsubscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)

	select {
	case <-sub.conn.Done():
	default:
		t.Fatal("unsubscribed connection remains active")
	}
	require.False(t, sub.conn.TrySend([]byte(`{}`)))
}

func TestRPCSub_AttachRejectionRollsBackRegistryState(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	key, rpcErr := canonicalRPCSubURL(sink.srv.URL)
	require.Nil(t, rpcErr)
	occupied := subscription.NewConnection("rpcsub:"+key, make(chan []byte, 1))
	registration, attached := ws.subscriptionManager.Attach(occupied)
	require.True(t, attached)
	t.Cleanup(func() { ws.subscriptionManager.Detach(registration) })

	_, rpcErr = ws.urlSubs.Subscribe(adminCtx(services), types.SubscriptionRequest{
		URL: sink.srv.URL, Streams: []types.SubscriptionType{types.SubLedger},
	})
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
	ws.urlSubs.mu.Lock()
	defer ws.urlSubs.mu.Unlock()
	assert.Empty(t, ws.urlSubs.subs)
	assert.Empty(t, ws.urlSubs.principalCounts)
	assert.Empty(t, ws.urlSubs.principalWorkers)
	assert.Zero(t, ws.urlSubs.workers)
}

func TestRPCSub_SlowConsumerRetiresAllOwnership(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	transport := &blockingRPCSubTransport{started: make(chan struct{}), release: make(chan struct{})}
	ws.urlSubs.client.Transport = transport
	ctx := adminCtx(services)
	ctx.ClientIP = "slow-owner"
	request := types.SubscriptionRequest{
		URL: "http://127.0.0.1:18085/slow", Streams: []types.SubscriptionType{types.SubLedger},
	}
	_, rpcErr := ws.urlSubs.Subscribe(ctx, request)
	require.Nil(t, rpcErr)
	ws.urlSubs.mu.Lock()
	sub := ws.urlSubs.subs[request.URL]
	ws.urlSubs.mu.Unlock()
	require.NotNil(t, sub)

	data := []byte(`{"type":"ledgerClosed"}`)
	ws.subscriptionManager.BroadcastToStream(types.SubLedger, data)
	<-transport.started
	for range rpcSubQueueLimit + subscription.MaxConsecutiveDrops {
		ws.subscriptionManager.BroadcastToStream(types.SubLedger, data)
	}
	close(transport.release)
	select {
	case <-sub.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal URL subscriber was not retired")
	}

	ws.urlSubs.mu.Lock()
	defer ws.urlSubs.mu.Unlock()
	assert.Empty(t, ws.urlSubs.subs)
	assert.Empty(t, ws.urlSubs.principalCounts)
	assert.Empty(t, ws.urlSubs.principalWorkers)
	assert.Zero(t, ws.urlSubs.workers)
	assert.Zero(t, ws.subscriptionManager.Metrics().Connections)
	assert.True(t, sub.conn.Stats().Terminal)
	assert.Equal(t, uint64(1), sub.conn.Stats().Disconnects)
}

func TestRPCSub_TerminalEntryCannotBeReusedBeforeAsyncRetirement(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)
	ctx := adminCtx(services)
	ctx.ClientIP = "terminal-owner"
	request := types.SubscriptionRequest{
		URL: sink.srv.URL, Streams: []types.SubscriptionType{types.SubLedger},
	}
	_, rpcErr := ws.urlSubs.Subscribe(ctx, request)
	require.Nil(t, rpcErr)
	key, rpcErr := canonicalRPCSubURL(request.URL)
	require.Nil(t, rpcErr)

	ws.urlSubs.mu.Lock()
	sub := ws.urlSubs.subs[key]
	if sub == nil {
		ws.urlSubs.mu.Unlock()
		t.Fatal("URL subscription was not registered")
	}
	sub.conn.Cancel()
	retireStarted := make(chan struct{})
	retireDone := make(chan struct{})
	go func() {
		close(retireStarted)
		ws.urlSubs.retire(sub)
		close(retireDone)
	}()
	<-retireStarted
	lookup, reuseErr := ws.urlSubs.findOrCreateLocked(request, key, ctx.ClientIP)
	entryUnchanged := ws.urlSubs.subs[key] == sub
	entries := len(ws.urlSubs.subs)
	workers := ws.urlSubs.workers
	ws.urlSubs.mu.Unlock()

	require.NotNil(t, reuseErr)
	assert.Equal(t, rpcerrors.RpcTOO_BUSY, reuseErr.Code)
	assert.Nil(t, lookup.sub)
	assert.True(t, entryUnchanged)
	assert.Equal(t, 1, entries)
	assert.Equal(t, 1, workers)

	select {
	case <-retireDone:
	case <-time.After(time.Second):
		t.Fatal("async retirement did not complete")
	}
	ws.urlSubs.mu.Lock()
	defer ws.urlSubs.mu.Unlock()
	assert.Empty(t, ws.urlSubs.subs)
	assert.Empty(t, ws.urlSubs.principalCounts)
	assert.Empty(t, ws.urlSubs.principalWorkers)
	assert.Zero(t, ws.urlSubs.workers)
	assert.Zero(t, ws.subscriptionManager.Metrics().Connections)
	assert.Equal(t, uint64(1), ws.urlSubs.metricsSnapshot().CapacityRejects)
}

func TestRPCSub_PrincipalWorkerCapDuringRetirement(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	defer ws.urlSubs.Close()
	ws.urlSubs.maxEntries = 8
	ws.urlSubs.maxWorkers = 8
	ws.urlSubs.maxPerPrincipal = 1

	transport := &blockingRPCSubTransport{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	ws.urlSubs.client.Transport = transport
	ctx := adminCtx(services)
	ctx.ClientIP = "principal-retiring"
	first := types.SubscriptionRequest{
		URL: "http://127.0.0.1:18081/blocked", Streams: []types.SubscriptionType{types.SubLedger},
	}
	replacement := types.SubscriptionRequest{
		URL: "http://127.0.0.1:18082/replacement", Streams: []types.SubscriptionType{types.SubLedger},
	}
	_, rpcErr := ws.urlSubs.Subscribe(ctx, first)
	require.Nil(t, rpcErr)

	data, err := json.Marshal(map[string]any{"type": "ledgerClosed"})
	require.NoError(t, err)
	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)
	select {
	case <-transport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocked delivery")
	}

	unsubDone := make(chan *rpcerrors.RpcError, 1)
	go func() {
		_, unsubErr := ws.urlSubs.Unsubscribe(ctx, first)
		unsubDone <- unsubErr
	}()

	assert.Eventually(t, func() bool {
		ws.urlSubs.mu.Lock()
		defer ws.urlSubs.mu.Unlock()
		return len(ws.urlSubs.subs) == 0 && ws.urlSubs.principalCounts[ctx.ClientIP] == 0 && ws.urlSubs.principalWorkers[ctx.ClientIP] == 1
	}, time.Second, time.Millisecond, "retiring worker must retain the principal charge")

	maxLive := 0
	for i := 0; i < 100; i++ {
		_, rpcErr = ws.urlSubs.Subscribe(ctx, replacement)
		require.NotNil(t, rpcErr, "replacement %d should remain blocked while the old worker is retiring", i)
		assert.Equal(t, rpcerrors.RpcTOO_BUSY, rpcErr.Code)
		ws.urlSubs.mu.Lock()
		live := ws.urlSubs.principalWorkers[ctx.ClientIP]
		ws.urlSubs.mu.Unlock()
		if live > maxLive {
			maxLive = live
		}
	}
	assert.LessOrEqual(t, maxLive, ws.urlSubs.maxPerPrincipal)

	close(transport.release)
	select {
	case unsubErr := <-unsubDone:
		require.Nil(t, unsubErr)
	case <-time.After(2 * time.Second):
		t.Fatal("unsubscribe did not join the retiring worker")
	}

	ws.urlSubs.mu.Lock()
	live := ws.urlSubs.principalWorkers[ctx.ClientIP]
	ws.urlSubs.mu.Unlock()
	assert.Zero(t, live)
	_, rpcErr = ws.urlSubs.Subscribe(ctx, replacement)
	require.Nil(t, rpcErr, "replacement should fit once the old worker exits")
	_, rpcErr = ws.urlSubs.Unsubscribe(ctx, replacement)
	require.Nil(t, rpcErr)
}

func TestRPCSub_ConcurrentSubscribeUnsubscribeWorkerAccounting(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	const (
		goroutines = 8
		iterations = 20
	)
	ws.urlSubs.maxEntries = goroutines
	ws.urlSubs.maxWorkers = goroutines
	ws.urlSubs.maxPerPrincipal = goroutines

	sinks := make([]*rpcSubSink, goroutines)
	for i := range sinks {
		sinks[i] = newRPCSubSink(t)
	}

	var wg sync.WaitGroup
	for _, sink := range sinks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := adminCtx(services)
			ctx.ClientIP = sink.srv.URL
			request := types.SubscriptionRequest{URL: sink.srv.URL, Streams: []types.SubscriptionType{types.SubLedger}}
			for i := 0; i < iterations; i++ {
				if _, rpcErr := ws.urlSubs.Subscribe(ctx, request); rpcErr != nil {
					t.Errorf("subscribe iteration %d: %v", i, rpcErr)
					return
				}
				if _, rpcErr := ws.urlSubs.Unsubscribe(ctx, request); rpcErr != nil {
					t.Errorf("unsubscribe iteration %d: %v", i, rpcErr)
					return
				}
			}
		}()
	}
	wg.Wait()

	ws.urlSubs.mu.Lock()
	workers, entries := ws.urlSubs.workers, len(ws.urlSubs.subs)
	ws.urlSubs.mu.Unlock()
	assert.Zero(t, workers)
	assert.Zero(t, entries)
}

func TestRPCSub_DeliveryObservability(t *testing.T) {
	metrics := &rpcSubMetrics{}
	conn := subscription.NewConnection("observability", make(chan []byte, rpcSubQueueLimit))
	conn.SetSendObserver(func(queued bool) {
		if queued {
			metrics.recordQueued("observability")
		} else {
			metrics.recordDropped("observability")
		}
	})
	data, _ := json.Marshal(map[string]any{"type": "ledgerClosed"})
	for i := 0; i < rpcSubQueueLimit+4; i++ {
		conn.TrySend(data)
	}
	snapshot := metrics.snapshot()
	assert.Equal(t, uint64(rpcSubQueueLimit), snapshot.Queued)
	assert.Equal(t, uint64(4), snapshot.Dropped)

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failing.Close()
	failed := &rpcSub{endpoint: failing.URL, client: failing.Client(), ctx: context.Background(), metrics: &rpcSubMetrics{}}
	failed.deliver([]byte(`{"method":"event"}`))
	assert.Equal(t, uint64(1), failed.metrics.snapshot().DeliveryFailures)

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", failing.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	redirectClient := redirect.Client()
	redirectClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	redirected := &rpcSub{endpoint: redirect.URL, client: redirectClient, ctx: context.Background(), metrics: &rpcSubMetrics{}}
	redirected.deliver([]byte(`{"method":"event"}`))
	assert.Equal(t, uint64(1), redirected.metrics.snapshot().DeliveryFailures)
}

func TestRPCSub_ProductionClientTreatsRedirectAsFailure(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	defer ws.urlSubs.Close()
	redirectHits := make(chan struct{}, 1)
	targetHits := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case redirectHits <- struct{}{}:
		default:
		}
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	_, rpcErr := subscribeURL(t, services, `{"url":"`+redirect.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	data, err := json.Marshal(map[string]any{"type": "ledgerClosed"})
	require.NoError(t, err)
	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)
	select {
	case <-redirectHits:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for production URL client request")
	}
	assert.Eventually(t, func() bool {
		return ws.urlSubs.metricsSnapshot().DeliveryFailures == 1
	}, time.Second, time.Millisecond)
	select {
	case <-targetHits:
		t.Fatal("redirect target must not receive a request")
	default:
	}
}

func TestRPCSub_MalformedStreamKeepsCreatedEntry(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)

	_, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["nonsense"]}`)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcSTREAM_MALFORMED, rpcErr.Code)
	assert.Equal(t, uint64(1), ws.SubscriptionManager().Metrics().Connections,
		"URL registration precedes stream validation")
}

// TestRPCSub_CloseStopsDelivery verifies registry shutdown through
// WebSocketServer.Close tears down url subscriptions.
func TestRPCSub_CloseStopsDelivery(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	sink := newRPCSubSink(t)

	_, rpcErr := subscribeURL(t, services, `{"url":"`+sink.srv.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)

	require.NoError(t, ws.Close(t.Context()))
	assert.Zero(t, ws.SubscriptionManager().Metrics().Connections)

	data, _ := json.Marshal(map[string]any{"type": "ledgerClosed"})
	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)
	sink.expectNone(t)
}

func TestRPCSub_CloseCancelsInFlightDelivery(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(sink.Close)

	ws, services := newRPCSubTestServer(t)
	_, rpcErr := subscribeURL(t, services, `{"url":"`+sink.URL+`","streams":["ledger"]}`)
	require.Nil(t, rpcErr)
	ws.urlSubs.mu.Lock()
	sub := ws.urlSubs.subs[sink.URL]
	ws.urlSubs.mu.Unlock()
	require.NotNil(t, sub)
	data, err := json.Marshal(map[string]any{"type": "ledgerClosed"})
	require.NoError(t, err)
	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-flight delivery")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- ws.Close(t.Context())
	}()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not cancel and join in-flight delivery")
	}
	assert.Error(t, sub.ctx.Err())
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		sink.CloseClientConnections()
		t.Fatal("in-flight request context was not canceled")
	}
}

func TestRPCSub_CloseJoinsRetiringWorker(t *testing.T) {
	ws, services := newRPCSubTestServer(t)
	ws.urlSubs.maxEntries = 4
	ws.urlSubs.maxWorkers = 4
	ws.urlSubs.maxPerPrincipal = 1
	transport := &blockingRPCSubTransport{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	ws.urlSubs.client.Transport = transport
	released := false
	t.Cleanup(func() {
		if !released {
			close(transport.release)
		}
		ws.urlSubs.Close()
	})

	ctx := adminCtx(services)
	ctx.ClientIP = "principal-close"
	request := types.SubscriptionRequest{
		URL: "http://127.0.0.1:18084/blocked", Streams: []types.SubscriptionType{types.SubLedger},
	}
	_, rpcErr := ws.urlSubs.Subscribe(ctx, request)
	require.Nil(t, rpcErr)
	ws.urlSubs.mu.Lock()
	sub := ws.urlSubs.subs[request.URL]
	ws.urlSubs.mu.Unlock()
	require.NotNil(t, sub)
	data, err := json.Marshal(map[string]any{"type": "ledgerClosed"})
	require.NoError(t, err)
	ws.SubscriptionManager().BroadcastToStream(types.SubLedger, data)
	select {
	case <-transport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocked delivery")
	}

	unsubDone := make(chan *rpcerrors.RpcError, 1)
	go func() {
		_, unsubErr := ws.urlSubs.Unsubscribe(ctx, request)
		unsubDone <- unsubErr
	}()
	assert.Eventually(t, func() bool {
		ws.urlSubs.mu.Lock()
		defer ws.urlSubs.mu.Unlock()
		return len(ws.urlSubs.subs) == 0 && ws.urlSubs.principalWorkers[ctx.ClientIP] == 1
	}, time.Second, time.Millisecond)

	closeDone := make(chan struct{})
	go func() {
		ws.urlSubs.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("Close returned while a removed worker was still retiring")
	case <-time.After(50 * time.Millisecond):
	}

	close(transport.release)
	released = true
	select {
	case unsubErr := <-unsubDone:
		require.Nil(t, unsubErr)
	case <-time.After(2 * time.Second):
		t.Fatal("unsubscribe did not join the retiring worker")
	}
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not join the retiring worker")
	}

	ws.urlSubs.mu.Lock()
	workers := ws.urlSubs.workers
	principalWorkers := ws.urlSubs.principalWorkers[ctx.ClientIP]
	ws.urlSubs.mu.Unlock()
	assert.Zero(t, workers)
	assert.Zero(t, principalWorkers)
	select {
	case <-sub.finished:
	default:
		t.Fatal("Close returned before the worker signaled finished")
	}
}
