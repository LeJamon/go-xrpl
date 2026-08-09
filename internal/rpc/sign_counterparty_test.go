package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
)

// signOffline runs the sign handler in offline mode and returns the response map.
func signOffline(t *testing.T, params json.RawMessage) map[string]any {
	t.Helper()
	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	result, rpcErr := handler.Handle(signingEnabledContext(ctx), params)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)
	return result.(map[string]any)
}

// TestSign_SignatureTarget_HappyPath signs a transaction normally and then
// attaches a counterparty signature via signature_target=CounterpartySignature.
// The nested object is populated, the top-level signature is preserved, and both
// signatures verify.
func TestSign_SignatureTarget_HappyPath(t *testing.T) {
	// Primary signs (masterpassphrase → rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh).
	primary := signOffline(t, json.RawMessage(`{
		"tx_json": {
			"TransactionType": "LoanSet",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"LoanBrokerID": "0000000000000000000000000000000000000000000000000000000000000000",
			"PrincipalRequested": "1",
			"Fee": "10",
			"Sequence": 1,
			"Memos": []
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"offline": true
	}`))
	primaryTx := primary["tx_json"].(map[string]any)
	require.NotEmpty(t, primaryTx["SigningPubKey"])
	require.NotEmpty(t, primaryTx["TxnSignature"])
	// Strip response-only aliases the codec cannot serialize on the re-sign.
	delete(primaryTx, "hash")
	delete(primaryTx, "DeliverMax")
	primaryPubKey := primaryTx["SigningPubKey"].(string)

	// Counterparty signs into the nested object with a different key.
	req, err := json.Marshal(map[string]any{
		"tx_json":          primaryTx,
		"passphrase":       "counterparty phrase",
		"key_type":         "secp256k1",
		"offline":          true,
		"signature_target": "CounterpartySignature",
	})
	require.NoError(t, err)
	res := signOffline(t, req)

	resTx := res["tx_json"].(map[string]any)
	// Top-level primary signature is untouched.
	assert.Equal(t, primaryPubKey, resTx["SigningPubKey"])
	assert.Equal(t, primaryTx["TxnSignature"], resTx["TxnSignature"])

	// The nested object carries the counterparty's key + signature.
	cpObj, ok := resTx["CounterpartySignature"].(map[string]any)
	require.True(t, ok, "response tx_json missing CounterpartySignature: %v", resTx)
	assert.NotEmpty(t, cpObj["SigningPubKey"])
	assert.NotEmpty(t, cpObj["TxnSignature"])
	assert.NotEqual(t, primaryPubKey, cpObj["SigningPubKey"], "counterparty key must differ from primary")

	// Reconstruct the transaction and verify both signatures.
	blob := res["tx_blob"].(string)
	txBytes, err := hex.DecodeString(blob)
	require.NoError(t, err)
	parsed, err := txcore.ParseFromBinary(txBytes)
	require.NoError(t, err)
	require.True(t, parsed.GetCommon().HasField("Memos"))
	require.Empty(t, parsed.GetCommon().Memos)

	require.NoError(t, sign.VerifySignature(parsed, false), "top-level signature must verify")
	cp := parsed.GetCommon().CounterpartySignature
	require.NotNil(t, cp)
	require.NoError(t, sign.VerifyCounterpartySignature(parsed, cp, false), "counterparty signature must verify")
}

// TestSign_SignatureTarget_Invalid rejects a signature_target that does not name
// the CounterpartySignature field, with rpcINVALID_PARAMS.
func TestSign_SignatureTarget_Invalid(t *testing.T) {
	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"Fee": "10",
			"Sequence": 1
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"offline": true,
		"signature_target": "NotAValidField"
	}`)
	_, err := handler.Handle(signingEnabledContext(ctx), params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "NotAValidField")
}

func TestSign_SignatureTarget_RequiresTopLevelSigningPubKey(t *testing.T) {
	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "LoanSet",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"LoanBrokerID": "0000000000000000000000000000000000000000000000000000000000000000",
			"PrincipalRequested": "1",
			"Fee": "10",
			"Sequence": 1
		},
		"passphrase": "counterparty phrase",
		"key_type": "secp256k1",
		"offline": true,
		"signature_target": "CounterpartySignature"
	}`)
	_, err := handler.Handle(signingEnabledContext(ctx), params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "SigningPubKey")
}

