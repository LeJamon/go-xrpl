package rpc

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test vectors from rippled's RPCCall_test.cpp and PayChan_test.cpp

const (
	channelRegressionID              = "D45FB631561136C1BFF7AE706AC13CCAF687A8216AF3189B6A04163507F97591"
	channelRegressionPublicKey       = "03EF6575F9E187A7749BE02C34753BFA11F87FA0CAD96B7C5442BA64589DAF3388"
	channelRegressionSignature       = "3045022100B248116D21BE0BDA1D25AF3A18A1F85EDD759FFD9F8F17A15DB3DA5C83713033022029719D29280EEAD08121539EBCE9C4E439780FFBCA4ACEAB3B7D0D2570D70145"
	channelRegressionSecret          = "shEonzD7pxzzjWbWBWD9jRfk1QJYu"
	channelRegressionInvalidBase58PK = "aKqmHHn8sjpSLJasyLQ7wRCus5RBS556ZJUeePX2kP2NrJUeABRu"
)

func channelJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

func channelAuthorizeTestContext(apiVersion int) *types.RpcContext {
	return &types.RpcContext{
		ApiVersion: apiVersion,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{
			Capabilities: types.RPCCapabilities{SigningEnabled: true},
		}),
	}
}

func TestChannelAuthorizeSigningDisabledPrecedesValidation(t *testing.T) {
	_, rpcErr := (&handlers.ChannelAuthorizeMethod{}).Handle(
		&types.RpcContext{ApiVersion: types.ApiVersion2, Services: types.NewTestServiceGraph(&types.ServiceContainer{})},
		json.RawMessage(`{}`),
	)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcNOT_SUPPORTED, rpcErr.Code)
	assert.Equal(t, "Signing is not supported by this server.", rpcErr.Message)
}

func TestChannelAuthorize_MissingChannelID(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "channel_id")
}

func TestChannelAuthorize_MissingAmount(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "amount")
}

func TestChannelAuthorize_MissingSecret(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "secret")
}

func TestChannelAuthorize_PresentEmptyRequiredFields(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion2)
	const channelID = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	const secret = "snoPBrXtMeMyMHUVTgbuqAfg1SUTb"

	tests := []struct {
		name   string
		params string
		code   int
	}{
		{
			name:   "empty channel id is malformed",
			params: `{"secret":"` + secret + `","channel_id":"","amount":"1"}`,
			code:   rpcerrors.RpcCHANNEL_MALFORMED,
		},
		{
			name:   "empty amount is malformed",
			params: `{"secret":"` + secret + `","channel_id":"` + channelID + `","amount":""}`,
			code:   rpcerrors.RpcCHANNEL_AMT_MALFORMED,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, rpcErr := handler.Handle(ctx, json.RawMessage(tc.params))
			require.NotNil(t, rpcErr)
			assert.Equal(t, tc.code, rpcErr.Code)
		})
	}
}

func TestChannelAuthorize_MultipleSecrets(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"seed": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "Exactly one")
}

func TestChannelAuthorize_SecretNotAllowedWithKeyType(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
	assert.Contains(t, err.Message, "secret field is not allowed")
}

