package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
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

// parseCredentialsAndDeriveKeypair parses credential parameters and derives a keypair
// This matches rippled's keypairForSignature function exactly
func parseCredentialsAndDeriveKeypair(secret, seed, seedHex, passphrase, keyType string, apiVersion int) (privateKeyHex string, publicKeyHex string, rpcErr *types.RpcError) {
	hasKeyType := keyType != ""

	// Count how many secret types are provided
	// rippled: static char const* const secretTypes[]{ jss::passphrase.c_str(), jss::secret.c_str(), jss::seed.c_str(), jss::seed_hex.c_str() };
	secretCount := 0
	var secretType string
	var secretValue string

	if passphrase != "" {
		secretCount++
		secretType = "passphrase"
		secretValue = passphrase
	}
	if secret != "" {
		secretCount++
		secretType = "secret"
		secretValue = secret
	}
	if seed != "" {
		secretCount++
		secretType = "seed"
		secretValue = seed
	}
	if seedHex != "" {
		secretCount++
		secretType = "seed_hex"
		secretValue = seedHex
	}

	// rippled: if (count == 0 || secretType == nullptr) { error = RPC::missing_field_error(jss::secret); return {}; }
	if secretCount == 0 {
		return "", "", &types.RpcError{
			Code:        types.RpcINVALID_PARAMS,
			ErrorString: "invalidParams",
			Type:        "invalidParams",
			Message:     "Missing field 'secret'.",
		}
	}

	// rippled: if (count > 1) { error = RPC::make_param_error("Exactly one of the following must be specified: ..."); return {}; }
	if secretCount > 1 {
		return "", "", &types.RpcError{
			Code:        types.RpcINVALID_PARAMS,
			ErrorString: "invalidParams",
			Type:        "invalidParams",
			Message:     "Exactly one of the following must be specified: passphrase, secret, seed or seed_hex",
		}
	}

	// Determine key type
	// rippled: if (has_key_type) { ... if (!keyType) { error = RPC::make_error(rpcBAD_KEY_TYPE); ... } }
	var useEd25519 bool
	if hasKeyType {
		switch strings.ToLower(keyType) {
		case "secp256k1":
			useEd25519 = false
		case "ed25519":
			useEd25519 = true
		default:
			if apiVersion > 1 {
				return "", "", &types.RpcError{
					Code:        types.RpcBAD_KEY_TYPE,
					ErrorString: "badKeyType",
					Type:        "badKeyType",
					Message:     "Bad key type.",
				}
			}
			return "", "", &types.RpcError{
				Code:        types.RpcINVALID_PARAMS,
				ErrorString: "invalidParams",
				Type:        "invalidParams",
				Message:     "Invalid field 'key_type'.",
			}
		}

		// rippled: if (strcmp(secretType, jss::secret.c_str()) == 0) { error = RPC::make_param_error("The secret field is not allowed if key_type is used."); return {}; }
		if secretType == "secret" {
			return "", "", &types.RpcError{
				Code:        types.RpcINVALID_PARAMS,
				ErrorString: "invalidParams",
				Type:        "invalidParams",
				Message:     "The secret field is not allowed if key_type is used.",
			}
		}
	}

	// Parse the seed value
	var seedBytes []byte
	var err error

	switch secretType {
	case "seed":
		// Base58 encoded seed (starts with 's')
		// Use DecodeSeed which returns the seed bytes and algorithm
		var algo interface{}
		seedBytes, algo, err = addresscodec.DecodeSeed(secretValue)
		if err != nil {
			return "", "", &types.RpcError{
				Code:        types.RpcBAD_SEED,
				ErrorString: "badSeed",
				Type:        "badSeed",
				Message:     "Disallowed seed.",
			}
		}
		// If key_type not specified, use the algorithm from the seed
		if !hasKeyType {
			_, isEd := algo.(ed25519.ED25519CryptoAlgorithm)
			useEd25519 = isEd
		}

	case "seed_hex":
		// Hex-encoded 16-byte seed
		seedBytes, err = hex.DecodeString(secretValue)
		if err != nil || len(seedBytes) != 16 {
			return "", "", &types.RpcError{
				Code:        types.RpcBAD_SEED,
				ErrorString: "badSeed",
				Type:        "badSeed",
				Message:     "Disallowed seed.",
			}
		}

	case "passphrase":
		// SHA512-Half of the passphrase, take first 16 bytes
		hash := common.Sha512Half([]byte(secretValue))
		seedBytes = hash[:16]

	case "secret":
		// "secret" is the legacy field - can be a seed (base58), hex, or passphrase
		// Try to parse as base58 seed first
		var algo interface{}
		seedBytes, algo, err = addresscodec.DecodeSeed(secretValue)
		if err == nil {
			// Successfully parsed as base58 seed
			_, isEd := algo.(ed25519.ED25519CryptoAlgorithm)
			useEd25519 = isEd
		} else {
			// Try as hex
			seedBytes, err = hex.DecodeString(secretValue)
			if err != nil || len(seedBytes) != 16 {
				// Treat as passphrase
				hash := common.Sha512Half([]byte(secretValue))
				seedBytes = hash[:16]
			}
		}
	}

	// Derive keypair using the appropriate algorithm
	if useEd25519 {
		algo := ed25519.ED25519()
		privateKeyHex, publicKeyHex, err = algo.DeriveKeypair(seedBytes, false)
	} else {
		algo := secp256k1.SECP256K1()
		privateKeyHex, publicKeyHex, err = algo.DeriveKeypair(seedBytes, false)
	}

	if err != nil {
		return "", "", &types.RpcError{
			Code:        types.RpcBAD_SEED,
			ErrorString: "badSeed",
			Type:        "badSeed",
			Message:     "Disallowed seed.",
		}
	}

	return privateKeyHex, publicKeyHex, nil
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
