package rpc

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test vectors from rippled's RPCCall_test.cpp and PayChan_test.cpp

func TestChannelAuthorize_MissingChannelID(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "channel_id")
}

func TestChannelAuthorize_MissingAmount(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "amount")
}

func TestChannelAuthorize_MissingSecret(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "secret")
}

func TestChannelAuthorize_MultipleSecrets(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"seed": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "Exactly one")
}

func TestChannelAuthorize_SecretNotAllowedWithKeyType(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "secret field is not allowed")
}

func TestChannelAuthorize_BadKeyType(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion2,
	}

	params := json.RawMessage(`{
		"seed": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"key_type": "secp257k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcBAD_KEY_TYPE, err.Code)
	assert.Equal(t, "badKeyType", err.ErrorString)
}

func TestChannelAuthorize_ChannelIDTooShort(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcCHANNEL_MALFORMED, err.Code)
	assert.Equal(t, "channelMalformed", err.ErrorString)
}

func TestChannelAuthorize_ChannelIDTooLong(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "10123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcCHANNEL_MALFORMED, err.Code)
}

func TestChannelAuthorize_ChannelIDNotHex(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEZ",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcCHANNEL_MALFORMED, err.Code)
}

func TestChannelAuthorize_NegativeAmount(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "-1"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcCHANNEL_AMT_MALFORMED, err.Code)
	assert.Equal(t, "channelAmtMalformed", err.ErrorString)
}

func TestChannelAuthorize_AmountOverflow(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "18446744073709551616"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcCHANNEL_AMT_MALFORMED, err.Code)
}

func TestChannelAuthorize_ValidWithSecret(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	// Use masterpassphrase seed which generates a known keypair
	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]interface{})
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
	// DER signature for secp256k1 is typically 70-72 bytes, hex encoded = 140-144 chars
	assert.GreaterOrEqual(t, len(signature), 128)
}

func TestChannelAuthorize_ValidWithSeed(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"seed": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "2000"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]interface{})
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
}

func TestChannelAuthorize_ValidWithPassphrase(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]interface{})
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
}

func TestChannelAuthorize_ValidWithSeedHex(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	// masterpassphrase SHA512Half first 16 bytes = DEDCE9CE67B451D852FD4E846FCDE31C
	params := json.RawMessage(`{
		"seed_hex": "DEDCE9CE67B451D852FD4E846FCDE31C",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]interface{})
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
}

func TestChannelAuthorize_ValidEd25519(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	// Ed25519 seed
	params := json.RawMessage(`{
		"seed": "sEdTzRkEgPoxDG1mJ6WkSucHWnMkm1H",
		"key_type": "ed25519",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]interface{})
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
	// Ed25519 signature is 64 bytes, hex encoded = 128 chars
	assert.Equal(t, 128, len(signature))
}

func TestChannelAuthorize_MaxAmount(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	// Max uint64 value
	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "18446744073709551615"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]interface{})
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
}

// Test that channel_authorize and channel_verify work together
func TestChannelAuthorizeAndVerify_Integration(t *testing.T) {
	channelID := "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	amount := "1000000"

	// First, generate the keypair for verification
	seedBytes, _ := hex.DecodeString("DEDCE9CE67B451D852FD4E846FCDE31C")
	algo := secp256k1.SECP256K1()
	_, pubKeyHex, err := algo.DeriveKeypair(seedBytes, false)
	require.NoError(t, err)

	// Authorize
	authorizeHandler := &handlers.ChannelAuthorizeMethod{}
	authorizeCtx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	authorizeParams := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"channel_id": "` + channelID + `",
		"amount": "` + amount + `"
	}`)

	authorizeResult, authorizeErr := authorizeHandler.Handle(authorizeCtx, authorizeParams)
	require.Nil(t, authorizeErr)

	resultMap := authorizeResult.(map[string]interface{})
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)

	// Verify
	verifyHandler := &handlers.ChannelVerifyMethod{}
	verifyCtx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	verifyParams := json.RawMessage(`{
		"public_key": "` + pubKeyHex + `",
		"channel_id": "` + channelID + `",
		"amount": "` + amount + `",
		"signature": "` + signature + `"
	}`)

	verifyResult, verifyErr := verifyHandler.Handle(verifyCtx, verifyParams)
	require.Nil(t, verifyErr)

	verifyResultMap := verifyResult.(map[string]interface{})
	verified, ok := verifyResultMap["signature_verified"].(bool)
	require.True(t, ok)
	assert.True(t, verified, "Signature should verify correctly")
}

func TestChannelAuthorizeAndVerify_IntegrationEd25519(t *testing.T) {
	channelID := "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	amount := "5000000"

	// First, generate the keypair for verification
	seedBytes, _ := hex.DecodeString("DEDCE9CE67B451D852FD4E846FCDE31C")
	algo := ed25519.ED25519()
	_, pubKeyHex, err := algo.DeriveKeypair(seedBytes, false)
	require.NoError(t, err)

	// Authorize
	authorizeHandler := &handlers.ChannelAuthorizeMethod{}
	authorizeCtx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	authorizeParams := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "ed25519",
		"channel_id": "` + channelID + `",
		"amount": "` + amount + `"
	}`)

	authorizeResult, authorizeErr := authorizeHandler.Handle(authorizeCtx, authorizeParams)
	require.Nil(t, authorizeErr)

	resultMap := authorizeResult.(map[string]interface{})
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)

	// Verify
	verifyHandler := &handlers.ChannelVerifyMethod{}
	verifyCtx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	verifyParams := json.RawMessage(`{
		"public_key": "` + pubKeyHex + `",
		"channel_id": "` + channelID + `",
		"amount": "` + amount + `",
		"signature": "` + signature + `"
	}`)

	verifyResult, verifyErr := verifyHandler.Handle(verifyCtx, verifyParams)
	require.Nil(t, verifyErr)

	verifyResultMap := verifyResult.(map[string]interface{})
	verified, ok := verifyResultMap["signature_verified"].(bool)
	require.True(t, ok)
	assert.True(t, verified, "Ed25519 signature should verify correctly")
}

// Test that the message serialization matches rippled
func TestChannelAuthorize_MessageFormat(t *testing.T) {
	channelID := "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	amount := "1000000"

	claimJSON := map[string]any{
		"Channel": channelID,
		"Amount":  amount,
	}

	messageHex, err := binarycodec.EncodeForSigningClaim(claimJSON)
	require.NoError(t, err)

	// Verify the message starts with the CLM prefix (434C4D00)
	assert.True(t, strings.HasPrefix(messageHex, "434C4D00"), "Message should start with CLM hash prefix")

	// Message should be: prefix (4) + channel_id (32) + amount (8) = 44 bytes = 88 hex chars
	assert.Equal(t, 88, len(messageHex), "Message should be 44 bytes (88 hex chars)")

	// Channel ID should follow the prefix
	assert.Equal(t, channelID, messageHex[8:72], "Channel ID should follow the prefix")
}

func TestChannelAuthorize_BadSeed(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"seed": "invalid_seed",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcBAD_SEED, err.Code)
	assert.Equal(t, "badSeed", err.ErrorString)
}
