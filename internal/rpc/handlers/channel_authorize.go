package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// ChannelAuthorizeMethod handles the channel_authorize RPC method
// This creates a signature that can be used to redeem a specific amount from a payment channel.
type ChannelAuthorizeMethod struct{ baseHandler }

func (m *ChannelAuthorizeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	if rpcErr := rejectDisabledSigning(ctx); rpcErr != nil {
		return nil, rpcErr
	}

	fields, rpcErr := rawJSONFields(params)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Validate required fields: channel_id and amount
	// rippled: for (auto const& p : {jss::channel_id, jss::amount}) if (!params.isMember(p)) return RPC::missing_field_error(p);
	if _, ok := fields["channel_id"]; !ok {
		return nil, rpcerrors.RpcErrorMissingField("channel_id")
	}
	if _, ok := fields["amount"]; !ok {
		return nil, rpcerrors.RpcErrorMissingField("amount")
	}

	// Parse credentials and derive keypair
	// rippled: if (!params.isMember(jss::key_type) && !params.isMember(jss::secret)) return RPC::missing_field_error(jss::secret);
	privateKeyHex, _, keyType, rpcErr := signCredentials{}.deriveKeypair(ctx.ApiVersion, params)
	if rpcErr != nil {
		return nil, rpcErr
	}

	channelIDHex, rpcErr := parseChannelID(fields["channel_id"])
	if rpcErr != nil {
		return nil, rpcErr
	}
	drops, rpcErr := parseChannelAmount(fields["amount"])
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Serialize the payment channel claim message using EncodeForSigningClaim
	// Message format: HashPrefix('CLM\0') + channel_id (32 bytes) + amount (8 bytes)
	claimJSON := map[string]any{
		"Channel": channelIDHex,
		"Amount":  strconv.FormatUint(drops, 10),
	}
	messageHex, err := binarycodec.EncodeForSigningClaim(claimJSON)
	if err != nil {
		return nil, rpcInternalError("channel_authorize: claim encoding failed", err)
	}

	// Convert hex message to raw bytes for signing
	messageBytes, err := hex.DecodeString(messageHex)
	if err != nil {
		return nil, rpcInternalError("channel_authorize: claim decoding failed", err)
	}

	// Sign the message
	// The Sign functions expect the raw message bytes (as a string)
	signature, err := signMessage(messageBytes, privateKeyHex, keyType)
	if err != nil {
		return nil, rpcInternalError("channel_authorize: claim signing failed", err)
	}

	response := map[string]any{
		"signature": signature,
	}

	return response, nil
}

// parseChannelID reads a channel_id that must be a string holding a hash256
// (64 hex digits, or "0"), returned as 64 upper-case hex digits.
func parseChannelID(raw json.RawMessage) (string, *rpcerrors.RpcError) {
	channelID, ok := rawJSONString(raw)
	if !ok {
		return "", rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	if !validHash256Hex(channelID) {
		return "", rpcerrors.RpcErrorChannelMalformed()
	}
	if channelID == "0" {
		return strings.Repeat("0", 64), nil
	}
	return strings.ToUpper(channelID), nil
}

// parseChannelAmount reads an amount that must be a string of drops.
func parseChannelAmount(raw json.RawMessage) (uint64, *rpcerrors.RpcError) {
	amount, ok := rawJSONString(raw)
	if !ok {
		return 0, rpcerrors.RpcErrorChannelAmountMalformed()
	}
	drops, err := strconv.ParseUint(amount, 10, 64)
	if err != nil {
		return 0, rpcerrors.RpcErrorChannelAmountMalformed()
	}
	return drops, nil
}

// signMessage signs a message using the appropriate algorithm
func signMessage(message []byte, privateKeyHex string, keyType string) (string, error) {
	// Convert message bytes to string for the Sign functions
	// The Sign functions do []byte(msg) internally, which correctly handles binary data
	msgStr := string(message)

	isEd25519 := keyType == "ed25519"

	if isEd25519 {
		algo := ed25519.Algorithm{}
		return algo.Sign(msgStr, privateKeyHex)
	}

	// Default to secp256k1
	algo := secp256k1.Algorithm{}
	return algo.Sign(msgStr, privateKeyHex)
}

func (m *ChannelAuthorizeMethod) RequiredRole() types.Role {
	return types.RoleUser
}
