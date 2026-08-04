package rpc

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTxMethodOpenTransactionResponseOmitsClosedFields(t *testing.T) {
	txBytes, err := binarycodec.EncodeBytes(validStoredPaymentTransaction())
	require.NoError(t, err)
	openData, err := txcore.EncodeWithVL(txBytes)
	require.NoError(t, err)

	const hash = "1111111111111111111111111111111111111111111111111111111111111111"
	mock := newMockLedgerServiceTx()
	mock.transactions[hash] = &types.TransactionInfo{
		TxData:      openData,
		Validated:   false,
		LedgerIndex: 0,
	}

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		for _, binary := range []bool{false, true} {
			t.Run(apiVersionName(apiVersion, binary), func(t *testing.T) {
				ctx := &types.RpcContext{
					Context:    context.Background(),
					Role:       types.RoleGuest,
					ApiVersion: apiVersion,
					Services:   servicesForTx(mock),
				}
				result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, txRequest(hash, binary))
				require.Nil(t, rpcErr)
				response, ok := result.(map[string]any)
				require.True(t, ok)
				assert.Equal(t, hash, response["hash"])
				assert.Equal(t, false, response["validated"])
				for _, field := range []string{"meta", "meta_blob", "ledger_hash", "ledger_index", "inLedger", "date", "close_time_iso", "ctid"} {
					assert.NotContains(t, response, field, "open tx must omit %s", field)
				}
				if binary {
					if apiVersion == types.ApiVersion1 {
						assert.Contains(t, response, "tx")
					} else {
						assert.Contains(t, response, "tx_blob")
					}
				} else if apiVersion == types.ApiVersion1 {
					assert.Equal(t, "Payment", response["TransactionType"])
				} else {
					txJSON, ok := response["tx_json"].(map[string]any)
					require.True(t, ok)
					assert.Equal(t, "Payment", txJSON["TransactionType"])
				}
			})
		}
	}
}

func txRequest(hash string, binary bool) []byte {
	request := `{"transaction":"` + hash + `"}`
	if binary {
		request = `{"transaction":"` + hash + `","binary":true}`
	}
	return []byte(request)
}

func apiVersionName(version int, binary bool) string {
	name := "v1"
	if version > 1 {
		name = "v2"
	}
	if binary {
		return name + "-binary"
	}
	return name + "-json"
}

func TestTxMethodOpenTransactionRejectsMetadata(t *testing.T) {
	txBytes, err := binarycodec.EncodeBytes(validStoredPaymentTransaction())
	require.NoError(t, err)
	vlTx, err := txcore.EncodeWithVL(txBytes)
	require.NoError(t, err)
	vlMeta, err := txcore.EncodeWithVL([]byte{0x01})
	require.NoError(t, err)
	openData := append(append([]byte(nil), vlTx...), vlMeta...)

	const hash = "2222222222222222222222222222222222222222222222222222222222222222"
	mock := newMockLedgerServiceTx()
	mock.transactions[hash] = &types.TransactionInfo{TxData: openData}
	ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleGuest, ApiVersion: types.ApiVersion2, Services: servicesForTx(mock)}
	result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, txRequest(hash, false))
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcDB_DESERIALIZATION, rpcErr.Code)
}

func TestTxMethodCorruptLookupMapsDBDeserialization(t *testing.T) {
	const hash = "5555555555555555555555555555555555555555555555555555555555555555"
	mock := newMockLedgerServiceTx()
	mock.txLookupError = fmt.Errorf("%w: cached transaction hash mismatch", svcerr.ErrTxnDataCorrupt)
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion2,
		Services:   servicesForTx(mock),
	}
	result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, txRequest(hash, false))
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcDB_DESERIALIZATION, rpcErr.Code)
}

func TestTxMethodOpenTransactionBinaryIsCanonical(t *testing.T) {
	txBytes, err := binarycodec.EncodeBytes(validStoredPaymentTransaction())
	require.NoError(t, err)
	openData, err := txcore.EncodeWithVL(txBytes)
	require.NoError(t, err)
	const hash = "3333333333333333333333333333333333333333333333333333333333333333"
	mock := newMockLedgerServiceTx()
	mock.transactions[hash] = &types.TransactionInfo{TxData: openData}
	ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleGuest, ApiVersion: types.ApiVersion2, Services: servicesForTx(mock)}
	result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, txRequest(hash, true))
	require.Nil(t, rpcErr)
	response := result.(map[string]any)
	blob, ok := response["tx_blob"].(string)
	require.True(t, ok)
	decoded, err := hex.DecodeString(blob)
	require.NoError(t, err)
	canonical, err := binarycodec.EncodeBytes(validStoredPaymentTransaction())
	require.NoError(t, err)
	assert.Equal(t, canonical, decoded)
}

func TestTxMethodClosedTransactionRetainsRealMetadataAndLedgerFields(t *testing.T) {
	txBytes, err := binarycodec.EncodeBytes(validStoredPaymentTransaction())
	require.NoError(t, err)
	meta := validStoredMetadata()
	meta["TransactionIndex"] = uint32(7)
	metaBytes, err := binarycodec.EncodeBytes(meta)
	require.NoError(t, err)
	vlTx, err := txcore.EncodeWithVL(txBytes)
	require.NoError(t, err)
	vlMeta, err := txcore.EncodeWithVL(metaBytes)
	require.NoError(t, err)
	closedData := append(append([]byte(nil), vlTx...), vlMeta...)

	const hash = "4444444444444444444444444444444444444444444444444444444444444444"
	mock := newMockLedgerServiceTx()
	mock.transactions[hash] = &types.TransactionInfo{
		TxData:      closedData,
		LedgerIndex: 100,
		LedgerHash:  strings.Repeat("A", 64),
		Validated:   true,
		TxIndex:     7,
		CloseTime:   123,
	}
	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleGuest, ApiVersion: apiVersion, Services: servicesForTx(mock)}
		result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, txRequest(hash, false))
		require.Nil(t, rpcErr)
		response := result.(map[string]any)
		responseMeta, ok := response["meta"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "tesSUCCESS", responseMeta["TransactionResult"])
		assert.Equal(t, uint32(7), responseMeta["TransactionIndex"])
		assert.Equal(t, []any{}, responseMeta["AffectedNodes"])
		assert.Equal(t, true, response["validated"])
		if apiVersion == types.ApiVersion1 {
			assert.Equal(t, uint32(100), response["ledger_index"])
			assert.Equal(t, uint32(100), response["inLedger"])
			assert.Equal(t, int64(123), response["date"])
		} else {
			assert.Equal(t, uint32(100), response["ledger_index"])
			assert.Equal(t, strings.Repeat("A", 64), response["ledger_hash"])
			txJSON, ok := response["tx_json"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, uint32(100), txJSON["ledger_index"])
			assert.Equal(t, int64(123), txJSON["date"])
		}
	}
}
