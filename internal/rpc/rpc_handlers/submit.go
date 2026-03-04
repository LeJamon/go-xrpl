package rpc_handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/internal/codec/binary-codec"
	crypto "github.com/LeJamon/goXRPLd/internal/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// SubmitMethod handles the submit RPC method.
// PARTIAL: Works for pre-signed tx_json submissions. Missing:
//
// TODO [submit]: Support tx_blob submission.
//   - Decode hex blob via binarycodec.Decode(txBlob) → tx_json map
//   - Then submit as normal. Reference: rippled Submit.cpp transactionSubmit()
//
// TODO [submit]: Support server-side signing (secret/seed/passphrase params).
//   - When signing credentials are provided with tx_json:
//     1. Derive keypair from secret/seed/seed_hex/passphrase + key_type
//     2. Auto-fill missing fields: Sequence (from account), Fee (from fee escalation)
//     3. Sign the transaction using internal/crypto
//     4. Submit the signed transaction
//   - Reference: rippled Submit.cpp transactionSign()
//   - Note: This is the "sign-and-submit" mode used by rippled CLI
type SubmitMethod struct{}

func (m *SubmitMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	var request struct {
		TxBlob     string          `json:"tx_blob,omitempty"`
		TxJson     json.RawMessage `json:"tx_json,omitempty"`
		Secret     string          `json:"secret,omitempty"`
		Seed       string          `json:"seed,omitempty"`
		SeedHex    string          `json:"seed_hex,omitempty"`
		Passphrase string          `json:"passphrase,omitempty"`
		KeyType    string          `json:"key_type,omitempty"`
		FailHard   bool            `json:"fail_hard,omitempty"`
		Offline    bool            `json:"offline,omitempty"`
		BuildPath  bool            `json:"build_path,omitempty"`
		FeeMultMax uint32          `json:"fee_mult_max,omitempty"`
		FeeDivMax  uint32          `json:"fee_div_max,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, rpc_types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if request.TxBlob == "" && len(request.TxJson) == 0 {
		return nil, rpc_types.RpcErrorInvalidParams("Either tx_blob or tx_json must be provided")
	}

	// Check if ledger service is available
	if rpc_types.Services == nil || rpc_types.Services.Ledger == nil {
		return nil, rpc_types.RpcErrorInternal("Ledger service not available")
	}

	var txJSON []byte

	if len(request.TxJson) > 0 {
		// Submit using tx_json
		txJSON = request.TxJson

		// TODO [submit]: sign-and-submit when credentials provided (see type-level TODO)
		if request.Secret != "" || request.Seed != "" || request.SeedHex != "" || request.Passphrase != "" {
			// For now, just use the tx_json as-is (assumes pre-signed)
		}
	} else {
		// TODO [submit]: decode tx_blob via binarycodec.Decode() (see type-level TODO)
		return nil, rpc_types.RpcErrorInvalidParams("tx_blob submission not yet implemented, use tx_json instead")
	}

	// Parse tx_json to include in response and calculate hash
	var txJsonMap map[string]interface{}
	if err := json.Unmarshal(txJSON, &txJsonMap); err != nil {
		txJsonMap = map[string]interface{}{}
	}

	// Submit the transaction
	result, err := rpc_types.Services.Ledger.SubmitTransaction(txJSON)
	if err != nil {
		return nil, rpc_types.RpcErrorInternal("Failed to submit transaction: " + err.Error())
	}

	// If transaction was applied, store it for later lookup
	var txHashStr string
	if result.Applied {
		// Encode transaction to get binary for hash calculation
		txBlob, encErr := binarycodec.Encode(txJsonMap)
		if encErr == nil {
			txHashStr = CalculateTxHash(txBlob)

			// Parse the hash back to bytes
			if txHashBytes, err := hex.DecodeString(txHashStr); err == nil && len(txHashBytes) == 32 {
				var txHash [32]byte
				copy(txHash[:], txHashBytes)

				// Store the transaction with metadata
				storedTx := StoredTransaction{
					TxJSON: txJsonMap,
					Meta: map[string]interface{}{
						"TransactionResult": result.EngineResult,
						"TransactionIndex":  0,
					},
				}
				storedData, _ := json.Marshal(storedTx)
				_ = rpc_types.Services.Ledger.StoreTransaction(txHash, storedData)
			}
		}
	}

	// Get current fees for response
	baseFee, _, _ := rpc_types.Services.Ledger.GetCurrentFees()

	response := map[string]interface{}{
		"engine_result":          result.EngineResult,
		"engine_result_code":     result.EngineResultCode,
		"engine_result_message":  result.EngineResultMessage,
		"tx_json":                txJsonMap,
		"accepted":               result.Applied,
		"applied":                result.Applied,
		"broadcast":              result.Applied, // In standalone mode, no broadcast
		"kept":                   result.Applied,
		"queued":                 false,
		"open_ledger_cost":       baseFee,
		"validated_ledger_index": result.ValidatedLedger,
		"current_ledger_index":   result.CurrentLedger,
	}

	// Add hash if we calculated it
	if txHashStr != "" {
		response["tx_hash"] = txHashStr
		txJsonMap["hash"] = txHashStr
	}

	// Include result-specific status based on engine result
	if result.EngineResultCode == 0 { // tesSUCCESS
		response["status"] = "success"
	} else if result.EngineResultCode >= 100 && result.EngineResultCode < 200 { // tec codes
		response["status"] = "success" // tec codes are still "successful" submissions
	} else {
		response["status"] = "error"
	}

	return response, nil
}

// CalculateTxHash calculates the hash of a signed transaction
func CalculateTxHash(txBlobHex string) string {
	// The transaction hash is SHA512Half of prefix + transaction blob
	// Prefix is "TXN\x00" = 0x54584E00
	prefix := []byte{0x54, 0x58, 0x4E, 0x00}

	txBytes, err := hex.DecodeString(txBlobHex)
	if err != nil {
		return ""
	}

	data := append(prefix, txBytes...)
	hash := crypto.Sha512Half(data)
	return strings.ToUpper(hex.EncodeToString(hash[:]))
}

func (m *SubmitMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleUser // Transaction submission requires user privileges
}

func (m *SubmitMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
