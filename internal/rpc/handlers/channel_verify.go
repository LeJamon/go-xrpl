package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// ChannelVerifyMethod handles the channel_verify RPC method
// This verifies a signature that can be used to redeem a specific amount from a payment channel.
type ChannelVerifyMethod struct{ BaseHandler }

// channelVerifyRequest represents the request parameters
type channelVerifyRequest struct {
	PublicKey string `json:"public_key"`
	ChannelID string `json:"channel_id"`
	Amount    string `json:"amount"`
	Signature string `json:"signature"`
}

func (m *ChannelVerifyMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request channelVerifyRequest

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	// Validate required fields
	// rippled: for (auto const& p : {jss::public_key, jss::channel_id, jss::amount, jss::signature}) if (!params.isMember(p)) return RPC::missing_field_error(p);
	if request.PublicKey == "" {
		return nil, &types.RpcError{
			Code:        types.RpcINVALID_PARAMS,
			ErrorString: "invalidParams",
			Type:        "invalidParams",
			Message:     "Missing field 'public_key'.",
		}
	}
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
	if request.Signature == "" {
		return nil, &types.RpcError{
			Code:        types.RpcINVALID_PARAMS,
			ErrorString: "invalidParams",
			Type:        "invalidParams",
			Message:     "Missing field 'signature'.",
		}
	}

	// Parse public key - can be base58 (AccountPublic) or hex
	// rippled:
	// pk = parseBase58<PublicKey>(TokenType::AccountPublic, strPk);
	// if (!pk) { pkHex = strUnHex(strPk); if (!pkHex) return rpcError(rpcPUBLIC_MALFORMED); ... }
	pubKeyHex, err := parsePublicKey(request.PublicKey)
	if err != nil {
		return nil, &types.RpcError{
			Code:        types.RpcPUBLIC_MALFORMED,
			ErrorString: "publicMalformed",
			Type:        "publicMalformed",
			Message:     "Public key is malformed.",
		}
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

	// Validate signature - must be valid hex and non-empty
	// rippled: auto sig = strUnHex(params[jss::signature].asString()); if (!sig || !sig->size()) return rpcError(rpcINVALID_PARAMS);
	sigHex := strings.ToUpper(request.Signature)
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil || len(sigBytes) == 0 {
		return nil, &types.RpcError{
			Code:        types.RpcINVALID_PARAMS,
			ErrorString: "invalidParams",
			Type:        "invalidParams",
			Message:     "Invalid field 'signature'.",
		}
	}

	// Serialize the payment channel claim message
	// Message format: HashPrefix('CLM\0') + channel_id (32 bytes) + amount (8 bytes)
	claimJSON := map[string]any{
		"Channel": channelIDHex,
		"Amount":  strconv.FormatUint(drops, 10),
	}
	messageHex, err := binarycodec.EncodeForSigningClaim(claimJSON)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to encode claim: " + err.Error())
	}

	// Convert hex message to raw bytes for verification
	messageBytes, err := hex.DecodeString(messageHex)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to decode message: " + err.Error())
	}

	// Verify the signature
	// rippled: result[jss::signature_verified] = verify(*pk, msg.slice(), makeSlice(*sig), /*canonical*/ true);
	verified := verifySignature(messageBytes, pubKeyHex, sigHex)

	response := map[string]interface{}{
		"signature_verified": verified,
	}

	return response, nil
}

// parsePublicKey parses a public key from base58 or hex format
// Returns the hex-encoded public key
func parsePublicKey(pubKey string) (pubKeyHex string, err error) {
	// Handle panics from address-codec (which can panic on invalid input)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid public key")
		}
	}()

	// Try to decode as hex first (this is the more common case and safer)
	pubKeyBytes, hexErr := hex.DecodeString(pubKey)
	if hexErr == nil && len(pubKeyBytes) == 33 {
		// Validate public key type
		// SECP256K1 compressed: 33 bytes, starts with 02 or 03
		// ED25519: 33 bytes, starts with ED
		if pubKeyBytes[0] == 0x02 || pubKeyBytes[0] == 0x03 || pubKeyBytes[0] == 0xED {
			return strings.ToUpper(pubKey), nil
		}
	}

	// Try to decode as base58 AccountPublic
	pubKeyBytes, decodeErr := addresscodec.DecodeAccountPublicKey(pubKey)
	if decodeErr == nil && len(pubKeyBytes) == 33 {
		return strings.ToUpper(hex.EncodeToString(pubKeyBytes)), nil
	}

	return "", fmt.Errorf("invalid public key")
}

// verifySignature verifies a signature against a message using the public key
func verifySignature(message []byte, pubKeyHex string, sigHex string) bool {
	// Convert message bytes to string for the Validate functions
	msgStr := string(message)

	// Decode public key to determine algorithm
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubKeyBytes) < 1 {
		return false
	}

	// ED25519 keys start with 0xED prefix
	if pubKeyBytes[0] == 0xED {
		algo := ed25519.ED25519()
		return algo.Validate(msgStr, pubKeyHex, sigHex)
	}

	// Otherwise use secp256k1
	algo := secp256k1.SECP256K1()
	return algo.Validate(msgStr, pubKeyHex, sigHex)
}

