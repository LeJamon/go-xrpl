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

	// Autofill NetworkID first so the autofill probe sees the final tx
	// shape. rippled autofillTx() (Simulate.cpp) fills NetworkID before
	// Sequence; the network id only matters at parse time.
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
	// the unified service call is the Go analog.
	_, hasSeq := txJsonMap["Sequence"]
	_, hasFee := txJsonMap["Fee"]
	if !hasSeq || !hasFee {
		_, hasTicket := txJsonMap["TicketSequence"]
		probe, marshalErr := json.Marshal(txJsonMap)
		if marshalErr != nil {
			return nil, types.RpcErrorInternal("Failed to marshal tx_json for autofill")
		}
		seq, fee, autoErr := ctx.Services.Ledger.GetAutofill(accountStr, hasTicket, probe, ctx.Unlimited)
		if autoErr != nil {
			switch {
			case errors.Is(autoErr, svcerr.ErrAccountNotFound):
				return nil, types.RpcErrorSrcActMissing("Source account not found.")
			case errors.Is(autoErr, svcerr.ErrHighFee):
				return nil, types.RpcErrorHighFee(autoErr.Error())
			default:
				return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to autofill tx: %v", autoErr))
			}
		}
		if !hasSeq && !hasTicket {
			txJsonMap["Sequence"] = seq
		}
		if !hasFee {
			txJsonMap["Fee"] = strconv.FormatUint(fee, 10)
		}
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
		} else if result.Metadata.JSON != nil {
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
