package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLedgerServiceSimulate extends mockLedgerService with simulate-specific behavior.
type mockLedgerServiceSimulate struct {
	*mockLedgerService
	simulateResult *types.SubmitResult
	simulateError  error
}

func newMockLedgerServiceSimulate() *mockLedgerServiceSimulate {
	return &mockLedgerServiceSimulate{
		mockLedgerService: newMockLedgerService(),
		simulateResult: &types.SubmitResult{
			EngineResult:        "tesSUCCESS",
			EngineResultCode:    0,
			EngineResultMessage: "The simulated transaction would have been applied.",
			Applied:             false,
			CurrentLedger:       3,
		},
	}
}

func (m *mockLedgerServiceSimulate) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.simulateResult, nil
}

func newSimulateTestServices(mock *mockLedgerServiceSimulate) *types.ServiceContainer {
	return &types.ServiceContainer{
		Ledger: mock,
	}
}

// validAccountAddress is a well-known XRPL genesis account used in tests.
const validAccountAddress = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func TestSimulateMethod_ParamErrors(t *testing.T) {
	mock := newMockLedgerServiceSimulate()
	services := newSimulateTestServices(mock)

	method := &handlers.SimulateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   services,
	}

	tests := []struct {
		name         string
		params       interface{}
		expectedMsg  string
		expectedCode int
	}{
		{
			name:         "No params — neither tx_blob nor tx_json",
			params:       map[string]interface{}{},
			expectedMsg:  "Neither `tx_blob` nor `tx_json` included.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
		{
			name: "Both tx_blob and tx_json",
			params: map[string]interface{}{
				"tx_blob": "1200",
				"tx_json": map[string]interface{}{},
			},
			expectedMsg:  "Can only include one of `tx_blob` and `tx_json`.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
		{
			name: "binary is not a boolean",
			params: map[string]interface{}{
				"tx_blob": "1200",
				"binary":  "100",
			},
			expectedMsg:  "Invalid field 'binary'.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
		{
			name: "binary is an integer",
			params: map[string]interface{}{
				"tx_blob": "1200",
				"binary":  1,
			},
			expectedMsg:  "Invalid field 'binary'.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
		{
			name: "secret field included",
			params: map[string]interface{}{
				"secret": "doesnt_matter",
				"tx_json": map[string]interface{}{
					"TransactionType": "AccountSet",
					"Account":         validAccountAddress,
				},
			},
			expectedMsg:  "Invalid field 'secret'.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
		{
			name: "seed field included",
			params: map[string]interface{}{
				"seed": "doesnt_matter",
				"tx_json": map[string]interface{}{
					"TransactionType": "AccountSet",
					"Account":         validAccountAddress,
				},
			},
			expectedMsg:  "Invalid field 'seed'.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
		{
			name: "seed_hex field included",
			params: map[string]interface{}{
				"seed_hex": "doesnt_matter",
				"tx_json": map[string]interface{}{
					"TransactionType": "AccountSet",
					"Account":         validAccountAddress,
				},
			},
			expectedMsg:  "Invalid field 'seed_hex'.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
		{
			name: "passphrase field included",
			params: map[string]interface{}{
				"passphrase": "doesnt_matter",
				"tx_json": map[string]interface{}{
					"TransactionType": "AccountSet",
					"Account":         validAccountAddress,
				},
			},
			expectedMsg:  "Invalid field 'passphrase'.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
		{
			name: "Empty tx_json — missing TransactionType",
			params: map[string]interface{}{
				"tx_json": map[string]interface{}{},
			},
			expectedMsg:  "Missing field 'tx.TransactionType'.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
		{
			name: "Missing Account field",
			params: map[string]interface{}{
				"tx_json": map[string]interface{}{
					"TransactionType": "Payment",
				},
			},
			expectedMsg:  "Missing field 'tx.Account'.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
		{
			name: "Bad Account address",
			params: map[string]interface{}{
				"tx_json": map[string]interface{}{
					"TransactionType": "AccountSet",
					"Account":         "badAccount",
				},
			},
			expectedMsg:  "Invalid field 'tx.Account'.",
			expectedCode: types.RpcSRC_ACT_MALFORMED,
		},
		{
			name: "tx_json is not an object (string)",
			params: map[string]interface{}{
				"tx_json": "not_an_object",
			},
			expectedMsg:  "Invalid field 'tx_json', not object.",
			expectedCode: types.RpcINVALID_PARAMS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			_, rpcErr := method.Handle(ctx, paramsJSON)
			require.NotNil(t, rpcErr, "Expected an error but got nil")
			assert.Equal(t, tc.expectedCode, rpcErr.Code, "Error code mismatch")
			assert.Equal(t, tc.expectedMsg, rpcErr.Message, "Error message mismatch")
		})
	}
}

func TestSimulateMethod_TxnSignature(t *testing.T) {
	mock := newMockLedgerServiceSimulate()
	services := newSimulateTestServices(mock)

	method := &handlers.SimulateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   services,
	}

	t.Run("Signed transaction — non-empty TxnSignature", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"TxnSignature":    "1200ABCD",
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcTX_SIGNED, rpcErr.Code)
		assert.Equal(t, "transactionSigned", rpcErr.ErrorString)
		assert.Equal(t, "Transaction should not be signed.", rpcErr.Message)
	})

	t.Run("Empty TxnSignature — allowed", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"TxnSignature":    "",
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, rpcErr, "Empty TxnSignature should be allowed")
		assert.NotNil(t, result)
	})

	t.Run("Missing TxnSignature — autofilled to empty", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, rpcErr, "Missing TxnSignature should be autofilled")
		require.NotNil(t, result)

		resp, ok := result.(map[string]interface{})
		require.True(t, ok)
		txJSON, ok := resp["tx_json"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "", txJSON["TxnSignature"], "TxnSignature should be autofilled to empty string")
		assert.Equal(t, "", txJSON["SigningPubKey"], "SigningPubKey should be autofilled to empty string")
	})
}

func TestSimulateMethod_SignedMultisig(t *testing.T) {
	mock := newMockLedgerServiceSimulate()
	services := newSimulateTestServices(mock)

	method := &handlers.SimulateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   services,
	}

	t.Run("Signed multisig transaction — non-empty signer TxnSignature", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"Signers": []interface{}{
					map[string]interface{}{
						"Signer": map[string]interface{}{
							"Account":       validAccountAddress,
							"SigningPubKey": validAccountAddress,
							"TxnSignature":  "1200ABCD",
						},
					},
				},
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcTX_SIGNED, rpcErr.Code)
		assert.Equal(t, "Transaction should not be signed.", rpcErr.Message)
	})

	t.Run("Invalid Signers field — not an array", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"Signers":         "1",
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid field 'tx.Signers'.", rpcErr.Message)
	})

	t.Run("Invalid Signers entry — not an object", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"Signers":         []interface{}{"1"},
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid field 'tx.Signers[0]'.", rpcErr.Message)
	})

	t.Run("Signers autofill — missing SigningPubKey and TxnSignature", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"Signers": []interface{}{
					map[string]interface{}{
						"Signer": map[string]interface{}{
							"Account": validAccountAddress,
						},
					},
				},
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, rpcErr, "Valid signers without TxnSignature should pass")
		require.NotNil(t, result)

		resp, ok := result.(map[string]interface{})
		require.True(t, ok)
		txJSON, ok := resp["tx_json"].(map[string]interface{})
		require.True(t, ok)

		signers, ok := txJSON["Signers"].([]interface{})
		require.True(t, ok)
		require.Len(t, signers, 1)

		entry, ok := signers[0].(map[string]interface{})
		require.True(t, ok)
		signer, ok := entry["Signer"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "", signer["SigningPubKey"], "Signer SigningPubKey should be autofilled")
		assert.Equal(t, "", signer["TxnSignature"], "Signer TxnSignature should be autofilled")
	})
}

