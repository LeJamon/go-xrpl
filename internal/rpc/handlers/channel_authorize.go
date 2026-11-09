package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	var fields map[string]json.RawMessage
	if len(params) > 0 {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	for _, name := range []string{"channel_id", "amount"} {
		if _, ok := fields[name]; !ok {
			return nil, rpcerrors.RpcErrorMissingField(name)
		}
	}
	_, hasKeyType := fields["key_type"]
	_, hasSecret := fields["secret"]
	if !hasKeyType && !hasSecret {
		return nil, rpcerrors.RpcErrorMissingField("secret")
	}

	privateKeyHex, _, keyType, rpcErr := parseCredentialsAndDeriveKeypair(ctx.ApiVersion, params)
	if rpcErr != nil {
		return nil, rpcErr
	}

	channelIDHex, rpcErr := parseChannelID(fields["channel_id"])
	if rpcErr != nil {
		return nil, rpcErr
	}
	drops, ok := parseChannelDrops(fields["amount"])
	if !ok {
		return nil, rpcerrors.RpcErrorChannelAmountMalformed()
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

// parseChannelID requires a JSON string holding a 256-bit hex channel ID.
func parseChannelID(raw json.RawMessage) (string, *rpcerrors.RpcError) {
	value, ok := rawString(raw)
	if !ok {
		return "", rpcerrors.RpcErrorInvalidParams("")
	}
	channelID := strings.ToUpper(value)
	if len(channelID) != 64 {
		return "", rpcerrors.RpcErrorChannelMalformed()
	}
	if _, err := hex.DecodeString(channelID); err != nil {
		return "", rpcerrors.RpcErrorChannelMalformed()
	}
	return channelID, nil
}

// parseChannelDrops requires a JSON string holding an unsigned 64-bit drop
// count, optionally prefixed with a single '+'.
func parseChannelDrops(raw json.RawMessage) (uint64, bool) {
	value, ok := rawString(raw)
	if !ok {
		return 0, false
	}
	drops, err := strconv.ParseUint(strings.TrimPrefix(value, "+"), 10, 64)
	return drops, err == nil
}
