package handlers

import (
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// SignForMethod handles the sign_for RPC method
// This adds a signature to a transaction for multi-signing
type SignForMethod struct{}

func (m *SignForMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Account    string          `json:"account"`
		TxJson     json.RawMessage `json:"tx_json"`
		Secret     string          `json:"secret,omitempty"`
		Seed       string          `json:"seed,omitempty"`
		SeedHex    string          `json:"seed_hex,omitempty"`
		Passphrase string          `json:"passphrase,omitempty"`
		KeyType    string          `json:"key_type,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	// Validate required fields
	if request.Account == "" {
		return nil, types.RpcErrorMissingField("account")
	}

	if len(request.TxJson) == 0 {
		return nil, types.RpcErrorMissingField("tx_json")
	}

	// Check for signing credentials
	if request.Secret == "" && request.Seed == "" && request.SeedHex == "" && request.Passphrase == "" {
		return nil, types.RpcErrorInvalidParams("Missing signing credentials")
	}

	// Validate account address
	if !addresscodec.IsValidClassicAddress(request.Account) {
		return nil, &types.RpcError{
			Code:        types.RpcACT_MALFORMED,
			ErrorString: "actMalformed",
			Type:        "actMalformed",
			Message:     "Account malformed.",
		}
	}

	// Determine the key type
	keyType := strings.ToLower(request.KeyType)
	if keyType == "" {
		keyType = "secp256k1" // Default
	}
	if keyType != "secp256k1" && keyType != "ed25519" {
		return nil, &types.RpcError{
			Code:        types.RpcBAD_KEY_TYPE,
			ErrorString: "badKeyType",
			Type:        "badKeyType",
			Message:     "Invalid field 'key_type'.",
		}
	}

	// Derive entropy from the provided credential
	var entropy []byte
	var detectedKeyType string

	if request.Seed != "" || request.Secret != "" {
		// secret is an alias for seed
		seedStr := request.Seed
		if seedStr == "" {
			seedStr = request.Secret
		}

		// Decode the seed
		var err error
		var algo interface{}
		entropy, algo, err = addresscodec.DecodeSeed(seedStr)
		if err != nil {
			return nil, &types.RpcError{
				Code:        types.RpcBAD_SEED,
				ErrorString: "badSeed",
				Type:        "badSeed",
				Message:     "Disallowed seed.",
			}
		}

		// Detect key type from seed
		if _, isEd25519 := algo.(ed25519.ED25519CryptoAlgorithm); isEd25519 {
			detectedKeyType = "ed25519"
		} else {
			detectedKeyType = "secp256k1"
		}

		// If key_type was specified, verify it matches
		if request.KeyType != "" && keyType != detectedKeyType {
			return nil, &types.RpcError{
				Code:        types.RpcBAD_SEED,
				ErrorString: "badSeed",
				Type:        "badSeed",
				Message:     "Disallowed seed.",
			}
		}
		keyType = detectedKeyType
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
	}

	// Derive keypair based on key type
	var privateKey, publicKey string
	var err error

	if keyType == "ed25519" {
		algo := ed25519.ED25519()
		privateKey, publicKey, err = algo.DeriveKeypair(entropy, false)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to derive keypair: " + err.Error())
		}
	} else {
		algo := secp256k1.SECP256K1()
		privateKey, publicKey, err = algo.DeriveKeypair(entropy, false)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to derive keypair: " + err.Error())
		}
	}

	// Parse the transaction JSON
	var txMap map[string]interface{}
	if err := json.Unmarshal(request.TxJson, &txMap); err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid tx_json: " + err.Error())
	}

	// Verify that Account field exists in transaction
	if _, ok := txMap["Account"]; !ok {
		return nil, types.RpcErrorMissingField("Account")
	}

	// For multi-signing, SigningPubKey must be empty string
	txMap["SigningPubKey"] = ""

	// Get existing signers array or create new one
	var signers []map[string]interface{}
	if existingSigners, ok := txMap["Signers"].([]interface{}); ok {
		for _, s := range existingSigners {
			if signer, ok := s.(map[string]interface{}); ok {
				signers = append(signers, signer)
			}
		}
	}

	// Check if this account has already signed
	for _, signerWrapper := range signers {
		if signer, ok := signerWrapper["Signer"].(map[string]interface{}); ok {
			if signer["Account"] == request.Account {
				return nil, types.RpcErrorInvalidParams("Account has already signed this transaction")
			}
		}
	}

	// Create signing payload for multisigning
	// Remove Signers from the map for signing
	txMapForSigning := make(map[string]interface{})
	for k, v := range txMap {
		if k != "Signers" {
			txMapForSigning[k] = v
		}
	}

	// Encode for multisigning (adds the signer's account as suffix)
	signingPayload, err := binarycodec.EncodeForMultisigning(txMapForSigning, request.Account)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to encode for multisigning: " + err.Error())
	}

	// Sign the payload
	signature, err := signPayload(signingPayload, privateKey, keyType)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to sign transaction: " + err.Error())
	}

	// Create new signer entry
	newSigner := map[string]interface{}{
		"Signer": map[string]interface{}{
			"Account":       request.Account,
			"SigningPubKey": publicKey,
			"TxnSignature":  signature,
		},
	}

	// Add new signer to the list
	signers = append(signers, newSigner)

	// Sort signers by account (required by XRPL protocol)
	sort.Slice(signers, func(i, j int) bool {
		iAccount := ""
		jAccount := ""
		if s, ok := signers[i]["Signer"].(map[string]interface{}); ok {
			iAccount, _ = s["Account"].(string)
		}
		if s, ok := signers[j]["Signer"].(map[string]interface{}); ok {
			jAccount, _ = s["Account"].(string)
		}
		return iAccount < jAccount
	})

	// Update transaction with sorted signers
	txMap["Signers"] = signers

	// Encode the transaction to binary
	txBlob, err := binarycodec.Encode(txMap)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to encode transaction: " + err.Error())
	}

	// Calculate transaction hash
	txHash := CalculateTxHash(txBlob)

	// Add hash to response tx_json
	txMap["hash"] = txHash

	response := map[string]interface{}{
		"tx_blob": txBlob,
		"tx_json": txMap,
	}

	return response, nil
}

// signPayload signs a hex-encoded payload with the given private key
func signPayload(payloadHex string, privateKeyHex string, keyType string) (string, error) {
	// Decode the payload
	payloadBytes, err := hex.DecodeString(payloadHex)
	if err != nil {
		return "", err
	}

	// Convert to string for crypto functions
	payloadStr := string(payloadBytes)

	var signature string

	if keyType == "ed25519" {
		algo := ed25519.ED25519()
		signature, err = algo.Sign(payloadStr, privateKeyHex)
	} else {
		algo := secp256k1.SECP256K1()
		signature, err = algo.Sign(payloadStr, privateKeyHex)
	}

	if err != nil {
		return "", err
	}

	return strings.ToUpper(signature), nil
}

func (m *SignForMethod) RequiredRole() types.Role {
	return types.RoleUser
}

func (m *SignForMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
