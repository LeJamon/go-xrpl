package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAccountInfoPseudoAccount verifies the rippled 3.0.0 pseudo_account block:
// an account root carrying a pseudo-account designator (AMMID / VaultID /
// LoanBrokerID) surfaces result.pseudo_account.type (the field name minus
// "ID"); a normal account omits it entirely.
// Reference: rippled AccountInfo.cpp:153-173 (PR #5270).
func TestAccountInfoPseudoAccount(t *testing.T) {
	mock := newMockLedgerService()
	services := newTestServices(mock)

	method := &handlers.AccountInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	designatorHash := "AE0A97F385FFE42E3096BB352C7A2AA5B0E82C90D80E4DFD0EAABD5B2E4B6E1F"

	// buildRaw serializes an AccountRoot with an optional designator field via
	// the binary codec, matching what the service supplies in RawData.
	buildRaw := func(designator string) []byte {
		obj := map[string]any{
			"LedgerEntryType": "AccountRoot",
			"Account":         validAccount,
			"Balance":         "100000000000",
			"Flags":           uint32(0),
			"OwnerCount":      uint32(0),
			"Sequence":        uint32(1),
		}
		if designator != "" {
			obj[designator] = designatorHash
		}
		encoded, err := binarycodec.Encode(obj)
		require.NoError(t, err)
		raw, err := hex.DecodeString(encoded)
		require.NoError(t, err)
		return raw
	}

	tests := []struct {
		name         string
		designator   string
		expectedType string // "" means pseudo_account must be absent
	}{
		{"AMM pseudo-account", "AMMID", "AMM"},
		{"Vault pseudo-account", "VaultID", "Vault"},
		{"LoanBroker pseudo-account", "LoanBrokerID", "LoanBroker"},
		{"normal account", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.accountInfo = &types.AccountInfo{
				Account:     validAccount,
				Balance:     "100000000000",
				Sequence:    1,
				LedgerIndex: 2,
				LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
				Validated:   true,
				RawData:     buildRaw(tc.designator),
			}
			mock.accountInfoErr = nil

			paramsJSON, err := json.Marshal(map[string]any{"account": validAccount})
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr)
			require.NotNil(t, result)

			resp := result.(map[string]any)
			if tc.expectedType == "" {
				assert.NotContains(t, resp, "pseudo_account",
					"normal account must not carry pseudo_account")
				return
			}

			pseudo, ok := resp["pseudo_account"].(map[string]any)
			require.True(t, ok, "pseudo_account should be present and an object")
			assert.Equal(t, tc.expectedType, pseudo["type"])

			// The designator hash stays in account_data, not under pseudo_account.
			accountData := resp["account_data"].(map[string]any)
			assert.Equal(t, designatorHash, accountData[tc.designator])
			assert.NotContains(t, pseudo, tc.designator)
		})
	}
}
