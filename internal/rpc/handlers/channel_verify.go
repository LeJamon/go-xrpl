package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
)

// ChannelVerifyMethod handles channel_verify: it checks a payment-channel
// claim signature against the channel ID, amount, and public key, without
// touching ledger state.
type ChannelVerifyMethod struct{ baseHandler }

func (m *ChannelVerifyMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	fields, rpcErr := rawJSONFields(params)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Validate required fields
	// rippled: for (auto const& p : {jss::public_key, jss::channel_id, jss::amount, jss::signature}) if (!params.isMember(p)) return RPC::missing_field_error(p);
	if _, ok := fields["public_key"]; !ok {
		return nil, rpcerrors.RpcErrorMissingField("public_key")
	}
	if _, ok := fields["channel_id"]; !ok {
		return nil, rpcerrors.RpcErrorMissingField("channel_id")
	}
	if _, ok := fields["amount"]; !ok {
		return nil, rpcerrors.RpcErrorMissingField("amount")
	}
	if _, ok := fields["signature"]; !ok {
		return nil, rpcerrors.RpcErrorMissingField("signature")
	}

	// Parse public key - can be base58 (AccountPublic) or hex
	// rippled:
	// pk = parseBase58<PublicKey>(TokenType::AccountPublic, strPk);
	// if (!pk) { pkHex = strUnHex(strPk); if (!pkHex) return rpcError(rpcPUBLIC_MALFORMED); ... }
	publicKey, ok := jsonCppStringRaw(fields["public_key"])
	if !ok {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	pubKeyHex, err := parsePublicKey(publicKey)
	if err != nil {
		return nil, rpcerrors.RpcErrorPublicMalformed()
	}

	channelIDHex, rpcErr := parseChannelID(fields["channel_id"])
	if rpcErr != nil {
		return nil, rpcErr
	}
	drops, rpcErr := parseChannelAmount(fields["amount"])
	if rpcErr != nil {
		return nil, rpcErr
	}

	// The signature must be a non-empty hex string.
	signature, ok := rawJSONString(fields["signature"])
	if !ok {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	sigBytes, ok := unHexOddLength(signature)
	sigHex := strings.ToUpper(hex.EncodeToString(sigBytes))
	if !ok || len(sigBytes) == 0 {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}

	// Serialize the payment channel claim message
	// Message format: HashPrefix('CLM\0') + channel_id (32 bytes) + amount (8 bytes)
	claimJSON := map[string]any{
		"Channel": channelIDHex,
		"Amount":  strconv.FormatUint(drops, 10),
	}
	messageHex, err := binarycodec.EncodeForSigningClaim(claimJSON)
	if err != nil {
		return nil, rpcInternalError("channel_verify: claim encoding failed", err)
	}

	// Convert hex message to raw bytes for verification
	messageBytes, err := hex.DecodeString(messageHex)
	if err != nil {
		return nil, rpcInternalError("channel_verify: claim decoding failed", err)
	}

	// Verify the signature
	// rippled: result[jss::signature_verified] = verify(*pk, msg.slice(), makeSlice(*sig), /*canonical*/ true);
	verified := verifySignature(messageBytes, pubKeyHex, sigHex)

	response := map[string]any{
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
	pubKeyBytes, ok := unHexOddLength(pubKey)
	if ok && len(pubKeyBytes) == 33 {
		// Validate public key type
		// SECP256K1 compressed: 33 bytes, starts with 02 or 03
		// ED25519: 33 bytes, starts with ED
		if pubKeyBytes[0] == 0x02 || pubKeyBytes[0] == 0x03 || pubKeyBytes[0] == 0xED {
			return strings.ToUpper(hex.EncodeToString(pubKeyBytes)), nil
		}
	}

	// Try to decode as base58 AccountPublic
	pubKeyBytes, decodeErr := addresscodec.DecodeAccountPublicKey(pubKey)
	if decodeErr == nil && len(pubKeyBytes) == 33 {
		return strings.ToUpper(hex.EncodeToString(pubKeyBytes)), nil
	}

	return "", fmt.Errorf("invalid public key")
}

// unHexOddLength decodes hex the way rippled's strUnHex does: an odd-length
// string takes its first digit as a whole leading byte, so "ABC" is 0x0A 0xBC.
func unHexOddLength(s string) ([]byte, bool) {
	if len(s)%2 == 1 {
		s = "0" + s
	}
	decoded, err := hex.DecodeString(s)
	return decoded, err == nil
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
		algo := ed25519.Algorithm{}
		return algo.Validate(msgStr, pubKeyHex, sigHex)
	}

	// Otherwise use secp256k1
	algo := secp256k1.Algorithm{}
	return algo.Validate(msgStr, pubKeyHex, sigHex)
}
