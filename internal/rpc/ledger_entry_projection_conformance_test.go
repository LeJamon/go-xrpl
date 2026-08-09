package rpc

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLedgerEntryProjectionUsesResolvedLedgerState(t *testing.T) {
	mock := newMockLedgerEntryService()
	method, ctx := ledgerEntryParserContext(mock)
	const index = "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"
	mock.ledgerEntryResult = &types.LedgerEntryResult{
		Index:       "SERVICE_RESULT_INDEX_IS_NOT_AUTHORITATIVE",
		LedgerIndex: 99,
		LedgerHash:  [32]byte{0xAA},
		Node:        []byte(`{"LedgerEntryType":"AccountRoot","Balance":"1"}`),
		Validated:   true,
	}

	result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
		"index":        index,
		"ledger_index": 3,
	})
	require.Nil(t, rpcErr)
	response, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, index, response["index"])
	assert.Equal(t, uint32(3), response["ledger_current_index"])
	assert.Equal(t, false, response["validated"])
	assert.NotContains(t, response, "ledger_hash")
	assert.NotContains(t, response, "ledger_index")

	node, ok := response["node"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, index, node["index"])
}

func TestLedgerEntryProjectionAddsMPTokenIssuanceFields(t *testing.T) {
	mock := newMockLedgerEntryService()
	method, ctx := ledgerEntryParserContext(mock)
	const index = "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"
	mock.ledgerEntryResult = &types.LedgerEntryResult{
		Node: []byte(`{"LedgerEntryType":"MPTokenIssuance","Sequence":16909060,"Issuer":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`),
	}

	result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{"index": index})
	require.Nil(t, rpcErr)
	response, ok := result.(map[string]any)
	require.True(t, ok)
	node, ok := response["node"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, index, node["index"])
	assert.Equal(t, "01020304B5F762798A53D543A014CAF8B297CFF8F2F937E8", node["mpt_issuance_id"])
}

func TestLedgerEntryBinaryUsesJsonCppAsBoolCoercion(t *testing.T) {
	mock := newMockLedgerEntryService()
	method, ctx := ledgerEntryParserContext(mock)
	const index = "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"
	mock.ledgerEntryResult = &types.LedgerEntryResult{
		Node:       []byte(`{"LedgerEntryType":"AccountRoot"}`),
		NodeBinary: "ABCD",
	}

	tests := []struct {
		name   string
		binary any
		want   bool
	}{
		{"true boolean", true, true},
		{"nonzero integer", 1, true},
		{"nonzero real", 0.5, true},
		{"nonempty string", "false", true},
		{"nonempty array", []any{0}, true},
		{"nonempty object", map[string]any{"value": 0}, true},
		{"false boolean", false, false},
		{"zero number", 0, false},
		{"empty string", "", false},
		{"string beginning with NUL", "\x00truthy-tail", false},
		{"empty array", []any{}, false},
		{"empty object", map[string]any{}, false},
		{"null", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
				"index":        index,
				"ledger_index": "validated",
				"binary":       tc.binary,
			})
			require.Nil(t, rpcErr)
			response, ok := result.(map[string]any)
			require.True(t, ok)
			if tc.want {
				assert.Equal(t, "ABCD", response["node_binary"])
				assert.NotContains(t, response, "node")
			} else {
				assert.Contains(t, response, "node")
				assert.NotContains(t, response, "node_binary")
			}
		})
	}
}

func TestLedgerEntryErrorProjectionPreservesLedgerMetadata(t *testing.T) {
	mock := newMockLedgerEntryService()
	method, ctx := ledgerEntryParserContext(mock)
	const index = "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"
	validatedHash := handlers.FormatLedgerHash([32]byte{0x4B, 0xC5, 0x0C, 0x9B})

	t.Run("entryNotFound on validated ledger", func(t *testing.T) {
		mock.ledgerEntryResult = nil
		mock.ledgerEntryErr = svcerr.ErrLedgerEntryNotFound
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
			"index":        index,
			"ledger_index": "validated",
		})
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcENTRY_NOT_FOUND, rpcErr.Code)
		assert.Equal(t, index, rpcErr.Extra["index"])
		assert.Equal(t, uint32(2), rpcErr.Extra["ledger_index"])
		assert.Equal(t, validatedHash, rpcErr.Extra["ledger_hash"])
		assert.Equal(t, true, rpcErr.Extra["validated"])
		assert.NotContains(t, rpcErr.Extra, "ledger_current_index")
	})

	t.Run("entryNotFound on numerically selected open ledger", func(t *testing.T) {
		mock.ledgerEntryResult = nil
		mock.ledgerEntryErr = svcerr.ErrLedgerEntryNotFound
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
			"index":        index,
			"ledger_index": 3,
		})
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcENTRY_NOT_FOUND, rpcErr.Code)
		assert.Equal(t, index, rpcErr.Extra["index"])
		assert.Equal(t, uint32(3), rpcErr.Extra["ledger_current_index"])
		assert.Equal(t, false, rpcErr.Extra["validated"])
		assert.NotContains(t, rpcErr.Extra, "ledger_hash")
		assert.NotContains(t, rpcErr.Extra, "ledger_index")
	})

	t.Run("unexpectedLedgerType", func(t *testing.T) {
		mock.ledgerEntryErr = nil
		mock.ledgerEntryResult = &types.LedgerEntryResult{
			Node: []byte(`{"LedgerEntryType":"AccountRoot"}`),
		}
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
			"check":        index,
			"ledger_index": "validated",
		})
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcUNEXPECTED_LEDGER_TYPE, rpcErr.Code)
		assert.Equal(t, index, rpcErr.Extra["index"])
		assert.Equal(t, uint32(2), rpcErr.Extra["ledger_index"])
		assert.Equal(t, validatedHash, rpcErr.Extra["ledger_hash"])
		assert.Equal(t, true, rpcErr.Extra["validated"])
	})
}
