package rpc

// subscribe_shape_conformance_test.go
//
// Conformance for the array-field shape distinctions rippled's testSubErrors
// (Subscribe_test.cpp:646-851) drives over the wire — present-but-empty vs
// null vs non-array — and for parsing book currencies to their 160-bit form.
// These cases only exist when the request is JSON-decoded, so the tests decode
// params the way the WebSocket server does rather than building a typed struct.

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const shapeTestAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

// mustDecodeSubscriptionRequest decodes JSON params the way websocket.go does,
// so the manager sees the same presence/shape information it does in
// production. A typed SubscriptionRequest built in Go cannot express null,
// a non-array value, or a present-but-empty array.
func mustDecodeSubscriptionRequest(t *testing.T, params string) types.SubscriptionRequest {
	t.Helper()
	var req types.SubscriptionRequest
	require.NoError(t, json.Unmarshal([]byte(params), &req))
	return req
}

type shapeCase struct {
	name    string
	params  string
	wantErr bool
	code    int
	token   string
	message string
}

var subscribeShapeCases = []shapeCase{
	// accounts (Subscribe.cpp:192-200; Subscribe_test.cpp:666-672 empty,
	// :646-664 non-array). parseAccountIds returns an empty set for an empty
	// array, a non-string element, or a bad id → rpcACT_MALFORMED.
	{"empty accounts array", `{"accounts":[]}`, true, types.RpcACT_MALFORMED, "actMalformed", "Account malformed."},
	{"null accounts", `{"accounts":null}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
	{"string accounts", `{"accounts":"notanarray"}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
	{"object accounts", `{"accounts":{}}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
	{"non-string account element", `{"accounts":[123]}`, true, types.RpcACT_MALFORMED, "actMalformed", "Account malformed."},
	{"bad account id", `{"accounts":["notanaccount"]}`, true, types.RpcACT_MALFORMED, "actMalformed", "Account malformed."},
	// accounts_proposed, same semantics (Subscribe.cpp:181-189).
	{"empty accounts_proposed", `{"accounts_proposed":[]}`, true, types.RpcACT_MALFORMED, "actMalformed", "Account malformed."},
	{"null accounts_proposed", `{"accounts_proposed":null}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
	// streams (Subscribe.cpp:118-122 non-array, :126-127 non-string entry).
	{"non-array streams", `{"streams":"ledger"}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
	{"non-string stream entry", `{"streams":[123]}`, true, types.RpcSTREAM_MALFORMED, "malformedStream", "Stream malformed."},
	// books (Subscribe.cpp:233-234 non-array, :238 non-object entry;
	// Subscribe_test.cpp:675-690).
	{"non-array books", `{"books":"x"}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
	{"non-object book entry", `{"books":[1]}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
	// Accepted shapes: an absent field, an empty books array, and valid
	// values must all succeed.
	{"empty params", `{}`, false, 0, "", ""},
	{"empty books array", `{"books":[]}`, false, 0, "", ""},
	{"valid accounts", `{"accounts":["` + shapeTestAccount + `"]}`, false, 0, "", ""},
	{"valid stream", `{"streams":["ledger"]}`, false, 0, "", ""},
}

func TestSubscribeConformanceArrayFieldShapes(t *testing.T) {
	for _, tc := range subscribeShapeCases {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSubscriptionManager()
			conn := newTestConnection("shape-conn")
			sm.AddConnection(conn)
			defer sm.RemoveConnection(conn.ID)

			err := sm.HandleSubscribe(conn, mustDecodeSubscriptionRequest(t, tc.params), true)
			if !tc.wantErr {
				assert.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			assert.Equal(t, tc.code, err.Code)
			assert.Equal(t, tc.token, err.ErrorString)
			assert.Equal(t, tc.message, err.Message)
		})
	}
}

// TestUnsubscribeConformanceArrayFieldShapes mirrors the subscribe shape cases
// on the unsubscribe path, which rippled's testSubErrors(false) also exercises
// (Unsubscribe.cpp:63-64, 113-136, 164-242).
func TestUnsubscribeConformanceArrayFieldShapes(t *testing.T) {
	cases := []shapeCase{
		{"empty accounts array", `{"accounts":[]}`, true, types.RpcACT_MALFORMED, "actMalformed", "Account malformed."},
		{"null accounts", `{"accounts":null}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
		{"string accounts", `{"accounts":"notanarray"}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
		{"empty accounts_proposed", `{"accounts_proposed":[]}`, true, types.RpcACT_MALFORMED, "actMalformed", "Account malformed."},
		{"non-array streams", `{"streams":"ledger"}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
		{"non-string stream entry", `{"streams":[123]}`, true, types.RpcSTREAM_MALFORMED, "malformedStream", "Stream malformed."},
		{"non-object book entry", `{"books":[1]}`, true, types.RpcINVALID_PARAMS, "invalidParams", "Invalid parameters."},
		{"empty params", `{}`, false, 0, "", ""},
		{"empty books array", `{"books":[]}`, false, 0, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSubscriptionManager()
			conn := newTestConnection("shape-conn")
			sm.AddConnection(conn)
			defer sm.RemoveConnection(conn.ID)

			err := sm.HandleUnsubscribe(conn, mustDecodeSubscriptionRequest(t, tc.params), true)
			if !tc.wantErr {
				assert.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			assert.Equal(t, tc.code, err.Code)
			assert.Equal(t, tc.token, err.ErrorString)
			assert.Equal(t, tc.message, err.Message)
		})
	}
}

// TestSubscribeConformanceCurrencyParsedTo160Bit pins that book currencies are
// compared as their 160-bit value (rippled to_currency + book.in == book.out),
// not as the raw JSON string. The standard encoding of "USD" places the ISO
// code at bytes 12-14, and a 40-hex of zeroes is the XRP currency.
func TestSubscribeConformanceCurrencyParsedTo160Bit(t *testing.T) {
	const gw = shapeTestAccount
	const usdHex = "0000000000000000000000005553440000000000" // to_currency("USD")
	const xrpHex = "0000000000000000000000000000000000000000"

	subscribe := func(t *testing.T, params string) *types.RpcError {
		t.Helper()
		sm := newTestSubscriptionManager()
		conn := newTestConnection("cur-conn")
		sm.AddConnection(conn)
		defer sm.RemoveConnection(conn.ID)
		return sm.HandleSubscribe(conn, mustDecodeSubscriptionRequest(t, params), true)
	}

	t.Run("3-char and 40-hex form of one currency are the same market", func(t *testing.T) {
		params := `{"books":[{"taker_pays":{"currency":"USD","issuer":"` + gw + `"},` +
			`"taker_gets":{"currency":"` + usdHex + `","issuer":"` + gw + `"}}]}`
		err := subscribe(t, params)
		require.NotNil(t, err)
		assert.Equal(t, types.RpcBAD_MARKET, err.Code)
		assert.Equal(t, "badMarket", err.ErrorString)
	})

	t.Run("40-hex of zeroes is the XRP currency (valid market)", func(t *testing.T) {
		params := `{"books":[{"taker_pays":{"currency":"USD","issuer":"` + gw + `"},` +
			`"taker_gets":{"currency":"` + xrpHex + `"}}]}`
		assert.Nil(t, subscribe(t, params))
	})

	t.Run("40-hex of zeroes with an issuer is an illegal XRP issuer", func(t *testing.T) {
		params := `{"books":[{"taker_pays":{"currency":"USD","issuer":"` + gw + `"},` +
			`"taker_gets":{"currency":"` + xrpHex + `","issuer":"` + gw + `"}}]}`
		err := subscribe(t, params)
		require.NotNil(t, err)
		assert.Equal(t, types.RpcDST_ISR_MALFORMED, err.Code)
		assert.Equal(t, "dstIsrMalformed", err.ErrorString)
	})
}
