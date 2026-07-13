package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func encodeSyntheticRPCObject(t *testing.T, object map[string]any) []byte {
	t.Helper()

	encoded, err := binarycodec.Encode(object)
	require.NoError(t, err)
	blob, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	return blob
}

func mptSyntheticRPCFixture(t *testing.T) (map[string]any, map[string]any, []byte, []byte, string) {
	t.Helper()

	const issuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const sequence = uint32(42)
	txJSON := map[string]any{
		"Account":         issuer,
		"Fee":             "10",
		"Sequence":        sequence,
		"SigningPubKey":   "",
		"TransactionType": "MPTokenIssuanceCreate",
	}
	meta := map[string]any{
		"AffectedNodes": []any{
			map[string]any{"CreatedNode": map[string]any{
				"LedgerEntryType": "MPTokenIssuance",
				"LedgerIndex":     strings.Repeat("A", 64),
				"NewFields": map[string]any{
					"Issuer":   issuer,
					"Sequence": sequence,
				},
			}},
		},
		"TransactionIndex":  uint32(0),
		"TransactionResult": "tesSUCCESS",
	}

	txBlob := encodeSyntheticRPCObject(t, txJSON)
	metaBlob := encodeSyntheticRPCObject(t, meta)

	_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuer)
	require.NoError(t, err)
	var accountID [20]byte
	copy(accountID[:], accountBytes)
	mptID := keylet.MakeMPTID(sequence, accountID)
	want := strings.ToUpper(hex.EncodeToString(mptID[:]))

	return txJSON, meta, txBlob, metaBlob, want
}

func TestTxSyntheticMetadata(t *testing.T) {
	txJSON, meta, _, _, want := mptSyntheticRPCFixture(t)
	stored, err := json.Marshal(handlers.StoredTransaction{TxJSON: txJSON, Meta: meta})
	require.NoError(t, err)

	const txHash = "E2FE8D4AF3FCC3944DDF6CD8CDDC5E3F0AD50863EF8919AFEF10CB6408CD4D05"
	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		t.Run("api_v"+strconv.Itoa(apiVersion), func(t *testing.T) {
			mock := newMockLedgerServiceTx()
			mock.transactions[txHash] = &types.TransactionInfo{
				TxData:      stored,
				LedgerIndex: 2,
				Validated:   true,
			}
			ctx := &types.RPCContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: apiVersion,
				Services:   servicesForTx(mock),
			}

			result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, json.RawMessage(`{"transaction":"`+txHash+`"}`))
			require.Nil(t, rpcErr)
			response := result.(map[string]any)
			responseMeta := response["meta"].(map[string]any)
			require.Equal(t, want, responseMeta["mpt_issuance_id"])
		})
	}
}

func TestAccountTxSyntheticMetadata(t *testing.T) {
	_, _, txBlob, metaBlob, want := mptSyntheticRPCFixture(t)
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		t.Run("api_v"+strconv.Itoa(apiVersion), func(t *testing.T) {
			mock := newAccountTxMock()
			mock.getAccountTransactionsFn = func(context.Context, string, int64, int64, uint32, *types.AccountTxMarker, bool) (*types.AccountTxResult, error) {
				return &types.AccountTxResult{
					Account: account,
					Transactions: []types.AccountTransaction{{
						Hash:        [32]byte{1},
						LedgerIndex: 2,
						TxBlob:      txBlob,
						Meta:        metaBlob,
					}},
					Validated: true,
				}, nil
			}
			ctx := &types.RPCContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: apiVersion,
				Services:   newTestServicesAccountTx(mock),
			}

			result, rpcErr := (&handlers.AccountTxMethod{}).Handle(ctx, json.RawMessage(`{"account":"`+account+`"}`))
			require.Nil(t, rpcErr)
			response := result.(map[string]any)
			transactions := response["transactions"].([]map[string]any)
			responseMeta := transactions[0]["meta"].(map[string]any)
			require.Equal(t, want, responseMeta["mpt_issuance_id"])
		})
	}
}

