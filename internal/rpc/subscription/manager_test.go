package subscription

import (
	"encoding/hex"
	"encoding/json"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testIssuer   = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	testAccountB = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	testMPTA     = "000000000000000000000000000000000000000000000001"
	testMPTB     = "000000000000000000000000000000000000000000000002"
)

func attach(t testing.TB, manager *Manager, id string, queue int) (*Connection, *Registration) {
	t.Helper()
	connection := NewConnection(id, make(chan []byte, queue))
	registration, ok := manager.Attach(connection)
	require.True(t, ok)
	t.Cleanup(func() { manager.Detach(registration) })
	return connection, registration
}

func decodeRequest(t testing.TB, body string) types.SubscriptionRequest {
	t.Helper()
	var request types.SubscriptionRequest
	require.NoError(t, json.Unmarshal([]byte(body), &request))
	return request
}

func TestBookAssetCombinations(t *testing.T) {
	tests := []struct {
		name string
		pays string
		gets string
	}{
		{"mpt-xrp", `{"mpt_issuance_id":"` + testMPTA + `"}`, `{"currency":"XRP"}`},
		{"mpt-iou", `{"mpt_issuance_id":"` + testMPTA + `"}`, `{"currency":"USD","issuer":"` + testIssuer + `"}`},
		{"mpt-mpt", `{"mpt_issuance_id":"` + testMPTA + `"}`, `{"mpt_issuance_id":"` + testMPTB + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager()
			_, registration := attach(t, manager, test.name, 1)
			request := decodeRequest(t, `{"books":[{"taker_pays":`+test.pays+`,"taker_gets":`+test.gets+`}]}`)
			require.Nil(t, manager.HandleSubscribe(registration, request, true))
			assert.Equal(t, 1, registration.Snapshot().BookCount())
		})
	}
}

func TestBookAssetErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		code int
	}{
		{"mixed pays", `{"books":[{"taker_pays":{"mpt_issuance_id":"` + testMPTA + `","currency":"USD"},"taker_gets":{"currency":"XRP"}}]}`, types.RpcINVALID_PARAMS},
		{"mixed gets", `{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"mpt_issuance_id":"` + testMPTA + `","issuer":"` + testIssuer + `"}}]}`, types.RpcINVALID_PARAMS},
		{"malformed pays", `{"books":[{"taker_pays":{"mpt_issuance_id":"xyz"},"taker_gets":{"currency":"XRP"}}]}`, types.RpcSRC_CUR_MALFORMED},
		{"malformed gets", `{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"mpt_issuance_id":"xyz"}}]}`, types.RpcDST_AMT_MALFORMED},
		{"same mpt", `{"books":[{"taker_pays":{"mpt_issuance_id":"` + testMPTA + `"},"taker_gets":{"mpt_issuance_id":"` + testMPTA + `"}}]}`, types.RpcBAD_MARKET},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager()
			_, registration := attach(t, manager, test.name, 1)
			rpcErr := manager.HandleSubscribe(registration, decodeRequest(t, test.body), true)
			require.NotNil(t, rpcErr)
			assert.Equal(t, test.code, rpcErr.Code)
		})
	}
}

func TestBookOptionalFieldPresenceAndPrecedence(t *testing.T) {
	base := `"taker_pays":{"currency":"USD","issuer":"` + testIssuer + `"},"taker_gets":{"currency":"XRP"}`
	for _, test := range []struct {
		name  string
		field string
		code  int
	}{
		{"null taker", `"taker":null`, types.RpcACT_MALFORMED},
		{"empty taker", `"taker":""`, types.RpcACT_MALFORMED},
		{"numeric taker", `"taker":1`, types.RpcACT_MALFORMED},
		{"null domain", `"domain":null`, types.RpcDOMAIN_MALFORMED},
		{"empty domain", `"domain":""`, types.RpcDOMAIN_MALFORMED},
		{"numeric domain", `"domain":0`, types.RpcDOMAIN_MALFORMED},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager()
			_, registration := attach(t, manager, test.name, 1)
			rpcErr := manager.HandleSubscribe(registration, decodeRequest(t, `{"books":[{`+base+`,`+test.field+`}]}`), true)
			require.NotNil(t, rpcErr)
			assert.Equal(t, test.code, rpcErr.Code)
		})
	}

	manager := NewManager()
	_, registration := attach(t, manager, "unsubscribe-taker", 1)
	subscribe := decodeRequest(t, `{"books":[{`+base+`}]}`)
	require.Nil(t, manager.HandleSubscribe(registration, subscribe, true))
	unsubscribe := decodeRequest(t, `{"books":[{`+base+`,"taker":null}]}`)
	require.Nil(t, manager.HandleUnsubscribe(registration, unsubscribe))
	assert.Zero(t, registration.Snapshot().BookCount())

	same := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":null},"taker":null}]}`)
	rpcErr := manager.HandleSubscribe(registration, same, true)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcBAD_MARKET, rpcErr.Code)
}