func TestSign_SignatureTarget_DisallowedForTransaction(t *testing.T) {
	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"Fee": "10",
			"Sequence": 1,
			"SigningPubKey": ""
		},
		"passphrase": "counterparty phrase",
		"key_type": "secp256k1",
		"offline": true,
		"signature_target": "CounterpartySignature"
	}`)
	_, err := handler.Handle(signingEnabledContext(ctx), params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "disallowed location")
}

func TestSignFor_SignatureTarget(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}

	primary := signOffline(t, json.RawMessage(`{
		"tx_json": {
			"TransactionType": "LoanSet",
			"Account": "rHSXa2gvfegaC7767QZGFZjebqkWKMCkTf",
			"LoanBrokerID": "0000000000000000000000000000000000000000000000000000000000000000",
			"PrincipalRequested": "1",
			"Fee": "10",
			"Sequence": 1
		},
		"passphrase": "primary signer phrase",
		"key_type": "secp256k1",
		"offline": true
	}`))
	primaryTx := primary["tx_json"].(map[string]any)
	primaryPubKey := primaryTx["SigningPubKey"]
	require.NotEmpty(t, primaryPubKey)
	delete(primaryTx, "TxnSignature")
	delete(primaryTx, "hash")
	delete(primaryTx, "DeliverMax")

	params, err := json.Marshal(map[string]any{
		"account":          "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json":          primaryTx,
		"passphrase":       "masterpassphrase",
		"key_type":         "secp256k1",
		"signature_target": "CounterpartySignature",
	})
	require.NoError(t, err)
	result, rpcErr := handler.Handle(signingEnabledContext(ctx), params)
	require.Nil(t, rpcErr)
	response := result.(map[string]any)
	resTx := response["tx_json"].(map[string]any)
	assert.Equal(t, primaryPubKey, resTx["SigningPubKey"])
	assert.NotContains(t, resTx, "TxnSignature")

	cpObj, ok := resTx["CounterpartySignature"].(map[string]any)
	require.True(t, ok, "expected CounterpartySignature in %v", resTx)
	switch signers := cpObj["Signers"].(type) {
	case []map[string]any:
		assert.Len(t, signers, 1)
	case []any:
		assert.Len(t, signers, 1)
	default:
		t.Fatalf("expected nested Signers array, got %T in %v", cpObj["Signers"], cpObj)
	}
	// The top-level tx must not have picked up a Signers array.
	_, topHasSigners := resTx["Signers"]
	assert.False(t, topHasSigners, "signer must go into the nested object, not the top level")

	blob, err := hex.DecodeString(response["tx_blob"].(string))
	require.NoError(t, err)
	parsed, err := txcore.ParseFromBinary(blob)
	require.NoError(t, err)
	require.NoError(t, sign.VerifyCounterpartySignature(
		parsed, parsed.GetCommon().CounterpartySignature, false))
}

func TestSignFor_SignatureTarget_DisallowedForTransaction(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	params := json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Destination": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Amount": "1000000",
			"Fee": "10",
			"Sequence": 1
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"signature_target": "CounterpartySignature"
	}`)
	_, err := handler.Handle(signingEnabledContext(ctx), params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "disallowed location")
}

