package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// ChannelAuthorizeMethod handles the channel_authorize RPC method
// This creates a signature that can be used to redeem a specific amount from a payment channel.
type ChannelAuthorizeMethod struct{ BaseHandler }

// channelAuthorizeRequest represents the request parameters
type channelAuthorizeRequest struct {
	signCredentials

	// Required fields
	ChannelID string `json:"channel_id"`
	Amount    string `json:"amount"`
}

func (m *ChannelAuthorizeMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	var request channelAuthorizeRequest

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(params, &fields)

	// Validate required fields: channel_id and amount
	// rippled: for (auto const& p : {jss::channel_id, jss::amount}) if (!params.isMember(p)) return RPC::missing_field_error(p);
	if _, ok := fields["channel_id"]; !ok {
		return nil, types.RPCErrorMissingField("channel_id")
	}
	if _, ok := fields["amount"]; !ok {
		return nil, types.RPCErrorMissingField("amount")
	}

	// Parse credentials and derive keypair
	// rippled: if (!params.isMember(jss::key_type) && !params.isMember(jss::secret)) return RPC::missing_field_error(jss::secret);
	privateKeyHex, _, keyType, rpcErr := request.signCredentials.deriveKeypair(ctx.ApiVersion, params)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Validate channel_id - must be valid 256-bit hex (64 characters)
	// rippled: if (!channelId.parseHex(params[jss::channel_id].asString())) return rpcError(rpcCHANNEL_MALFORMED);
	channelIDHex := strings.ToUpper(request.ChannelID)
	if len(channelIDHex) != 64 {
		return nil, types.RPCErrorChannelMalformed()
	}
	if _, err := hex.DecodeString(channelIDHex); err != nil {
		return nil, types.RPCErrorChannelMalformed()
	}

	// Validate amount - must be a string that parses to uint64
	// rippled: std::optional<std::uint64_t> const optDrops = params[jss::amount].isString() ? to_uint64(params[jss::amount].asString()) : std::nullopt;
	// rippled: if (!optDrops) return rpcError(rpcCHANNEL_AMT_MALFORMED);
	drops, err := strconv.ParseUint(request.Amount, 10, 64)
	if err != nil {
		return nil, types.RPCErrorChannelAmountMalformed()
	}

	// Serialize the payment channel claim message using EncodeForSigningClaim
	// Message format: HashPrefix('CLM\0') + channel_id (32 bytes) + amount (8 bytes)
	claimJSON := map[string]any{
		"Channel": channelIDHex,
		"Amount":  strconv.FormatUint(drops, 10),
	}
	messageHex, err := binarycodec.EncodeForSigningClaim(claimJSON)
	if err != nil {
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to encode claim: %v", err))
	}

	// Convert hex message to raw bytes for signing
	messageBytes, err := hex.DecodeString(messageHex)
	if err != nil {
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to decode message: %v", err))
	}

	// Sign the message
	// The Sign functions expect the raw message bytes (as a string)
	signature, err := signMessage(messageBytes, privateKeyHex, keyType)
	if err != nil {
		return nil, types.RPCErrorInternal(fmt.Sprintf("Exception occurred during signing: %v", err))
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
	// Note: rippled requires admin role OR signing enabled
	// For now, allow user role since we're implementing the signing functionality
	return types.RoleUser
}
