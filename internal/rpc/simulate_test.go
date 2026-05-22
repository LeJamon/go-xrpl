package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/service/svcerr"
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLedgerServiceSimulate extends mockLedgerService with simulate-specific behavior.
type mockLedgerServiceSimulate struct {
	*mockLedgerService
	simulateResult       *types.SubmitResult
	simulateError        error
	autofillSeq          uint32
	autofillFeeErr       error
	autofillSeqErr       error
	currentNetworkFee    uint64
	lastSeqHasTicket     bool
	feeAutofillCallCount int
	seqAutofillCallCount int
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
		autofillSeq:       1,
		currentNetworkFee: 10,
	}
}

func (m *mockLedgerServiceSimulate) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	if m.simulateError != nil {
		return nil, m.simulateError
	}
	return m.simulateResult, nil
}

func (m *mockLedgerServiceSimulate) GetAutofillFee(txJSON []byte) (uint64, error) {
	m.feeAutofillCallCount++
	if m.autofillFeeErr != nil {
		return 0, m.autofillFeeErr
	}
	return m.currentNetworkFee, nil
}

func (m *mockLedgerServiceSimulate) GetAutofillSequence(account string, hasTicketSequence bool) (uint32, error) {
	m.seqAutofillCallCount++
	m.lastSeqHasTicket = hasTicketSequence
	// Mirror the real service's address validation so handler tests that
	// expect SrcActMalformed on a bad Account still receive ErrAccountMalformed
	// from the service path, not a canned success.
	if _, _, decodeErr := addresscodec.DecodeClassicAddressToAccountID(account); decodeErr != nil {
		return 0, fmt.Errorf("%w: %v", svcerr.ErrAccountMalformed, decodeErr)
	}
	if m.autofillSeqErr != nil {
		return 0, m.autofillSeqErr
	}
	if hasTicketSequence {
		return 0, nil
	}
	return m.autofillSeq, nil
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

func TestSimulateMethod_SequenceFeeAutofill(t *testing.T) {
	method := &handlers.SimulateMethod{}

	makeCtx := func(mock *mockLedgerServiceSimulate) *types.RpcContext {
		return &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion2,
			Services:   newSimulateTestServices(mock),
		}
	}

	t.Run("Sequence and Fee autofilled from ledger service", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()
		mock.autofillSeq = 42
		mock.currentNetworkFee = 15

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.Nil(t, rpcErr)
		resp := result.(map[string]interface{})
		txJSON := resp["tx_json"].(map[string]interface{})

		assert.Equal(t, uint32(42), txJSON["Sequence"])
		assert.Equal(t, "15", txJSON["Fee"])
		assert.Equal(t, 1, mock.feeAutofillCallCount, "GetAutofillFee invoked once")
		assert.Equal(t, 1, mock.seqAutofillCallCount, "GetAutofillSequence invoked once")
	})

	t.Run("Pre-set Sequence and Fee are preserved", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()
		mock.autofillSeq = 99
		mock.currentNetworkFee = 99

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"Sequence":        7,
				"Fee":             "12",
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.Nil(t, rpcErr)
		resp := result.(map[string]interface{})
		txJSON := resp["tx_json"].(map[string]interface{})

		assert.EqualValues(t, 7, txJSON["Sequence"])
		assert.Equal(t, "12", txJSON["Fee"])
		assert.Equal(t, 0, mock.feeAutofillCallCount,
			"GetAutofillFee must not run when Fee is supplied")
		assert.Equal(t, 0, mock.seqAutofillCallCount,
			"GetAutofillSequence must not run when Sequence is supplied")
	})

	t.Run("Sequence supplied skips GetAutofillSequence", func(t *testing.T) {
		// rippled only invokes getAutofillSequence when Sequence is absent
		// (Simulate.cpp:140-146); when the caller supplies Sequence, the
		// handler must not perform the source-account lookup that would
		// otherwise surface rpcSRC_ACT_NOT_FOUND.
		mock := newMockLedgerServiceSimulate()
		mock.currentNetworkFee = 17

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"Sequence":        9,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.Nil(t, rpcErr)
		resp := result.(map[string]interface{})
		txJSON := resp["tx_json"].(map[string]interface{})

		assert.EqualValues(t, 9, txJSON["Sequence"], "caller-supplied Sequence preserved")
		assert.Equal(t, "17", txJSON["Fee"], "Fee still autofilled")
		assert.Equal(t, 1, mock.feeAutofillCallCount, "GetAutofillFee invoked once")
		assert.Equal(t, 0, mock.seqAutofillCallCount,
			"GetAutofillSequence must not run when Sequence is supplied")
	})

	t.Run("Sequence absent invokes GetAutofillSequence", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.Nil(t, rpcErr)
		assert.Equal(t, 1, mock.seqAutofillCallCount,
			"GetAutofillSequence must run when Sequence is absent")
		assert.False(t, mock.lastSeqHasTicket)
	})

	t.Run("TicketSequence writes Sequence=0 and propagates hasTicket", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()
		mock.autofillSeq = 99

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"TicketSequence":  5,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.Nil(t, rpcErr)
		resp := result.(map[string]interface{})
		txJSON := resp["tx_json"].(map[string]interface{})

		assert.Equal(t, uint32(0), txJSON["Sequence"],
			"rippled Simulate.cpp:68,140-146 writes Sequence=0 when TicketSequence is set")
		assert.True(t, mock.lastSeqHasTicket, "hasTicketSequence must propagate to the service")
		assert.Equal(t, uint32(5), txJSON["TicketSequence"],
			"caller-supplied TicketSequence is normalized to uint32 for downstream consumers")
	})

	t.Run("Source account not found maps to srcActNotFound", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()
		mock.autofillSeqErr = svcerr.ErrAccountNotFound

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcSRC_ACT_NOT_FOUND, rpcErr.Code)
		assert.Equal(t, "srcActNotFound", rpcErr.ErrorString,
			"rippled ErrorCodes.cpp:109 maps rpcSRC_ACT_NOT_FOUND to token 'srcActNotFound'")
		assert.Equal(t, "Source account not found.", rpcErr.Message)
	})

	t.Run("HighFeeError maps to rpcHIGH_FEE with rippled-format message", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()
		mock.autofillFeeErr = &svcerr.HighFeeError{Fee: 5000, Limit: 100}

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "highFee", rpcErr.ErrorString)
		assert.Equal(t, "Fee of 5000 exceeds the requested tx limit of 100", rpcErr.Message,
			"rippled TransactionSign.cpp:870-873 emits the 'Fee of X exceeds…' message verbatim")
	})

	t.Run("HighFeeError via wrapped sentinel still errors.As-extracts", func(t *testing.T) {
		// Defence-in-depth: a service that wraps the typed error via fmt.Errorf
		// "%w" must still be recoverable by errors.As in the handler.
		mock := newMockLedgerServiceSimulate()
		mock.autofillFeeErr = fmt.Errorf("autofill: %w", &svcerr.HighFeeError{Fee: 5000, Limit: 100})

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "highFee", rpcErr.ErrorString)
		assert.Equal(t, "Fee of 5000 exceeds the requested tx limit of 100", rpcErr.Message)
	})

	t.Run("highFee precedes txSigned when both inputs conflict", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()
		mock.autofillFeeErr = &svcerr.HighFeeError{Fee: 5000, Limit: 100}

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"TxnSignature":    "DEADBEEF",
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "highFee", rpcErr.ErrorString,
			"rippled autofillTx runs Fee before the TxnSignature signed-check (Simulate.cpp:74-138)")
	})

	t.Run("txSigned precedes srcActNotFound (signed-check before Sequence autofill)", func(t *testing.T) {
		// rippled autofillTx runs the top-level TxnSignature signed-check at
		// Simulate.cpp:129-138, BEFORE the Sequence autofill at 140-146. So a
		// request with a non-empty TxnSignature and a missing source account
		// must surface rpcTX_SIGNED, not rpcSRC_ACT_NOT_FOUND.
		mock := newMockLedgerServiceSimulate()
		mock.autofillSeqErr = svcerr.ErrAccountNotFound

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"TxnSignature":    "DEADBEEF",
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "transactionSigned", rpcErr.ErrorString,
			"signed-check fires before Sequence autofill — rippled Simulate.cpp:129-146")
		assert.Equal(t, 0, mock.seqAutofillCallCount,
			"GetAutofillSequence must not run once the signed-check has rejected")
	})

	t.Run("Signers signed-check precedes later signer's structural error", func(t *testing.T) {
		// rippled's autofillTx Signers loop interleaves structural and
		// signed-checks per-iteration (Simulate.cpp:97-127), so a signed
		// TxnSignature on signers[0] fires rpcTX_SIGNED before signers[2]'s
		// structural error is reached.
		mock := newMockLedgerServiceSimulate()

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
				"Signers": []interface{}{
					map[string]interface{}{
						"Signer": map[string]interface{}{
							"Account":      validAccountAddress,
							"TxnSignature": "DEADBEEF",
						},
					},
					"not-an-object",
				},
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "transactionSigned", rpcErr.ErrorString,
			"signers[0] signed-check must fire before signers[1] structural error")
	})

	t.Run("txSigned precedes srcActMalformed (Account check deferred to Sequence step)", func(t *testing.T) {
		// rippled checks Account validity inside getAutofillSequence
		// (Simulate.cpp:43-55), AFTER the top-level TxnSignature signed-check
		// at 129-138. A bad Account combined with a non-empty TxnSignature
		// must therefore surface rpcTX_SIGNED, not rpcSRC_ACT_MALFORMED.
		mock := newMockLedgerServiceSimulate()

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         "badAccount",
				"TxnSignature":    "DEADBEEF",
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "transactionSigned", rpcErr.ErrorString,
			"signed-check fires before Account format check — rippled Simulate.cpp:129-138 vs 50-54")
	})

	t.Run("Sequence supplied + bad Account surfaces invalid_field, not srcActMalformed", func(t *testing.T) {
		// rippled's autofill skips the Account check inside
		// getAutofillSequence (Simulate.cpp:43-55) when Sequence is
		// supplied, but STParsedJSONObject at Simulate.cpp:328-330 then
		// validates the AccountID and surfaces invalid_field on
		// failure — distinct from the rpcSRC_ACT_MALFORMED that the
		// Sequence-absent path returns. Go mirrors this via a
		// post-autofill format check that fires only in the
		// Sequence-supplied case.
		mock := newMockLedgerServiceSimulate()

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         "badAccount",
				"Sequence":        7,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code,
			"bad Account on the Sequence-supplied path surfaces invalid_field, not srcActMalformed")
		assert.Equal(t, "Invalid field 'tx.Account'.", rpcErr.Message)
		assert.Equal(t, 0, mock.seqAutofillCallCount,
			"GetAutofillSequence must be skipped when Sequence is supplied")
	})

	t.Run("out-of-range Sequence is rejected as invalid_field", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()

		// 2^33 — well within float64 precision, well outside uint32.
		paramsJSON := []byte(`{"tx_json":{"TransactionType":"AccountSet","Account":"` +
			validAccountAddress + `","Sequence":8589934592}}`)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code,
			"out-of-range Sequence must surface invalid_field, not silent truncation")
		assert.Equal(t, "Invalid field 'tx.Sequence'.", rpcErr.Message)
	})

	t.Run("fractional Sequence is rejected as invalid_field", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()

		paramsJSON := []byte(`{"tx_json":{"TransactionType":"AccountSet","Account":"` +
			validAccountAddress + `","Sequence":7.5}}`)

		_, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "Invalid field 'tx.Sequence'.", rpcErr.Message)
	})

	t.Run("Custom NetworkID > 1024 is autofilled", func(t *testing.T) {
		// rippled Simulate.cpp:148-153: NetworkID is autofilled only when
		// the server's NETWORK_ID exceeds 1024.
		mock := newMockLedgerServiceSimulate()
		mock.serverInfo = types.LedgerServerInfo{NetworkID: 1025}

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.Nil(t, rpcErr)
		txJSON := result.(map[string]interface{})["tx_json"].(map[string]interface{})
		assert.EqualValues(t, 1025, txJSON["NetworkID"])
	})

	t.Run("NetworkID <= 1024 is not autofilled", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()
		mock.serverInfo = types.LedgerServerInfo{NetworkID: 1024}

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.Nil(t, rpcErr)
		txJSON := result.(map[string]interface{})["tx_json"].(map[string]interface{})
		_, has := txJSON["NetworkID"]
		assert.False(t, has, "NetworkID must be absent when server NetworkID <= 1024")
	})

	t.Run("non-tesSUCCESS engine_result_message passes through unchanged", func(t *testing.T) {
		// rippled only overrides the message for tesSUCCESS
		// (Simulate.cpp:258-262); other TER messages from transResultInfo
		// must be returned verbatim.
		mock := newMockLedgerServiceSimulate()
		mock.simulateResult = &types.SubmitResult{
			EngineResult:        "temBAD_AMOUNT",
			EngineResultCode:    -298,
			EngineResultMessage: "Malformed: Bad amount.",
			Applied:             false,
			CurrentLedger:       3,
		}

		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(makeCtx(mock), paramsJSON)
		require.Nil(t, rpcErr)
		resp := result.(map[string]interface{})
		assert.Equal(t, "Malformed: Bad amount.", resp["engine_result_message"],
			"non-tesSUCCESS messages must not be clobbered by the success override")
	})
}

