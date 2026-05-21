package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/service/svcerr"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// SimulateMethod handles the simulate RPC method.
// Runs a transaction against a snapshot of the open ledger without committing.
// Reference: rippled Simulate.cpp
type SimulateMethod struct{}

func (m *SimulateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Parse raw params into a generic map first so we can check for forbidden fields
	// and validate the `binary` field type before standard unmarshalling.
	var rawParams map[string]json.RawMessage
	if params != nil {
		if err := json.Unmarshal(params, &rawParams); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	} else {
		rawParams = make(map[string]json.RawMessage)
	}

	// Validate `binary` field type if present — must be a boolean.
	// rippled: if context.params.isMember(jss::binary) && !context.params[jss::binary].isBool()
	var binaryOutput bool
	if raw, ok := rawParams["binary"]; ok {
		if err := json.Unmarshal(raw, &binaryOutput); err != nil {
			// Not a boolean — return invalid_field_error matching rippled
			return nil, types.RpcErrorInvalidField("binary")
		}
	}

	// Reject forbidden fields: secret, seed, seed_hex, passphrase.
	// rippled checks these before parsing tx_json/tx_blob.
	for _, field := range []string{"secret", "seed", "seed_hex", "passphrase"} {
		if _, ok := rawParams[field]; ok {
			return nil, types.RpcErrorInvalidField(field)
		}
	}

	// Determine tx source: exactly one of tx_blob or tx_json.
	_, hasTxBlobRaw := rawParams["tx_blob"]
	_, hasTxJsonRaw := rawParams["tx_json"]

	if hasTxBlobRaw && hasTxJsonRaw {
		return nil, types.RpcErrorInvalidParams("Can only include one of `tx_blob` and `tx_json`.")
	}
	if !hasTxBlobRaw && !hasTxJsonRaw {
		return nil, types.RpcErrorInvalidParams("Neither `tx_blob` nor `tx_json` included.")
	}

	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	var txJsonMap map[string]interface{}

	if hasTxBlobRaw {
		// Decode tx_blob string
		var txBlobStr string
		if err := json.Unmarshal(rawParams["tx_blob"], &txBlobStr); err != nil {
			return nil, types.RpcErrorInvalidField("tx_blob")
		}
		if txBlobStr == "" {
			return nil, types.RpcErrorInvalidField("tx_blob")
		}
		decoded, err := binarycodec.Decode(txBlobStr)
		if err != nil {
			return nil, types.RpcErrorInvalidField("tx_blob")
		}
		txJsonMap = decoded
	} else {
		// Parse tx_json object
		var txObj map[string]interface{}
		if err := json.Unmarshal(rawParams["tx_json"], &txObj); err != nil {
			return nil, types.RpcErrorExpectedField("tx_json", "object")
		}
		txJsonMap = txObj
	}

	// Basic sanity checks for transaction shape (matching rippled getTxJsonFromParams).
	if _, ok := txJsonMap["TransactionType"]; !ok {
		return nil, types.RpcErrorMissingField("tx.TransactionType")
	}
	if _, ok := txJsonMap["Account"]; !ok {
		return nil, types.RpcErrorMissingField("tx.Account")
	}

	// Validate Account is a valid Base58 address up front.
	//
	// rippled gates this check inside getAutofillSequence (Simulate.cpp:50-55)
	// and therefore skips it when the caller supplies Sequence. We validate
	// unconditionally because every downstream branch (Fee dispatch through
	// GetAutofill, structural simulate apply) needs the AccountID anyway, so
	// deferring would only delay the same error. The wire response for the
	// "no Sequence + bad Account" case still matches rippled
	// (rpcSRC_ACT_MALFORMED, "Invalid field 'tx.Account'.").
	accountStr, ok := txJsonMap["Account"].(string)
	if !ok || !types.IsValidXRPLAddress(accountStr) {
		return nil, types.RpcErrorSrcActMalformed("Invalid field 'tx.Account'.")
	}

	// Reference: rippled autofillTx() — Simulate.cpp:71-156.

	if _, ok := txJsonMap["SigningPubKey"]; !ok {
		txJsonMap["SigningPubKey"] = ""
	}

	// Structural Signers validation + per-signer field autofill. The
	// signed-payload check (signer.TxnSignature != "") is deferred to the
	// post-autofill block below so rippled's error precedence is preserved:
	// Fee autofill (→ rpcHIGH_FEE) must fire before rpcTX_SIGNED.
	signerObjs, rpcErr := normalizeSigners(txJsonMap)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if _, ok := txJsonMap["TxnSignature"]; !ok {
		txJsonMap["TxnSignature"] = ""
	}

	// Autofill NetworkID (rippled Simulate.cpp:148-153). Order is functionally
	// inert: NetworkID does not affect fee dispatch.
	if _, ok := txJsonMap["NetworkID"]; !ok {
		serverInfo := ctx.Services.Ledger.GetServerInfo()
		if serverInfo.NetworkID > 1024 {
			txJsonMap["NetworkID"] = serverInfo.NetworkID
		}
	}

	// Autofill Sequence + Fee in one service call so they observe a
	// consistent ledger snapshot. rippled splits these (getAutofillSequence
	// and getCurrentNetworkFee in Simulate.cpp / TransactionSign.cpp), but
	// rippled holds one openLedger().current() reference for both reads;
	// the unified service call is the Go analog. This runs *before* the
	// signed-payload checks so rpcHIGH_FEE wins over rpcTX_SIGNED on
	// combined inputs, matching rippled's autofillTx order (Fee → Signers
	// → TxnSignature).
	_, hasSeq := txJsonMap["Sequence"]
	_, hasFee := txJsonMap["Fee"]
	if !hasSeq || !hasFee {
		_, hasTicket := txJsonMap["TicketSequence"]
		probe, marshalErr := json.Marshal(txJsonMap)
		if marshalErr != nil {
			return nil, types.RpcErrorInternal("Failed to marshal tx_json for autofill")
		}
		seq, fee, autoErr := ctx.Services.Ledger.GetAutofill(accountStr, hasTicket, probe)
		if autoErr != nil {
			switch {
			case errors.Is(autoErr, svcerr.ErrAccountNotFound):
				return nil, types.RpcErrorSrcActMissing("Source account not found.")
			case errors.Is(autoErr, svcerr.ErrHighFee):
				msg := strings.TrimPrefix(autoErr.Error(), svcerr.ErrHighFee.Error()+": ")
				return nil, types.RpcErrorHighFee(msg)
			default:
				return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to autofill tx: %v", autoErr))
			}
		}
		// rippled writes Sequence unconditionally (Simulate.cpp:140-146); the
		// value is 0 in the ticket case.
		if !hasSeq {
			txJsonMap["Sequence"] = seq
		}
		if !hasFee {
			txJsonMap["Fee"] = strconv.FormatUint(fee, 10)
		}
	}

	// Deferred signed-payload checks — must follow GetAutofill so rpcHIGH_FEE
	// precedes rpcTX_SIGNED for clients that supply both a non-empty
	// TxnSignature and trigger fee escalation.
	for _, signerObj := range signerObjs {
		if sigStr, _ := signerObj["TxnSignature"].(string); sigStr != "" {
			return nil, types.RpcErrorTxSigned()
		}
	}
	if sigStr, _ := txJsonMap["TxnSignature"].(string); sigStr != "" {
		return nil, types.RpcErrorTxSigned()
	}

	// Reject Batch transaction type.
	// rippled: if (stTx->getTxnType() == ttBATCH) return RPC::make_error(rpcNOT_IMPL)
	if txType, ok := txJsonMap["TransactionType"].(string); ok && txType == "Batch" {
		return nil, types.RpcErrorNotImpl()
	}

	// Marshal tx_json for service call
	txJSON, err := json.Marshal(txJsonMap)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to marshal tx_json")
	}

	// Run the transaction in simulation mode (snapshot, no commit)
	result, err := ctx.Services.Ledger.SimulateTransaction(txJSON)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Simulation failed: %v", err))
	}

	// rippled overrides the tesSUCCESS message for simulate (Simulate.cpp:258-262).
	engineMessage := result.EngineResultMessage
	if result.EngineResult == "tesSUCCESS" {
		engineMessage = "The simulated transaction would have been applied."
	}

	response := map[string]interface{}{
		"engine_result":         result.EngineResult,
		"engine_result_code":    result.EngineResultCode,
		"engine_result_message": engineMessage,
		"applied":               result.Applied,
		"ledger_index":          result.CurrentLedger,
	}

	// rippled emits "meta" (JSON) when binary=false and "meta_blob" (hex)
	// when binary=true. Always emit when Metadata is present, mirroring
	// rippled's `if (result.metadata)` guard (Simulate.cpp:264-276).
	if result.Metadata != nil {
		if binaryOutput {
			response["meta_blob"] = strings.ToUpper(hex.EncodeToString(result.Metadata.Blob))
		} else {
			response["meta"] = result.Metadata.JSON
		}
	}

	if binaryOutput {
		if encoded, err := binarycodec.Encode(txJsonMap); err == nil {
			response["tx_blob"] = encoded
		}
	} else {
		response["tx_json"] = txJsonMap
	}

	return response, nil
}

