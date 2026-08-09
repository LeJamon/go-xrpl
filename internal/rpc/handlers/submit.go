package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/rpc/txprojection"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
)

// SubmitMethod handles the submit RPC method.
// Supports both tx_blob (pre-signed hex) and tx_json submissions.
type SubmitMethod struct{ baseHandler }

func (m *SubmitMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (result any, rpcErr *rpcerrors.RpcError) {
	setLoadMedium(ctx)
	rawParams, err := decodeSubmitParams(params)
	if err != nil {
		return nil, err
	}

	_, hasTxBlob := rawParams["tx_blob"]

	var txJSON []byte
	var txJsonMap map[string]any
	var txBlobHex string
	var failHard bool
	projectionPath := txprojection.PathSigned

	if hasTxBlob {
		projectionPath = txprojection.PathCanonical
		blobHex, ok := submitJSONString(rawParams["tx_blob"])
		if !ok || blobHex == "" {
			return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
		rawBlob, decodeErr := hex.DecodeString(blobHex)
		if decodeErr != nil || len(rawBlob) == 0 {
			return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}

		parsed, txParseErr := tx.ParseFromBinary(rawBlob)
		if txParseErr != nil {
			return nil, rpcerrors.RpcErrorInvalidTransaction(txParseErr.Error())
		}
		canonical, canonicalErr := binarycodec.DecodeBytes(rawBlob)
		if canonicalErr != nil {
			return nil, rpcerrors.RpcErrorInvalidTransaction(canonicalErr.Error())
		}
		canonicalBlobHex, canonicalEncodeErr := binarycodec.Encode(canonical)
		if canonicalEncodeErr != nil {
			return nil, rpcerrors.RpcErrorInvalidTransaction(canonicalEncodeErr.Error())
		}
		canonicalBlob, canonicalDecodeErr := hex.DecodeString(canonicalBlobHex)
		if canonicalDecodeErr != nil {
			return nil, rpcInternalError("submit: canonical transaction decoding failed", canonicalDecodeErr)
		}
		parsed.SetRawBytes(canonicalBlob)

		signatureReason := ""
		signatureChecked := false
		checkSigs := ctx.Services == nil || ctx.Services.Ledger() == nil || !ctx.Services.Ledger().IsStandalone()
		if ctx.Services != nil && ctx.Services.Ledger() != nil {
			if rulesSource, ok := ctx.Services.Ledger().(types.TransactionRulesSource); ok {
				signatureReason = sign.CheckSTTxSignature(parsed, rulesSource.TransactionRules(), checkSigs)
				signatureChecked = true
			}
		}
		if !signatureChecked {
			signatureReason = sign.CheckSTTxSignature(parsed, nil, checkSigs)
		}
		if signatureReason != "" {
			return nil, rpcerrors.RpcErrorInvalidTransaction("fails local checks: " + signatureReason)
		}
		if reason := tx.TransactionLocalChecksFailureReason(parsed); reason != "" {
			return nil, rpcerrors.RpcErrorInvalidTransaction("fails local checks: " + reason)
		}
		var parseErr *rpcerrors.RpcError
		failHard, parseErr = parseSubmitFailHard(rawParams["fail_hard"])
		if parseErr != nil {
			return nil, parseErr
		}
		txJsonMap = canonical
		txBlobHex = strings.ToUpper(canonicalBlobHex)
		if marshaled, marshalErr := json.Marshal(txJsonMap); marshalErr != nil {
			return nil, rpcInternalError("submit: decoded transaction marshaling failed", marshalErr)
		} else {
			txJSON = marshaled
		}
	} else {
		var parseErr *rpcerrors.RpcError
		failHard, parseErr = parseSubmitFailHard(rawParams["fail_hard"])
		if parseErr != nil {
			return nil, parseErr
		}
		if gateErr := rejectDisabledSigning(ctx); gateErr != nil {
			return nil, gateErr
		}
		defer func() {
			result, rpcErr = addDeprecation(result, rpcErr, submitSigningDeprecation)
		}()

		var request struct{ signingRequest }
		requestParams := params
		if len(requestParams) == 0 || bytes.Equal(bytes.TrimSpace(requestParams), []byte("null")) {
			requestParams = json.RawMessage(`{}`)
		}
		if unmarshalErr := json.Unmarshal(requestParams, &request); unmarshalErr != nil {
			return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", unmarshalErr))
		}

		signed, signErr := signTransactionJSON(
			ctx,
			request.TxJson,
			request.signCredentials,
			request.Offline,
			params,
			request.SignatureTarget,
		)
		if signErr != nil {
			return nil, signErr
		}
		txJsonMap = signed.TxMap
		txBlobHex = strings.ToUpper(signed.TxBlob)
		if marshaled, marshalErr := json.Marshal(txJsonMap); marshalErr != nil {
			return nil, rpcInternalError("submit: signed transaction marshaling failed", marshalErr)
		} else {
			txJSON = marshaled
		}
	}

	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Submit the transaction with the original signed blob.
	// The blob is needed for canonical re-ordering during AcceptLedger.
	// When the client passed fail_hard:true and the ledger service
	// implements the FailHardSubmitter surface, route through it so
	// non-applying submissions are not held or relayed.
	submitResult, submitErr := submitWithFailHard(ctx.Services.LedgerMutation(), txJSON, txBlobHex, failHard)
	if submitErr != nil {
		return nil, rpcTransactionSubmissionError("submit: transaction submission failed", submitErr)
	}
	txHashStr := CalculateTxHash(txBlobHex)

	projectedTxJSON := txprojection.ProjectJSONForPath(
		txJsonMap,
		txHashStr,
		ctx.ApiVersion,
		projectionPath,
	)

	// Build response with independent boolean fields matching rippled's
	// Transaction::SubmitResult struct. "accepted" = any() in rippled.
	response := map[string]any{
		"engine_result":         submitResult.EngineResult,
		"engine_result_code":    submitResult.EngineResultCode,
		"engine_result_message": submitResult.EngineResultMessage,
		"tx_json":               projectedTxJSON,
		"tx_blob":               txBlobHex,
		"accepted":              submitResult.Accepted(),
		"applied":               submitResult.Applied,
		"broadcast":             submitResult.Broadcast,
		"kept":                  submitResult.Kept,
		"queued":                submitResult.Queued,
	}

	// Signed paths use the modern root hash for API v2+. Raw-blob submit keeps
	// its legacy nested hash shape on every API version.
	if projectionPath != txprojection.PathCanonical && ctx.ApiVersion > 1 && txHashStr != "" {
		response["hash"] = txHashStr
	}

	if state := submitResult.CurrentLedgerState; state != nil {
		response["account_sequence_next"] = state.AccountSequenceNext
		response["account_sequence_available"] = state.AccountSequenceAvailable
		response["open_ledger_cost"] = fmt.Sprintf("%d", state.OpenLedgerCost)
		response["validated_ledger_index"] = state.ValidatedLedgerIndex
	}

	return response, nil
}

func decodeSubmitParams(params json.RawMessage) (map[string]json.RawMessage, *rpcerrors.RpcError) {
	if len(params) == 0 || bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		return map[string]json.RawMessage{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil || raw == nil {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	return raw, nil
}

func parseSubmitFailHard(raw json.RawMessage) (bool, *rpcerrors.RpcError) {
	if len(raw) == 0 {
		return false, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("true")) {
		return true, nil
	}
	if bytes.Equal(trimmed, []byte("false")) {
		return false, nil
	}
	return false, rpcerrors.RpcErrorExpectedField("fail_hard", "boolean")
}

func submitJSONString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// CalculateTxHash calculates the hash of a signed transaction
func CalculateTxHash(txBlobHex string) string {
	// The transaction hash is SHA512Half of prefix + transaction blob
	// Prefix is "TXN\x00" = 0x54584E00
	prefix := []byte{0x54, 0x58, 0x4E, 0x00} //nolint:prealloc // prealloc: static 4-byte composite literal followed by a single append

	txBytes, err := hex.DecodeString(txBlobHex)
	if err != nil {
		return ""
	}

	data := append(prefix, txBytes...)
	hash := sha512half.Sum(data)
	return strings.ToUpper(hex.EncodeToString(hash[:]))
}

func (m *SubmitMethod) RequiredRole() types.Role {
	return types.RoleUser // Transaction submission requires user privileges
}

func (m *SubmitMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