func TestSnapshotBookUsesCanonicalAssets(t *testing.T) {
	request := decodeRequest(t, `{"books":[{"taker_pays":{"currency":0},"taker_gets":{"mpt_issuance_id":0},"domain":"0"}]}`)
	pays, gets, domain, rpcErr := SnapshotBook(request.Books[0])
	require.Nil(t, rpcErr)
	assert.Equal(t, "XRP", pays.Currency)
	assert.Equal(t, "000000000000000000000000000000000000000000000000", gets.MPTIssuanceID)
	assert.Equal(t, "0000000000000000000000000000000000000000000000000000000000000000", domain)

	nullXRP := decodeRequest(t, `{"books":[{"taker_pays":{"currency":null},"taker_gets":{"currency":"USD","issuer":"`+testIssuer+`"}}]}`)
	pays, _, _, rpcErr = SnapshotBook(nullXRP.Books[0])
	require.Nil(t, rpcErr)
	assert.Equal(t, "XRP", pays.Currency)

	negativeZero := decodeRequest(t, `{"books":[{"taker_pays":{"currency":-0},"taker_gets":{"currency":"USD","issuer":"`+testIssuer+`"}}]}`)
	pays, _, _, rpcErr = SnapshotBook(negativeZero.Books[0])
	require.Nil(t, rpcErr)
	assert.Equal(t, "XRP", pays.Currency)
}

func TestBookNumericRealZeroIsMalformed(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		code int
	}{
		{"currency zero", `{"books":[{"taker_pays":{"currency":0.0},"taker_gets":{"currency":"USD","issuer":"` + testIssuer + `"}}]}`, types.RpcSRC_CUR_MALFORMED},
		{"negative currency zero", `{"books":[{"taker_pays":{"currency":-0.0},"taker_gets":{"currency":"USD","issuer":"` + testIssuer + `"}}]}`, types.RpcSRC_CUR_MALFORMED},
		{"mpt zero", `{"books":[{"taker_pays":{"mpt_issuance_id":0.0},"taker_gets":{"currency":"XRP"}}]}`, types.RpcSRC_CUR_MALFORMED},
		{"negative mpt zero", `{"books":[{"taker_pays":{"mpt_issuance_id":-0.0},"taker_gets":{"currency":"XRP"}}]}`, types.RpcSRC_CUR_MALFORMED},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager()
			_, registration := attach(t, manager, test.name, 1)
			rpcErr := manager.HandleSubscribe(registration, decodeRequest(t, test.body), true)
			require.NotNil(t, rpcErr)
			assert.Equal(t, test.code, rpcErr.Code)
		})
	}
}

func TestBookIssuerHexForms(t *testing.T) {
	issuerID, ok := parseIssuer(testIssuer)
	require.True(t, ok)
	hexIssuer := strings.ToUpper(hex.EncodeToString(issuerID[:]))

	manager := NewManager()
	_, registration := attach(t, manager, "hex-issuer", 1)
	for _, issuer := range []string{"0", strings.Repeat("0", 40)} {
		request := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"XRP","issuer":"`+issuer+`"},"taker_gets":{"currency":"USD","issuer":"`+testIssuer+`"}}]}`)
		require.Nil(t, manager.HandleSubscribe(registration, request, true))
	}
	request := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"USD","issuer":"`+hexIssuer+`"},"taker_gets":{"currency":"XRP"}}]}`)
	require.Nil(t, manager.HandleSubscribe(registration, request, true))
	assert.Equal(t, 2, registration.Snapshot().BookCount())

	noAccount := strings.Repeat("0", 39) + "1"
	malformed := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"USD","issuer":"`+noAccount+`"},"taker_gets":{"currency":"XRP"}}]}`)
	rpcErr := manager.HandleSubscribe(registration, malformed, true)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcSRC_ISR_MALFORMED, rpcErr.Code)
}

func TestOrderBookSpecNormalizesZeroCurrency(t *testing.T) {
	manager := NewManager()
	connection, registration := attach(t, manager, "zero-currency", 1)
	request := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"USD","issuer":"`+testIssuer+`"}}]}`)
	require.Nil(t, manager.HandleSubscribe(registration, request, true))
	manager.BroadcastToOrderBooksVersioned([]byte("matched"), []byte("matched"), []types.OrderBookSpec{{
		TakerPays: types.CurrencySpec{Currency: "0", Issuer: "0"},
		TakerGets: types.CurrencySpec{Currency: "USD", Issuer: testIssuer},
	}})
	select {
	case message := <-connection.Outbound():
		assert.Equal(t, []byte("matched"), message)
	case <-time.After(time.Second):
		t.Fatal("normalized XRP book did not receive publication")
	}
}

