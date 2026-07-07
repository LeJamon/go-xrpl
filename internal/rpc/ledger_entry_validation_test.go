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

// TestLedgerEntryLedgerSelectorValidation covers the rippled 3.0.0
// ledgerFromRequest drift: null ledger_hash / ledger_index are treated as
// absent, a non-string ledger_hash is ledgerHashNotString, and an unparseable
// ledger_index is ledgerIndexMalformed (all invalidParams, code 31).
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
			name:       "non-string ledger_hash is ledgerHashNotString",
			params:     map[string]any{"index": validIndex, "ledger_hash": 12345},
			expectErr:  true,
			expectMsg:  "ledgerHashNotString",
			expectCode: types.RpcINVALID_PARAMS,
		},
		{
			name:       "bad-hex ledger_hash is ledgerHashMalformed",
			params:     map[string]any{"index": validIndex, "ledger_hash": "not-hex"},
			expectErr:  true,
			expectMsg:  "ledgerHashMalformed",
			expectCode: types.RpcINVALID_PARAMS,
		},
		{
			name:      "null ledger_hash is treated as absent",
			params:    map[string]any{"index": validIndex, "ledger_hash": nil},
			expectErr: false,
		},
		{
			name:       "object ledger_index is ledgerIndexMalformed",
			params:     map[string]any{"index": validIndex, "ledger_index": map[string]any{"x": 1}},
			expectErr:  true,
			expectMsg:  "ledgerIndexMalformed",
			expectCode: types.RpcINVALID_PARAMS,
		},
		{
			name:       "non-integral ledger_index is ledgerIndexMalformed",
			params:     map[string]any{"index": validIndex, "ledger_index": 2.5},
			expectErr:  true,
			expectMsg:  "ledgerIndexMalformed",
			expectCode: types.RpcINVALID_PARAMS,
		},
		{
			name:      "null ledger_index is treated as absent",
			params:    map[string]any{"index": validIndex, "ledger_index": nil},
			expectErr: false,
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

	for _, key := range []string{"nft_offer", "nftoken_offer"} {
		t.Run(key, func(t *testing.T) {
			mock.ledgerEntryResult = &types.LedgerEntryResult{
				Index:       offerIndex,
				LedgerIndex: 2,
				LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
				Node:        []byte(`{"LedgerEntryType": "NFTokenOffer", "Amount": "1"}`),
				Validated:   true,
			}
			mock.ledgerEntryErr = nil

			paramsJSON, err := json.Marshal(map[string]any{
				key:            offerIndex,
				"ledger_index": "validated",
			})
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "%s should resolve to a lookup", key)
			require.NotNil(t, result)
		})
	}
}
