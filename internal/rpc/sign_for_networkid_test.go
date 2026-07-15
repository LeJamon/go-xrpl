package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// TestSignFor_NetworkIDEnforcement verifies sign_for enforces NetworkID on
// networks with ID > 1024: tx_json must carry a matching integral NetworkID,
// else invalidParams. Unlike sign/submit, sign_for rejects rather than autofills.
// Reference: rippled commit 2ddef8c87d (checkNetworkID in transactionSignFor).
func TestSignFor_NetworkIDEnforcement(t *testing.T) {
	method := &handlers.SignForMethod{}
	const signer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh" // masterpassphrase

	ctxWith := func(networkID uint32) *types.RpcContext {
		mock := newMockLedgerService()
		mock.serverInfo = types.LedgerServerInfo{NetworkID: networkID}
		return &types.RpcContext{
			Context:    context.Background(),
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: mock},
		}
	}

	params := func(networkID *int) json.RawMessage {
		txJSON := map[string]any{
			"TransactionType": "Payment",
			"Account":         signer,
			"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount":          "1000000",
			"Fee":             "10",
			"Sequence":        1,
		}
		if networkID != nil {
			txJSON["NetworkID"] = *networkID
		}
		b, err := json.Marshal(map[string]any{
			"account":    signer,
			"passphrase": "masterpassphrase",
			"key_type":   "secp256k1",
			"tx_json":    txJSON,
		})
		require.NoError(t, err)
		return b
	}

	t.Run("network > 1024 requires NetworkID", func(t *testing.T) {
		_, rpcErr := method.Handle(ctxWith(1025), params(nil))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Missing field 'tx_json.NetworkID'.", rpcErr.Message)
	})

	t.Run("network > 1024 rejects mismatched NetworkID", func(t *testing.T) {
		nid := 999
		_, rpcErr := method.Handle(ctxWith(1025), params(&nid))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid field 'tx_json.NetworkID'.", rpcErr.Message)
	})

	t.Run("network > 1024 accepts matching NetworkID", func(t *testing.T) {
		nid := 1025
		_, rpcErr := method.Handle(ctxWith(1025), params(&nid))
		require.Nil(t, rpcErr)
	})

	t.Run("network <= 1024 does not require NetworkID", func(t *testing.T) {
		_, rpcErr := method.Handle(ctxWith(1024), params(nil))
		require.Nil(t, rpcErr)
	})
}