func TestCanonicalBookIdentityAndDomainPresence(t *testing.T) {
	manager := NewManager()
	_, registration := attach(t, manager, "canonical", 4)
	iso := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"USD","issuer":"`+testIssuer+`"},"taker_gets":{"currency":"XRP"}}]}`)
	hex := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"0000000000000000000000005553440000000000","issuer":"`+testIssuer+`"},"taker_gets":{"currency":null}}]}`)
	require.Nil(t, manager.HandleSubscribe(registration, iso, true))
	require.Nil(t, manager.HandleSubscribe(registration, hex, true))
	assert.Equal(t, 1, registration.Snapshot().BookCount())
	require.Nil(t, manager.HandleUnsubscribe(registration, hex))
	assert.Zero(t, registration.Snapshot().BookCount())

	absent := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"USD","issuer":"`+testIssuer+`"},"taker_gets":{"currency":"XRP"}}]}`)
	zero := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"USD","issuer":"`+testIssuer+`"},"taker_gets":{"currency":"XRP"},"domain":"0"}]}`)
	require.Nil(t, manager.HandleSubscribe(registration, absent, true))
	require.Nil(t, manager.HandleSubscribe(registration, zero, true))
	assert.Equal(t, 2, registration.Snapshot().BookCount())
}

func TestCanonicalRequestCapCountsUniqueEdgesAcrossSplits(t *testing.T) {
	manager, err := NewManagerWithLimits(Limits{MaxItemsPerRequest: 4, MaxItemsPerConnection: 5, MaxItemsGlobal: 8})
	require.NoError(t, err)
	_, registration := attach(t, manager, "caps", 1)
	scope := manager.NewRequestScope()
	require.Nil(t, manager.HandleSubscribeScoped(registration, scope, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubTransactionsProposed, "rt_transactions"},
	}, true))
	book := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"USD","issuer":"`+testIssuer+`"},"taker_gets":{"currency":"XRP"}}]}`)
	require.Nil(t, manager.HandleSubscribeScoped(registration, scope, book, true))
	equivalent := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"0000000000000000000000005553440000000000","issuer":"`+testIssuer+`"},"taker_gets":{"currency":0}}]}`)
	require.Nil(t, manager.HandleSubscribeScoped(registration, scope, equivalent, true))
	rpcErr := manager.HandleSubscribeScoped(registration, scope, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
	assert.Equal(t, 2, registration.Snapshot().ItemCount())
}