func TestSimulateMethod_MetaInResponse(t *testing.T) {
	method := &handlers.SimulateMethod{}

	t.Run("meta field present when binary=false", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()
		metaJSON := map[string]interface{}{
			"AffectedNodes":     []interface{}{},
			"TransactionIndex":  uint32(0),
			"TransactionResult": "tesSUCCESS",
		}
		mock.simulateResult = &types.SubmitResult{
			EngineResult:        "tesSUCCESS",
			EngineResultCode:    0,
			EngineResultMessage: "ignored",
			Applied:             false,
			CurrentLedger:       3,
			Metadata:            &types.SubmitMetadata{JSON: metaJSON, Blob: []byte{0xAB, 0xCD}},
		}

		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion2,
			Services:   newSimulateTestServices(mock),
		}
		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		resp := result.(map[string]interface{})

		assert.Equal(t, metaJSON, resp["meta"])
		_, hasBlob := resp["meta_blob"]
		assert.False(t, hasBlob, "meta_blob must not appear when binary=false")
		assert.Equal(t, "The simulated transaction would have been applied.", resp["engine_result_message"])
	})

	t.Run("nil Metadata omits both meta and meta_blob", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()
		mock.simulateResult = &types.SubmitResult{
			EngineResult:        "temBAD_AMOUNT",
			EngineResultCode:    -298,
			EngineResultMessage: "Malformed: Bad amount.",
			Applied:             false,
			CurrentLedger:       3,
			Metadata:            nil,
		}

		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion2,
			Services:   newSimulateTestServices(mock),
		}
		for _, binary := range []bool{false, true} {
			params := map[string]interface{}{
				"binary": binary,
				"tx_json": map[string]interface{}{
					"TransactionType": "AccountSet",
					"Account":         validAccountAddress,
				},
			}
			paramsJSON, _ := json.Marshal(params)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr)
			resp := result.(map[string]interface{})

			_, hasMeta := resp["meta"]
			_, hasBlob := resp["meta_blob"]
			assert.False(t, hasMeta, "binary=%v: meta must be absent when Metadata is nil "+
				"(rippled Simulate.cpp:264 gates emit on result.metadata)", binary)
			assert.False(t, hasBlob, "binary=%v: meta_blob must be absent when Metadata is nil", binary)
		}
	})

	t.Run("meta_blob field present when binary=true", func(t *testing.T) {
		mock := newMockLedgerServiceSimulate()
		mock.simulateResult = &types.SubmitResult{
			EngineResult:        "tesSUCCESS",
			EngineResultCode:    0,
			EngineResultMessage: "ignored",
			Applied:             false,
			CurrentLedger:       3,
			Metadata:            &types.SubmitMetadata{Blob: []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		}

		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion2,
			Services:   newSimulateTestServices(mock),
		}
		params := map[string]interface{}{
			"binary": true,
			"tx_json": map[string]interface{}{
				"TransactionType": "AccountSet",
				"Account":         validAccountAddress,
			},
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		resp := result.(map[string]interface{})

		assert.Equal(t, "DEADBEEF", resp["meta_blob"])
		_, hasMeta := resp["meta"]
		assert.False(t, hasMeta, "meta must not appear when binary=true")
	})
}

