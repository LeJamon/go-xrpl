package handlers

import (
	"encoding/hex"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// parseCredentialsAndDeriveKeypair parses credential parameters and derives a keypair.
// This matches rippled's keypairForSignature function exactly.
//
// It validates that exactly one credential source is provided (secret, seed, seed_hex,
// or passphrase), enforces that "secret" cannot be combined with key_type, and derives
// the keypair using the appropriate algorithm.
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