func TestBothBookExpansionIsAtomicAtCapacityBoundaries(t *testing.T) {
	both := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"USD","issuer":"`+testIssuer+`"},"taker_gets":{"currency":"XRP"},"both":true}]}`)
	t.Run("request", func(t *testing.T) {
		manager, err := NewManagerWithLimits(Limits{MaxItemsPerRequest: 1, MaxItemsPerConnection: 2, MaxItemsGlobal: 2})
		require.NoError(t, err)
		_, registration := attach(t, manager, "request", 1)
		rpcErr := manager.HandleSubscribe(registration, both, true)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
		assert.Zero(t, registration.Snapshot().BookCount())
		assert.Zero(t, manager.items)
		assert.Empty(t, manager.bookIndex)
		assert.Equal(t, uint64(1), manager.requestLimitRejections)
		require.NoError(t, manager.checkInvariants())
	})
	t.Run("unsubscribe request", func(t *testing.T) {
		manager, err := NewManagerWithLimits(Limits{MaxItemsPerRequest: 1, MaxItemsPerConnection: 2, MaxItemsGlobal: 2})
		require.NoError(t, err)
		_, registration := attach(t, manager, "unsubscribe-request", 1)
		forwardBook := both.Books[0]
		forwardBook.Both = false
		forwardBook.BothSides = false
		forward := types.SubscriptionRequest{Books: []types.BookRequest{forwardBook}}
		require.Nil(t, manager.HandleSubscribe(registration, forward, true))
		reverse := forward.Books[0]
		reverse.TakerPays, reverse.TakerGets = reverse.TakerGets, reverse.TakerPays
		require.Nil(t, manager.HandleSubscribe(registration, types.SubscriptionRequest{Books: []types.BookRequest{reverse}}, true))
		rpcErr := manager.HandleUnsubscribe(registration, both)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
		assert.Equal(t, 2, registration.Snapshot().BookCount())
		assert.Equal(t, 2, manager.items)
		assert.Len(t, manager.bookIndex, 2)
		require.NoError(t, manager.checkInvariants())
	})
	t.Run("connection", func(t *testing.T) {
		manager, err := NewManagerWithLimits(Limits{MaxItemsPerRequest: 2, MaxItemsPerConnection: 2, MaxItemsGlobal: 4})
		require.NoError(t, err)
		_, registration := attach(t, manager, "connection", 1)
		require.Nil(t, manager.HandleSubscribe(registration, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true))
		rpcErr := manager.HandleSubscribe(registration, both, true)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
		assert.Zero(t, registration.Snapshot().BookCount())
		assert.Equal(t, 1, manager.items)
		assert.Empty(t, manager.bookIndex)
		assert.Equal(t, uint64(1), manager.connectionLimitRejections)
		require.NoError(t, manager.checkInvariants())
	})
	t.Run("global", func(t *testing.T) {
		manager, err := NewManagerWithLimits(Limits{MaxItemsPerRequest: 2, MaxItemsPerConnection: 2, MaxItemsGlobal: 2})
		require.NoError(t, err)
		_, existing := attach(t, manager, "existing", 1)
		require.Nil(t, manager.HandleSubscribe(existing, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true))
		_, registration := attach(t, manager, "global", 1)
		rpcErr := manager.HandleSubscribe(registration, both, true)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
		assert.Zero(t, registration.Snapshot().BookCount())
		assert.Equal(t, 1, manager.items)
		assert.Empty(t, manager.bookIndex)
		assert.Equal(t, uint64(1), manager.globalLimitRejections)
		require.NoError(t, manager.checkInvariants())
	})
}

func TestRawElementCapCountsDuplicatesAcrossScopedCalls(t *testing.T) {
	manager, err := NewManagerWithLimits(Limits{MaxItemsPerRequest: 3, MaxItemsPerConnection: 8, MaxItemsGlobal: 16})
	require.NoError(t, err)
	_, registration := attach(t, manager, "raw", 1)
	scope := manager.NewRequestScope()
	for range 3 {
		require.Nil(t, manager.HandleSubscribeScoped(registration, scope, types.SubscriptionRequest{
			Streams: []types.SubscriptionType{types.SubLedger},
		}, true))
	}
	rpcErr := manager.HandleSubscribeScoped(registration, scope, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}, true)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
	assert.True(t, registration.Snapshot().Has(types.SubLedger))

	require.Nil(t, manager.HandleSubscribe(registration, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubServer}}, true))
	scope = manager.NewRequestScope()
	for range 3 {
		require.Nil(t, manager.HandleUnsubscribeScoped(registration, scope, types.SubscriptionRequest{
			Streams: []types.SubscriptionType{types.SubLedger},
		}))
	}
	rpcErr = manager.HandleUnsubscribeScoped(registration, scope, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	})
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
	assert.False(t, registration.Snapshot().Has(types.SubLedger))
	assert.True(t, registration.Snapshot().Has(types.SubServer))
}

func TestRawElementCapRetainsEarlierFieldMutations(t *testing.T) {
	manager, err := NewManagerWithLimits(Limits{MaxItemsPerRequest: 3, MaxItemsPerConnection: 8, MaxItemsGlobal: 16})
	require.NoError(t, err)
	_, registration := attach(t, manager, "raw-fields", 1)
	request := types.SubscriptionRequest{
		Streams:  []types.SubscriptionType{types.SubLedger},
		Accounts: []string{testIssuer, testIssuer, testIssuer},
	}
	rpcErr := manager.HandleSubscribe(registration, request, true)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
	assert.True(t, registration.Snapshot().Has(types.SubLedger))
	assert.Empty(t, registration.Snapshot().Accounts(types.SubAccounts))

	book := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"USD","issuer":"`+testIssuer+`"},"taker_gets":{"currency":"XRP"}}]}`)
	require.Nil(t, manager.HandleSubscribe(registration, book, true))
	scope := manager.NewRequestScope()
	require.Nil(t, manager.HandleUnsubscribeScoped(registration, scope, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}))
	for range 2 {
		require.Nil(t, manager.HandleUnsubscribeScoped(registration, scope, book))
	}
	rpcErr = manager.HandleUnsubscribeScoped(registration, scope, book)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
	assert.False(t, registration.Snapshot().Has(types.SubLedger))
	assert.Zero(t, registration.Snapshot().BookCount())
}

