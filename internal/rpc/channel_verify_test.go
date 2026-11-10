package rpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
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
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
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
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
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
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
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
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Equal(t, "Missing field 'signature'.", err.Message)
}

func TestChannelVerify_PresentEmptyRequiredFields(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	ctx := &types.RpcContext{ApiVersion: types.ApiVersion2}
	const publicKey = "021D93E21C44160A1B3B66DA1F37B86BE39FFEA3FC4B95FAA2063F82EE823599F6"
	const channelID = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"

	tests := []struct {
		name    string
		params  string
		code    int
		message string
	}{
		{
			name:    "empty public key is malformed",
			params:  `{"public_key":"","channel_id":"` + channelID + `","amount":"1","signature":"AA"}`,
			code:    rpcerrors.RpcPUBLIC_MALFORMED,
			message: "Public key is malformed.",
		},
		{
			name:    "empty channel id is malformed",
			params:  `{"public_key":"` + publicKey + `","channel_id":"","amount":"1","signature":"AA"}`,
			code:    rpcerrors.RpcCHANNEL_MALFORMED,
			message: "Payment channel is malformed.",
		},
		{
			name:    "empty amount is malformed",
			params:  `{"public_key":"` + publicKey + `","channel_id":"` + channelID + `","amount":"","signature":"AA"}`,
			code:    rpcerrors.RpcCHANNEL_AMT_MALFORMED,
			message: "Payment channel amount is malformed.",
		},
		{
			name:    "empty signature is invalid",
			params:  `{"public_key":"` + publicKey + `","channel_id":"` + channelID + `","amount":"1","signature":""}`,
			code:    rpcerrors.RpcINVALID_PARAMS,
			message: "Invalid parameters.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, rpcErr := handler.Handle(ctx, json.RawMessage(tc.params))
			require.NotNil(t, rpcErr)
			assert.Equal(t, tc.code, rpcErr.Code)
			assert.Equal(t, tc.message, rpcErr.Message)
		})
	}
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
	assert.Equal(t, rpcerrors.RpcPUBLIC_MALFORMED, err.Code)
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
	assert.Equal(t, rpcerrors.RpcPUBLIC_MALFORMED, err.Code)
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
	assert.Equal(t, rpcerrors.RpcCHANNEL_MALFORMED, err.Code)
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
	assert.Equal(t, rpcerrors.RpcCHANNEL_MALFORMED, err.Code)
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
	assert.Equal(t, rpcerrors.RpcCHANNEL_AMT_MALFORMED, err.Code)
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
	assert.Equal(t, rpcerrors.RpcCHANNEL_AMT_MALFORMED, err.Code)
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
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Equal(t, "Invalid parameters.", err.Message)
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

	resultMap := result.(map[string]any)
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

	resultMap := result.(map[string]any)
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

	resultMap := result.(map[string]any)
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

	resultMap := result.(map[string]any)
	_, ok := resultMap["signature_verified"].(bool)
	require.True(t, ok)
}

func TestChannelVerify_WrongSignatureForAmount(t *testing.T) {
	// First create a valid signature for amount "1000000"
	authorizeHandler := &handlers.ChannelAuthorizeMethod{}
	authorizeCtx := channelAuthorizeTestContext(types.ApiVersion1)

	authorizeParams := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	authorizeResult, authorizeErr := authorizeHandler.Handle(authorizeCtx, authorizeParams)
	require.Nil(t, authorizeErr)

	resultMap := authorizeResult.(map[string]any)
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

	verifyResultMap := verifyResult.(map[string]any)
	verified, ok := verifyResultMap["signature_verified"].(bool)
	require.True(t, ok)
	assert.False(t, verified, "Signature for wrong amount should not verify")
}

func TestChannelVerify_WrongChannelID(t *testing.T) {
	// First create a valid signature for channel_id
	authorizeHandler := &handlers.ChannelAuthorizeMethod{}
	authorizeCtx := channelAuthorizeTestContext(types.ApiVersion1)

	authorizeParams := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	authorizeResult, authorizeErr := authorizeHandler.Handle(authorizeCtx, authorizeParams)
	require.Nil(t, authorizeErr)

	resultMap := authorizeResult.(map[string]any)
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

	verifyResultMap := verifyResult.(map[string]any)
	verified, ok := verifyResultMap["signature_verified"].(bool)
	require.True(t, ok)
	assert.False(t, verified, "Signature for wrong channel_id should not verify")
}