func TestSignFor_SignatureTarget_Invalid(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	params := json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Destination": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Amount": "1000000",
			"Fee": "10",
			"Sequence": 1
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"signature_target": "Memo"
	}`)
	_, err := handler.Handle(signingEnabledContext(ctx), params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "Memo")
}

func TestSignatureTarget_ExplicitEmptyRejected(t *testing.T) {
	t.Run("sign", func(t *testing.T) {
		handler := &handlers.SignMethod{}
		ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
		_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
			"tx_json": {
				"TransactionType": "LoanSet",
				"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"LoanBrokerID": "0000000000000000000000000000000000000000000000000000000000000000",
				"PrincipalRequested": "1",
				"Fee": "10",
				"Sequence": 1
			},
			"passphrase": "masterpassphrase",
			"key_type": "secp256k1",
			"offline": true,
			"signature_target": ""
		}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("sign_for", func(t *testing.T) {
		handler := &handlers.SignForMethod{}
		ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
		_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
			"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"tx_json": {
				"TransactionType": "Payment",
				"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Destination": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Amount": "1",
				"Fee": "10",
				"Sequence": 1
			},
			"passphrase": "masterpassphrase",
			"key_type": "secp256k1",
			"signature_target": ""
		}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	})
}

func TestSignFor_RejectsTopLevelTxnSignature(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json": {
			"TransactionType": "LoanSet",
			"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"LoanBrokerID": "0000000000000000000000000000000000000000000000000000000000000000",
			"PrincipalRequested": "1",
			"Fee": "10",
			"Sequence": 1,
			"SigningPubKey": "03EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3",
			"TxnSignature": "AA"
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"signature_target": "CounterpartySignature"
	}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcALREADY_SINGLE_SIG, rpcErr.Code)
}

func TestSignFor_TxnSignatureDoesNotMaskMissingAccount(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json": {
			"TransactionType": "LoanSet",
			"LoanBrokerID": "0000000000000000000000000000000000000000000000000000000000000000",
			"PrincipalRequested": "1",
			"Fee": "10",
			"Sequence": 1,
			"SigningPubKey": "03EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3",
			"TxnSignature": "AA"
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"signature_target": "CounterpartySignature"
	}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcSRC_ACT_MISSING, rpcErr.Code)
	assert.Equal(t, "Missing field 'tx_json.Account'.", rpcErr.Message)
}

func TestSignFor_TxnSignatureDoesNotMaskMissingSequence(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json": {
			"TransactionType": "LoanSet",
			"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"LoanBrokerID": "0000000000000000000000000000000000000000000000000000000000000000",
			"PrincipalRequested": "1",
			"Fee": "10",
			"SigningPubKey": "03EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3",
			"TxnSignature": "AA"
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"signature_target": "CounterpartySignature"
	}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "Missing field 'tx_json.Sequence'.", rpcErr.Message)
}

func TestSignFor_MissingSequencePrecedesLaterValidation(t *testing.T) {
	tests := []struct {
		name   string
		extra  string
		fields string
	}{
		{
			name:   "invalid signature target",
			extra:  `,"passphrase":"masterpassphrase","key_type":"secp256k1","signature_target":"Memo"`,
			fields: `"Account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"`,
		},
		{
			name:   "missing credentials",
			extra:  `,"signature_target":"CounterpartySignature"`,
			fields: `"Account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"`,
		},
		{
			name:  "missing transaction account",
			extra: `,"passphrase":"masterpassphrase","key_type":"secp256k1","signature_target":"CounterpartySignature"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := test.fields
			if fields != "" {
				fields += ","
			}
			params := json.RawMessage(`{"account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","tx_json":{` +
				fields + `"TransactionType":"LoanSet","LoanBrokerID":"` +
				`0000000000000000000000000000000000000000000000000000000000000000",` +
				`"PrincipalRequested":"1","Fee":"10"}` + test.extra + `}`)
			handler := &handlers.SignForMethod{}
			ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
			_, rpcErr := handler.Handle(signingEnabledContext(ctx), params)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Equal(t, "Missing field 'tx_json.Sequence'.", rpcErr.Message)
		})
	}
}

func TestSignFor_TxnSignatureDoesNotMaskNonEmptySigningPubKey(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Destination": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Amount": "1",
			"Fee": "10",
			"Sequence": 1,
			"SigningPubKey": "03EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3",
			"TxnSignature": "AA"
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1"
	}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "When multi-signing 'tx_json.SigningPubKey' must be empty.", rpcErr.Message)
}

func TestSignFor_TargetTxnSignaturePrecedesTargetLocationValidation(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Destination": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Amount": "1",
			"Fee": "10",
			"Sequence": 1,
			"SigningPubKey": "",
			"TxnSignature": "AA"
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"signature_target": "CounterpartySignature"
	}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcALREADY_SINGLE_SIG, rpcErr.Code)
}

func TestSignFor_MissingFeePrecedesTxnSignature(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Destination": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Amount": "1",
			"Sequence": 1,
			"SigningPubKey": "",
			"TxnSignature": "AA"
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"signature_target": "CounterpartySignature"
	}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "Missing field 'tx_json.Fee'.", rpcErr.Message)
}