func TestSimulateMethod_BatchRejection(t *testing.T) {
	mock := newMockLedgerServiceSimulate()
	services := newSimulateTestServices(mock)

	method := &handlers.SimulateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   services,
	}

	params := map[string]interface{}{
		"tx_json": map[string]interface{}{
			"TransactionType": "Batch",
			"Account":         validAccountAddress,
		},
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	_, rpcErr := method.Handle(ctx, paramsJSON)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcNOT_IMPL, rpcErr.Code)
	assert.Equal(t, "notImpl", rpcErr.ErrorString)
	assert.Equal(t, "Not implemented.", rpcErr.Message)
}

func TestSimulateMethod_SuccessfulSimulation(t *testing.T) {
	mock := newMockLedgerServiceSimulate()
	services := newSimulateTestServices(mock)

	method := &handlers.SimulateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   services,
	}

	params := map[string]interface{}{
		"tx_json": map[string]interface{}{
			"TransactionType": "AccountSet",
			"Account":         validAccountAddress,
		},
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	assert.Nil(t, rpcErr, "Expected no error for valid simulation")
	require.NotNil(t, result)

	resp, ok := result.(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "tesSUCCESS", resp["engine_result"])
	assert.Equal(t, 0, resp["engine_result_code"])
	assert.Equal(t, "The simulated transaction would have been applied.", resp["engine_result_message"])
	assert.Equal(t, false, resp["applied"])
	assert.Equal(t, uint32(3), resp["ledger_index"])

	// Verify tx_json is returned with autofilled fields
	txJSON, ok := resp["tx_json"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "AccountSet", txJSON["TransactionType"])
	assert.Equal(t, validAccountAddress, txJSON["Account"])
	assert.Equal(t, "", txJSON["SigningPubKey"])
	assert.Equal(t, "", txJSON["TxnSignature"])
}

func TestSimulateMethod_SrcActMalformed(t *testing.T) {
	mock := newMockLedgerServiceSimulate()
	services := newSimulateTestServices(mock)

	method := &handlers.SimulateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   services,
	}

	params := map[string]interface{}{
		"tx_json": map[string]interface{}{
			"TransactionType": "AccountSet",
			"Account":         "badAccount",
		},
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	_, rpcErr := method.Handle(ctx, paramsJSON)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcSRC_ACT_MALFORMED, rpcErr.Code)
	assert.Equal(t, "srcActMalformed", rpcErr.ErrorString)
	assert.Equal(t, "Invalid field 'tx.Account'.", rpcErr.Message)
}

func TestSimulateMethod_RequiredRole(t *testing.T) {
	method := &handlers.SimulateMethod{}
	assert.Equal(t, types.RoleGuest, method.RequiredRole())
}

func TestSimulateMethod_RequiredCondition(t *testing.T) {
	method := &handlers.SimulateMethod{}
	assert.Equal(t, types.NeedsCurrentLedger, method.RequiredCondition())
}

func TestSimulateMethod_SupportedApiVersions(t *testing.T) {
	method := &handlers.SimulateMethod{}
	versions := method.SupportedApiVersions()
	assert.Contains(t, versions, types.ApiVersion1)
	assert.Contains(t, versions, types.ApiVersion2)
	assert.Contains(t, versions, types.ApiVersion3)
}