func TestChannelVerify_ParamTypes(t *testing.T) {
	channelID := channelJSONString(t, channelRegressionID)
	publicKey := channelJSONString(t, channelRegressionPublicKey)
	signature := channelJSONString(t, channelRegressionSignature)
	request := func(pk, id, amount, sig string) json.RawMessage {
		return json.RawMessage(`{"public_key":` + pk + `,"channel_id":` + id + `,"amount":` + amount + `,"signature":` + sig + `}`)
	}

	errTests := []struct {
		name    string
		params  json.RawMessage
		code    int
		message string
	}{
		{name: "numeric amount", params: request(publicKey, channelID, `1000`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "boolean amount", params: request(publicKey, channelID, `false`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "null amount", params: request(publicKey, channelID, `null`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "object amount", params: request(publicKey, channelID, `{}`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "array amount", params: request(publicKey, channelID, `[]`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "trailing amount text", params: request(publicKey, channelID, `"1000x"`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "trailing amount whitespace", params: request(publicKey, channelID, `"1000 "`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "leading amount text", params: request(publicKey, channelID, `"x1000"`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "non-numeric amount", params: request(publicKey, channelID, `"x"`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "space amount", params: request(publicKey, channelID, `" "`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "split amount", params: request(publicKey, channelID, `"1000 1000"`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "comma amount", params: request(publicKey, channelID, `"1,000"`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "leading amount whitespace", params: request(publicKey, channelID, `" 1000"`, signature), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "numeric public key", params: request(`5`, channelID, `"1000"`, signature), code: rpcerrors.RpcPUBLIC_MALFORMED, message: "Public key is malformed."},
		{name: "boolean public key", params: request(`true`, channelID, `"1000"`, signature), code: rpcerrors.RpcPUBLIC_MALFORMED, message: "Public key is malformed."},
		{name: "null public key", params: request(`null`, channelID, `"1000"`, signature), code: rpcerrors.RpcPUBLIC_MALFORMED, message: "Public key is malformed."},
		{name: "double public key", params: request(`5.0`, channelID, `"1000"`, signature), code: rpcerrors.RpcPUBLIC_MALFORMED, message: "Public key is malformed."},
		{name: "object public key", params: request(`{}`, channelID, `"1000"`, signature), code: rpcerrors.RpcINTERNAL, message: "Internal error."},
		{name: "array public key", params: request(`[]`, channelID, `"1000"`, signature), code: rpcerrors.RpcINTERNAL, message: "Internal error."},
		{name: "public key checked before amount", params: request(`5`, channelID, `5`, signature), code: rpcerrors.RpcPUBLIC_MALFORMED, message: "Public key is malformed."},
		{name: "numeric channel id", params: request(publicKey, `5`, `"1000"`, signature), code: rpcerrors.RpcCHANNEL_MALFORMED, message: "Payment channel is malformed."},
		{name: "null channel id", params: request(publicKey, `null`, `"1000"`, signature), code: rpcerrors.RpcCHANNEL_MALFORMED, message: "Payment channel is malformed."},
		{name: "boolean channel id", params: request(publicKey, `true`, `"1000"`, signature), code: rpcerrors.RpcCHANNEL_MALFORMED, message: "Payment channel is malformed."},
		{name: "double channel id", params: request(publicKey, `0.0`, `"1000"`, signature), code: rpcerrors.RpcCHANNEL_MALFORMED, message: "Payment channel is malformed."},
		{name: "object channel id", params: request(publicKey, `{}`, `"1000"`, signature), code: rpcerrors.RpcINTERNAL, message: "Internal error."},
		{name: "array channel id", params: request(publicKey, `[]`, `"1000"`, signature), code: rpcerrors.RpcINTERNAL, message: "Internal error."},
		{name: "double signature", params: request(publicKey, channelID, `"1000"`, `5.0`), code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid parameters."},
		{name: "scientific signature", params: request(publicKey, channelID, `"1000"`, `5e0`), code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid parameters."},
		{name: "null signature", params: request(publicKey, channelID, `"1000"`, `null`), code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid parameters."},
		{name: "boolean signature", params: request(publicKey, channelID, `"1000"`, `true`), code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid parameters."},
		{name: "object signature", params: request(publicKey, channelID, `"1000"`, `{}`), code: rpcerrors.RpcINTERNAL, message: "Internal error."},
		{name: "array signature", params: request(publicKey, channelID, `"1000"`, `[]`), code: rpcerrors.RpcINTERNAL, message: "Internal error."},
	}

	handler := &handlers.ChannelVerifyMethod{}
	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		apiName := "api1"
		if apiVersion == types.ApiVersion2 {
			apiName = "api2"
		}
		ctx := &types.RpcContext{ApiVersion: apiVersion}
		for _, tc := range errTests {
			t.Run(apiName+"/"+tc.name, func(t *testing.T) {
				_, rpcErr := handler.Handle(ctx, tc.params)
				require.NotNil(t, rpcErr)
				assert.Equal(t, tc.code, rpcErr.Code)
				assert.Equal(t, tc.message, rpcErr.Message)
			})
		}
	}

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		ctx := &types.RpcContext{ApiVersion: apiVersion}
		for _, test := range []struct {
			name      string
			signature string
		}{
			{name: "integer signature", signature: `5`},
			{name: "zero integer signature", signature: `0`},
		} {
			apiName := "api1"
			if apiVersion == types.ApiVersion2 {
				apiName = "api2"
			}
			t.Run(apiName+"/"+test.name, func(t *testing.T) {
				result, rpcErr := handler.Handle(ctx, request(publicKey, channelID, `"1000"`, test.signature))
				require.Nil(t, rpcErr)
				assert.False(t, result.(map[string]any)["signature_verified"].(bool))
			})
		}
	}

	ctx := &types.RpcContext{ApiVersion: types.ApiVersion1}
	verified := func(amount, sig string) bool {
		result, rpcErr := handler.Handle(ctx, request(publicKey, channelID, amount, sig))
		require.Nil(t, rpcErr)
		return result.(map[string]any)["signature_verified"].(bool)
	}
	assert.True(t, verified(`"+1000"`, signature), "a leading plus sign is accepted")
	assert.False(t, verified(`"1000"`, `"ABC"`), "an odd-length signature is decoded and checked")
}

func TestChannelVerify_ZeroChannelIDAliases(t *testing.T) {
	authorizeHandler := &handlers.ChannelAuthorizeMethod{}
	authorizeParams := json.RawMessage(`{"channel_id":"` + strings.Repeat("0", 64) + `","amount":"1000","secret":"` + channelRegressionSecret + `"}`)
	authorizeResult, authorizeErr := authorizeHandler.Handle(channelAuthorizeTestContext(types.ApiVersion1), authorizeParams)
	require.Nil(t, authorizeErr)
	signature := channelJSONString(t, authorizeResult.(map[string]any)["signature"].(string))

	verifyHandler := &handlers.ChannelVerifyMethod{}
	request := func(channelID string) json.RawMessage {
		return json.RawMessage(`{"public_key":` + channelJSONString(t, channelRegressionPublicKey) + `,"channel_id":` + channelID + `,"amount":"1000","signature":` + signature + `}`)
	}
	for _, channelID := range []string{
		`"` + strings.Repeat("0", 64) + `"`,
		`"0"`,
		`0`,
		`-0`,
	} {
		result, rpcErr := verifyHandler.Handle(&types.RpcContext{ApiVersion: types.ApiVersion1}, request(channelID))
		require.Nil(t, rpcErr)
		assert.True(t, result.(map[string]any)["signature_verified"].(bool))
	}
}

func TestChannelVerify_PublicKeyBoundaries(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	request := func(publicKey string) json.RawMessage {
		return json.RawMessage(`{"public_key":` + publicKey + `,"channel_id":` + channelJSONString(t, channelRegressionID) + `,"amount":"1000","signature":` + channelJSONString(t, channelRegressionSignature) + `}`)
	}

	oddPublicKey := channelJSONString(t, channelRegressionPublicKey[1:])
	result, rpcErr := handler.Handle(&types.RpcContext{ApiVersion: types.ApiVersion1}, request(oddPublicKey))
	require.Nil(t, rpcErr)
	assert.True(t, result.(map[string]any)["signature_verified"].(bool))

	_, rpcErr = handler.Handle(&types.RpcContext{ApiVersion: types.ApiVersion1}, request(channelJSONString(t, channelRegressionInvalidBase58PK)))
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcPUBLIC_MALFORMED, rpcErr.Code)
}

func TestChannelVerify_NULStringsAreTruncated(t *testing.T) {
	handler := &handlers.ChannelVerifyMethod{}
	verify := func(apiVersion int, publicKey, channelID, amount, signature string) bool {
		params, err := json.Marshal(map[string]string{
			"public_key": publicKey,
			"channel_id": channelID,
			"amount":     amount,
			"signature":  signature,
		})
		require.NoError(t, err)
		result, rpcErr := handler.Handle(&types.RpcContext{ApiVersion: apiVersion}, params)
		require.Nil(t, rpcErr)
		return result.(map[string]any)["signature_verified"].(bool)
	}

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		assert.True(t, verify(apiVersion, channelRegressionPublicKey, channelRegressionID, "1000", channelRegressionSignature))
		assert.True(t, verify(apiVersion, channelRegressionPublicKey+"\x00ignored", channelRegressionID, "1000", channelRegressionSignature))
		assert.True(t, verify(apiVersion, channelRegressionPublicKey, channelRegressionID+"\x00ignored", "1000", channelRegressionSignature))
		assert.True(t, verify(apiVersion, channelRegressionPublicKey, channelRegressionID, "1000\x00ignored", channelRegressionSignature))
		assert.True(t, verify(apiVersion, channelRegressionPublicKey, channelRegressionID, "1000", channelRegressionSignature+"\x00ignored"))
	}
}
