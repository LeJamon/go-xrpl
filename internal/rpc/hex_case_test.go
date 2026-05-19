package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRPCHexCaseRegression locks in the rippled-compatible uppercase-hex
// convention for hash-like response fields. Rippled returns every hash and
// ledger entry index as uppercase hex (see strHex() in
// rippled/src/libxrpl/basics/StringUtilities.cpp); clients diff responses
// byte-for-byte, so any lowercase regression silently breaks them.
//
// See issue #475.
func TestRPCHexCaseRegression(t *testing.T) {
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("ledger_closed", func(t *testing.T) {
		mock := &ledgerClosedMock{mockLedgerService: newMockLedgerService()}
		var closedHash [32]byte
		closedHash[0] = 0xAB
		closedHash[1] = 0xCD
		closedHash[31] = 0xEF
		mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
			return &mockLedgerReader{seq: 2, hash: closedHash, closed: true, validated: true}, nil
		}

		method := &handlers.LedgerClosedMethod{}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: mock},
		}

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		resp := resultToMapClosed(t, result)
		hash, _ := resp["ledger_hash"].(string)
		assert.Equal(t, strings.ToUpper(hash), hash, "ledger_closed ledger_hash must be uppercase")
	})

	t.Run("account_objects per-object index", func(t *testing.T) {
		mock := newAccountObjectsMock()
		mock.getAccountObjectsFn = func(account string, _ string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
			return &types.AccountObjectsResult{
				Account: account,
				AccountObjects: []types.AccountObjectItem{
					{
						Index:           "abc123def456",
						LedgerEntryType: "Offer",
						// Force the decode-fallback path by passing empty Data.
						Data: []byte{},
					},
				},
				LedgerIndex: 2,
				LedgerHash:  [32]byte{0xAA, 0xBB, 0xCC},
				Validated:   true,
			}, nil
		}

		method := &handlers.AccountObjectsMethod{}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: mock},
		}

		params, err := json.Marshal(map[string]interface{}{"account": validAccount})
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(resultJSON, &resp))

		ledgerHash, _ := resp["ledger_hash"].(string)
		assert.Equal(t, strings.ToUpper(ledgerHash), ledgerHash, "ledger_hash must be uppercase")

		objs, ok := resp["account_objects"].([]interface{})
		require.True(t, ok)
		require.Len(t, objs, 1)
		obj := objs[0].(map[string]interface{})
		idx, _ := obj["index"].(string)
		require.NotEmpty(t, idx)
		assert.Equal(t, strings.ToUpper(idx), idx, "account_objects[*].index must be uppercase")
	})
}
