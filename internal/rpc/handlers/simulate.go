package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	binarycodecdefs "github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// SimulateMethod handles the simulate RPC method.
// Runs a transaction against a snapshot of the open ledger without committing.
// Reference: rippled Simulate.cpp
type SimulateMethod struct{ BaseHandler }

func (m *SimulateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	setLoadMedium(ctx)
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

	_, hasTxBlobRaw := rawParams["tx_blob"]
	_, hasTxJsonRaw := rawParams["tx_json"]

	if hasTxBlobRaw && hasTxJsonRaw {
		return nil, types.RpcErrorInvalidParams("Can only include one of `tx_blob` and `tx_json`.")
	}
	if !hasTxBlobRaw && !hasTxJsonRaw {
		return nil, types.RpcErrorInvalidParams("Neither `tx_blob` nor `tx_json` included.")
	}

	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, rpcInternalInvariantError("simulate: ledger service unavailable")
	}

	var txJsonMap map[string]any

	if hasTxBlobRaw {
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
		var txObj map[string]any
		decoder := json.NewDecoder(bytes.NewReader(rawParams["tx_json"]))
		decoder.UseNumber()
		if err := decoder.Decode(&txObj); err != nil {
			return nil, types.RpcErrorExpectedField("tx_json", "object")
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
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
	transactionType := txJsonMap["TransactionType"]

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
			return nil, rpcInternalError("simulate: fee probe marshaling failed", marshalErr)
		}
		// rippled's simulate autofill uses the default fee_mult_max /
		// fee_div_max (getCurrentNetworkFee default arguments).
		feeOpts := defaultFeeOptions()
		fee, feeErr := ctx.Services.Ledger.GetAutofillFee(probe, ctx.Unlimited, feeOpts.Mult, feeOpts.Div)
		if feeErr != nil {
			var hfe *svcerr.HighFeeError
			if errors.As(feeErr, &hfe) {
				return nil, types.RpcErrorHighFee(hfe.Error())
			}
			return nil, rpcInternalError("simulate: fee autofill failed", feeErr)
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
	} else {
		sigStr, isString := txnSig.(string)
		if !isString || sigStr != "" {
			return nil, types.RpcErrorTxSigned()
		}
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
				return nil, rpcInternalError("simulate: sequence autofill failed", seqErr)
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

	// Post-autofill Account format check — the Account-format slice of
	// rippled's STParsedJSONObject (Simulate.cpp:328-330). Only catches
	// the Account field; unknown-field / missing-required-field
	// surfacing remains engine-side. The Sequence-absent path already
	// rejected malformed Accounts via GetAutofillSequence; this catches
	// the Sequence-supplied case where rippled's autofill skips the
	// check and STParsedJSONObject surfaces invalid_field.
	if accountStr, ok := txJsonMap["Account"].(string); !ok {
		return nil, types.RpcErrorInvalidField("tx.Account")
	} else if !types.IsValidClassicAddress(accountStr) {
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
	if parseMessage := serializedFieldParseMessage(txJsonMap, "tx_json", defs); parseMessage != "" {
		return nil, types.RpcErrorInvalidParams(parseMessage)
	}
	// STParsedJSONObject stores TransactionType as its UInt16 code, while the
	// Go transaction registry selects concrete types by their JSON name.
	txJsonMap["TransactionType"] = transactionType

	// STParsedJSONObject also caps each JSON array field at MaxJSONArrayElements
	// (rippled maxSTParsedJSONArraySize); surface an overflow as invalidParams
	// before the transaction is parsed and simulated. Other encode failures are
	// left to the parse/validate path below.
	if _, encErr := binarycodec.Encode(txJsonMap); encErr != nil {
		if e := arraySizeRpcError(encErr); e != nil {
			return nil, e
		}
	}

	// Marshal tx_json for parse + service call.
	txJSON, err := json.Marshal(txJsonMap)
	if err != nil {
		return nil, rpcInternalError("simulate: transaction marshaling failed", err)
	}

	// STTx ctor parity — rippled Simulate.cpp:332-343. A parse failure or
	// missing-required-field surface as
	// `error: "invalidTransaction"` + `error_exception: <reason>`
	// instead of flowing into the engine as a TER. This early structural
	// validation guarantees the error envelope shape matches rippled even
	// when type-specific engine preflight is rules-aware.
	parsedTx, parseErr := tx.ParseJSON(txJSON)
	if parseErr != nil {
		return nil, types.RpcErrorInvalidTransaction(parseErr.Error())
	}
	if templateErr := tx.ValidateTemplateFields(parsedTx.TxType(), txJsonMap); templateErr != nil {
		return nil, types.RpcErrorInvalidTransaction(templateErr.Error())
	}

	result, err := ctx.Services.Ledger.SimulateTransaction(txJSON)
	if err != nil {
		return nil, rpcInternalError("simulate: transaction simulation failed", err)
	}

	// rippled overrides the tesSUCCESS message for simulate (Simulate.cpp:258-262).
	engineMessage := result.EngineResultMessage
	if result.EngineResult == "tesSUCCESS" {
		engineMessage = "The simulated transaction would have been applied."
	}

	response := map[string]any{
		"engine_result":         result.EngineResult,
		"engine_result_code":    result.EngineResultCode,
		"engine_result_message": engineMessage,
		"applied":               result.Applied,
		"ledger_index":          result.CurrentLedger,
	}

	// rippled emits "meta" (JSON) when binary=false and "meta_blob" (hex)
	// when binary=true. Always emit when Metadata is present, mirroring
	// rippled's `if (result.metadata)` guard (Simulate.cpp:264-276). The
	// synthetic fields (delivered_amount / nftoken_id / nftoken_ids / offer_id /
	// mpt_issuance_id) are a JSON-meta-only enrichment; meta_blob carries only
	// the raw serialized metadata (Simulate.cpp:277-288).
	if result.Metadata != nil {
		if binaryOutput {
			response["meta_blob"] = strings.ToUpper(hex.EncodeToString(result.Metadata.Blob))
		} else if metaMap := metadataToMap(result.Metadata.JSON); metaMap != nil {
			enrichSimulateMeta(metaMap, txJsonMap, SyntheticMetadataContext{
				LedgerSequence: result.CurrentLedger,
				CloseTime:      result.CurrentLedgerCloseTime,
			})
			response["meta"] = metaMap
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

func (m *SimulateMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

// processSigners mirrors rippled's autofillTx Signers loop
// (Simulate.cpp:97-127): per-iteration structural check, then autofill of
// missing SigningPubKey / TxnSignature, then rejection of any non-empty
// signer TxnSignature. The inline ordering is observable: a signed
// TxnSignature on signers[0] returns rpcTX_SIGNED even when signers[2] is
// structurally malformed.
func processSigners(txJsonMap map[string]any) *types.RpcError {
	signersRaw, ok := txJsonMap["Signers"]
	if !ok {
		return nil
	}
	signers, ok := signersRaw.([]any)
	if !ok {
		return types.RpcErrorInvalidField("tx.Signers")
	}
	for i, entry := range signers {
		entryObj, ok := entry.(map[string]any)
		if !ok {
			return types.RpcErrorInvalidField("tx.Signers[" + strconv.Itoa(i) + "]")
		}
		signerInner, ok := entryObj["Signer"]
		if !ok {
			return types.RpcErrorInvalidField("tx.Signers[" + strconv.Itoa(i) + "]")
		}
		signerObj, ok := signerInner.(map[string]any)
		if !ok {
			return types.RpcErrorInvalidField("tx.Signers[" + strconv.Itoa(i) + "]")
		}
		if _, ok := signerObj["SigningPubKey"]; !ok {
			signerObj["SigningPubKey"] = ""
		}
		if txnSig, ok := signerObj["TxnSignature"]; !ok {
			signerObj["TxnSignature"] = ""
		} else {
			sigStr, isString := txnSig.(string)
			if !isString || sigStr != "" {
				return types.RpcErrorTxSigned()
			}
		}
	}
	return nil
}
