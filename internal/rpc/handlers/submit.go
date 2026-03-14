package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// SubmitMethod handles the submit RPC method.
// Supports both tx_blob (pre-signed hex) and tx_json submissions.
type SubmitMethod struct{}

func (m *SubmitMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Parse fee_mult_max / fee_div_max first with proper type validation,
	// matching rippled's checkFee() in TransactionSign.cpp.
	feeOpts, feeErr := parseFeeOptions(params)
	if feeErr != nil {
		return nil, feeErr
	}

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
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if request.TxBlob == "" && len(request.TxJson) == 0 {
		return nil, types.RpcErrorInvalidParams("Either tx_blob or tx_json must be provided")
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	var txJSON []byte
	var txJsonMap map[string]interface{}
	var txBlobHex string

	// Determine if this is a sign-and-submit request (tx_json + credentials)
	hasSigningCreds := request.Secret != "" || request.Seed != "" || request.SeedHex != "" || request.Passphrase != ""

	if request.TxBlob != "" {
		// Decode tx_blob to get tx_json
		decoded, err := binarycodec.Decode(request.TxBlob)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid tx_blob: " + err.Error())
		}
		txJsonMap = decoded
		txBlobHex = request.TxBlob

		// Marshal back to JSON for submission
		txJSON, err = json.Marshal(decoded)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to marshal decoded tx_blob")
		}
	} else if hasSigningCreds {
		// Sign-and-submit path: sign the transaction first, then submit the blob.
		// This matches rippled's behavior in doSubmit() when tx_blob is absent.
		signed, rpcErr := signTransactionJSON(request.TxJson, signCredentials{
			Secret:     request.Secret,
			Seed:       request.Seed,
			SeedHex:    request.SeedHex,
			Passphrase: request.Passphrase,
			KeyType:    request.KeyType,
		}, request.Offline, ctx.ApiVersion, feeOpts)
		if rpcErr != nil {
			return nil, rpcErr
		}

		txJsonMap = signed.TxMap
		txBlobHex = signed.TxBlob

		// Use the signed JSON for submission
		var err error
		txJSON, err = json.Marshal(txJsonMap)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to marshal signed transaction")
		}
	} else {
		// Submit using tx_json directly (no signing)
		txJSON = request.TxJson

		if err := json.Unmarshal(txJSON, &txJsonMap); err != nil {
			txJsonMap = map[string]interface{}{}
		}
	}

	// Ensure we have the tx_blob hex for both submission and hash calculation
	if txBlobHex == "" {
		if encoded, err := binarycodec.Encode(txJsonMap); err == nil {
			txBlobHex = encoded
		}
	}

	// Submit the transaction with the original signed blob.
	// The blob is needed for canonical re-ordering during AcceptLedger.
	result, err := types.Services.Ledger.SubmitTransaction(txJSON, txBlobHex)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to submit transaction: " + err.Error())
	}
	txHashStr := CalculateTxHash(txBlobHex)

	// Store transaction for later lookup if applied
	if result.Applied && txHashStr != "" {
		if txHashBytes, err := hex.DecodeString(txHashStr); err == nil && len(txHashBytes) == 32 {
			var txHash [32]byte
			copy(txHash[:], txHashBytes)
			storedTx := StoredTransaction{
				TxJSON: txJsonMap,
				Meta: map[string]interface{}{
					"TransactionResult": result.EngineResult,
					"TransactionIndex":  0,
				},
			}
			storedData, _ := json.Marshal(storedTx)
			_ = types.Services.Ledger.StoreTransaction(txHash, storedData)
		}
	}

	// Inject DeliverMax for Payment transactions, matching rippled's
	// RPC::insertDeliverMax behavior in TransactionSign.cpp.
	injectDeliverMax(txJsonMap, ctx.ApiVersion)

	// For API v2+: add hash at root level of response, matching
	// transactionFormatResultImpl in TransactionSign.cpp.
	// For API v1: hash goes inside tx_json only.
	if txHashStr != "" {
		txJsonMap["hash"] = txHashStr
	}

	// Get current fees for response
	baseFee, _, _ := types.Services.Ledger.GetCurrentFees()

	// Build response with independent boolean fields matching rippled's
	// Transaction::SubmitResult struct. "accepted" = any() in rippled.
	response := map[string]interface{}{
		"engine_result":         result.EngineResult,
		"engine_result_code":    result.EngineResultCode,
		"engine_result_message": result.EngineResultMessage,
		"tx_json":               txJsonMap,
		"tx_blob":               txBlobHex,
		"accepted":              result.Accepted(),
		"applied":               result.Applied,
		"broadcast":             result.Broadcast,
		"kept":                  result.Kept,
		"queued":                result.Queued,
		"open_ledger_cost":      fmt.Sprintf("%d", baseFee),
	}

	// API v2+: add hash at the root level of the response
	if ctx.ApiVersion > 1 && txHashStr != "" {
		response["hash"] = txHashStr
	}

	// Add validated_ledger_index only if we have one
	if result.ValidatedLedger > 0 {
		response["validated_ledger_index"] = result.ValidatedLedger
	}

	// Add account_sequence_next and account_sequence_available
	if account, ok := txJsonMap["Account"].(string); ok {
		if acctInfo, err := types.Services.Ledger.GetAccountInfo(account, "current"); err == nil {
			response["account_sequence_next"] = acctInfo.Sequence
			response["account_sequence_available"] = acctInfo.Sequence
		}
	}

	// Add deprecated warning when sign-and-submit credentials are used
	if request.Secret != "" || request.Seed != "" || request.SeedHex != "" || request.Passphrase != "" {
		response["deprecated"] = "Signing support in the 'submit' command has been deprecated and will be removed in a future version of the server. Please migrate to a standalone signing tool."
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
	hash := common.Sha512Half(data)
	return strings.ToUpper(hex.EncodeToString(hash[:]))
}

func (m *SubmitMethod) RequiredRole() types.Role {
	return types.RoleUser // Transaction submission requires user privileges
}

func (m *SubmitMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *SubmitMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
