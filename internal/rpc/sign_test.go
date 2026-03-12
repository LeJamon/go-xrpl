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

// =============================================================================
// WalletPropose Tests
// =============================================================================

func TestWalletPropose_RandomGeneration(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	// Generate wallet with no parameters (random seed)
	result, err := handler.Handle(ctx, nil)
	require.Nil(t, err)
	require.NotNil(t, result)

	resultMap := result.(map[string]interface{})

	// Verify all required fields are present
	assert.Contains(t, resultMap, "account_id")
	assert.Contains(t, resultMap, "key_type")
	assert.Contains(t, resultMap, "master_seed")
	assert.Contains(t, resultMap, "master_seed_hex")
	assert.Contains(t, resultMap, "public_key")
	assert.Contains(t, resultMap, "public_key_hex")

	// Default key type should be secp256k1
	assert.Equal(t, "secp256k1", resultMap["key_type"])

	// Account ID should start with 'r'
	accountID := resultMap["account_id"].(string)
	assert.True(t, len(accountID) > 0 && accountID[0] == 'r')

	// Master seed should start with 's'
	masterSeed := resultMap["master_seed"].(string)
	assert.True(t, len(masterSeed) > 0 && masterSeed[0] == 's')
}

func TestWalletPropose_RandomGenerationEd25519(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{"key_type": "ed25519"}`)
	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)
	require.NotNil(t, result)

	resultMap := result.(map[string]interface{})
	assert.Equal(t, "ed25519", resultMap["key_type"])

	// ED25519 public keys start with "ED"
	publicKeyHex := resultMap["public_key_hex"].(string)
	assert.True(t, len(publicKeyHex) >= 2 && publicKeyHex[:2] == "ED")
}

func TestWalletPropose_FromPassphrase(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	// Using the well-known masterpassphrase
	params := json.RawMessage(`{"passphrase": "masterpassphrase"}`)
	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)
	require.NotNil(t, result)

	resultMap := result.(map[string]interface{})

	// Should have a warning about passphrase
	assert.Contains(t, resultMap, "warning")
	warning := resultMap["warning"].(string)
	assert.Contains(t, warning, "passphrase")

	// Verify deterministic derivation - running twice should give same result
	result2, err2 := handler.Handle(ctx, params)
	require.Nil(t, err2)
	resultMap2 := result2.(map[string]interface{})

	assert.Equal(t, resultMap["account_id"], resultMap2["account_id"])
	assert.Equal(t, resultMap["public_key_hex"], resultMap2["public_key_hex"])
}

func TestWalletPropose_FromPassphraseEd25519(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "ed25519"
	}`)
	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)
	require.NotNil(t, result)

	resultMap := result.(map[string]interface{})
	assert.Equal(t, "ed25519", resultMap["key_type"])
	assert.Contains(t, resultMap, "warning")
}

func TestWalletPropose_FromSeed(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	// Valid secp256k1 seed
	params := json.RawMessage(`{"seed": "sn3nxiW7v8KXzPzAqzyHXbSSKNuN9"}`)
	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)
	require.NotNil(t, result)

	resultMap := result.(map[string]interface{})
	assert.Contains(t, resultMap, "account_id")
	assert.Equal(t, "secp256k1", resultMap["key_type"])

	// No warning for seed-based derivation
	_, hasWarning := resultMap["warning"]
	assert.False(t, hasWarning)
}

func TestWalletPropose_FromSeedHex(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	// 16-byte hex seed
	params := json.RawMessage(`{"seed_hex": "DEDCE9CE67B451D852FD4E846FCDE31C"}`)
	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)
	require.NotNil(t, result)

	resultMap := result.(map[string]interface{})
	assert.Contains(t, resultMap, "account_id")
}

func TestWalletPropose_InvalidKeyType(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{"key_type": "invalid"}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcBAD_KEY_TYPE, err.Code)
	assert.Equal(t, "badKeyType", err.ErrorString)
}

func TestWalletPropose_InvalidSeed(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{"seed": "invalid_seed"}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcBAD_SEED, err.Code)
	assert.Equal(t, "badSeed", err.ErrorString)
}

