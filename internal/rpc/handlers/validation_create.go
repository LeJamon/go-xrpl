package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/crypto"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/crypto/rfc1751"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// ValidationCreateMethod handles the validation_create RPC method.
// Mirrors rippled ValidationCreate.cpp doValidationCreate: derives a
// secp256k1 validator keypair from an optional secret (or a fresh random
// seed) and returns it in the formats a validator config consumes.
// Admin-only — it makes no sense to ask an untrusted server for this.
type ValidationCreateMethod struct{ AdminHandler }

type validationCreateRequest struct {
	Secret string `json:"secret,omitempty"`
}

func (m *ValidationCreateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request validationCreateRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	seed, ok := validationSeed(request.Secret)
	if !ok {
		return nil, &types.RpcError{
			Code:        types.RpcBAD_SEED,
			ErrorString: "badSeed",
			Type:        "badSeed",
			Message:     "Disallowed seed.",
		}
	}

	// Validator keys are always secp256k1, derived directly from the root
	// generator (rippled ValidationCreate.cpp:54).
	algo := secp256k1.SECP256K1()
	privHex, pubHex, err := algo.DeriveKeypair(seed, true)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to derive validator keypair: %v", err))
	}

	pubBytes, err := hex.DecodeString(pubHex)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to decode public key: %v", err))
	}
	validationPublicKey, err := addresscodec.EncodeNodePublicKey(pubBytes)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to encode validation public key: %v", err))
	}

	// DeriveKeypair returns the private key as "00"+64 hex; the NodePrivate
	// token encodes the raw 32-byte key, so drop the leading "00".
	privBytes, err := hex.DecodeString(privHex[2:])
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to decode private key: %v", err))
	}
	validationPrivateKey := addresscodec.Base58CheckEncode(privBytes, addresscodec.NodePrivateKeyPrefix)

	encodedSeed, err := addresscodec.EncodeSeed(seed, algo)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to encode seed: %v", err))
	}

	validationKey, err := rfc1751.SeedToEnglish(seed)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to encode RFC-1751 key: %v", err))
	}

	return map[string]interface{}{
		"validation_key":         validationKey,
		"validation_private_key": validationPrivateKey,
		"validation_public_key":  validationPublicKey,
		"validation_seed":        encodedSeed,
	}, nil
}

// validationSeed mirrors rippled parseGenericSeed (Seed.cpp): an empty secret
// yields a fresh random seed; otherwise the secret is parsed as a base58
// family seed, then as an RFC-1751 phrase, and finally hashed as a passphrase.
func validationSeed(secret string) ([]byte, bool) {
	if secret == "" {
		seed, err := crypto.RandomSeed()
		if err != nil {
			return nil, false
		}
		return seed, true
	}
	if entropy, _, err := addresscodec.DecodeSeed(secret); err == nil {
		return entropy, true
	}
	if entropy, err := rfc1751.EnglishToSeed(secret); err == nil {
		return entropy, true
	}
	hash := common.Sha512Half([]byte(secret))
	return hash[:16], true
}
