package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/address-codec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binary-codec"
	crypto "github.com/LeJamon/goXRPLd/crypto/common"
	ed25519crypto "github.com/LeJamon/goXRPLd/crypto/algorithms/ed25519"
	secp256k1crypto "github.com/LeJamon/goXRPLd/crypto/algorithms/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// SignMethod handles the sign RPC method
type SignMethod struct{}

func (m *SignMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		TxJson     json.RawMessage `json:"tx_json"`
		Secret     string          `json:"secret,omitempty"`
		Seed       string          `json:"seed,omitempty"`
		SeedHex    string          `json:"seed_hex,omitempty"`
		Passphrase string          `json:"passphrase,omitempty"`
		KeyType    string          `json:"key_type,omitempty"`
		Offline    bool            `json:"offline,omitempty"`
		BuildPath  bool            `json:"build_path,omitempty"`
		FeeMultMax uint32          `json:"fee_mult_max,omitempty"`
		FeeDivMax  uint32          `json:"fee_div_max,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if len(request.TxJson) == 0 {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: tx_json")
	}

	// Check for signing credentials
	if request.Secret == "" && request.Seed == "" && request.SeedHex == "" && request.Passphrase == "" {
		return nil, types.RpcErrorInvalidParams("Missing signing credentials")
	}

	// Check if ledger service is available (needed for auto-filling fields)
	if !request.Offline && (types.Services == nil || types.Services.Ledger == nil) {
		return nil, types.RpcErrorInternal("Ledger service not available")
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
		if _, isEd25519 := algo.(ed25519crypto.ED25519CryptoAlgorithm); isEd25519 {
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
		hash := crypto.Sha512Half([]byte(request.Passphrase))
		entropy = hash[:16]
	}

	// Derive keypair based on key type
	var privateKey, publicKey string
	var err error

	if keyType == "ed25519" {
		algo := ed25519crypto.ED25519()
		privateKey, publicKey, err = algo.DeriveKeypair(entropy, false)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to derive keypair: " + err.Error())
		}
	} else {
		algo := secp256k1crypto.SECP256K1()
		privateKey, publicKey, err = algo.DeriveKeypair(entropy, false)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to derive keypair: " + err.Error())
		}
	}

	// Derive address from public key
	address, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to derive address: " + err.Error())
	}

	// Parse the transaction JSON
	var txMap map[string]interface{}
	if err := json.Unmarshal(request.TxJson, &txMap); err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid tx_json: " + err.Error())
	}

	// Verify the account matches the signing key
	if txAccount, ok := txMap["Account"].(string); ok {
		if txAccount != address {
			return nil, types.RpcErrorInvalidParams("Account in tx_json does not match signing key")
		}
	} else {
		// Set the account if not present
		txMap["Account"] = address
	}

	// Fill in missing fields if not offline
	if !request.Offline {
		// Set Fee if not present
		if _, ok := txMap["Fee"]; !ok {
			baseFee, _, _ := types.Services.Ledger.GetCurrentFees()
			txMap["Fee"] = formatUint64AsString(baseFee)
		}

		// Set Sequence if not present
		if _, ok := txMap["Sequence"]; !ok {
			// Get account info to find current sequence
			info, err := types.Services.Ledger.GetAccountInfo(address, "current")
			if err != nil {
				return nil, types.RpcErrorInternal("Failed to get account sequence: " + err.Error())
			}
			txMap["Sequence"] = info.Sequence
		}

		// Set LastLedgerSequence if not present (current + 4)
		if _, ok := txMap["LastLedgerSequence"]; !ok {
			currentLedger := types.Services.Ledger.GetCurrentLedgerIndex()
			txMap["LastLedgerSequence"] = currentLedger + 4
		}
	}

	// Add the signing public key
	txMap["SigningPubKey"] = publicKey

	// Parse the transaction to get a proper Transaction object
	txBytes, err := json.Marshal(txMap)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to marshal transaction: " + err.Error())
	}

	transaction, err := tx.ParseJSON(txBytes)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Failed to parse transaction: " + err.Error())
	}

	// Update the common fields with signing key
	common := transaction.GetCommon()
	common.SigningPubKey = publicKey

	// Sign the transaction
	signature, err := tx.SignTransaction(transaction, privateKey)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to sign transaction: " + err.Error())
	}

	// Add signature to transaction map
	txMap["TxnSignature"] = signature

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

// formatUint64AsString formats a uint64 as a decimal string
func formatUint64AsString(v uint64) string {
	return strconv.FormatUint(v, 10)
}

func (m *SignMethod) RequiredRole() types.Role {
	return types.RoleUser // Signing requires user privileges
}

func (m *SignMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