// TestSimulateMethod_RealMetadataShape exercises the wire shape produced by
// tx.Metadata.MarshalJSON when SubmitMetadata.JSON carries the real engine
// metadata struct (as the production LedgerServiceAdapter does), not a
// hand-rolled map. Mirrors the field-paths rippled validates in
// Simulate_test.cpp:527-549.
func TestSimulateMethod_RealMetadataShape(t *testing.T) {
	method := &handlers.SimulateMethod{}
	mock := newMockLedgerServiceSimulate()

	realMeta := &tx.Metadata{
		AffectedNodes: []tx.AffectedNode{
			{
				NodeType:        "ModifiedNode",
				LedgerEntryType: "AccountRoot",
				LedgerIndex:     "ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890",
				FinalFields: map[string]any{
					"Domain": "123ABC",
				},
			},
		},
		TransactionIndex:  0,
		TransactionResult: tx.TesSUCCESS,
	}
	mock.simulateResult = &types.SubmitResult{
		EngineResult:        "tesSUCCESS",
		EngineResultCode:    0,
		EngineResultMessage: "ignored",
		Applied:             false,
		CurrentLedger:       3,
		Metadata:            &types.SubmitMetadata{JSON: realMeta, Blob: []byte{0x00}},
	}

	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   newSimulateTestServices(mock),
	}
	params := map[string]interface{}{
		"tx_json": map[string]interface{}{
			"TransactionType": "AccountSet",
			"Account":         validAccountAddress,
		},
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)

	wire, err := json.Marshal(result)
	require.NoError(t, err)
	var roundTrip map[string]interface{}
	require.NoError(t, json.Unmarshal(wire, &roundTrip))

	meta, ok := roundTrip["meta"].(map[string]interface{})
	require.True(t, ok, "meta must be a JSON object on the wire")
	assert.Equal(t, "tesSUCCESS", meta["TransactionResult"])
	assert.EqualValues(t, 0, meta["TransactionIndex"])

	nodes, ok := meta["AffectedNodes"].([]interface{})
	require.True(t, ok)
	require.Len(t, nodes, 1)
	mod, ok := nodes[0].(map[string]interface{})["ModifiedNode"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "AccountRoot", mod["LedgerEntryType"])
	final, ok := mod["FinalFields"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "123ABC", final["Domain"])
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