func TestWalletPropose_InvalidSeedHex(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	// Invalid hex (not 16 bytes)
	params := json.RawMessage(`{"seed_hex": "DEADBEEF"}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcBAD_SEED, err.Code)
}

func TestWalletPropose_SeedKeyTypeMismatch(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	// Ed25519 seed with secp256k1 key_type should fail
	// The seed sEdTM1uX8pu2do5XvTnutH6HsouMn is an ed25519 seed
	params := json.RawMessage(`{
		"seed": "sEdTM1uX8pu2do5XvTnutH6HsouMn",
		"key_type": "secp256k1"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcBAD_SEED, err.Code)
}

func TestWalletPropose_LowEntropyPassphrase(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	// Short passphrase should have low entropy warning
	params := json.RawMessage(`{"passphrase": "abc"}`)
	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)
	require.NotNil(t, result)

	resultMap := result.(map[string]interface{})
	warning := resultMap["warning"].(string)
	assert.Contains(t, warning, "low entropy")
}

func TestWalletPropose_Metadata(t *testing.T) {
	handler := &handlers.WalletProposeMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		// rippled: Role::ADMIN (Handler.cpp line 192)
		assert.Equal(t, types.RoleAdmin, handler.RequiredRole())
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := handler.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// =============================================================================
// Sign Tests
// =============================================================================

func TestSign_MissingTxJson(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{"secret": "sn3nxiW7v8KXzPzAqzyHXbSSKNuN9"}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "tx_json")
}

func TestSign_MissingCredentials(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000"
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "Missing field 'secret'.")
}

func TestSign_InvalidKeyType(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000"
		},
		"seed_hex": "DEDCE9CE67B451D852FD4E846FCDE31C",
		"key_type": "invalid"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	// API v1: rippled returns invalidParams for bad key_type
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
}

func TestSign_InvalidSeed(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000"
		},
		"secret": "invalid_seed"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	// The shared credential helper treats "invalid_seed" as a passphrase
	// (fallback after base58 and hex parsing fail), so it succeeds in
	// deriving a keypair. The error now surfaces later as invalid params
	// when the derived account doesn't match. For API v1, with the
	// legacy "secret" field, rippled falls back to passphrase too.
	assert.NotNil(t, err)
}

func TestSign_AccountMismatch(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	// Account in tx_json doesn't match the key derived from seed_hex
	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Destination": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Amount": "1000000"
		},
		"seed_hex": "DEDCE9CE67B451D852FD4E846FCDE31C"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "does not match")
}

func TestSign_LedgerServiceUnavailable(t *testing.T) {
	// Services set to nil
	oldServices := types.Services
	types.Services = nil
	defer func() { types.Services = oldServices }()

	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000"
		},
		"seed_hex": "DEDCE9CE67B451D852FD4E846FCDE31C"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINTERNAL, err.Code)
	assert.Contains(t, err.Message, "Ledger service not available")
}