func TestSignFor_PreConflictFieldOrder(t *testing.T) {
	tests := []struct {
		name         string
		txJSON       map[string]any
		expectedCode int
		expectedMsg  string
	}{
		{
			name: "transaction type precedes account",
			txJSON: map[string]any{
				"Fee": "10", "Sequence": 1, "SigningPubKey": "", "TxnSignature": "AA",
			},
			expectedCode: rpcerrors.RpcINVALID_PARAMS,
			expectedMsg:  "Missing field 'tx_json.TransactionType'.",
		},
		{
			name: "malformed account precedes fee",
			txJSON: map[string]any{
				"TransactionType": "LoanSet", "Account": "not-an-account",
				"Sequence": 1, "SigningPubKey": "", "TxnSignature": "AA",
			},
			expectedCode: rpcerrors.RpcSRC_ACT_MALFORMED,
			expectedMsg:  "Invalid field 'tx_json.Account'.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rpcErr := callTargetSignFor(t, test.txJSON)
			require.NotNil(t, rpcErr)
			assert.Equal(t, test.expectedCode, rpcErr.Code)
			assert.Equal(t, test.expectedMsg, rpcErr.Message)
		})
	}
}

func TestSignFor_TxnSignaturePrecedesFullTransactionParsing(t *testing.T) {
	tests := []struct {
		name   string
		txJSON map[string]any
	}{
		{
			name: "codec-invalid fee",
			txJSON: map[string]any{
				"TransactionType":    "LoanSet",
				"Account":            "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"LoanBrokerID":       "0000000000000000000000000000000000000000000000000000000000000000",
				"PrincipalRequested": "1",
				"Fee":                "not-a-fee",
				"Sequence":           1,
				"SigningPubKey":      "",
				"TxnSignature":       "AA",
			},
		},
		{
			name: "codec-invalid non-payment field",
			txJSON: map[string]any{
				"TransactionType":    "LoanSet",
				"Account":            "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"LoanBrokerID":       "not-a-hash",
				"PrincipalRequested": "1",
				"Fee":                "10",
				"Sequence":           1,
				"SigningPubKey":      "",
				"TxnSignature":       "AA",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rpcErr := callTargetSignFor(t, test.txJSON)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcALREADY_SINGLE_SIG, rpcErr.Code)
		})
	}
}

func TestSignFor_PaymentFieldsPrecedeTxnSignature(t *testing.T) {
	base := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "",
		"TxnSignature":    "AA",
	}
	tests := []struct {
		name        string
		fields      map[string]any
		expectedMsg string
	}{
		{name: "missing amount", fields: map[string]any{
			"Destination": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		}, expectedMsg: "Missing field 'tx_json.Amount'."},
		{name: "invalid amount precedes destination", fields: map[string]any{
			"Amount": "not-an-amount",
		}, expectedMsg: "Invalid field 'tx_json.Amount'."},
		{name: "missing destination", fields: map[string]any{
			"Amount": "1",
		}, expectedMsg: "Missing field 'tx_json.Destination'."},
		{name: "invalid destination", fields: map[string]any{
			"Amount": "1", "Destination": "not-an-account",
		}, expectedMsg: "Invalid field 'tx_json.Destination'."},
		{name: "differing deliver max", fields: map[string]any{
			"Amount": "1", "DeliverMax": "2",
			"Destination": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		}, expectedMsg: "Cannot specify differing 'Amount' and 'DeliverMax'"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			txJSON := make(map[string]any, len(base)+len(test.fields))
			for key, value := range base {
				txJSON[key] = value
			}
			for key, value := range test.fields {
				txJSON[key] = value
			}
			rpcErr := callTargetSignFor(t, txJSON)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Equal(t, test.expectedMsg, rpcErr.Message)
		})
	}
}

func TestSign_SignatureTargetRejectsTopLevelSigners(t *testing.T) {
	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
		"tx_json": {
			"TransactionType": "LoanSet",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"LoanBrokerID": "0000000000000000000000000000000000000000000000000000000000000000",
			"PrincipalRequested": "1",
			"Fee": "10",
			"Sequence": 1,
			"Signers": []
		},
		"passphrase": "counterparty phrase",
		"key_type": "secp256k1",
		"offline": true,
		"signature_target": "CounterpartySignature"
	}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcALREADY_MULTISIG, rpcErr.Code)
}

