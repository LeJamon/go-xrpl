package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	binarycodecdefs "github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// SubmitMultisignedMethod handles the submit_multisigned RPC method
// This submits a multi-signed transaction to the network
type SubmitMultisignedMethod struct{ BaseHandler }

func (m *SubmitMultisignedMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	setLoadHeavy(ctx)
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

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Parse the transaction JSON
	var txMap map[string]any
	decoder := json.NewDecoder(bytes.NewReader(request.TxJson))
	decoder.UseNumber()
	if err := decoder.Decode(&txMap); err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid tx_json: %v", err))
	}

	// --- checkMultiSignFields (rippled TransactionSign.cpp:1032-1057) ---

	// Sequence must be present.
	// Matches rippled: missing_field_error("tx_json.Sequence")
	if _, ok := txMap["Sequence"]; !ok {
		return nil, types.RpcErrorMissingField("tx_json.Sequence")
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

	if rpcErr := validateSigningTxJSONShape(txMap); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := rejectSigningWhenLoaded(ctx.Services, ctx.Unlimited); rpcErr != nil {
		return nil, rpcErr
	}

	// Get the source account address for self-signing detection later.
	txAccount := txMap["Account"].(string)

	// The source account must exist in the current ledger
	// (TransactionSign.cpp:1259-1270 → rpcSRC_ACT_NOT_FOUND). Signer-list
	// existence, signer weights, and quorum are deliberately not checked
	// here: rippled leaves them to the engine's checkMultiSign
	// (tefNOT_MULTI_SIGNING / tefBAD_SIGNATURE / tefBAD_QUORUM).
	if _, err := ctx.Services.Ledger.GetAccountInfo(ctx.Context, txAccount, "current"); err != nil {
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RpcErrorSrcActNotFound("Source account not found.")
		}
		return nil, rpcInternalError("submit_multisigned: source account lookup failed", err)
	}

	if rpcErr := validateSignForPreConflict(txMap, params); rpcErr != nil {
		return nil, rpcErr
	}

	definitions := binarycodecdefs.Get()
	if parseMessage := serializedFieldParseMessage(txMap, "tx_json", definitions); parseMessage != "" {
		return nil, types.RpcErrorInvalidParams(parseMessage)
	}
	transactionTypeCode, ok := txMap["TransactionType"].(uint16)
	if !ok {
		return nil, types.RpcErrorInvalidParams("Field 'tx_json.TransactionType' has invalid data.")
	}
	transactionTypeName, err := definitions.TransactionTypeName(int32(transactionTypeCode))
	if err != nil {
		return nil, types.RpcErrorInvalidTransactionType(transactionTypeCode)
	}
	txMap["TransactionType"] = transactionTypeName
	transactionType, _ := tx.TypeFromName(transactionTypeName)
	if err := tx.ValidateTemplateFields(transactionType, txMap); err != nil {
		return nil, types.RpcErrorInvalidParams(err.Error())
	}
	if reason := tx.TransactionMapLocalChecksFailureReason(transactionType, txMap); reason != "" {
		return nil, types.RpcErrorInvalidParams(reason)
	}
	if _, ok := txMap["Fee"].(string); !ok {
		return nil, types.RpcErrorInvalidParams("Invalid Fee field.  Fees must be specified in XRP.")
	}
	_, rpcErr := parseTransactionForSigning(txMap)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// TxnSignature must NOT be present on a multi-signed transaction.
	// Matches rippled: rpcError(rpcSIGNING_MALFORMED) -> code 63, "signingMalformed"
	if _, ok := txMap["TxnSignature"]; ok {
		return nil, types.RpcErrorSigningMalformed()
	}

	// Matches rippled: "Invalid Fee field.  Fees must be specified in XRP." /
	// "Invalid Fee field.  Fees must be greater than zero."
	feeVal := txMap["Fee"]
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
	signersValue, signersPresent := txMap["Signers"]
	if !signersPresent {
		return nil, types.RpcErrorMissingField("tx_json.Signers")
	}
	signers, ok := signersValue.([]any)
	if !ok || len(signers) == 0 {
		return nil, types.RpcErrorInvalidParams("tx_json.Signers array may not be empty.")
	}

	signerMapList, rpcErr := signerMaps(signers)
	if rpcErr != nil {
		return nil, rpcErr
	}
	feePayer := txAccount
	if delegate, ok := txMap["Delegate"].(string); ok {
		feePayer = delegate
	}
	if rpcErr := sortAndValidateSignerMaps(signerMapList, feePayer); rpcErr != nil {
		return nil, rpcErr
	}
	for i := range signerMapList {
		signers[i] = signerMapList[i]
	}

	// Encode the transaction to binary
	txBlob, encErr := binarycodec.Encode(txMap)
	if encErr != nil {
		return nil, rpcInternalError("submit_multisigned: transaction encoding failed", encErr)
	}

	// Calculate transaction hash
	txHash := CalculateTxHash(txBlob)

	// Submit the transaction
	txJSON, encErr := json.Marshal(txMap)
	if encErr != nil {
		return nil, rpcInternalError("submit_multisigned: transaction marshaling failed", encErr)
	}

	// Route fail_hard submissions through the optional surface so they
	// are not held or relayed on non-apply. Mirrors rippled
	// NetworkOPs.cpp:1685-1689 (`!enforceFailHard`).
	result, submitErr := submitWithFailHard(ctx.Services.Ledger, txJSON, txBlob, request.FailHard)
	if submitErr != nil {
		return nil, rpcTransactionSubmissionError("submit_multisigned: transaction submission failed", submitErr)
	}

	txMap["hash"] = txHash

	response := map[string]any{
		"engine_result":         result.EngineResult,
		"engine_result_code":    result.EngineResultCode,
		"engine_result_message": result.EngineResultMessage,
		"tx_blob":               txBlob,
		"tx_json":               txMap,
	}

	if result.Applied {
		response["applied"] = result.Applied
	}

	return response, nil
}

func (m *SubmitMultisignedMethod) RequiredRole() types.Role {
	return types.RoleUser
}

func (m *SubmitMultisignedMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
