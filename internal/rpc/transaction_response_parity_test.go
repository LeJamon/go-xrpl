package rpc

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

const (
	responseParityAmountMPTID  = "0123456789abcdef0123456789abcdef0123456789abcdef"
	responseParitySendMaxMPTID = "abcdef0123456789abcdef0123456789abcdef0123456789"
	responseParityPathMPTID    = "fedcba9876543210fedcba9876543210fedcba9876543210"
	responseParityAccount      = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	responseParityDestination  = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	responseParityLedger       = uint32(4_594_095)
	responseParityTxHash       = "A2FE8D4AF3FCC3944DDF6CD8CDDC5E3F0AD50863EF8919AFEF10CB6408CD4D05"
)

func responseParityPaymentFixture(t *testing.T) (txBlob, metaBlob, closedTxData []byte) {
	t.Helper()

	txJSON := map[string]any{
		"Account":     responseParityAccount,
		"Amount":      map[string]any{"mpt_issuance_id": responseParityAmountMPTID, "value": "100"},
		"Destination": responseParityDestination,
		"Fee":         "10",
		"Paths": []any{[]any{
			map[string]any{"account": responseParityAccount, "mpt_issuance_id": responseParityPathMPTID},
		}},
		"SendMax":         map[string]any{"mpt_issuance_id": responseParitySendMaxMPTID, "value": "200"},
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "Payment",
	}
	meta := map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  uint32(0),
		"TransactionResult": "tesSUCCESS",
	}
	txBlob = encodeSyntheticRPCObject(t, txJSON)
	metaBlob = encodeSyntheticRPCObject(t, meta)

	vlTx, err := txcore.EncodeWithVL(txBlob)
	require.NoError(t, err)
	vlMeta, err := txcore.EncodeWithVL(metaBlob)
	require.NoError(t, err)
	closedTxData = append(vlTx, vlMeta...)
	return txBlob, metaBlob, closedTxData
}

func responseParityAssertProjection(t *testing.T, txJSON map[string]any, apiVersion int) {
	t.Helper()

	amount := txJSON["Amount"]
	deliverMax, ok := txJSON["DeliverMax"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, strings.ToUpper(responseParityAmountMPTID), deliverMax["mpt_issuance_id"])
	require.Equal(t, "100", deliverMax["value"])

	if apiVersion == types.ApiVersion1 {
		amountMap, ok := amount.(map[string]any)
		require.True(t, ok)
		require.Equal(t, strings.ToUpper(responseParityAmountMPTID), amountMap["mpt_issuance_id"])
	} else {
		require.NotContains(t, txJSON, "Amount")
	}

	sendMax, ok := txJSON["SendMax"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, strings.ToUpper(responseParitySendMaxMPTID), sendMax["mpt_issuance_id"])
	require.Equal(t, "200", sendMax["value"])

	paths, ok := txJSON["Paths"].([]any)
	require.True(t, ok)
	require.Len(t, paths, 1)
	path, ok := paths[0].([]any)
	require.True(t, ok)
	require.Len(t, path, 1)
	step, ok := path[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, responseParityAccount, step["account"])
	require.Equal(t, strings.ToUpper(responseParityPathMPTID), step["mpt_issuance_id"])
	require.Equal(t, 65, step["type"])
	require.NotContains(t, step, "type_hex")
}

func responseParityAssertDeliveredAmount(t *testing.T, meta map[string]any) {
	t.Helper()

	delivered, ok := meta["delivered_amount"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, strings.ToUpper(responseParityAmountMPTID), delivered["mpt_issuance_id"])
	require.Equal(t, "100", delivered["value"])
}

func TestTransactionResponseMPTProjection(t *testing.T) {
	_, _, closedTxData := responseParityPaymentFixture(t)

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		t.Run("api_v"+strconv.Itoa(apiVersion), func(t *testing.T) {
			mock := newMockLedgerServiceTx()
			mock.transactions[responseParityTxHash] = &types.TransactionInfo{
				TxData:      closedTxData,
				LedgerIndex: responseParityLedger,
				Validated:   true,
			}
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: apiVersion,
				Services:   servicesForTx(mock),
			}

			result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, json.RawMessage(`{"transaction":"`+responseParityTxHash+`"}`))
			require.Nil(t, rpcErr)
			response := result.(map[string]any)
			responseTx := response
			if apiVersion > 1 {
				responseTx = response["tx_json"].(map[string]any)
			}
			responseParityAssertProjection(t, responseTx, apiVersion)
			responseParityAssertDeliveredAmount(t, response["meta"].(map[string]any))
		})
	}
}

func TestAccountTxResponseMPTProjection(t *testing.T) {
	txBlob, metaBlob, _ := responseParityPaymentFixture(t)

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		t.Run("api_v"+strconv.Itoa(apiVersion), func(t *testing.T) {
			mock := newAccountTxMock()
			mock.getAccountTransactionsFn = func(context.Context, string, int64, int64, uint32, *types.AccountTxMarker, bool) (*types.AccountTxResult, error) {
				return &types.AccountTxResult{
					Account: responseParityAccount,
					Transactions: []types.AccountTransaction{{
						Hash:        [32]byte{1},
						LedgerIndex: responseParityLedger,
						TxBlob:      txBlob,
						Meta:        metaBlob,
					}},
					Validated: true,
				}, nil
			}
			mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
				require.Equal(t, responseParityLedger, seq)
				return &mockLedgerReader{seq: seq, closed: true, validated: true}, nil
			}
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: apiVersion,
				Services:   newTestServicesAccountTx(mock),
			}

			result, rpcErr := (&handlers.AccountTxMethod{}).Handle(
				ctx,
				json.RawMessage(`{"account":"`+responseParityAccount+`"}`),
			)
			require.Nil(t, rpcErr)
			transactions := result.(map[string]any)["transactions"].([]map[string]any)
			require.Len(t, transactions, 1)
			entry := transactions[0]
			responseTx := entry["tx"]
			if apiVersion > 1 {
				responseTx = entry["tx_json"].(map[string]any)
			}
			responseParityAssertProjection(t, responseTx.(map[string]any), apiVersion)
			responseParityAssertDeliveredAmount(t, entry["meta"].(map[string]any))
		})
	}
}
