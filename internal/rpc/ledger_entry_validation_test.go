package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLedgerEntryLedgerSelectorValidation(t *testing.T) {
	mock := newMockLedgerEntryService()
	services := newLedgerEntryTestServices(mock)

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	tests := []struct {
		name       string
		params     map[string]any
		expectErr  bool
		expectMsg  string
		expectCode int
	}{
		{
			name:       "non-string ledger_hash is invalidParams",
			params:     map[string]any{"index": validIndex, "ledger_hash": 12345},
			expectErr:  true,
			expectMsg:  "Invalid field 'ledger_hash', not hex string.",
			expectCode: types.RpcINVALID_PARAMS,
		},
		{
			name:       "bad-hex ledger_hash is invalidParams",
			params:     map[string]any{"index": validIndex, "ledger_hash": "not-hex"},
			expectErr:  true,
			expectMsg:  "Invalid field 'ledger_hash', not hex string.",
			expectCode: types.RpcINVALID_PARAMS,
		},
		{
			name:       "null ledger_hash is invalidParams",
			params:     map[string]any{"index": validIndex, "ledger_hash": nil},
			expectErr:  true,
			expectMsg:  "Invalid field 'ledger_hash', not hex string.",
			expectCode: types.RpcINVALID_PARAMS,
		},
		{
			name:       "object ledger_index is invalidParams",
			params:     map[string]any{"index": validIndex, "ledger_index": map[string]any{"x": 1}},
			expectErr:  true,
			expectMsg:  "Invalid field 'ledger_index', not string or number.",
			expectCode: types.RpcINVALID_PARAMS,
		},
		{
			name:       "non-integral ledger_index is invalidParams",
			params:     map[string]any{"index": validIndex, "ledger_index": 2.5},
			expectErr:  true,
			expectMsg:  "Invalid field 'ledger_index', not string or number.",
			expectCode: types.RpcINVALID_PARAMS,
		},
		{
			name:       "null ledger_index is invalidParams",
			params:     map[string]any{"index": validIndex, "ledger_index": nil},
			expectErr:  true,
			expectMsg:  "Invalid field 'ledger_index', not string or number.",
			expectCode: types.RpcINVALID_PARAMS,
		},
		{
			name:      "string ledger_index keyword still works",
			params:    map[string]any{"index": validIndex, "ledger_index": "validated"},
			expectErr: false,
		},
		{
			name:      "integer ledger_index still works",
			params:    map[string]any{"index": validIndex, "ledger_index": 2},
			expectErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			if tc.expectErr {
				assert.Nil(t, result)
				require.NotNil(t, rpcErr)
				assert.Equal(t, tc.expectCode, rpcErr.Code)
				assert.Equal(t, tc.expectMsg, rpcErr.Message)
			} else {
				require.Nil(t, rpcErr, "unexpected error: %v", rpcErr)
				require.NotNil(t, result)
			}
		})
	}
}

func TestLedgerEntryLegacyAndConflictingSelectors(t *testing.T) {
	const (
		entryIndex = "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"
		ledgerHash = "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652"
	)
	mock := newMockLedgerEntryService()
	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:  context.Background(),
		Services: newLedgerEntryTestServices(mock),
	}

	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{"legacy sequence", 2, "2"},
		{"legacy hash", ledgerHash, ledgerHash},
	} {
		t.Run(test.name, func(t *testing.T) {
			params, err := json.Marshal(map[string]any{"index": entryIndex, "ledger": test.value})
			require.NoError(t, err)
			_, rpcErr := method.Handle(ctx, params)
			require.Nil(t, rpcErr)
			assert.Equal(t, test.want, mock.lastLedgerIndex)
		})
	}

	t.Run("conflicting fields", func(t *testing.T) {
		params, err := json.Marshal(map[string]any{
			"index":        entryIndex,
			"ledger_hash":  ledgerHash,
			"ledger_index": 2,
		})
		require.NoError(t, err)
		_, rpcErr := method.Handle(ctx, params)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "Exactly one of 'ledger_hash' or 'ledger_index' can be specified.", rpcErr.Message)
	})
}

// TestLedgerEntryNFTOfferAlias verifies the canonical rippled `nft_offer`
// selector and the go-xrpl `nftoken_offer` alias both resolve to a lookup and
// pass the type check for an NFTokenOffer entry.
func TestLedgerEntryNFTOfferAlias(t *testing.T) {
	mock := newMockLedgerEntryService()
	services := newLedgerEntryTestServices(mock)

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	offerIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	mock.ledgerEntryResult = &types.LedgerEntryResult{
		Index:       offerIndex,
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Node:        []byte(`{"LedgerEntryType": "NFTokenOffer", "Amount": "1"}`),
		Validated:   true,
	}
	mock.ledgerEntryErr = nil

	result, rpcErr := method.Handle(ctx, json.RawMessage(`{
		"nft_offer":"A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
		"ledger_index":"validated"
	}`))
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	ctx.ApiVersion = types.ApiVersion2
	result, rpcErr = method.Handle(ctx, json.RawMessage(`{
		"nftoken_offer":"A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
		"ledger_index":"validated"
	}`))
	require.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	assert.Equal(t, "No ledger_entry params provided.", rpcErr.Message)
}
