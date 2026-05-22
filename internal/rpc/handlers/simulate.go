package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	binarycodecdefs "github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/internal/ledger/service/svcerr"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/internal/tx"
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

	// rippled autofillTx() — Simulate.cpp:71-156. Steps run in the same
	// order so rippled's error precedence is preserved:
	//   1. Fee        (→ rpcHIGH_FEE)
	//   2. SigningPubKey
	//   3. Signers loop with inline signed-check (→ rpcTX_SIGNED)
	//   4. TxnSignature with signed-check (→ rpcTX_SIGNED)
	//   5. Sequence   (→ rpcSRC_ACT_NOT_FOUND)
	//   6. NetworkID

	// 1. Fee — rippled Simulate.cpp:74-89.
	if _, hasFee := txJsonMap["Fee"]; !hasFee {
		probe, marshalErr := json.Marshal(txJsonMap)
		if marshalErr != nil {
			return nil, types.RpcErrorInternal("Failed to marshal tx_json for fee autofill")
		}
		fee, feeErr := ctx.Services.Ledger.GetAutofillFee(probe)
		if feeErr != nil {
			var hfe *svcerr.HighFeeError
			if errors.As(feeErr, &hfe) {
				return nil, types.RpcErrorHighFee(hfe.Error())
			}
			return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to autofill fee: %v", feeErr))
		}
		txJsonMap["Fee"] = strconv.FormatUint(fee, 10)
	}

	// 2. SigningPubKey — rippled Simulate.cpp:91-95.
	if _, ok := txJsonMap["SigningPubKey"]; !ok {
		txJsonMap["SigningPubKey"] = ""
	}

	// 3. Signers — rippled Simulate.cpp:97-127. Structural check, autofill,
	// and signed-check happen per-iteration so an earlier signer's signed
	// TxnSignature fires before a later signer's structural error.
	if rpcErr := processSigners(txJsonMap); rpcErr != nil {
		return nil, rpcErr
	}

	// 4. TxnSignature — rippled Simulate.cpp:129-138.
	if txnSig, ok := txJsonMap["TxnSignature"]; !ok {
		txJsonMap["TxnSignature"] = ""
	} else if sigStr, _ := txnSig.(string); sigStr != "" {
		return nil, types.RpcErrorTxSigned()
	}

	// 5. Sequence — rippled Simulate.cpp:140-146. Account format is checked
	// inside GetAutofillSequence (mirrors rippled getAutofillSequence,
	// Simulate.cpp:43-55), so the txSigned and highFee precedence ahead
	// of srcActMalformed/NotFound is preserved.
	if _, hasSeq := txJsonMap["Sequence"]; !hasSeq {
		accountStr, ok := txJsonMap["Account"].(string)
		if !ok {
			return nil, types.RpcErrorInvalidField("tx.Account")
		}
		_, hasTicket := txJsonMap["TicketSequence"]
		seq, seqErr := ctx.Services.Ledger.GetAutofillSequence(accountStr, hasTicket)
		if seqErr != nil {
			switch {
			case errors.Is(seqErr, svcerr.ErrAccountMalformed):
				return nil, types.RpcErrorSrcActMalformed("Invalid field 'tx.Account'.")
			case errors.Is(seqErr, svcerr.ErrAccountNotFound):
				return nil, types.RpcErrorSrcActNotFound("Source account not found.")
			default:
				return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to autofill sequence: %v", seqErr))
			}
		}
		txJsonMap["Sequence"] = seq
	}

	// 6. NetworkID — rippled Simulate.cpp:148-153.
	if _, ok := txJsonMap["NetworkID"]; !ok {
		serverInfo := ctx.Services.Ledger.GetServerInfo()
		if serverInfo.NetworkID > 1024 {
			txJsonMap["NetworkID"] = serverInfo.NetworkID
		}
	}

	// Normalize caller-supplied numeric Sequence / TicketSequence: JSON
	// numbers unmarshal as float64 in map[string]interface{} but downstream
	// consumers (binarycodec, simulate engine) expect an integer type.
	if rpcErr := normalizeSequenceFields(txJsonMap); rpcErr != nil {
		return nil, rpcErr
	}

	// Post-autofill Account format check — the Account-format slice of
	// rippled's STParsedJSONObject (Simulate.cpp:328-330). Only catches
	// the Account field; unknown-field / missing-required-field
	// surfacing remains engine-side. The Sequence-absent path already
	// rejected malformed Accounts via GetAutofillSequence; this catches
	// the Sequence-supplied case where rippled's autofill skips the
	// check and STParsedJSONObject surfaces invalid_field.
	if accountStr, ok := txJsonMap["Account"].(string); !ok {
		return nil, types.RpcErrorInvalidField("tx.Account")
	} else if !types.IsValidXRPLAddress(accountStr) {
		return nil, types.RpcErrorInvalidField("tx.Account")
	}

	// Reject Batch — rippled Simulate.cpp:345-348.
	if txType, ok := txJsonMap["TransactionType"].(string); ok && txType == "Batch" {
		return nil, types.RpcErrorNotImpl()
	}

	// STParsedJSONObject parity — unknown-field surface (rippled
	// Simulate.cpp:328-330). Each top-level tx_json key must resolve to
	// a known SField; otherwise rippled returns
	// `error_message: "Field 'tx_json.<key>' is unknown."` from
	// STParsedJSONObject. binarycodec.definitions.Get() carries the
	// same registry rippled's STParsedJSONObject consults.
	defs := binarycodecdefs.Get()
	for k := range txJsonMap {
		if _, ok := defs.Fields[k]; !ok {
			return nil, types.RpcErrorInvalidParams(
				fmt.Sprintf("Field 'tx_json.%s' is unknown.", k))
		}
	}

	// Marshal tx_json for parse + service call.
	txJSON, err := json.Marshal(txJsonMap)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to marshal tx_json")
	}

	// STTx ctor parity — rippled Simulate.cpp:332-343. A parse failure or
	// missing-required-field surface as
	// `error: "invalidTransaction"` + `error_exception: <reason>`
	// instead of flowing into the engine as a TER. The duplicate
	// Validate() vs the engine's own Validate is intentional: it
	// guarantees the error envelope shape matches rippled even when the
	// underlying message text differs.
	parsedTx, parseErr := tx.ParseJSON(txJSON)
	if parseErr != nil {
		return nil, types.RpcErrorInvalidTransaction(parseErr.Error())
	}
	if validateErr := parsedTx.Validate(); validateErr != nil {
		return nil, types.RpcErrorInvalidTransaction(validateErr.Error())
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

// processSigners mirrors rippled's autofillTx Signers loop
// (Simulate.cpp:97-127): per-iteration structural check, then autofill of
// missing SigningPubKey / TxnSignature, then rejection of any non-empty
// signer TxnSignature. The inline ordering is observable: a signed
// TxnSignature on signers[0] returns rpcTX_SIGNED even when signers[2] is
// structurally malformed.
func processSigners(txJsonMap map[string]interface{}) *types.RpcError {
	signersRaw, ok := txJsonMap["Signers"]
	if !ok {
		return nil
	}
	signers, ok := signersRaw.([]interface{})
	if !ok {
		return types.RpcErrorInvalidField("tx.Signers")
	}
	for i, entry := range signers {
		entryObj, ok := entry.(map[string]interface{})
		if !ok {
			return types.RpcErrorInvalidField("tx.Signers[" + strconv.Itoa(i) + "]")
		}
		signerInner, ok := entryObj["Signer"]
		if !ok {
			return types.RpcErrorInvalidField("tx.Signers[" + strconv.Itoa(i) + "]")
		}
		signerObj, ok := signerInner.(map[string]interface{})
		if !ok {
			return types.RpcErrorInvalidField("tx.Signers[" + strconv.Itoa(i) + "]")
		}
		if _, ok := signerObj["SigningPubKey"]; !ok {
			signerObj["SigningPubKey"] = ""
		}
		if txnSig, ok := signerObj["TxnSignature"]; !ok {
			signerObj["TxnSignature"] = ""
		} else if sigStr, _ := txnSig.(string); sigStr != "" {
			return types.RpcErrorTxSigned()
		}
	}
	return nil
}

// normalizeSequenceFields coerces caller-supplied Sequence and
// TicketSequence values to uint32 (JSON numbers unmarshal as float64 in
// map[string]interface{}, but downstream consumers expect an integer
// type). Values outside [0, math.MaxUint32] are rejected to mirror
// rippled's STParsedJSONObject behaviour on UInt32 fields.
func normalizeSequenceFields(txJsonMap map[string]interface{}) *types.RpcError {
	for _, k := range [...]string{"Sequence", "TicketSequence"} {
		v, ok := txJsonMap[k]
		if !ok {
			continue
		}
		var (
			val   uint32
			valid bool
		)
		switch n := v.(type) {
		case uint32:
			val, valid = n, true
		case float64:
			if n >= 0 && n <= math.MaxUint32 && n == math.Trunc(n) {
				val, valid = uint32(n), true
			}
		case int:
			if n >= 0 && uint64(n) <= math.MaxUint32 {
				val, valid = uint32(n), true
			}
		case int64:
			if n >= 0 && uint64(n) <= math.MaxUint32 {
				val, valid = uint32(n), true
			}
		case uint64:
			if n <= math.MaxUint32 {
				val, valid = uint32(n), true
			}
		default:
			continue
		}
		if !valid {
			return types.RpcErrorInvalidField("tx." + k)
		}
		txJsonMap[k] = val
	}
	return nil
}
