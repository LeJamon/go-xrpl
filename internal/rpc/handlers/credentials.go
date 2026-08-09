package handlers

import (
	"encoding/hex"
	"encoding/json"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/rfc1751"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
)

func parseCredentialsAndDeriveKeypair(apiVersion int, params json.RawMessage) (privateKeyHex, publicKeyHex, keyType string, rpcErr *rpcerrors.RpcError) {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(params, &fields)

	secretType := ""
	count := 0
	for _, name := range []string{"passphrase", "secret", "seed", "seed_hex"} {
		if _, ok := fields[name]; ok {
			count++
			secretType = name
		}
	}
	if count == 0 {
		return "", "", "", rpcerrors.RpcErrorMissingField("secret")
	}
	if count > 1 {
		return "", "", "", rpcerrors.RpcErrorInvalidParams("Exactly one of the following must be specified: passphrase, secret, seed or seed_hex")
	}

	keyTypeRaw, hasKeyType := fields["key_type"]
	if hasKeyType {
		var value string
		if err := json.Unmarshal(keyTypeRaw, &value); err != nil || isJSONNull(keyTypeRaw) {
			return "", "", "", rpcerrors.RpcErrorExpectedField("key_type", "string")
		}
		switch value {
		case "secp256k1", "ed25519":
			keyType = value
		default:
			if apiVersion > 1 {
				return "", "", "", rpcerrors.RpcErrorBadKeyType("Bad key type.")
			}
			return "", "", "", rpcerrors.RpcErrorInvalidField("key_type")
		}
		if secretType == "secret" {
			return "", "", "", rpcerrors.RpcErrorInvalidParams("The secret field is not allowed if key_type is used.")
		}
	}

	var seedBytes []byte
	if secretType != "seed_hex" {
		if value, ok := rawString(fields[secretType]); ok {
			if decoded, algorithm, err := addresscodec.DecodeSeed(value); err == nil {
				if _, isEd := algorithm.(ed25519.Algorithm); isEd {
					if hasKeyType && keyType != "ed25519" {
						return "", "", "", rpcerrors.NewRpcError(rpcerrors.RpcBAD_SEED, "badSeed", "badSeed", "Specified seed is for an Ed25519 wallet.")
					}
					seedBytes = decoded
					keyType = "ed25519"
				}
			}
		}
	}
	if keyType == "" {
		keyType = "secp256k1"
	}

	if seedBytes == nil {
		if hasKeyType {
			value, ok := rawString(fields[secretType])
			if !ok {
				return "", "", "", rpcerrors.RpcErrorExpectedField(secretType, "string")
			}
			var err error
			switch secretType {
			case "passphrase":
				seedBytes = parseSigningGenericSeed(value)
			case "seed":
				seedBytes, _, err = addresscodec.DecodeSeed(value)
				if err != nil {
					seedBytes = nil
				}
			case "seed_hex":
				seedBytes, err = hex.DecodeString(value)
				if err != nil || len(seedBytes) != 16 {
					seedBytes = nil
				}
			}
			if seedBytes == nil {
				return "", "", "", rpcerrors.RpcErrorBadSeed()
			}
		} else {
			value, ok := rawString(fields["secret"])
			if !ok {
				return "", "", "", rpcerrors.RpcErrorExpectedField("secret", "string")
			}
			seedBytes = parseSigningGenericSeed(value)
			if seedBytes == nil {
				return "", "", "", invalidSeedField(secretType)
			}
		}
	}

	var err error
	if keyType == "ed25519" {
		privateKeyHex, publicKeyHex, err = (ed25519.Algorithm{}).DeriveKeypair(seedBytes, false)
	} else {
		privateKeyHex, publicKeyHex, err = (secp256k1.Algorithm{}).DeriveKeypair(seedBytes, false)
	}
	if err != nil {
		return "", "", "", rpcerrors.RpcErrorBadSeed()
	}
	return privateKeyHex, publicKeyHex, keyType, nil
}

func rawString(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || isJSONNull(raw) {
		return "", false
	}
	return value, true
}

func parseSigningGenericSeed(value string) []byte {
	if value == "" || isSeedToken(value) {
		return nil
	}
	if seed, err := hex.DecodeString(value); err == nil && len(seed) == 16 {
		return seed
	}
	if seed, _, err := addresscodec.DecodeSeed(value); err == nil {
		return seed
	}
	if seed, err := rfc1751.EnglishToSeed(value); err == nil {
		return seed
	}
	hash := sha512half.Sum([]byte(value))
	return hash[:16]
}

func isSeedToken(value string) bool {
	if addresscodec.IsValidClassicAddress(value) {
		return true
	}
	if key, err := addresscodec.DecodeNodePublicKey(value); err == nil && len(key) == addresscodec.NodePublicKeyLength {
		return true
	}
	if key, err := addresscodec.DecodeAccountPublicKey(value); err == nil && len(key) == addresscodec.AccountPublicKeyLength {
		return true
	}
	for _, prefix := range []byte{addresscodec.NodePrivateKeyPrefix, addresscodec.AccountSecretKeyPrefix} {
		if key, err := addresscodec.Decode(value, []byte{prefix}); err == nil && len(key) == 32 {
			return true
		}
	}
	return false
}

func invalidSeedField(field string) *rpcerrors.RpcError {
	return rpcerrors.NewRpcError(rpcerrors.RpcBAD_SEED, "badSeed", "badSeed", "Invalid field '"+field+"'.")
}