func TestChannelAuthorize_BadKeyType(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion2)

	params := json.RawMessage(`{
		"seed": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"key_type": "secp257k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcBAD_KEY_TYPE, err.Code)
	assert.Equal(t, "badKeyType", err.ErrorString)
}

func TestChannelAuthorize_ChannelIDTooShort(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcCHANNEL_MALFORMED, err.Code)
	assert.Equal(t, "channelMalformed", err.ErrorString)
}

func TestChannelAuthorize_ChannelIDTooLong(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "10123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcCHANNEL_MALFORMED, err.Code)
}

func TestChannelAuthorize_ChannelIDNotHex(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEZ",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcCHANNEL_MALFORMED, err.Code)
}

func TestChannelAuthorize_NegativeAmount(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "-1"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcCHANNEL_AMT_MALFORMED, err.Code)
	assert.Equal(t, "channelAmtMalformed", err.ErrorString)
}

func TestChannelAuthorize_AmountOverflow(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "18446744073709551616"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcCHANNEL_AMT_MALFORMED, err.Code)
}

func TestChannelAuthorize_ValidWithSecret(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	// Use masterpassphrase seed which generates a known keypair
	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]any)
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
	// DER signature for secp256k1 is typically 70-72 bytes, hex encoded = 140-144 chars
	assert.GreaterOrEqual(t, len(signature), 128)
}

func TestChannelAuthorize_LegacyRFC1751Secret(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)
	base := map[string]any{
		"channel_id": strings.Repeat("A", 64),
		"amount":     "1",
	}

	withPhrase := map[string]any{}
	for key, value := range base {
		withPhrase[key] = value
	}
	withPhrase["secret"] = "SCAT BERN ISLE FOR ROIL BUS SOAK AQUA FREE FOR DRAM BRIG"
	phraseJSON, err := json.Marshal(withPhrase)
	require.NoError(t, err)
	phraseResult, rpcErr := handler.Handle(ctx, phraseJSON)
	require.Nil(t, rpcErr)

	withHex := map[string]any{}
	for key, value := range base {
		withHex[key] = value
	}
	withHex["seed_hex"] = "BE6A670A19B209E112146D0A7ED2AAD7"
	withHex["key_type"] = "secp256k1"
	hexJSON, err := json.Marshal(withHex)
	require.NoError(t, err)
	hexResult, rpcErr := handler.Handle(ctx, hexJSON)
	require.Nil(t, rpcErr)

	assert.Equal(t, hexResult.(map[string]any)["signature"], phraseResult.(map[string]any)["signature"])
}

func TestChannelAuthorize_ValidWithSeed(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"seed": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "2000"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]any)
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
}

func TestChannelAuthorize_ValidWithPassphrase(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]any)
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
}

func TestChannelAuthorize_ValidWithSeedHex(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	// masterpassphrase SHA512Half first 16 bytes = DEDCE9CE67B451D852FD4E846FCDE31C
	params := json.RawMessage(`{
		"seed_hex": "DEDCE9CE67B451D852FD4E846FCDE31C",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]any)
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
}

func TestChannelAuthorize_ValidEd25519(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	// Ed25519 seed
	params := json.RawMessage(`{
		"seed": "sEdTzRkEgPoxDG1mJ6WkSucHWnMkm1H",
		"key_type": "ed25519",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]any)
	signature, ok := resultMap["signature"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, signature)
	// Ed25519 signature is 64 bytes, hex encoded = 128 chars
	assert.Equal(t, 128, len(signature))
}

func TestChannelAuthorize_MaxAmount(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	// Max uint64 value
	params := json.RawMessage(`{
		"secret": "snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "18446744073709551615"
	}`)

	result, err := handler.Handle(ctx, params)
	require.Nil(t, err)

	resultMap := result.(map[string]any)
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
	algo := secp256k1.Algorithm{}
	_, pubKeyHex, err := algo.DeriveKeypair(seedBytes, false)
	require.NoError(t, err)

	// Authorize
	authorizeHandler := &handlers.ChannelAuthorizeMethod{}
	authorizeCtx := channelAuthorizeTestContext(types.ApiVersion1)

	authorizeParams := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"channel_id": "` + channelID + `",
		"amount": "` + amount + `"
	}`)

	authorizeResult, authorizeErr := authorizeHandler.Handle(authorizeCtx, authorizeParams)
	require.Nil(t, authorizeErr)

	resultMap := authorizeResult.(map[string]any)
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

	verifyResultMap := verifyResult.(map[string]any)
	verified, ok := verifyResultMap["signature_verified"].(bool)
	require.True(t, ok)
	assert.True(t, verified, "Signature should verify correctly")
}

