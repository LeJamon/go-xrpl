package rpc

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validMultisignedTxJSON returns a minimal valid multi-signed tx_json for testing.
// Override individual fields to test specific validation failures.
func validMultisignedTxJSON() map[string]any {
	return map[string]any{
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"TransactionType": "Payment",
		"Destination":     "rPMh7Pi9ct699iZUTWzJaUOVnFNaREiPik",
		"Amount":          "1000000",
		"Fee":             "12",
		"Sequence":        float64(1),
		"SigningPubKey":   "",
		"Signers": []any{
			map[string]any{
				"Signer": map[string]any{
					"Account":       "rPMh7Pi9ct699iZUTWzJaUOVnFNaREiPik",
					"SigningPubKey": "0379F17CFA0FFD7518181594BE69FE9A10C2089E0FF0C4AE1DEF230657210000ED",
					"TxnSignature":  "3045022100DEADBEEF",
				},
			},
		},
	}
}

func makeSubmitMultisignedParams(t *testing.T, txJSON map[string]any) json.RawMessage {
	t.Helper()
	request := map[string]any{
		"tx_json": txJSON,
	}
	b, err := json.Marshal(request)
	require.NoError(t, err)
	return b
}

// TestSubmitMultisigned_MissingSequence verifies that the handler returns
// rpcINVALID_PARAMS "Missing field 'tx_json.Sequence'." when Sequence is absent.
// Matches rippled: checkMultiSignFields -> missing_field_error("tx_json.Sequence")
func TestSubmitMultisigned_MissingSequence(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	delete(txJSON, "Sequence")

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "Missing field 'tx_json.Sequence'.", rpcErr.Message)
}

// TestSubmitMultisigned_InvalidSequenceType verifies that a non-numeric Sequence is rejected.
func TestSubmitMultisigned_InvalidSequenceType(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	txJSON["Sequence"] = "not_a_number"

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "tx_json.Sequence")
}

// TestSubmitMultisigned_FeeNotPresent verifies that missing Fee is rejected.
// Matches rippled: "Invalid Fee field.  Fees must be specified in XRP."
func TestSubmitMultisigned_FeeNotPresent(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	delete(txJSON, "Fee")

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "Invalid Fee field.  Fees must be specified in XRP.", rpcErr.Message)
}

// TestSubmitMultisigned_FeeNotString verifies that a non-string Fee is rejected.
// Fee must be a string of drops.
func TestSubmitMultisigned_FeeNotString(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	txJSON["Fee"] = 12 // numeric, not string

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "Invalid Fee field.  Fees must be specified in XRP.", rpcErr.Message)
}

// TestSubmitMultisigned_FeeZero verifies that Fee "0" is rejected.
// Matches rippled: "Invalid Fee field.  Fees must be greater than zero."
func TestSubmitMultisigned_FeeZero(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	txJSON["Fee"] = "0"

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "Invalid Fee field.  Fees must be greater than zero.", rpcErr.Message)
}

// TestSubmitMultisigned_FeeNegative verifies that a negative Fee is rejected.
func TestSubmitMultisigned_FeeNegative(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	txJSON["Fee"] = "-10"

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "Invalid Fee field.  Fees must be greater than zero.", rpcErr.Message)
}

// TestSubmitMultisigned_FeeNotNumericString verifies that a non-numeric Fee string is rejected.
func TestSubmitMultisigned_FeeNotNumericString(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	txJSON["Fee"] = "abc"

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "Invalid Fee field.  Fees must be specified in XRP.", rpcErr.Message)
}

// TestSubmitMultisigned_TxnSignaturePresent verifies that a TxnSignature field on the
// outer tx_json is rejected.
// Matches rippled: rpcError(rpcSIGNING_MALFORMED) -> code 63, "signingMalformed"
func TestSubmitMultisigned_TxnSignaturePresent(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	txJSON["TxnSignature"] = "DEADBEEF"

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcSIGNING_MALFORMED, rpcErr.Code)
	assert.Equal(t, "signingMalformed", rpcErr.ErrorString)
	assert.Equal(t, "Signing of transaction is malformed.", rpcErr.Message)
}

// TestSubmitMultisigned_SelfSigning verifies that a signer whose Account matches the
// transaction's Account is rejected.
// Matches rippled sortAndValidateSigners:
// "A Signer may not be the transaction's Account (<addr>)."
func TestSubmitMultisigned_SelfSigning(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	txAccount := txJSON["Account"].(string)

	// Replace the signer's Account with the transaction source account.
	signers := txJSON["Signers"].([]any)
	signerWrapper := signers[0].(map[string]any)
	signer := signerWrapper["Signer"].(map[string]any)
	signer["Account"] = txAccount

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "A Signer may not be the transaction's Account")
	assert.Contains(t, rpcErr.Message, txAccount)
}

