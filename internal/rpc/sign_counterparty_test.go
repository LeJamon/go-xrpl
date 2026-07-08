package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
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
	result, rpcErr := handler.Handle(ctx, params)
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
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"Fee": "10",
			"Sequence": 1
		},
		"passphrase": "masterpassphrase",
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
	decoded, err := binarycodec.Decode(blob)
	require.NoError(t, err)
	txBytes, err := json.Marshal(decoded)
	require.NoError(t, err)
	parsed, err := txcore.ParseJSON(txBytes)
	require.NoError(t, err)

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
		"offline": true,
		"signature_target": "NotAValidField"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "NotAValidField")
}

// TestSign_SignatureTarget_SkipsOwnershipCheck confirms that with a
// signature_target the signing key need not correspond to tx_json.Account: a
// counterparty key that derives to a different address does not raise the
// "does not match" ownership error.
func TestSign_SignatureTarget_SkipsOwnershipCheck(t *testing.T) {
	res := signOffline(t, json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"Fee": "10",
			"Sequence": 1
		},
		"passphrase": "counterparty phrase",
		"offline": true,
		"signature_target": "CounterpartySignature"
	}`))
	resTx := res["tx_json"].(map[string]any)
	// The counterparty's key does not match Account, but signing succeeded and
	// its signature was written into the nested object.
	cpObj, ok := resTx["CounterpartySignature"].(map[string]any)
	require.True(t, ok, "expected CounterpartySignature in %v", resTx)
	assert.NotEmpty(t, cpObj["TxnSignature"])
}

// TestSignFor_SignatureTarget attaches a multi-signer into the nested
// CounterpartySignature object via sign_for.
func TestSignFor_SignatureTarget(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}

	// account is the signer; the tx Account is a different primary account.
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
		"signature_target": "CounterpartySignature"
	}`)
	result, rpcErr := handler.Handle(ctx, params)
	require.Nil(t, rpcErr)
	resTx := result.(map[string]any)["tx_json"].(map[string]any)

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
}

// TestSignFor_SignatureTarget_Invalid rejects an unknown signature_target.
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
		"signature_target": "Memo"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "Memo")
}
