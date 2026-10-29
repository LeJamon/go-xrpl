package subscription

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// Based on rippled Subscribe_test.cpp testSubErrors (mpt_issuances cases).
func TestMPTIssuancesRejectMalformedRequests(t *testing.T) {
	bodies := []string{
		`{"mpt_issuances":null}`,
		`{"mpt_issuances":1}`,
		`{"mpt_issuances":1.1}`,
		`{"mpt_issuances":true}`,
		`{"mpt_issuances":"` + testMPTA + `"}`,
		`{"mpt_issuances":{}}`,
		`{"mpt_issuances":[]}`,
		`{"mpt_issuances":[1]}`,
		`{"mpt_issuances":["not-an-mpt-issuance-id"]}`,
		`{"mpt_issuances":["0123456789ABCDEF"]}`,
		`{"mpt_issuances":["` + testMPTA + `",1]}`,
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			manager := NewManager()
			_, registration := attach(t, manager, "mpt", 1)
			for _, rpcErr := range []*rpcerrors.RpcError{
				manager.HandleSubscribe(registration, decodeRequest(t, body), false),
				manager.HandleUnsubscribe(registration, decodeRequest(t, body)),
			} {
				require.NotNil(t, rpcErr)
				assert.Equal(t, "invalidParams", rpcErr.ErrorString)
				assert.Equal(t, "Invalid parameters.", rpcErr.Message)
			}
			assert.False(t, manager.HasMPTIssuanceSubscribers())
		})
	}
}

func TestMPTIssuancesSubscribeDeliverAndUnsubscribe(t *testing.T) {
	manager := NewManager()
	both, bothRegistration := attach(t, manager, "both", 4)
	onlyB, onlyBRegistration := attach(t, manager, "only-b", 4)

	require.Nil(t, manager.HandleSubscribe(bothRegistration, decodeRequest(t, `{"mpt_issuances":["`+testMPTA+`","`+strings.ToLower(testMPTB)+`","`+testMPTA+`"]}`), false))
	require.Nil(t, manager.HandleSubscribe(onlyBRegistration, decodeRequest(t, `{"mpt_issuances":["`+testMPTB+`"]}`), false))
	require.True(t, manager.HasMPTIssuanceSubscribers())
	assert.Equal(t, 2, bothRegistration.Snapshot().ItemCount())
	require.NoError(t, manager.checkInvariants())

	manager.BroadcastToMPTIssuancesVersioned([]byte("v1"), []byte("v2"), []string{testMPTA, testMPTB})
	assert.Equal(t, []byte("v1"), <-both.Outbound())
	assert.Equal(t, []byte("v1"), <-onlyB.Outbound())
	assert.Empty(t, both.Outbound())

	require.Nil(t, manager.HandleUnsubscribe(bothRegistration, decodeRequest(t, `{"mpt_issuances":["`+testMPTA+`"]}`)))
	manager.BroadcastToMPTIssuancesVersioned([]byte("v1"), []byte("v2"), []string{testMPTA})
	assert.Empty(t, both.Outbound())
	assert.Empty(t, onlyB.Outbound())

	require.Nil(t, manager.HandleUnsubscribe(bothRegistration, decodeRequest(t, `{"mpt_issuances":["`+testMPTB+`"]}`)))
	require.Nil(t, manager.HandleUnsubscribe(onlyBRegistration, decodeRequest(t, `{"mpt_issuances":["`+testMPTB+`"]}`)))
	assert.False(t, manager.HasMPTIssuanceSubscribers())
	require.NoError(t, manager.checkInvariants())
}

func TestMPTIssuancesAreNotAStream(t *testing.T) {
	manager := NewManager()
	_, registration := attach(t, manager, "stream", 1)
	rpcErr := manager.HandleSubscribe(registration, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubMPTIssuances},
	}, false)
	require.NotNil(t, rpcErr)
	assert.Equal(t, "malformedStream", rpcErr.ErrorString)
}

// Duplicate issuance IDs count toward the per-request raw element cap before
// they are deduplicated, as account lists do, on both request forms.
func TestMPTIssuancesRawCapCountsDuplicates(t *testing.T) {
	for name, request := range map[string]types.SubscriptionRequest{
		"typed": {MPTIssuances: []string{testMPTA, testMPTA, testMPTA, testMPTA}},
		"wire":  decodeRequest(t, `{"mpt_issuances":["`+testMPTA+`","`+testMPTA+`","`+testMPTA+`","`+testMPTA+`"]}`),
	} {
		t.Run(name, func(t *testing.T) {
			manager := newManager(limits{maxItemsPerRequest: 3, maxItemsPerConnection: 8, maxItemsGlobal: 16})
			_, registration := attach(t, manager, "raw-mpt", 1)
			rpcErr := manager.HandleSubscribe(registration, request, false)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcTOO_BUSY, rpcErr.Code)
			assert.False(t, manager.HasMPTIssuanceSubscribers())
		})
	}
}
