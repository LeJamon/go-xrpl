package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/rfc1751"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
)

// WalletProposeMethod handles the wallet_propose RPC method
// This generates a new random keypair or derives one from a provided seed/passphrase
type WalletProposeMethod struct{ adminHandler }

func (m *WalletProposeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	var fields map[string]json.RawMessage
	if len(params) > 0 {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	keyType := ""
	if raw, ok := fields["key_type"]; ok {
		value, ok := rawString(raw)
		if !ok {
			return nil, rpcerrors.RpcErrorExpectedField("key_type", "string")
		}
		if value != "secp256k1" && value != "ed25519" {
			return nil, rpcerrors.RpcErrorInvalidParams("")
		}
		keyType = value
	}

	entropy := walletProposeLibSeed(fields)
	libSeed := entropy != nil
	if libSeed {
		if keyType != "" && keyType != "ed25519" {
			return nil, rpcerrors.RpcErrorBadSeed()
		}
		keyType = "ed25519"
	} else {
		var rpcErr *rpcerrors.RpcError
		if entropy, rpcErr = walletProposeSeed(fields); rpcErr != nil {
			return nil, rpcErr
		}
	}
	if keyType == "" {
		keyType = "secp256k1"
	}

	var publicKey string
	var err error
	if keyType == "ed25519" {
		_, publicKey, err = ed25519.Algorithm{}.DeriveKeypair(entropy, false)
	} else {
		_, publicKey, err = secp256k1.Algorithm{}.DeriveKeypair(entropy, false)
	}
	if err != nil {
		return nil, rpcInternalError("wallet_propose: keypair derivation failed", err)
	}

	encodedSeed, err := addresscodec.EncodeSeed(entropy, secp256k1.Algorithm{})
	if err != nil {
		return nil, rpcInternalError("wallet_propose: seed encoding failed", err)
	}

	accountID, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	if err != nil {
		return nil, rpcInternalError("wallet_propose: account address derivation failed", err)
	}

	pubKeyBytes, err := hex.DecodeString(publicKey)
	if err != nil {
		return nil, rpcInternalError("wallet_propose: public key decoding failed", err)
	}
	encodedPublicKey, err := addresscodec.EncodeAccountPublicKey(pubKeyBytes)
	if err != nil {
		return nil, rpcInternalError("wallet_propose: public key encoding failed", err)
	}

	masterKey, _ := rfc1751.SeedToEnglish(entropy)
	seedHexStr := strings.ToUpper(hex.EncodeToString(entropy))

	response := map[string]any{
		"account_id":      accountID,
		"key_type":        keyType,
		"master_key":      masterKey,
		"master_seed":     encodedSeed,
		"master_seed_hex": seedHexStr,
		"public_key":      encodedPublicKey,
		"public_key_hex":  strings.ToUpper(publicKey),
	}

	if passphrase, ok := rawString(fields["passphrase"]); ok && !libSeed &&
		passphrase != encodedSeed && passphrase != seedHexStr && passphrase != masterKey {
		if estimateEntropy(passphrase) < 80.0 {
			response["warning"] = "This wallet was generated using a user-supplied passphrase that has low entropy and is vulnerable to brute-force attacks."
		} else {
			response["warning"] = "This wallet was generated using a user-supplied passphrase. It may be vulnerable to brute-force attacks."
		}
	}

	return response, nil
}

// walletProposeLibSeed detects an Ed25519 seed in the xrpl.js "sEd" encoding,
// looking at passphrase first and only then at seed.
func walletProposeLibSeed(fields map[string]json.RawMessage) []byte {
	raw, ok := fields["passphrase"]
	if !ok {
		if raw, ok = fields["seed"]; !ok {
			return nil
		}
	}
	value, ok := rawString(raw)
	if !ok {
		return nil
	}
	decoded, algorithm, err := addresscodec.DecodeSeed(value)
	if err != nil {
		return nil
	}
	if _, isEd := algorithm.(ed25519.Algorithm); !isEd {
		return nil
	}
	return decoded
}

// walletProposeSeed resolves exactly one of passphrase, seed or seed_hex,
// or returns a random seed when none is given.
func walletProposeSeed(fields map[string]json.RawMessage) ([]byte, *rpcerrors.RpcError) {
	source := ""
	count := 0
	for _, name := range []string{"passphrase", "seed", "seed_hex"} {
		if _, ok := fields[name]; ok {
			source = name
			count++
		}
	}
	if count == 0 {
		entropy := make([]byte, 16)
		if _, err := rand.Read(entropy); err != nil {
			return nil, rpcInternalError("wallet_propose: random seed generation failed", err)
		}
		return entropy, nil
	}
	if count > 1 {
		return nil, rpcerrors.RpcErrorInvalidParams("Exactly one of the following must be specified: passphrase, seed or seed_hex")
	}

	value, ok := rawString(fields[source])
	if !ok {
		return nil, rpcerrors.RpcErrorExpectedField(source, "string")
	}

	var seed []byte
	switch source {
	case "passphrase":
		seed = parseSigningGenericSeed(value)
	case "seed":
		if decoded, algorithm, err := addresscodec.DecodeSeed(value); err == nil {
			if _, isEd := algorithm.(ed25519.Algorithm); !isEd {
				seed = decoded
			}
		}
	case "seed_hex":
		if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == 16 {
			seed = decoded
		}
	}
	if seed == nil {
		return nil, rpcerrors.RpcErrorBadSeed()
	}
	return seed, nil
}

// estimateEntropy estimates the Shannon entropy of a string in bits
// This matches rippled's estimate_entropy function
func estimateEntropy(input string) float64 {
	if len(input) == 0 {
		return 0
	}

	// Calculate character frequency
	freq := make(map[byte]float64)
	for i := 0; i < len(input); i++ {
		freq[input[i]]++
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