func TestChannelAuthorizeAndVerify_IntegrationEd25519(t *testing.T) {
	channelID := "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	amount := "5000000"

	// First, generate the keypair for verification
	seedBytes, _ := hex.DecodeString("DEDCE9CE67B451D852FD4E846FCDE31C")
	algo := ed25519.Algorithm{}
	_, pubKeyHex, err := algo.DeriveKeypair(seedBytes, false)
	require.NoError(t, err)

	// Authorize
	authorizeHandler := &handlers.ChannelAuthorizeMethod{}
	authorizeCtx := channelAuthorizeTestContext(types.ApiVersion1)

	authorizeParams := json.RawMessage(`{
		"passphrase": "masterpassphrase",
		"key_type": "ed25519",
		"channel_id": "` + channelID + `",
		"amount": "` + amount + `"
	}`)

	authorizeResult, authorizeErr := authorizeHandler.Handle(authorizeCtx, authorizeParams)
	require.Nil(t, authorizeErr)

	resultMap := authorizeResult.(map[string]any)
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

	verifyResultMap := verifyResult.(map[string]any)
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
	ctx := channelAuthorizeTestContext(types.ApiVersion1)

	params := json.RawMessage(`{
		"seed": "invalid_seed",
		"key_type": "secp256k1",
		"channel_id": "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"amount": "1000000"
	}`)

	_, err := handler.Handle(ctx, params)
	require.NotNil(t, err)
	assert.Equal(t, rpcerrors.RpcBAD_SEED, err.Code)
	assert.Equal(t, "badSeed", err.ErrorString)
}