func TestRawWireElementCapStopsAndRetainsIncrementalMutations(t *testing.T) {
	manager, err := NewManagerWithLimits(Limits{MaxItemsPerRequest: 3, MaxItemsPerConnection: 8, MaxItemsGlobal: 16})
	require.NoError(t, err)
	_, registration := attach(t, manager, "raw-wire", 1)
	rpcErr := manager.HandleSubscribe(registration, decodeRequest(t, `{"streams":["ledger","ledger","ledger","ledger"]}`), true)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
	assert.True(t, registration.Snapshot().Has(types.SubLedger))

	bookBody := `{"taker_pays":{"currency":"USD","issuer":"` + testIssuer + `"},"taker_gets":{"currency":"XRP"}}`
	rpcErr = manager.HandleSubscribe(registration, decodeRequest(t, `{"books":[`+bookBody+`,`+bookBody+`,`+bookBody+`,`+bookBody+`]}`), true)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
	assert.Equal(t, 1, registration.Snapshot().BookCount())

	rpcErr = manager.HandleUnsubscribe(registration, decodeRequest(t, `{"books":[`+bookBody+`,`+bookBody+`,`+bookBody+`,`+bookBody+`]}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcTOO_BUSY, rpcErr.Code)
	assert.Zero(t, registration.Snapshot().BookCount())
	require.NoError(t, manager.checkInvariants())
}

func TestLimitsRejectInvalidConfiguration(t *testing.T) {
	for _, limits := range []Limits{
		{},
		{MaxItemsPerRequest: 2, MaxItemsPerConnection: 1, MaxItemsGlobal: 3},
		{MaxItemsPerRequest: 1, MaxItemsPerConnection: 3, MaxItemsGlobal: 2},
	} {
		_, err := NewManagerWithLimits(limits)
		require.Error(t, err)
	}
}

func TestRegistrationGenerationAndDetachFence(t *testing.T) {
	manager := NewManager()
	oldConnection := NewConnection("same", make(chan []byte, 1))
	old, ok := manager.Attach(oldConnection)
	require.True(t, ok)
	_, duplicate := manager.Attach(NewConnection("same", make(chan []byte, 1)))
	assert.False(t, duplicate)
	require.True(t, manager.Detach(old))

	newConnection := NewConnection("same", make(chan []byte, 1))
	replacement, ok := manager.Attach(newConnection)
	require.True(t, ok)
	t.Cleanup(func() { manager.Detach(replacement) })
	assert.False(t, manager.Detach(old))
	rpcErr := manager.HandleSubscribe(old, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	require.Nil(t, manager.HandleSubscribe(replacement, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true))
	assert.True(t, replacement.Snapshot().Has(types.SubLedger))
}

func TestDetachFencesCollectedDelivery(t *testing.T) {
	manager := NewManager()
	connection, registration := attach(t, manager, "fence", 2)
	require.Nil(t, manager.HandleSubscribe(registration, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true))
	encodeEntered := make(chan struct{})
	releaseEncode := make(chan struct{})
	connection.SetEncodeOutbound(func(data []byte) []byte {
		close(encodeEntered)
		<-releaseEncode
		return data
	})
	broadcastDone := make(chan struct{})
	go func() {
		manager.BroadcastToStream(types.SubLedger, []byte("before"))
		close(broadcastDone)
	}()
	<-encodeEntered
	detachDone := make(chan bool, 1)
	go func() { detachDone <- manager.Detach(registration) }()
	tombstoned := make(chan struct{})
	go func() {
		for {
			manager.mu.RLock()
			detaching := registration.record.detaching
			manager.mu.RUnlock()
			if detaching {
				close(tombstoned)
				return
			}
			runtime.Gosched()
		}
	}()
	<-tombstoned
	_, duplicate := manager.Attach(NewConnection("fence", make(chan []byte, 1)))
	assert.False(t, duplicate)
	close(releaseEncode)
	<-broadcastDone
	require.True(t, <-detachDone)
	manager.BroadcastToStream(types.SubLedger, []byte("after"))
	assert.Equal(t, []byte("before"), <-connection.Outbound())
	select {
	case message := <-connection.Outbound():
		t.Fatalf("enqueue after detach: %q", message)
	default:
	}
	replacement, attached := manager.Attach(NewConnection("fence", make(chan []byte, 1)))
	require.True(t, attached)
	t.Cleanup(func() { manager.Detach(replacement) })
}

func TestConcurrentIndexLifecycleMaintainsInvariants(t *testing.T) {
	manager := NewManager()
	const connections = 24
	registrations := make([]*Registration, 0, connections)
	for i := range connections {
		connection := NewConnection("race-"+strconv.Itoa(i), make(chan []byte, 4))
		registration, attached := manager.Attach(connection)
		require.True(t, attached)
		registrations = append(registrations, registration)
	}
	bookRequest := decodeRequest(t, `{"books":[{"taker_pays":{"currency":"USD","issuer":"`+testIssuer+`"},"taker_gets":{"currency":"XRP"},"both":true}]}`)
	accountRequest := types.SubscriptionRequest{Accounts: []string{testIssuer}, Streams: []types.SubscriptionType{types.SubLedger}}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, registration := range registrations {
		workers.Add(1)
		go func(registration *Registration) {
			defer workers.Done()
			<-start
			for range 40 {
				require.Nil(t, manager.HandleSubscribe(registration, accountRequest, true))
				require.Nil(t, manager.HandleSubscribe(registration, bookRequest, true))
				require.Nil(t, manager.HandleUnsubscribe(registration, bookRequest))
				require.Nil(t, manager.HandleUnsubscribe(registration, accountRequest))
			}
			require.True(t, manager.Detach(registration))
		}(registration)
	}
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for range 200 {
				manager.BroadcastToStream(types.SubLedger, []byte("ledger"))
				manager.BroadcastToAccountsVersioned([]byte("v1"), []byte("v2"), []string{testIssuer})
				manager.BroadcastToOrderBooksVersioned([]byte("v1"), []byte("v2"), []types.OrderBookSpec{{
					TakerPays: types.CurrencySpec{Currency: "USD", Issuer: testIssuer},
					TakerGets: types.CurrencySpec{Currency: "XRP"},
				}})
			}
		}()
	}
	close(start)
	workers.Wait()
	require.NoError(t, manager.checkInvariants())
	assert.Zero(t, manager.Metrics().Connections)
	assert.Zero(t, manager.items)
}

func TestIndexedFanoutUnionsTargetsExactlyOnce(t *testing.T) {
	manager := NewManager()
	connection, registration := attach(t, manager, "union", 4)
	account := testIssuer
	require.Nil(t, manager.HandleSubscribe(registration, types.SubscriptionRequest{
		Accounts:         []string{account, account},
		AccountsProposed: []string{account},
	}, true))
	manager.BroadcastToAcceptedAccountsVersioned([]byte("v1"), []byte("v2"), []string{account, account})
	assert.Equal(t, []byte("v1"), <-connection.Outbound())
	select {
	case duplicate := <-connection.Outbound():
		t.Fatalf("duplicate delivery: %q", duplicate)
	default:
	}
}

func TestConcurrentSaturationDisconnectsExactlyOnce(t *testing.T) {
	connection := NewConnection("slow", make(chan []byte, 1))
	require.True(t, connection.TrySend([]byte("full")))
	var callbacks atomic.Int32
	connection.SetDisconnect(func() { callbacks.Add(1) })
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			connection.TrySend([]byte("drop"))
		}()
	}
	workers.Wait()
	stats := connection.Stats()
	assert.True(t, stats.Terminal)
	assert.Equal(t, uint64(1), stats.Disconnects)
	assert.Equal(t, int32(1), callbacks.Load())
}

func TestManagerMetricsAggregateDeliveryOutcomes(t *testing.T) {
	manager := NewManager()
	_, registration := attach(t, manager, "metrics", 1)
	require.Nil(t, manager.HandleSubscribe(registration, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true))
	manager.BroadcastToStream(types.SubLedger, []byte("queued"))
	for range MaxConsecutiveDrops {
		manager.BroadcastToStream(types.SubLedger, []byte("dropped"))
	}
	metrics := manager.Metrics()
	assert.Equal(t, uint64(1), metrics.Connections)
	assert.Equal(t, uint64(1), metrics.Items)
	assert.Equal(t, uint64(1), metrics.DeliveriesQueued)
	assert.Equal(t, uint64(MaxConsecutiveDrops), metrics.DeliveriesDropped)
	assert.Equal(t, uint64(1), metrics.DeliveryDisconnects)
}

func BenchmarkIndexedStreamFanoutUnrelatedState(b *testing.B) {
	for _, unrelated := range []int{0, 10_000} {
		b.Run(strconv.Itoa(unrelated), func(b *testing.B) {
			manager := NewManager()
			_, target := attach(b, manager, "target", 1)
			require.Nil(b, manager.HandleSubscribe(target, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true))
			for i := range unrelated {
				connection := NewConnection("other-"+strconv.Itoa(i), make(chan []byte, 1))
				registration, ok := manager.Attach(connection)
				require.True(b, ok)
				require.Nil(b, manager.HandleSubscribe(registration, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubServer}}, true))
			}
			b.ResetTimer()
			for range b.N {
				manager.collectStreamTargets(types.SubLedger)
			}
		})
	}
}

func BenchmarkIndexedAccountFanoutUnrelatedState(b *testing.B) {
	for _, unrelated := range []int{0, 10_000} {
		b.Run(strconv.Itoa(unrelated), func(b *testing.B) {
			manager := NewManager()
			_, target := attach(b, manager, "target", 1)
			require.Nil(b, manager.HandleSubscribe(target, types.SubscriptionRequest{Accounts: []string{testIssuer}}, true))
			for i := range unrelated {
				connection := NewConnection("other-"+strconv.Itoa(i), make(chan []byte, 1))
				registration, ok := manager.Attach(connection)
				require.True(b, ok)
				require.Nil(b, manager.HandleSubscribe(registration, types.SubscriptionRequest{Accounts: []string{testAccountB}}, true))
			}
			b.ResetTimer()
			for range b.N {
				manager.collectAccountTargets(types.SubAccounts, []string{testIssuer})
			}
		})
	}
}

func BenchmarkIndexedBookFanoutUnrelatedState(b *testing.B) {
	targetBook := types.OrderBookSpec{
		TakerPays: types.CurrencySpec{Currency: "USD", Issuer: testIssuer},
		TakerGets: types.CurrencySpec{Currency: "XRP"},
	}
	for _, unrelated := range []int{0, 10_000} {
		b.Run(strconv.Itoa(unrelated), func(b *testing.B) {
			manager := NewManager()
			_, target := attach(b, manager, "target", 1)
			require.Nil(b, manager.HandleSubscribe(target, types.SubscriptionRequest{Books: []types.BookRequest{{
				TakerPays: json.RawMessage(`{"currency":"USD","issuer":"` + testIssuer + `"}`),
				TakerGets: json.RawMessage(`{"currency":"XRP"}`),
			}}}, true))
			for i := range unrelated {
				connection := NewConnection("other-"+strconv.Itoa(i), make(chan []byte, 1))
				registration, ok := manager.Attach(connection)
				require.True(b, ok)
				require.Nil(b, manager.HandleSubscribe(registration, types.SubscriptionRequest{Books: []types.BookRequest{{
					TakerPays: json.RawMessage(`{"currency":"EUR","issuer":"` + testIssuer + `"}`),
					TakerGets: json.RawMessage(`{"currency":"XRP"}`),
				}}}, true))
			}
			b.ResetTimer()
			for range b.N {
				manager.collectOrderBookTargets([]types.OrderBookSpec{targetBook})
			}
		})
	}
}
