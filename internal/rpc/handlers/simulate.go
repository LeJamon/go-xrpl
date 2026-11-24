package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	binarycodectypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/txprojection"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
)

// SimulateMethod handles the simulate RPC method.
// Runs a transaction against a snapshot of the open ledger without committing.
// Reference: rippled Simulate.cpp
type SimulateMethod struct{ baseHandler }

func (m *SimulateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	setLoadMedium(ctx)
	var rawParams map[string]json.RawMessage
	if params != nil {
		if err := json.Unmarshal(params, &rawParams); err != nil {
			return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	} else {
		rawParams = make(map[string]json.RawMessage)
	}

	// Validate `binary` field type if present — must be a JSON boolean.
	// JsonCpp's isBool() rejects null and numeric values.
	var binaryOutput bool
	if raw, ok := rawParams["binary"]; ok {
		trimmed := bytes.TrimSpace(raw)
		if !bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false")) {
			return nil, rpcerrors.RpcErrorInvalidField("binary")
		}
		_ = json.Unmarshal(raw, &binaryOutput)
	}

	// Reject forbidden fields: secret, seed, seed_hex, passphrase.
	// rippled checks these before parsing tx_json/tx_blob.
	for _, field := range []string{"secret", "seed", "seed_hex", "passphrase"} {
		if _, ok := rawParams[field]; ok {
			return nil, rpcerrors.RpcErrorInvalidField(field)
		}
	}

	_, hasTxBlobRaw := rawParams["tx_blob"]
	_, hasTxJsonRaw := rawParams["tx_json"]

	if hasTxBlobRaw && hasTxJsonRaw {
		return nil, rpcerrors.RpcErrorInvalidParams("Can only include one of `tx_blob` and `tx_json`.")
	}
	if !hasTxBlobRaw && !hasTxJsonRaw {
		return nil, rpcerrors.RpcErrorInvalidParams("Neither `tx_blob` nor `tx_json` included.")
	}

	if ctx.Services == nil || ctx.Services.Ledger() == nil {
		return nil, rpcInternalInvariantError("simulate: ledger service unavailable")
	}

	var txJsonMap map[string]any

	if hasTxBlobRaw {
		var txBlobStr string
		if err := json.Unmarshal(rawParams["tx_blob"], &txBlobStr); err != nil || txBlobStr == "" {
			return nil, rpcerrors.RpcErrorInvalidField("tx_blob")
		}
		rawBlob, err := hex.DecodeString(txBlobStr)
		if err != nil || len(rawBlob) == 0 {
			return nil, rpcerrors.RpcErrorInvalidField("tx_blob")
		}
		txJsonMap, err = binarycodec.DecodeBytesAllowOverCap(rawBlob)
		if err != nil {
			return nil, rpcerrors.RpcErrorInvalidField("tx_blob")
		}
	} else {
		var txObj map[string]any
		decoder := json.NewDecoder(bytes.NewReader(rawParams["tx_json"]))
		decoder.UseNumber()
		if err := decoder.Decode(&txObj); err != nil {
			return nil, rpcerrors.RpcErrorExpectedField("tx_json", "object")
		}
		if txObj == nil {
			return nil, rpcerrors.RpcErrorExpectedField("tx_json", "object")
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, rpcerrors.RpcErrorExpectedField("tx_json", "object")
		}
		txJsonMap = txObj
	}

	// Basic sanity checks for transaction shape (matching rippled getTxJsonFromParams).
	if _, ok := txJsonMap["TransactionType"]; !ok {
		return nil, rpcerrors.RpcErrorMissingField("tx.TransactionType")
	}
	if _, ok := txJsonMap["Account"]; !ok {
		return nil, rpcerrors.RpcErrorMissingField("tx.Account")
	}
	// rippled autofillTx() — Simulate.cpp:79-179. Signature fields are
	// normalized before sequence, network ID, and fee autofill so signed
	// requests and malformed nested signatures take precedence over later
	// ledger lookups.
	if rpcErr := autofillSignatureObject(txJsonMap, "tx"); rpcErr != nil {
		return nil, rpcErr
	}

	if sponsorValue, ok := txJsonMap["SponsorSignature"]; ok {
		sponsor, ok := sponsorValue.(map[string]any)
		if !ok || sponsor == nil {
			return nil, rpcerrors.RpcErrorExpectedField("SponsorSignature", "object")
		}
		if rpcErr := autofillSignatureObject(sponsor, "tx.SponsorSignature"); rpcErr != nil {
			return nil, rpcErr
		}
	}

	// Sequence — rippled Simulate.cpp:150-156. Account format is checked
	// inside GetAutofillSequence (mirrors rippled getAutofillSequence,
	// Simulate.cpp:46-75), so signature errors take precedence over
	// srcActMalformed/srcActNotFound.
	if _, hasSeq := txJsonMap["Sequence"]; !hasSeq {
		accountStr, ok := txJsonMap["Account"].(string)
		if !ok {
			return nil, rpcerrors.RpcErrorInvalidField("tx.Account")
		}
		_, hasTicket := txJsonMap["TicketSequence"]
		seq, seqErr := ctx.Services.LedgerMutation().GetAutofillSequence(accountStr, hasTicket)
		if seqErr != nil {
			switch {
			case errors.Is(seqErr, svcerr.ErrAccountMalformed):
				return nil, rpcerrors.RpcErrorSrcActMalformed("Invalid field 'tx.Account'.")
			case errors.Is(seqErr, svcerr.ErrAccountNotFound):
				return nil, rpcerrors.RpcErrorSrcActNotFound("Source account not found.")
			default:
				return nil, rpcInternalError("simulate: sequence autofill failed", seqErr)
			}
		}
		txJsonMap["Sequence"] = seq
	}

	// NetworkID — rippled Simulate.cpp:158-163.
	if _, ok := txJsonMap["NetworkID"]; !ok {
		serverInfo := ctx.Services.Ledger().GetServerInfo()
		if serverInfo.NetworkID > 1024 {
			txJsonMap["NetworkID"] = serverInfo.NetworkID
		}
	}

	// Fee — rippled Simulate.cpp:165-179. The fee estimator sees all
	// normalized signature, sequence, and network fields.
	if _, hasFee := txJsonMap["Fee"]; !hasFee {
		probe, marshalErr := json.Marshal(txJsonMap)
		if marshalErr != nil {
			return nil, rpcInternalError("simulate: fee probe marshaling failed", marshalErr)
		}
		feeOpts := defaultFeeOptions()
		fee, feeErr := ctx.Services.LedgerMutation().GetAutofillFee(
			probe,
			ctx.Role.IsUnlimited(),
			feeOpts.Mult,
			feeOpts.Div,
		)
		if feeErr != nil {
			var hfe *svcerr.HighFeeError
			if errors.As(feeErr, &hfe) {
				return nil, rpcerrors.RpcErrorHighFee(hfe.Error())
			}
			return nil, rpcInternalError("simulate: fee autofill failed", feeErr)
		}
		txJsonMap["Fee"] = strconv.FormatUint(fee, 10)
	}

	// Post-autofill Account format check — the Account-format slice of
	// rippled's STParsedJSONObject (Simulate.cpp:328-330). Only catches
	// the Account field; unknown-field / missing-required-field
	// surfacing remains engine-side. The Sequence-absent path already
	// rejected malformed Accounts via GetAutofillSequence; this catches
	// the Sequence-supplied case where rippled's autofill skips the
	// check and STParsedJSONObject surfaces invalid_field.
	if accountStr, ok := txJsonMap["Account"].(string); !ok {
		return nil, rpcerrors.RpcErrorInvalidField("tx.Account")
	} else if !types.IsValidClassicAddress(accountStr) {
		return nil, rpcerrors.RpcErrorInvalidField("tx.Account")
	}

	if hasTxBlobRaw {
		if message := normalizeDecodedMPTValuesForSimulation(txJsonMap, "tx_json"); message != "" {
			return nil, rpcerrors.RpcErrorInvalidParams(message)
		}
	}

	// Parse and validate the canonical transaction once. The simulation mode
	// preserves STTx's invalidTransaction error envelope while the shared
	// preprocessor handles serialized-field normalization and array limits.
	parsedTx, rpcErr := preprocessTransaction(txJsonMap, transactionPreprocessOptions{
		mode:          transactionPreprocessSimulate,
		decodedFields: hasTxBlobRaw,
	})
	if rpcErr != nil {
		return nil, rpcErr
	}
	// Batch simulation is intentionally unsupported, but the type must be
	// checked after the full serialized parse so malformed Batch JSON reports
	// its structural error first and numeric TransactionType values are covered.
	if parsedTx.TxType() == tx.TypeBatch {
		return nil, rpcerrors.RpcErrorNotImpl()
	}
	if parsedTx.GetCommon().GetFlags()&tx.TfInnerBatchTxn != 0 {
		return nil, rpcerrors.RpcErrorInvalidParams("tfInnerBatchTxn flag is not allowed on top-level transactions.")
	}

	canonicalMap, err := flattenCanonicalTransaction(parsedTx, txJsonMap)
	if err != nil {
		return nil, rpcInternalError("simulate: transaction flattening failed", err)
	}
	canonicalBlob, err := encodeCanonicalTransaction(canonicalMap)
	if err != nil {
		return nil, rpcInternalError("simulate: transaction encoding failed", err)
	}
	canonicalHash := CalculateTxHash(canonicalBlob)
	txJSON, err := json.Marshal(canonicalMap)
	if err != nil {
		return nil, rpcInternalError("simulate: transaction marshaling failed", err)
	}

	result, err := ctx.Services.LedgerMutation().SimulateTransaction(txJSON)
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
			enrichSimulateMeta(metaMap, canonicalMap, SyntheticMetadataContext{
				LedgerSequence: result.CurrentLedger,
				CloseTime:      result.CurrentLedgerCloseTime,
			})
			response["meta"] = metaMap
		} else {
			response["meta"] = result.Metadata.JSON
		}
	}

	if binaryOutput {
		response["tx_blob"] = canonicalBlob
	} else {
		response["tx_json"] = txprojection.ProjectJSONForPath(
			canonicalMap,
			canonicalHash,
			ctx.ApiVersion,
			txprojection.PathCanonical,
		)
	}

	return response, nil
}