func TestChannelAuthorize_ParamTypes(t *testing.T) {
	const secret = `"secret":"` + channelRegressionSecret + `"`
	channelID := channelJSONString(t, channelRegressionID)
	request := func(rawChannelID, rawAmount string) json.RawMessage {
		return json.RawMessage(`{"channel_id":` + rawChannelID + `,"amount":` + rawAmount + `,` + secret + `}`)
	}

	tests := []struct {
		name    string
		params  json.RawMessage
		code    int
		message string
	}{
		{name: "numeric amount", params: request(channelID, `1000`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "boolean amount", params: request(channelID, `true`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "object amount", params: request(channelID, `{}`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "array amount", params: request(channelID, `[]`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "null amount", params: request(channelID, `null`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "trailing amount text", params: request(channelID, `"1000x"`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "trailing amount whitespace", params: request(channelID, `"1000 "`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "leading amount text", params: request(channelID, `"x1000"`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "non-numeric amount", params: request(channelID, `"x"`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "space amount", params: request(channelID, `" "`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "split amount", params: request(channelID, `"1000 1000"`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "comma amount", params: request(channelID, `"1,000"`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "leading amount whitespace", params: request(channelID, `" 1000"`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "two plus signs", params: request(channelID, `"++1000"`), code: rpcerrors.RpcCHANNEL_AMT_MALFORMED, message: "Payment channel amount is malformed."},
		{name: "numeric channel id", params: request(`5`, `"1000"`), code: rpcerrors.RpcCHANNEL_MALFORMED, message: "Payment channel is malformed."},
		{name: "null channel id", params: request(`null`, `"1000"`), code: rpcerrors.RpcCHANNEL_MALFORMED, message: "Payment channel is malformed."},
		{name: "boolean channel id", params: request(`true`, `"1000"`), code: rpcerrors.RpcCHANNEL_MALFORMED, message: "Payment channel is malformed."},
		{name: "double channel id", params: request(`0.0`, `"1000"`), code: rpcerrors.RpcCHANNEL_MALFORMED, message: "Payment channel is malformed."},
		{name: "object channel id", params: request(`{}`, `"1000"`), code: rpcerrors.RpcINTERNAL, message: "Internal error."},
		{name: "array channel id", params: request(`[]`, `"1000"`), code: rpcerrors.RpcINTERNAL, message: "Internal error."},
		{name: "key checked before channel id", params: json.RawMessage(`{"channel_id":{},"amount":{},"secret":5}`), code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid field 'secret', not string."},
		{name: "seed without key type", params: json.RawMessage(`{"channel_id":"` + channelRegressionID + `","amount":"1000","seed":"` + channelRegressionSecret + `"}`), code: rpcerrors.RpcINVALID_PARAMS, message: "Missing field 'secret'."},
	}

	handler := &handlers.ChannelAuthorizeMethod{}
	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		apiName := "api1"
		if apiVersion == types.ApiVersion2 {
			apiName = "api2"
		}
		for _, tc := range tests {
			t.Run(apiName+"/"+tc.name, func(t *testing.T) {
				_, rpcErr := handler.Handle(channelAuthorizeTestContext(apiVersion), tc.params)
				require.NotNil(t, rpcErr)
				assert.Equal(t, tc.code, rpcErr.Code)
				assert.Equal(t, tc.message, rpcErr.Message)
			})
		}
	}
}

func TestChannelAuthorize_InvalidKeyTypePrecedesAmount(t *testing.T) {
	params := json.RawMessage(`{"channel_id":"` + channelRegressionID + `","amount":1000,"seed":"` + channelRegressionSecret + `","key_type":"secp257k1"}`)
	tests := []struct {
		name    string
		version int
		code    int
		message string
	}{
		{name: "api 1", version: types.ApiVersion1, code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid field 'key_type'."},
		{name: "api 2", version: types.ApiVersion2, code: rpcerrors.RpcBAD_KEY_TYPE, message: "Bad key type."},
	}

	handler := &handlers.ChannelAuthorizeMethod{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, rpcErr := handler.Handle(channelAuthorizeTestContext(tc.version), params)
			require.NotNil(t, rpcErr)
			assert.Equal(t, tc.code, rpcErr.Code)
			assert.Equal(t, tc.message, rpcErr.Message)
		})
	}
}

func TestChannelAuthorize_ZeroChannelIDAliases(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	sign := func(apiVersion int, channelID string) string {
		params := json.RawMessage(`{"channel_id":` + channelID + `,"amount":"1000","secret":"` + channelRegressionSecret + `"}`)
		result, rpcErr := handler.Handle(channelAuthorizeTestContext(apiVersion), params)
		require.Nil(t, rpcErr)
		return result.(map[string]any)["signature"].(string)
	}

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		want := sign(apiVersion, `"`+strings.Repeat("0", 64)+`"`)
		assert.Equal(t, want, sign(apiVersion, `"0"`))
		assert.Equal(t, want, sign(apiVersion, `0`))
		assert.Equal(t, want, sign(apiVersion, `-0`))
	}
}

func TestChannelAuthorize_NULStringsAreTruncated(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	sign := func(apiVersion int, channelID, amount string) string {
		params, err := json.Marshal(map[string]string{
			"channel_id": channelID,
			"amount":     amount,
			"secret":     channelRegressionSecret,
		})
		require.NoError(t, err)
		result, rpcErr := handler.Handle(channelAuthorizeTestContext(apiVersion), params)
		require.Nil(t, rpcErr)
		return result.(map[string]any)["signature"].(string)
	}

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		want := sign(apiVersion, channelRegressionID, "1000")
		assert.Equal(t, want, sign(apiVersion, channelRegressionID+"\x00ignored", "1000"))
		assert.Equal(t, want, sign(apiVersion, channelRegressionID, "1000\x00ignored"))
	}
}

func TestChannelAuthorize_PlusSignedAmount(t *testing.T) {
	handler := &handlers.ChannelAuthorizeMethod{}
	sign := func(amount string) string {
		result, rpcErr := handler.Handle(channelAuthorizeTestContext(types.ApiVersion1), json.RawMessage(`{
			"channel_id": "`+channelRegressionID+`",
			"amount": "`+amount+`",
			"secret": "`+channelRegressionSecret+`"
		}`))
		require.Nil(t, rpcErr)
		return result.(map[string]any)["signature"].(string)
	}
	assert.Equal(t, sign("1000"), sign("+1000"))
}
