package rpc

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test vectors from rippled's RPCCall_test.cpp

func TestChannelVerify_MissingPublicKey(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000",
		"signature": "DEADBEEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "public_key")
}

func TestChannelVerify_MissingChannelID(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"public_key": "021D93E21C44160A1B3B66DA1F37B86BE39FFEA3FC4B95FAA2063F82EE823599F6",
		"amount": "1000000",
		"signature": "DEADBEEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "channel_id")
}

func TestChannelVerify_MissingAmount(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"public_key": "021D93E21C44160A1B3B66DA1F37B86BE39FFEA3FC4B95FAA2063F82EE823599F6",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"signature": "DEADBEEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "amount")
}

func TestChannelVerify_MissingSignature(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"public_key": "021D93E21C44160A1B3B66DA1F37B86BE39FFEA3FC4B95FAA2063F82EE823599F6",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "signature")
}

func TestChannelVerify_MalformedPublicKey(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	// Public key with invalid checksum (changed last character from 'v' to 'V')
	params := json.RawMessage(`{
		"public_key": "aB4BXXLuPu8DpVuyq1DBiu3SrPdtK9AYZisKhu8mvkoiUD8J9GoV",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "2000",
		"signature": "DEADBEEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcPUBLIC_MALFORMED, err.Code)
	assert.Equal(t, "publicMalformed", err.ErrorString)
}

func TestChannelVerify_MalformedHexPublicKey(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	// Public key too short (missing last character)
	params := json.RawMessage(`{
		"public_key": "021D93E21C44160A1B3B66DA1F37B86BE39FFEA3FC4B95FAA2063F82EE823599F",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "2000",
		"signature": "DEADBEEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcPUBLIC_MALFORMED, err.Code)
}

func TestChannelVerify_InvalidChannelIDTooLong(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"public_key": "aB4BXXLuPu8DpVuyq1DBiu3SrPdtK9AYZisKhu8mvkoiUD8J9Gov",
		"channel_id": "10123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "2000",
		"signature": "DEADBEEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcCHANNEL_MALFORMED, err.Code)
	assert.Equal(t, "channelMalformed", err.ErrorString)
}

func TestChannelVerify_InvalidChannelIDTooShort(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"public_key": "aB4BXXLuPu8DpVuyq1DBiu3SrPdtK9AYZisKhu8mvkoiUD8J9Gov",
		"channel_id": "123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "2000",
		"signature": "DEADBEEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcCHANNEL_MALFORMED, err.Code)
}

func TestChannelVerify_AmountTooSmall(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"public_key": "021D93E21C44160A1B3B66DA1F37B86BE39FFEA3FC4B95FAA2063F82EE823599F6",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "-1",
		"signature": "DEADBEEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcCHANNEL_AMT_MALFORMED, err.Code)
	assert.Equal(t, "channelAmtMalformed", err.ErrorString)
}

func TestChannelVerify_AmountTooLarge(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"public_key": "021D93E21C44160A1B3B66DA1F37B86BE39FFEA3FC4B95FAA2063F82EE823599F6",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "18446744073709551616",
		"signature": "DEADBEEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcCHANNEL_AMT_MALFORMED, err.Code)
}

func TestChannelVerify_NonHexSignature(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"public_key": "aB4BXXLuPu8DpVuyq1DBiu3SrPdtK9AYZisKhu8mvkoiUD8J9Gov",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "40000000",
		"signature": "ThisIsNotHexadecimal"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "signature")
}

func TestChannelVerify_ValidHexPublicKey(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	// Valid hex public key but invalid signature - should return signature_verified: false
	params := json.RawMessage(`{
		"public_key": "021D93E21C44160A1B3B66DA1F37B86BE39FFEA3FC4B95FAA2063F82EE823599F6",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "0",
		"signature": "DEADBEEF"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]interface{})
	verified, ok := resultMap["signature_verified"].(bool)
	require.True(t, ok)
	assert.False(t, verified, "Invalid signature should not verify")
}

func TestChannelVerify_ValidBase58PublicKey(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	// Valid base58 public key but invalid signature
	params := json.RawMessage(`{
		"public_key": "aB4BXXLuPu8DpVuyq1DBiu3SrPdtK9AYZisKhu8mvkoiUD8J9Gov",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "0",
		"signature": "DEADBEEF"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]interface{})
	verified, ok := resultMap["signature_verified"].(bool)
	require.True(t, ok)
	assert.False(t, verified, "Invalid signature should not verify")
}

func TestChannelVerify_MaxAmount(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	// Max uint64 value
	params := json.RawMessage(`{
		"public_key": "021D93E21C44160A1B3B66DA1F37B86BE39FFEA3FC4B95FAA2063F82EE823599F6",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "18446744073709551615",
		"signature": "DEADBEEF"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]interface{})
	_, ok := resultMap["signature_verified"].(bool)
	require.True(t, ok)
}

func TestChannelVerify_ZeroAmount(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	params := json.RawMessage(`{
		"public_key": "021D93E21C44160A1B3B66DA1F37B86BE39FFEA3FC4B95FAA2063F82EE823599F6",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "0",
		"signature": "DEADBEEF"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]interface{})
	_, ok := resultMap["signature_verified"].(bool)
	require.True(t, ok)
}

func TestChannelVerify_WrongSignatureForAmount(t *testing.T) {
	// First create a valid signature for amount "1000000"
	authorizeHandler := &handlers.ChannelAuthorizeMethod{}
	authorizeCtx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	authorizeParams := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	authorizeResult, authorizeErr := authorizeHandler.Handle(authorizeCtx, authorizeParams)
	require.Nil(t, authorizeErr)

	resultMap := authorizeResult.(map[string]interface{})
	signature, _ := resultMap["signature"].(string)

	// Now try to verify with a different amount - should fail
	verifyHandler := &handlers.ChannelVerifyMethod{}
	verifyCtx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	// Use the public key derived from masterpassphrase
	verifyParams := json.RawMessage(`{
		"public_key": "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "2000000",
		"signature": "` + signature + `"
	}`)

	verifyResult, verifyErr := verifyHandler.Handle(verifyCtx, verifyParams)
	require.Nil(t, verifyErr)

	verifyResultMap := verifyResult.(map[string]interface{})
	verified, ok := verifyResultMap["signature_verified"].(bool)
	require.True(t, ok)
	assert.False(t, verified, "Signature for wrong amount should not verify")
}

func TestChannelVerify_WrongChannelID(t *testing.T) {
	// First create a valid signature for channel_id
	authorizeHandler := &handlers.ChannelAuthorizeMethod{}
	authorizeCtx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	authorizeParams := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	authorizeResult, authorizeErr := authorizeHandler.Handle(authorizeCtx, authorizeParams)
	require.Nil(t, authorizeErr)

	resultMap := authorizeResult.(map[string]interface{})
	signature, _ := resultMap["signature"].(string)

	// Now try to verify with a different channel_id - should fail
	verifyHandler := &handlers.ChannelVerifyMethod{}
	verifyCtx := &types.RpcContext{
		ApiVersion: types.ApiVersion1,
	}

	verifyParams := json.RawMessage(`{
		"public_key": "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
		"channel_id": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"amount": "1000000",
		"signature": "` + signature + `"
	}`)

	verifyResult, verifyErr := verifyHandler.Handle(verifyCtx, verifyParams)
	require.Nil(t, verifyErr)

	verifyResultMap := verifyResult.(map[string]interface{})
	verified, ok := verifyResultMap["signature_verified"].(bool)
	require.True(t, ok)
	assert.False(t, verified, "Signature for wrong channel_id should not verify")
}
