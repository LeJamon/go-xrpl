package handlers

import (
	"encoding/json"
	"strconv"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// SubmitMultisignedMethod handles the submit_multisigned RPC method
// This submits a multi-signed transaction to the network
type SubmitMultisignedMethod struct{}

func (m *SubmitMultisignedMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		TxJson   json.RawMessage `json:"tx_json"`
		FailHard bool            `json:"fail_hard,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if len(request.TxJson) == 0 {
		return nil, types.RpcErrorMissingField("tx_json")
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Parse the transaction JSON
	var txMap map[string]interface{}
	if err := json.Unmarshal(request.TxJson, &txMap); err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid tx_json: " + err.Error())
	}

	// --- checkMultiSignFields (rippled TransactionSign.cpp:1032-1057) ---

	// Sequence must be present.
	// Matches rippled: missing_field_error("tx_json.Sequence")
	if _, ok := txMap["Sequence"]; !ok {
		return nil, types.RpcErrorMissingField("tx_json.Sequence")
	}

	// Validate that Sequence is a valid number (JSON numbers unmarshal as float64).
	switch seq := txMap["Sequence"].(type) {
	case float64:
		if seq < 0 || seq != float64(int64(seq)) {
			return nil, types.RpcErrorInvalidField("tx_json.Sequence")
		}
	case json.Number:
		if _, err := seq.Int64(); err != nil {
			return nil, types.RpcErrorInvalidField("tx_json.Sequence")
		}
	default:
		return nil, types.RpcErrorInvalidField("tx_json.Sequence")
	}

	// SigningPubKey must be present and empty.
	// Matches rippled: missing_field_error("tx_json.SigningPubKey") /
	// "When multi-signing 'tx_json.SigningPubKey' must be empty."
	signingPubKey, spkPresent := txMap["SigningPubKey"]
	if !spkPresent {
		return nil, types.RpcErrorMissingField("tx_json.SigningPubKey")
	}
	if spkStr, ok := signingPubKey.(string); !ok || spkStr != "" {
		return nil, types.RpcErrorInvalidParams("When multi-signing 'tx_json.SigningPubKey' must be empty.")
	}

	// --- checkTxJsonFields (rippled TransactionSign.cpp:315-375) ---

	// Validate required fields for multi-signed transaction
	if _, ok := txMap["Account"]; !ok {
		return nil, types.RpcErrorMissingField("tx_json.Account")
	}

	// Get the source account address for self-signing detection later.
	txAccount, _ := txMap["Account"].(string)

	// --- Post-serialization validation (rippled TransactionSign.cpp:1325-1391) ---

	// TxnSignature must NOT be present on a multi-signed transaction.
	// Matches rippled: rpcError(rpcSIGNING_MALFORMED) -> code 63, "signingMalformed"
	if _, ok := txMap["TxnSignature"]; ok {
		return nil, types.RpcErrorSigningMalformed()
	}

	// Fee must be present, must be XRP drops (string of digits), and must be > 0.
	// Matches rippled: "Invalid Fee field.  Fees must be specified in XRP." /
	// "Invalid Fee field.  Fees must be greater than zero."
	feeVal, feePresent := txMap["Fee"]
	if !feePresent {
		return nil, types.RpcErrorInvalidParams("Invalid Fee field.  Fees must be specified in XRP.")
	}
	feeStr, ok := feeVal.(string)
	if !ok {
		return nil, types.RpcErrorInvalidParams("Invalid Fee field.  Fees must be specified in XRP.")
	}
	feeDrops, err := strconv.ParseInt(feeStr, 10, 64)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid Fee field.  Fees must be specified in XRP.")
	}
	if feeDrops <= 0 {
		return nil, types.RpcErrorInvalidParams("Invalid Fee field.  Fees must be greater than zero.")
	}

	// Check that Signers array exists and is not empty
	signers, ok := txMap["Signers"].([]interface{})
	if !ok || len(signers) == 0 {
		return nil, types.RpcErrorInvalidParams("tx_json.Signers array may not be empty.")
	}

	// Validate signer entries and collect accounts for duplicate/self-sign checks
	seenAccounts := make(map[string]bool, len(signers))
	var prevAccount string
	for i, signerEntry := range signers {
		signerWrapper, ok := signerEntry.(map[string]interface{})
		if !ok {
			return nil, types.RpcErrorInvalidParams("Signers array may only contain Signer entries.")
		}

		signer, ok := signerWrapper["Signer"].(map[string]interface{})
		if !ok {
			return nil, types.RpcErrorInvalidParams("Signers array may only contain Signer entries.")
		}

		// Check required signer fields
		account, ok := signer["Account"].(string)
		if !ok || account == "" {
			return nil, types.RpcErrorInvalidParams("Signer entry missing Account")
		}

		if _, ok := signer["SigningPubKey"].(string); !ok {
			return nil, types.RpcErrorInvalidParams("Signer entry missing SigningPubKey")
		}

		if _, ok := signer["TxnSignature"].(string); !ok {
			return nil, types.RpcErrorInvalidParams("Signer entry missing TxnSignature")
		}

		// Check signers are sorted by account (XRPL protocol requirement)
		if i > 0 && account < prevAccount {
			return nil, types.RpcErrorInvalidParams("Signers must be sorted by Account")
		}

		// Duplicate signer detection.
		// Matches rippled sortAndValidateSigners: "Duplicate Signers:Signer:Account entries (<addr>) are not allowed."
		if seenAccounts[account] {
			return nil, types.RpcErrorInvalidParams(
				"Duplicate Signers:Signer:Account entries (" + account + ") are not allowed.")
		}
		seenAccounts[account] = true

		// Self-signing detection: a signer may not be the transaction's Account.
		// Matches rippled sortAndValidateSigners: "A Signer may not be the transaction's Account (<addr>)."
		if account == txAccount {
			return nil, types.RpcErrorInvalidParams(
				"A Signer may not be the transaction's Account (" + txAccount + ").")
		}

		prevAccount = account
	}

	// TODO: Validate source account exists in ledger (needs service layer - rpcSRC_ACT_NOT_FOUND)
	// TODO: Validate signer list exists on source account (needs service layer)
	// TODO: Validate quorum threshold is met (needs service layer)
	// TODO: Validate signer weights against quorum (needs service layer)

	// Encode the transaction to binary
	txBlob, encErr := binarycodec.Encode(txMap)
	if encErr != nil {
		return nil, types.RpcErrorInternal("Failed to encode transaction: " + encErr.Error())
	}

	// Calculate transaction hash
	txHash := CalculateTxHash(txBlob)

	// Submit the transaction
	txJSON, encErr := json.Marshal(txMap)
	if encErr != nil {
		return nil, types.RpcErrorInternal("Failed to marshal transaction: " + encErr.Error())
	}

	result, submitErr := types.Services.Ledger.SubmitTransaction(txJSON)
	if submitErr != nil {
		// Return submission error with details
		return nil, types.RpcErrorInternal("Transaction submission failed: " + submitErr.Error())
	}

	// Add hash to response tx_json
	txMap["hash"] = txHash

	// Build response
	response := map[string]interface{}{
		"engine_result":         result.EngineResult,
		"engine_result_code":    result.EngineResultCode,
		"engine_result_message": result.EngineResultMessage,
		"tx_blob":               txBlob,
		"tx_json":               txMap,
	}

	// Add applied status from result
	if result.Applied {
		response["applied"] = result.Applied
	}

	return response, nil
}

func (m *SubmitMultisignedMethod) RequiredRole() types.Role {
	return types.RoleUser
}

func (m *SubmitMultisignedMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *SubmitMultisignedMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
