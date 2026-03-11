package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// ChannelAuthorizeMethod handles the channel_authorize RPC method
// This creates a signature that can be used to redeem a specific amount from a payment channel.
type ChannelAuthorizeMethod struct{}

// channelAuthorizeRequest represents the request parameters
type channelAuthorizeRequest struct {
	// Credentials (only one allowed, except key_type can be combined with seed/seed_hex/passphrase)
	Secret     string `json:"secret,omitempty"`
	Seed       string `json:"seed,omitempty"`
	SeedHex    string `json:"seed_hex,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	KeyType    string `json:"key_type,omitempty"`

	// Required fields
	ChannelID string `json:"channel_id"`
	Amount    string `json:"amount"`
}

func (m *ChannelAuthorizeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request channelAuthorizeRequest

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	// Validate required fields: channel_id and amount
	// rippled: for (auto const& p : {jss::channel_id, jss::amount}) if (!params.isMember(p)) return RPC::missing_field_error(p);
	if request.ChannelID == "" {
		return nil, &types.RpcError{
			Code:        types.RpcINVALID_PARAMS,
			ErrorString: "invalidParams",
			Type:        "invalidParams",
			Message:     "Missing field 'channel_id'.",
		}
	}
	if request.Amount == "" {
		return nil, &types.RpcError{
			Code:        types.RpcINVALID_PARAMS,
			ErrorString: "invalidParams",
			Type:        "invalidParams",
			Message:     "Missing field 'amount'.",
		}
	}

	// Parse credentials and derive keypair
	// rippled: if (!params.isMember(jss::key_type) && !params.isMember(jss::secret)) return RPC::missing_field_error(jss::secret);
	privateKeyHex, _, rpcErr := parseCredentialsAndDeriveKeypair(
		request.Secret,
		request.Seed,
		request.SeedHex,
		request.Passphrase,
		request.KeyType,
		ctx.ApiVersion,
	)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Validate channel_id - must be valid 256-bit hex (64 characters)
	// rippled: if (!channelId.parseHex(params[jss::channel_id].asString())) return rpcError(rpcCHANNEL_MALFORMED);
	channelIDHex := strings.ToUpper(request.ChannelID)
	if len(channelIDHex) != 64 {
		return nil, &types.RpcError{
			Code:        types.RpcCHANNEL_MALFORMED,
			ErrorString: "channelMalformed",
			Type:        "channelMalformed",
			Message:     "Payment channel is malformed.",
		}
	}
	if _, err := hex.DecodeString(channelIDHex); err != nil {
		return nil, &types.RpcError{
			Code:        types.RpcCHANNEL_MALFORMED,
			ErrorString: "channelMalformed",
			Type:        "channelMalformed",
			Message:     "Payment channel is malformed.",
		}
	}

	// Validate amount - must be a string that parses to uint64
	// rippled: std::optional<std::uint64_t> const optDrops = params[jss::amount].isString() ? to_uint64(params[jss::amount].asString()) : std::nullopt;
	// rippled: if (!optDrops) return rpcError(rpcCHANNEL_AMT_MALFORMED);
	drops, err := strconv.ParseUint(request.Amount, 10, 64)
	if err != nil {
		return nil, &types.RpcError{
			Code:        types.RpcCHANNEL_AMT_MALFORMED,
			ErrorString: "channelAmtMalformed",
			Type:        "channelAmtMalformed",
			Message:     "Payment channel amount is malformed.",
		}
	}

	// Serialize the payment channel claim message using EncodeForSigningClaim
	// Message format: HashPrefix('CLM\0') + channel_id (32 bytes) + amount (8 bytes)
	claimJSON := map[string]any{
		"Channel": channelIDHex,
		"Amount":  strconv.FormatUint(drops, 10),
	}
	messageHex, err := binarycodec.EncodeForSigningClaim(claimJSON)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to encode claim: " + err.Error())
	}

	// Convert hex message to raw bytes for signing
	messageBytes, err := hex.DecodeString(messageHex)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to decode message: " + err.Error())
	}

	// Sign the message
	// The Sign functions expect the raw message bytes (as a string)
	signature, err := signMessage(messageBytes, privateKeyHex, request.KeyType)
	if err != nil {
		return nil, types.RpcErrorInternal("Exception occurred during signing: " + err.Error())
	}

	response := map[string]interface{}{
		"signature": signature,
	}

	return response, nil
}

// signMessage signs a message using the appropriate algorithm
func signMessage(message []byte, privateKeyHex string, keyType string) (string, error) {
	// Convert message bytes to string for the Sign functions
	// The Sign functions do []byte(msg) internally, which correctly handles binary data
	msgStr := string(message)

	isEd25519 := strings.ToLower(keyType) == "ed25519"

	if isEd25519 {
		algo := ed25519.ED25519()
		return algo.Sign(msgStr, privateKeyHex)
	}

	// Default to secp256k1
	algo := secp256k1.SECP256K1()
	return algo.Sign(msgStr, privateKeyHex)
}

func (m *ChannelAuthorizeMethod) RequiredRole() types.Role {
	// Note: rippled requires admin role OR signing enabled
	// For now, allow user role since we're implementing the signing functionality
	return types.RoleUser
}

func (m *ChannelAuthorizeMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *ChannelAuthorizeMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
