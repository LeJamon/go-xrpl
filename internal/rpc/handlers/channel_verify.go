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
	var fields map[string]json.RawMessage
	if len(params) > 0 {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	for _, name := range []string{"public_key", "channel_id", "amount", "signature"} {
		if _, ok := fields[name]; !ok {
			return nil, rpcerrors.RpcErrorMissingField(name)
		}
	}

	publicKey, ok := rawString(fields["public_key"])
	if !ok {
		return nil, rpcerrors.RpcErrorPublicMalformed()
	}
	pubKeyHex, err := parsePublicKey(publicKey)
	if err != nil {
		return nil, rpcerrors.RpcErrorPublicMalformed()
	}

	channelIDHex, rpcErr := parseChannelID(fields["channel_id"])
	if rpcErr != nil {
		return nil, rpcErr
	}
	drops, ok := parseChannelDrops(fields["amount"])
	if !ok {
		return nil, rpcerrors.RpcErrorChannelAmountMalformed()
	}

	signature, ok := rawString(fields["signature"])
	if !ok {
		return nil, rpcerrors.RpcErrorInvalidParams("")
	}
	if len(signature)%2 == 1 {
		signature = "0" + signature
	}
	sigBytes, err := hex.DecodeString(signature)
	if err != nil || len(sigBytes) == 0 {
		return nil, rpcerrors.RpcErrorInvalidParams("")
	}
	sigHex := strings.ToUpper(hex.EncodeToString(sigBytes))

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
		algo := ed25519.Algorithm{}
		return algo.Validate(msgStr, pubKeyHex, sigHex)
	}

	// Otherwise use secp256k1
	algo := secp256k1.Algorithm{}
	return algo.Validate(msgStr, pubKeyHex, sigHex)
}