// TestSubmitMultisigned_DuplicateSigners verifies that duplicate signer accounts are rejected.
// Matches rippled sortAndValidateSigners:
// "Duplicate Signers:Signer:Account entries (<addr>) are not allowed."
func TestSubmitMultisigned_DuplicateSigners(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	dupAccount := "rPMh7Pi9ct699iZUTWzJaUOVnFNaREiPik"
	txJSON := validMultisignedTxJSON()
	txJSON["Signers"] = []any{
		map[string]any{
			"Signer": map[string]any{
				"Account":       dupAccount,
				"SigningPubKey": "0379F17CFA0FFD7518181594BE69FE9A10C2089E0FF0C4AE1DEF230657210000ED",
				"TxnSignature":  "3045022100DEADBEEF",
			},
		},
		map[string]any{
			"Signer": map[string]any{
				"Account":       dupAccount,
				"SigningPubKey": "0279F17CFA0FFD7518181594BE69FE9A10C2089E0FF0C4AE1DEF230657210000EE",
				"TxnSignature":  "3045022100BEEFDEAD",
			},
		},
	}

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Duplicate Signers:Signer:Account entries")
	assert.Contains(t, rpcErr.Message, dupAccount)
}

// TestSubmitMultisigned_FeeValidPositive verifies that a valid positive Fee passes validation.
// This test proceeds past Fee validation and hits the binary encoding step.
func TestSubmitMultisigned_FeeValidPositive(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	txJSON["Fee"] = "10"

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	// Should fail at encoding/submission, not at Fee validation.
	if rpcErr != nil {
		assert.NotContains(t, rpcErr.Message, "Fee")
	}
}

// TestSubmitMultisigned_ValidationOrder_SequenceBeforeTxnSignature verifies that
// Sequence is checked before TxnSignature.
func TestSubmitMultisigned_ValidationOrder_SequenceBeforeTxnSignature(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	// Both Sequence missing AND TxnSignature present -- Sequence error should come first.
	txJSON := validMultisignedTxJSON()
	delete(txJSON, "Sequence")
	txJSON["TxnSignature"] = "DEADBEEF"

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, "Missing field 'tx_json.Sequence'.", rpcErr.Message)
}

// TestSubmitMultisigned_SrcActMalformed verifies that an unparseable source
// account is rejected with rpcSRC_ACT_MALFORMED.
// Matches rippled checkTxJsonFields: "Invalid field 'tx_json.Account'."
func TestSubmitMultisigned_SrcActMalformed(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	txJSON["Account"] = "not_an_address"

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcSRC_ACT_MALFORMED, rpcErr.Code)
	assert.Equal(t, "srcActMalformed", rpcErr.ErrorString)
	assert.Equal(t, "Invalid field 'tx_json.Account'.", rpcErr.Message)
}

// TestSubmitMultisigned_SrcActNotFound verifies that a source account absent
// from the current ledger is rejected with rpcSRC_ACT_NOT_FOUND.
// Matches rippled transactionSubmitMultiSigned: the account SLE read
// (TransactionSign.cpp:1259-1270).
func TestSubmitMultisigned_SrcActNotFound(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	mock.accountInfoErr = svcerr.ErrAccountNotFound
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, validMultisignedTxJSON()))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcSRC_ACT_NOT_FOUND, rpcErr.Code)
	assert.Equal(t, "srcActNotFound", rpcErr.ErrorString)
	assert.Equal(t, "Source account not found.", rpcErr.Message)
}

// TestSubmitMultisigned_ValidationOrder_SrcActNotFoundBeforeTxnSignature
// verifies that the source-account existence check fires before the
// post-serialization TxnSignature check, matching rippled's order (the SLE
// read at TransactionSign.cpp:1259 precedes the serialized-field checks at
// 1325-1390).
func TestSubmitMultisigned_ValidationOrder_SrcActNotFoundBeforeTxnSignature(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	mock.accountInfoErr = svcerr.ErrAccountNotFound
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	txJSON["TxnSignature"] = "DEADBEEF"

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcSRC_ACT_NOT_FOUND, rpcErr.Code)
}

// TestSubmitMultisigned_HappyPath submits a well-formed multi-signed
// transaction against a ledger where the source account exists and asserts
// the engine response is surfaced. Signer-list / quorum validation is left
// to the engine (tefNOT_MULTI_SIGNING / tefBAD_QUORUM), so the RPC layer
// accepts the submission.
func TestSubmitMultisigned_HappyPath(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	txJSON := validMultisignedTxJSON()
	// Use addresses that pass base58 decoding so binary encoding succeeds.
	txJSON["Destination"] = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	signer := txJSON["Signers"].([]any)[0].(map[string]any)["Signer"].(map[string]any)
	signer["Account"] = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	result, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resp, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "tesSUCCESS", resp["engine_result"])
	assert.NotEmpty(t, resp["tx_blob"])
	respTxJSON, ok := resp["tx_json"].(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, respTxJSON["hash"])
}

// TestSubmitMultisigned_ValidationOrder_TxnSignatureBeforeFee verifies that
// TxnSignature is checked before Fee.
func TestSubmitMultisigned_ValidationOrder_TxnSignatureBeforeFee(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1, Services: services}

	// Both TxnSignature present AND Fee invalid -- TxnSignature error should come first.
	txJSON := validMultisignedTxJSON()
	txJSON["TxnSignature"] = "DEADBEEF"
	txJSON["Fee"] = "0"

	_, rpcErr := handler.Handle(ctx, makeSubmitMultisignedParams(t, txJSON))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcSIGNING_MALFORMED, rpcErr.Code)
}
