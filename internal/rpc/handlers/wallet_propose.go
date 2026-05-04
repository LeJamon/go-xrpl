package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/rfc1751"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// WalletProposeMethod handles the wallet_propose RPC method
// This generates a new random keypair or derives one from a provided seed/passphrase
type WalletProposeMethod struct{ AdminHandler }

// walletProposeRequest represents the request parameters
type walletProposeRequest struct {
	Seed       string `json:"seed,omitempty"`
	SeedHex    string `json:"seed_hex,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	KeyType    string `json:"key_type,omitempty"`
}

func (m *WalletProposeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request walletProposeRequest

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	// Default key type to secp256k1 if not specified
	keyType := strings.ToLower(request.KeyType)
	if keyType == "" {
		keyType = "secp256k1"
	}

	// Validate key type
	if keyType != "secp256k1" && keyType != "ed25519" {
		return nil, &types.RpcError{
			Code:        types.RpcBAD_KEY_TYPE,
			ErrorString: "badKeyType",
			Type:        "badKeyType",
			Message:     "Invalid field 'key_type'.",
		}
	}

	var entropy []byte
	var warning string
	var passphraseUsed bool

	// Determine the seed source
	// Priority: seed > seed_hex > passphrase > random
	if request.Seed != "" {
		// Decode the provided seed
		decodedEntropy, algo, err := addresscodec.DecodeSeed(request.Seed)
		if err != nil {
			return nil, &types.RpcError{
				Code:        types.RpcBAD_SEED,
				ErrorString: "badSeed",
				Type:        "badSeed",
				Message:     "Disallowed seed.",
			}
		}
		entropy = decodedEntropy

		// Check if the seed's algorithm matches the requested key type
		// If a seed encodes ed25519 but user requests secp256k1, that's an error
		if _, isEd25519 := algo.(ed25519.ED25519CryptoAlgorithm); isEd25519 {
			if keyType != "ed25519" {
				return nil, &types.RpcError{
					Code:        types.RpcBAD_SEED,
					ErrorString: "badSeed",
					Type:        "badSeed",
					Message:     "Disallowed seed.",
				}
			}
		}
	} else if request.SeedHex != "" {
		// Decode hex seed
		var err error
		entropy, err = hex.DecodeString(request.SeedHex)
		if err != nil || len(entropy) != 16 {
			return nil, &types.RpcError{
				Code:        types.RpcBAD_SEED,
				ErrorString: "badSeed",
				Type:        "badSeed",
				Message:     "Disallowed seed.",
			}
		}
	} else if request.Passphrase != "" {
		// Derive seed from passphrase using SHA-512 Half (first 16 bytes of SHA-512)
		hash := common.Sha512Half([]byte(request.Passphrase))
		entropy = hash[:16]
		passphraseUsed = true
	} else {
		// Generate random seed
		entropy = make([]byte, 16)
		if _, err := rand.Read(entropy); err != nil {
			return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to generate random seed: %v", err))
		}
	}

	// Derive keypair based on key type
	var privateKey, publicKey string
	var encodedSeed string
	var err error

	if keyType == "ed25519" {
		algo := ed25519.ED25519()
		privateKey, publicKey, err = algo.DeriveKeypair(entropy, false)
		if err != nil {
			return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to derive keypair: %v", err))
		}
		encodedSeed, err = addresscodec.EncodeSeed(entropy, algo)
		if err != nil {
			return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to encode seed: %v", err))
		}
	} else {
		algo := secp256k1.SECP256K1()
		privateKey, publicKey, err = algo.DeriveKeypair(entropy, false)
		if err != nil {
			return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to derive keypair: %v", err))
		}
		encodedSeed, err = addresscodec.EncodeSeed(entropy, algo)
		if err != nil {
			return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to encode seed: %v", err))
		}
	}
	_ = privateKey // Private key is derived but not returned (security)

	// Derive account address from public key
	accountID, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to derive account address: %v", err))
	}

	// Encode public key in base58
	pubKeyBytes, err := hex.DecodeString(publicKey)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to decode public key: %v", err))
	}
	encodedPublicKey, err := addresscodec.EncodeAccountPublicKey(pubKeyBytes)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to encode public key: %v", err))
	}

	// Encode seed as RFC-1751 human-readable words (master_key)
	masterKey, _ := rfc1751.SeedToEnglish(entropy)

	// Add passphrase warning matching rippled logic
	// rippled skips warning if passphrase equals any seed encoding
	seedHexStr := strings.ToUpper(hex.EncodeToString(entropy))
	if passphraseUsed {
		if request.Passphrase != encodedSeed && request.Passphrase != seedHexStr && request.Passphrase != masterKey {
			entropyBits := estimateEntropy(request.Passphrase)
			if entropyBits < 80.0 {
				warning = "This wallet was generated using a user-supplied passphrase that has low entropy and is vulnerable to brute-force attacks."
			} else {
				warning = "This wallet was generated using a user-supplied passphrase. It may be vulnerable to brute-force attacks."
			}
		}
	}

	// Build response matching rippled format
	response := map[string]interface{}{
		"account_id":      accountID,
		"key_type":        keyType,
		"master_key":      masterKey,
		"master_seed":     encodedSeed,
		"master_seed_hex": seedHexStr,
		"public_key":      encodedPublicKey,
		"public_key_hex":  strings.ToUpper(publicKey),
	}

	if warning != "" {
		response["warning"] = warning
	}

	return response, nil
}

// estimateEntropy estimates the Shannon entropy of a string in bits
// This matches rippled's estimate_entropy function
func estimateEntropy(input string) float64 {
	if len(input) == 0 {
		return 0
	}

	// Calculate character frequency
	freq := make(map[rune]float64)
	for _, c := range input {
		freq[c]++
	}

	// Calculate Shannon entropy
	var se float64
	length := float64(len(input))
	for _, f := range freq {
		x := f / length
		se += x * math.Log2(x)
	}

	// Multiply by length to get total entropy estimate
	return math.Floor(-se * length)
}