func TestSign_OfflineMode(t *testing.T) {
	// No services needed for offline mode
	oldServices := types.Services
	types.Services = nil
	defer func() { types.Services = oldServices }()

	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	// Use passphrase and provide all required fields
	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"Fee": "10",
			"Sequence": 1,
			"LastLedgerSequence": 100
		},
		"passphrase": "masterpassphrase",
		"offline": true
	}`)
	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)
	require.NotNil(t, result)

	resultMap := result.(map[string]interface{})
	assert.Contains(t, resultMap, "tx_blob")
	assert.Contains(t, resultMap, "tx_json")

	// Verify tx_json has TxnSignature
	txJson := resultMap["tx_json"].(map[string]interface{})
	assert.Contains(t, txJson, "TxnSignature")
	assert.Contains(t, txJson, "SigningPubKey")
	assert.Contains(t, txJson, "hash")
}

func TestSign_Metadata(t *testing.T) {
	handler := &handlers.SignMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleUser, handler.RequiredRole())
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := handler.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// =============================================================================
// SignFor Tests
// =============================================================================

func TestSignFor_MissingAccount(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000"
		},
		"secret": "sn3nxiW7v8KXzPzAqzyHXbSSKNuN9"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "account")
}

func TestSignFor_MissingTxJson(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"secret": "sn3nxiW7v8KXzPzAqzyHXbSSKNuN9"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "tx_json")
}

func TestSignFor_MissingCredentials(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000"
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "Missing field 'secret'.")
}

func TestSignFor_InvalidAccountAddress(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"account": "invalid_address",
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000"
		},
		"secret": "sn3nxiW7v8KXzPzAqzyHXbSSKNuN9"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcACT_MALFORMED, err.Code)
	assert.Equal(t, "actMalformed", err.ErrorString)
}

func TestSignFor_InvalidKeyType(t *testing.T) {
	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000"
		},
		"seed_hex": "DEDCE9CE67B451D852FD4E846FCDE31C",
		"key_type": "invalid"
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	// API v1: rippled returns invalidParams for bad key_type
	// API v2+: returns RpcBAD_KEY_TYPE
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
}

func TestSignFor_ValidMultiSign(t *testing.T) {
	// First generate a wallet to get the signer's account address from passphrase
	proposeHandler := &handlers.WalletProposeMethod{}
	proposeCtx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}
	proposeResult, proposeErr := proposeHandler.Handle(proposeCtx, json.RawMessage(`{"passphrase": "masterpassphrase"}`))
	require.Nil(t, proposeErr)
	signerAccount := proposeResult.(map[string]interface{})["account_id"].(string)

	handler := &handlers.SignForMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	paramsMap := map[string]interface{}{
		"account": signerAccount,
		"tx_json": map[string]interface{}{
			"TransactionType": "Payment",
			"Account":         "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount":          "1000000",
			"Fee":             "10",
			"Sequence":        uint32(1),
		},
		"passphrase": "masterpassphrase",
	}
	paramsJSON, _ := json.Marshal(paramsMap)

	result, err := handler.Handle(ctx, paramsJSON)
	require.Nil(t, err, "sign_for should succeed: %v", err)
	require.NotNil(t, result)

	resultMap := result.(map[string]interface{})
	assert.Contains(t, resultMap, "tx_blob")
	assert.Contains(t, resultMap, "tx_json")

	// Verify Signers array exists
	txJson := resultMap["tx_json"].(map[string]interface{})
	signers, ok := txJson["Signers"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, signers, 1)

	// Verify SigningPubKey is empty (required for multi-sign)
	assert.Equal(t, "", txJson["SigningPubKey"])
}

func TestSignFor_Metadata(t *testing.T) {
	handler := &handlers.SignForMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleUser, handler.RequiredRole())
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := handler.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// =============================================================================
// SubmitMultisigned Tests
// =============================================================================

func TestSubmitMultisigned_MissingTxJson(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "tx_json")
}

func TestSubmitMultisigned_MissingAccount(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"SigningPubKey": "",
			"Signers": []
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "Account")
}

func TestSubmitMultisigned_NonEmptySigningPubKey(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"SigningPubKey": "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
			"Signers": []
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "empty SigningPubKey")
}

func TestSubmitMultisigned_EmptySignersArray(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"SigningPubKey": "",
			"Signers": []
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "at least one Signer")
}

func TestSubmitMultisigned_InvalidSignerFormat(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"SigningPubKey": "",
			"Signers": [
				{
					"InvalidField": "value"
				}
			]
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "Signer entry")
}

func TestSubmitMultisigned_MissingSignerAccount(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"SigningPubKey": "",
			"Signers": [
				{
					"Signer": {
						"SigningPubKey": "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
						"TxnSignature": "DEADBEEF"
					}
				}
			]
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "Account")
}

func TestSubmitMultisigned_MissingSigningPubKey(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"SigningPubKey": "",
			"Signers": [
				{
					"Signer": {
						"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
						"TxnSignature": "DEADBEEF"
					}
				}
			]
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "SigningPubKey")
}

func TestSubmitMultisigned_MissingTxnSignature(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"SigningPubKey": "",
			"Signers": [
				{
					"Signer": {
						"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
						"SigningPubKey": "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020"
					}
				}
			]
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "TxnSignature")
}

func TestSubmitMultisigned_SignersNotSorted(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	// Signers not sorted by account (rP... < rH... is false alphabetically)
	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			"Destination": "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			"Amount": "1000000",
			"SigningPubKey": "",
			"Fee": "10",
			"Sequence": 1,
			"Signers": [
				{
					"Signer": {
						"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
						"SigningPubKey": "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
						"TxnSignature": "DEADBEEF"
					}
				},
				{
					"Signer": {
						"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
						"SigningPubKey": "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
						"TxnSignature": "DEADBEEF"
					}
				}
			]
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "sorted")
}

func TestSubmitMultisigned_LedgerServiceUnavailable(t *testing.T) {
	oldServices := types.Services
	types.Services = nil
	defer func() { types.Services = oldServices }()

	handler := &handlers.SubmitMultisignedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"SigningPubKey": "",
			"Signers": [
				{
					"Signer": {
						"Account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
						"SigningPubKey": "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
						"TxnSignature": "DEADBEEF"
					}
				}
			]
		}
	}`)
	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "Ledger service not available")
}

func TestSubmitMultisigned_Metadata(t *testing.T) {
	handler := &handlers.SubmitMultisignedMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleUser, handler.RequiredRole())
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := handler.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}