func TestAccountTxDeliveredAmountUsesSourceTransaction(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	txBlob := encodeSyntheticRPCObject(t, map[string]any{
		"Account":         account,
		"Amount":          "100",
		"Destination":     "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "Payment",
	})
	metaBlob := encodeSyntheticRPCObject(t, map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  uint32(0),
		"TransactionResult": "tesSUCCESS",
	})
	mock := newAccountTxMock()
	mock.getAccountTransactionsFn = func(context.Context, string, int64, int64, uint32, *types.AccountTxMarker, bool) (*types.AccountTxResult, error) {
		return &types.AccountTxResult{
			Account: account,
			Transactions: []types.AccountTransaction{{
				Hash:        [32]byte{1},
				LedgerIndex: 4_594_095,
				TxBlob:      txBlob,
				Meta:        metaBlob,
			}},
			Validated: true,
		}, nil
	}
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion2,
		Services:   newTestServicesAccountTx(mock),
	}

	result, rpcErr := (&handlers.AccountTxMethod{}).Handle(ctx, json.RawMessage(`{"account":"`+account+`"}`))
	require.Nil(t, rpcErr)
	response := result.(map[string]any)
	entry := response["transactions"].([]map[string]any)[0]
	txJSON := entry["tx_json"].(map[string]any)
	require.Equal(t, "100", txJSON["DeliverMax"])
	require.NotContains(t, txJSON, "Amount")
	meta := entry["meta"].(map[string]any)
	require.Equal(t, "100", meta["delivered_amount"])
}

func TestTransactionEntryDoesNotInjectSyntheticMetadata(t *testing.T) {
	mock := newMockLedgerServiceTE()
	services := newTransactionEntryTestServices(mock)
	ledger := newMockLedgerReaderTE(2)
	mock.addLedger(ledger)

	tests := []struct {
		name   string
		hash   string
		txJSON map[string]any
		meta   map[string]any
		field  string
	}{
		{
			name: "delivered amount",
			hash: strings.Repeat("A", 64),
			txJSON: map[string]any{
				"Amount":          "100",
				"TransactionType": "Payment",
			},
			meta:  map[string]any{"TransactionResult": "tesSUCCESS"},
			field: "delivered_amount",
		},
		{
			name: "MPT issuance ID",
			hash: strings.Repeat("B", 64),
			txJSON: map[string]any{
				"TransactionType": "MPTokenIssuanceCreate",
			},
			meta: map[string]any{
				"TransactionResult": "tesSUCCESS",
				"AffectedNodes": []any{
					map[string]any{"CreatedNode": map[string]any{
						"LedgerEntryType": "MPTokenIssuance",
						"NewFields": map[string]any{
							"Issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
							"Sequence": float64(42),
						},
					}},
				},
			},
			field: "mpt_issuance_id",
		},
	}

	for _, tc := range tests {
		stored, err := json.Marshal(handlers.StoredTransaction{TxJSON: tc.txJSON, Meta: tc.meta})
		require.NoError(t, err)
		mock.transactions[tc.hash] = &types.TransactionInfo{
			TxData:      stored,
			LedgerIndex: 2,
			Validated:   true,
		}

		for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
			t.Run(tc.name+"/api_v"+strconv.Itoa(apiVersion), func(t *testing.T) {
				ctx := &types.RPCContext{
					Context:    context.Background(),
					Role:       types.RoleGuest,
					ApiVersion: apiVersion,
					Services:   services,
				}
				params, err := json.Marshal(map[string]any{"tx_hash": tc.hash, "ledger_index": 2})
				require.NoError(t, err)

				result, rpcErr := (&handlers.TransactionEntryMethod{}).Handle(ctx, params)
				require.Nil(t, rpcErr)
				response := result.(map[string]any)
				metaKey := "metadata"
				if apiVersion > 1 {
					metaKey = "meta"
				}
				responseMeta := response[metaKey].(map[string]any)
				require.NotContains(t, responseMeta, tc.field)
			})
		}
	}
}