// Simulation parses the decoded MPT's display value after autofill. This erases
// the wire sign of zero and applies the JSON magnitude limit.
func normalizeDecodedMPTValuesForSimulation(fields map[string]any, path string) string {
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		switch child := fields[name].(type) {
		case map[string]any:
			fieldPath := path + "." + name
			if raw, ok := child["value"].(binarycodectypes.RawMPTValue); ok {
				if raw.Magnitude > protocol.MaxMPTokenAmount {
					return fmt.Sprintf("Field '%s' has invalid data.", fieldPath)
				}
				child["value"] = raw.String()
			} else if message := normalizeDecodedMPTValuesForSimulation(child, fieldPath); message != "" {
				return message
			}
		case []any:
			for i, item := range child {
				wrapper, _ := item.(map[string]any)
				for wrapperName, value := range wrapper {
					object, ok := value.(map[string]any)
					if !ok {
						continue
					}
					itemPath := fmt.Sprintf("%s.%s.[%d].%s", path, name, i, wrapperName)
					if message := normalizeDecodedMPTValuesForSimulation(object, itemPath); message != "" {
						return fmt.Sprintf("Error at '%s'. %s", itemPath, message)
					}
				}
			}
		}
	}
	return ""
}

func (m *SimulateMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

// autofillSignatureObject mirrors rippled's autofillSignature
// (Simulate.cpp:79-131). The prefix identifies the top-level transaction or
// nested SponsorSignature in invalid-field responses.
func autofillSignatureObject(signature map[string]any, prefix string) *rpcerrors.RpcError {
	if _, ok := signature["SigningPubKey"]; !ok {
		signature["SigningPubKey"] = ""
	}

	if signersRaw, hasSigners := signature["Signers"]; hasSigners {
		signers, ok := signersRaw.([]any)
		if !ok {
			return rpcerrors.RpcErrorInvalidField(prefix + ".Signers")
		}
		for i, entry := range signers {
			entryObj, ok := entry.(map[string]any)
			if !ok {
				return rpcerrors.RpcErrorInvalidField(prefix + ".Signers[" + strconv.Itoa(i) + "]")
			}
			signerInner, ok := entryObj["Signer"]
			if !ok {
				return rpcerrors.RpcErrorInvalidField(prefix + ".Signers[" + strconv.Itoa(i) + "]")
			}
			signerObj, ok := signerInner.(map[string]any)
			if !ok {
				return rpcerrors.RpcErrorInvalidField(prefix + ".Signers[" + strconv.Itoa(i) + "]")
			}
			if _, ok := signerObj["SigningPubKey"]; !ok {
				signerObj["SigningPubKey"] = ""
			}
			if txnSig, ok := signerObj["TxnSignature"]; !ok {
				signerObj["TxnSignature"] = ""
			} else {
				sigStr, isString := txnSig.(string)
				if !isString || sigStr != "" {
					return rpcerrors.RpcErrorTxSigned()
				}
			}
		}
	}

	if _, ok := signature["TxnSignature"]; !ok {
		signature["TxnSignature"] = ""
	} else if txnSig, ok := signature["TxnSignature"].(string); !ok || txnSig != "" {
		return rpcerrors.RpcErrorTxSigned()
	}
	return nil
}