func (m *SimulateMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *SimulateMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *SimulateMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

// normalizeSigners validates the structural shape of the Signers array and
// autofills missing SigningPubKey / TxnSignature on each entry. The returned
// slice points at the inner Signer maps inside txJsonMap so the caller can
// run signed-payload checks against the same objects after fee autofill.
//
// Mirrors the structural half of rippled's autofillTx Signers loop
// (Simulate.cpp:97-126), excluding the rpcTX_SIGNED branch.
func normalizeSigners(txJsonMap map[string]interface{}) ([]map[string]interface{}, *types.RpcError) {
	signersRaw, ok := txJsonMap["Signers"]
	if !ok {
		return nil, nil
	}
	signers, ok := signersRaw.([]interface{})
	if !ok {
		return nil, types.RpcErrorInvalidField("tx.Signers")
	}
	out := make([]map[string]interface{}, 0, len(signers))
	for i, entry := range signers {
		entryObj, ok := entry.(map[string]interface{})
		if !ok {
			return nil, types.RpcErrorInvalidField("tx.Signers[" + strconv.Itoa(i) + "]")
		}
		signerInner, ok := entryObj["Signer"]
		if !ok {
			return nil, types.RpcErrorInvalidField("tx.Signers[" + strconv.Itoa(i) + "]")
		}
		signerObj, ok := signerInner.(map[string]interface{})
		if !ok {
			return nil, types.RpcErrorInvalidField("tx.Signers[" + strconv.Itoa(i) + "]")
		}
		if _, ok := signerObj["SigningPubKey"]; !ok {
			signerObj["SigningPubKey"] = ""
		}
		if _, ok := signerObj["TxnSignature"]; !ok {
			signerObj["TxnSignature"] = ""
		}
		out = append(out, signerObj)
	}
	return out, nil
}
