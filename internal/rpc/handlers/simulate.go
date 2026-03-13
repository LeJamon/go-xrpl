package handlers

import (
	"encoding/json"
	"strconv"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
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
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
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

	if types.Services == nil || types.Services.Ledger == nil {
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
			// tx_json is not an object
			return nil, types.RpcErrorExpectedField("tx_json", "object")
		}
		if len(txObj) == 0 {
			// Empty tx_json — will fail TransactionType check below
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

	// Validate Account is a valid Base58 address.
	// rippled: getAutofillSequence checks parseBase58<AccountID>(accountStr) and returns
	// rpcSRC_ACT_MALFORMED with message "Invalid field 'tx.Account'."
	accountStr, ok := txJsonMap["Account"].(string)
	if !ok || !types.IsValidXRPLAddress(accountStr) {
		return nil, types.RpcErrorSrcActMalformed("Invalid field 'tx.Account'.")
	}

	// --- Autofill fields (handler-level, no ledger access needed) ---
	// Reference: rippled autofillTx()

	// Autofill SigningPubKey: if not present, set to ""
	if _, ok := txJsonMap["SigningPubKey"]; !ok {
		txJsonMap["SigningPubKey"] = ""
	}

	// Validate and autofill Signers array.
	// rippled checks Signers before TxnSignature.
	if signersRaw, ok := txJsonMap["Signers"]; ok {
		signers, ok := signersRaw.([]interface{})
		if !ok {
			return nil, types.RpcErrorInvalidField("tx.Signers")
		}
		for i, signerEntry := range signers {
			entryObj, ok := signerEntry.(map[string]interface{})
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

			// Autofill SigningPubKey if not present
			if _, ok := signerObj["SigningPubKey"]; !ok {
				signerObj["SigningPubKey"] = ""
			}

			// Autofill TxnSignature if not present; reject if non-empty
			if txnSig, ok := signerObj["TxnSignature"]; !ok {
				signerObj["TxnSignature"] = ""
			} else {
				sigStr, _ := txnSig.(string)
				if sigStr != "" {
					return nil, types.RpcErrorTxSigned()
				}
			}
		}
	}

	// Autofill TxnSignature: if not present, set to "". If present and non-empty, reject.
	if txnSig, ok := txJsonMap["TxnSignature"]; !ok {
		txJsonMap["TxnSignature"] = ""
	} else {
		sigStr, _ := txnSig.(string)
		if sigStr != "" {
			return nil, types.RpcErrorTxSigned()
		}
	}

	// TODO: Autofill Sequence from ledger (service-level) — getAutofillSequence
	// TODO: Autofill Fee from ledger (service-level) — getCurrentNetworkFee
	// TODO: Autofill NetworkID from config (service-level)

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
	result, err := types.Services.Ledger.SimulateTransaction(txJSON)
	if err != nil {
		return nil, types.RpcErrorInternal("Simulation failed: " + err.Error())
	}

	response := map[string]interface{}{
		"engine_result":         result.EngineResult,
		"engine_result_code":    result.EngineResultCode,
		"engine_result_message": result.EngineResultMessage,
		"applied":               result.Applied,
		"ledger_index":          result.CurrentLedger,
	}

	// TODO: Include metadata when SubmitResult is extended with a Metadata field.
	// rippled returns "meta" (JSON) or "meta_blob" (binary) from the simulation result.

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