func TestSign_SignersDoesNotMaskInvalidAccount(t *testing.T) {
	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
		"tx_json": {
			"TransactionType": "LoanSet",
			"Account": "not-an-account",
			"LoanBrokerID": "0000000000000000000000000000000000000000000000000000000000000000",
			"PrincipalRequested": "1",
			"Fee": "10",
			"Sequence": 1,
			"SigningPubKey": "",
			"Signers": []
		},
		"passphrase": "counterparty phrase",
		"key_type": "secp256k1",
		"offline": true,
		"signature_target": "CounterpartySignature"
	}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcSRC_ACT_MALFORMED, rpcErr.Code)
	assert.Equal(t, "Invalid field 'tx_json.Account'.", rpcErr.Message)
}

func TestSignFor_SortsSignersByAccountIDBytes(t *testing.T) {
	const signerAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	otherAccount := accountWithOppositeStringOrder(t, signerAccount)
	existing := testSignerWrapper(otherAccount)
	txJSON := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Destination":     signerAccount,
		"Amount":          "1",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "",
		"Signers":         []any{existing},
	}

	result, rpcErr := callSignFor(t, txJSON, signerAccount)
	require.Nil(t, rpcErr)
	signers := result["tx_json"].(map[string]any)["Signers"].([]map[string]any)
	require.Len(t, signers, 2)
	first := signers[0]["Signer"].(map[string]any)["Account"].(string)
	second := signers[1]["Signer"].(map[string]any)["Account"].(string)
	_, firstID, err := addresscodec.DecodeClassicAddressToAccountID(first)
	require.NoError(t, err)
	_, secondID, err := addresscodec.DecodeClassicAddressToAccountID(second)
	require.NoError(t, err)
	assert.Less(t, bytes.Compare(firstID, secondID), 0)
}

func TestSignFor_RejectsDuplicateExistingSigners(t *testing.T) {
	const signerAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	duplicate := accountWithOppositeStringOrder(t, signerAccount)
	txJSON := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Destination":     signerAccount,
		"Amount":          "1",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "",
		"Signers": []any{
			testSignerWrapper(duplicate),
			testSignerWrapper(duplicate),
		},
	}

	_, rpcErr := callSignFor(t, txJSON, signerAccount)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Duplicate Signers:Signer:Account entries")
}

func TestSignFor_RejectsFeePayerSigner(t *testing.T) {
	const signerAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	txJSON := map[string]any{
		"TransactionType": "Payment",
		"Account":         signerAccount,
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "",
	}

	_, rpcErr := callSignFor(t, txJSON, signerAccount)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "A Signer may not be the transaction's Account")
}

func callSignFor(t *testing.T, txJSON map[string]any, account string) (map[string]any, *rpcerrors.RpcError) {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"account":    account,
		"tx_json":    txJSON,
		"passphrase": "masterpassphrase",
		"key_type":   "secp256k1",
	})
	require.NoError(t, err)
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	result, rpcErr := handler.Handle(signingEnabledContext(ctx), params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return result.(map[string]any), nil
}

func callTargetSignFor(t *testing.T, txJSON map[string]any) *rpcerrors.RpcError {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"account":          "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json":          txJSON,
		"passphrase":       "masterpassphrase",
		"key_type":         "secp256k1",
		"signature_target": "CounterpartySignature",
	})
	require.NoError(t, err)
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	_, rpcErr := handler.Handle(signingEnabledContext(ctx), params)
	return rpcErr
}

func testSignerWrapper(account string) map[string]any {
	return map[string]any{
		"Signer": map[string]any{
			"Account":       account,
			"SigningPubKey": "03EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE3",
			"TxnSignature":  "AA",
		},
	}
}

func accountWithOppositeStringOrder(t *testing.T, reference string) string {
	t.Helper()
	_, referenceID, err := addresscodec.DecodeClassicAddressToAccountID(reference)
	require.NoError(t, err)
	for first := 0; first <= 255; first++ {
		for second := 0; second <= 255; second++ {
			candidateID := make([]byte, 20)
			candidateID[0] = byte(first)
			candidateID[1] = byte(second)
			candidateID[19] = 0x42
			candidate, encodeErr := addresscodec.EncodeAccountIDToClassicAddress(candidateID)
			require.NoError(t, encodeErr)
			if bytes.Equal(candidateID, referenceID) {
				continue
			}
			if (bytes.Compare(candidateID, referenceID) < 0) != (candidate < reference) {
				return candidate
			}
		}
	}
	t.Fatal("failed to find AccountID whose byte and base58 orders differ")
	return ""
}
