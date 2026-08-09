package handlers

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodecdefs "github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

type transactionPreprocessMode uint8

const (
	transactionPreprocessSign transactionPreprocessMode = iota
	transactionPreprocessSignFor
	transactionPreprocessSubmitMultisigned
	transactionPreprocessSimulate
)

type transactionPreprocessOptions struct {
	mode               transactionPreprocessMode
	preserveSigners    bool
	rejectTxnSignature bool
}

// preprocessTransaction performs the serialized-field validation and returns
// the parsed transaction that the caller subsequently mutates. Callers must
// flatten the object after mutation; the parser's raw bytes are retained only
// for signing preimages and are never used for the final response/submit blob.
func preprocessTransaction(
	txMap map[string]any,
	options transactionPreprocessOptions,
) (tx.Transaction, *rpcerrors.RpcError) {
	var rawSigners any
	strictSignerError := false
	if options.preserveSigners {
		if value, ok := txMap["Signers"]; ok {
			rawSigners = cloneJSONValue(value)
		}
	}

	if message := serializedFieldParseMessage(txMap, "tx_json", binarycodecdefs.Get()); message != "" {
		if options.preserveSigners && signerShapeMessage(message) {
			return nil, rpcerrors.RpcErrorInvalidParams("Signers array may only contain Signer entries.")
		}
		return nil, rpcerrors.RpcErrorInvalidParams(message)
	}
	if options.preserveSigners && rawSigners != nil {
		strictSignerError = strictSignerExtra(rawSigners)
	}
	if rpcErr := canonicalizeAccountFields(txMap); rpcErr != nil {
		return nil, rpcErr
	}

	transactionTypeCode, rpcErr := parseTransactionTypeCode(txMap["TransactionType"])
	if rpcErr != nil {
		return nil, rpcErr
	}
	transactionTypeName, err := binarycodecdefs.Get().TransactionTypeName(int32(transactionTypeCode))
	if err != nil {
		return nil, rpcerrors.RpcErrorInvalidTransactionType(transactionTypeCode)
	}
	txMap["TransactionType"] = transactionTypeName
	txType, _ := tx.TypeFromName(transactionTypeName)

	if err := tx.ValidateTemplateFields(txType, txMap); err != nil {
		if options.mode == transactionPreprocessSimulate {
			return nil, rpcerrors.RpcErrorInvalidTransaction(err.Error())
		}
		return nil, rpcerrors.RpcErrorInvalidParams(err.Error())
	}
	if options.mode != transactionPreprocessSimulate {
		if reason := tx.TransactionMapLocalChecksFailureReason(txType, txMap); reason != "" {
			return nil, rpcerrors.RpcErrorInvalidParams(reason)
		}
	}

	transaction, rpcErr := parseTransactionForSigning(txMap)
	if rpcErr != nil {
		if options.mode == transactionPreprocessSimulate {
			return nil, rpcerrors.RpcErrorInvalidTransaction(rpcErr.Message)
		}
		return nil, rpcErr
	}
	if options.rejectTxnSignature {
		if _, present := txMap["TxnSignature"]; present {
			return nil, rpcerrors.RpcErrorSigningMalformed()
		}
	}
	if strictSignerError {
		return nil, rpcerrors.RpcErrorInvalidParams("Signers array may only contain Signer entries.")
	}
	return transaction, nil
}

func parseTransactionTypeCode(value any) (uint16, *rpcerrors.RpcError) {
	switch value := value.(type) {
	case string:
		code, err := binarycodecdefs.Get().TransactionTypeCode(value)
		if err != nil {
			return 0, rpcerrors.RpcErrorInvalidParams("Field 'tx_json.TransactionType' has invalid data.")
		}
		return uint16(code), nil
	case uint16:
		return value, nil
	case uint8:
		return uint16(value), nil
	case uint32:
		if value > math.MaxUint16 {
			return 0, rpcerrors.RpcErrorInvalidParams("Field 'tx_json.TransactionType' has invalid data.")
		}
		return uint16(value), nil
	case int:
		if value < 0 || value > math.MaxUint16 {
			return 0, rpcerrors.RpcErrorInvalidParams("Field 'tx_json.TransactionType' has invalid data.")
		}
		return uint16(value), nil
	case int64:
		if value < 0 || value > math.MaxUint16 {
			return 0, rpcerrors.RpcErrorInvalidParams("Field 'tx_json.TransactionType' has invalid data.")
		}
		return uint16(value), nil
	case float64:
		if value < 0 || value > math.MaxUint16 || value != math.Trunc(value) {
			return 0, rpcerrors.RpcErrorInvalidParams("Field 'tx_json.TransactionType' has invalid data.")
		}
		return uint16(value), nil
	case json.Number:
		parsed, err := value.Int64()
		if err != nil || parsed < 0 || parsed > math.MaxUint16 {
			return 0, rpcerrors.RpcErrorInvalidParams("Field 'tx_json.TransactionType' has invalid data.")
		}
		return uint16(parsed), nil
	default:
		return 0, rpcerrors.RpcErrorInvalidParams("Field 'tx_json.TransactionType' has invalid data.")
	}
}

func cloneJSONValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var clone any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return value
	}
	return clone
}

func signerShapeMessage(message string) bool {
	return strings.Contains(message, "tx_json.Signers.") && strings.Contains(message, ".Signer.") &&
		(strings.Contains(message, "unknown.") || strings.Contains(message, "disallowed location."))
}

func strictSignerExtra(value any) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		wrapper, ok := item.(map[string]any)
		if !ok {
			continue
		}
		inner, ok := wrapper["Signer"].(map[string]any)
		if !ok {
			continue
		}
		if len(wrapper) != 1 || len(inner) != 3 {
			return true
		}
		if _, ok := inner["Account"]; !ok {
			continue
		}
		if _, ok := inner["SigningPubKey"]; !ok {
			continue
		}
		if _, ok := inner["TxnSignature"]; !ok {
			continue
		}
	}
	return false
}

func canonicalizeAccountFields(value any) *rpcerrors.RpcError {
	switch value := value.(type) {
	case map[string]any:
		for field, child := range value {
			if field == "Account" || field == "Destination" || field == "Delegate" || field == "Issuer" {
				if text, ok := child.(string); ok {
					canonical, err := canonicalAccountID(text)
					if err != nil {
						continue
					}
					value[field] = canonical
					continue
				}
			}
			if rpcErr := canonicalizeAccountFields(child); rpcErr != nil {
				return rpcErr
			}
		}
	case []any:
		for _, child := range value {
			if rpcErr := canonicalizeAccountFields(child); rpcErr != nil {
				return rpcErr
			}
		}
	}
	return nil
}

func canonicalAccountID(value string) (string, error) {
	if _, _, err := addresscodec.DecodeClassicAddressToAccountID(value); err == nil {
		return value, nil
	}
	if len(value) != 40 {
		return "", addresscodec.ErrInvalidClassicAddress
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 20 {
		return "", addresscodec.ErrInvalidClassicAddress
	}
	return addresscodec.EncodeAccountIDToClassicAddress(decoded)
}

func flattenCanonicalTransaction(transaction tx.Transaction, source map[string]any) (map[string]any, error) {
	flat, err := transaction.Flatten()
	if err != nil {
		return nil, err
	}
	tx.PopulateRequiredWireFields(flat, transaction.GetCommon())
	mergeExplicitEmptyFields(flat, source)
	flat = normalizeJSONContainers(flat).(map[string]any)
	return flat, nil
}

func normalizeJSONContainers(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for field, child := range value {
			value[field] = normalizeJSONContainers(child)
		}
		return value
	case []any:
		for i, child := range value {
			value[i] = normalizeJSONContainers(child)
		}
		return value
	case []map[string]any:
		result := make([]any, len(value))
		for i, child := range value {
			result[i] = normalizeJSONContainers(child)
		}
		return result
	default:
		return value
	}
}

func normalizeSignerResponseContainers(transactionMap map[string]any) {
	normalizeSignerResponseContainer(transactionMap)
	if counterparty, ok := transactionMap["CounterpartySignature"].(map[string]any); ok {
		normalizeSignerResponseContainer(counterparty)
	}
}

func normalizeSignerResponseContainer(fields map[string]any) {
	signers, ok := fields["Signers"].([]any)
	if !ok {
		return
	}
	result := make([]map[string]any, len(signers))
	for i, signer := range signers {
		wrapper, ok := signer.(map[string]any)
		if !ok {
			return
		}
		result[i] = wrapper
	}
	fields["Signers"] = result
}

func mergeExplicitEmptyFields(destination, source map[string]any) {
	for field, sourceValue := range source {
		destinationValue, present := destination[field]
		if !present {
			if isEmptyJSONValue(sourceValue) {
				destination[field] = sourceValue
			}
			continue
		}
		mergeExplicitEmptyValue(destinationValue, sourceValue)
	}
}

func mergeExplicitEmptyValue(destination, source any) {
	sourceMap, sourceIsMap := source.(map[string]any)
	if sourceIsMap {
		if destinationMap, ok := destination.(map[string]any); ok {
			mergeExplicitEmptyFields(destinationMap, sourceMap)
		}
		return
	}
	sourceSlice, sourceIsSlice := source.([]any)
	if !sourceIsSlice {
		return
	}
	switch destinationSlice := destination.(type) {
	case []any:
		for i := 0; i < len(sourceSlice) && i < len(destinationSlice); i++ {
			mergeExplicitEmptyValue(destinationSlice[i], sourceSlice[i])
		}
	case []map[string]any:
		for i := 0; i < len(sourceSlice) && i < len(destinationSlice); i++ {
			if sourceMap, ok := sourceSlice[i].(map[string]any); ok {
				mergeExplicitEmptyFields(destinationSlice[i], sourceMap)
			}
		}
	}
}

func isEmptyJSONValue(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return value == ""
	case []any:
		return len(value) == 0
	case map[string]any:
		return len(value) == 0
	default:
		return false
	}
}
