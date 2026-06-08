package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
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

	if err := RequireLedgerService(ctx.Services); err != nil {
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
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid tx_blob: %v", err))
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
		signed, rpcErr := signTransactionJSON(ctx.Context, ctx.Services, request.TxJson, signCredentials{
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
	// When the client passed fail_hard:true and the ledger service
	// implements the FailHardSubmitter surface, route through it so
	// non-applying submissions are not held or relayed.
	var (
		result    *types.SubmitResult
		submitErr error
	)
	if request.FailHard {
		if fh, ok := ctx.Services.Ledger.(types.FailHardSubmitter); ok {
			result, submitErr = fh.SubmitTransactionFailHard(txJSON, txBlobHex)
		} else {
			result, submitErr = ctx.Services.Ledger.SubmitTransaction(txJSON, txBlobHex)
		}
	} else {
		result, submitErr = ctx.Services.Ledger.SubmitTransaction(txJSON, txBlobHex)
	}
	if submitErr != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to submit transaction: %v", submitErr))
	}
	hash, _ := protocol.ComputeTxHashString(txBlobHex)
	txHashStr := hash.Hex()

	// Store transaction for later lookup if applied. The submit response is
	// still successful even when persistence fails — the tx is already in the
	// open ledger and will be re-applied on close — but we log so silent
	// storage failures don't go unnoticed.
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
			storedData, mErr := json.Marshal(storedTx)
			if mErr != nil {
				xrpllog.Named(xrpllog.PartitionRPC).Warn("submit: marshal stored tx failed", "hash", txHashStr, "err", mErr)
			} else if sErr := ctx.Services.Ledger.StoreTransaction(txHash, storedData); sErr != nil {
				xrpllog.Named(xrpllog.PartitionRPC).Warn("submit: StoreTransaction failed", "hash", txHashStr, "err", sErr)
			}
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

	baseFee, _, _ := ctx.Services.Ledger.GetCurrentFees()

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
		if acctInfo, err := ctx.Services.Ledger.GetAccountInfo(ctx.Context, account, "current"); err == nil {
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

func (m *SubmitMethod) RequiredRole() types.Role {
	return types.RoleUser // Transaction submission requires user privileges
}

func (m *SubmitMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *SubmitMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
