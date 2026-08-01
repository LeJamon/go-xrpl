package tx

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// MaxSerializedMemosSize is the maximum serialized length of a transaction's
// Memos array. rippled isMemoOkay serializes the array with per-object field
// headers and end markers and rejects it above 1024 bytes; there are no
// per-field size caps. Reference: rippled STTx.cpp isMemoOkay.
const MaxSerializedMemosSize = 1024

// PassesLocalChecks preserves the common-field memo check for callers that do
// not have a parsed transaction. Submission paths should use
// PassesTransactionLocalChecks so the transaction-specific checks also run.
func PassesLocalChecks(common *Common) ter.Result {
	if LocalChecksFailureReason(common) != "" {
		return ter.TemMALFORMED
	}
	return ter.TesSUCCESS
}

// PassesTransactionLocalChecks runs the non-consensus checks applied only at
// local transaction-submission ingress.
func PassesTransactionLocalChecks(transaction Transaction) ter.Result {
	if TransactionLocalChecksFailureReason(transaction) != "" {
		return ter.TemMALFORMED
	}
	return ter.TesSUCCESS
}

// TransactionLocalChecksFailureReason returns the first local-submission
// rejection reason in protocol order.
func TransactionLocalChecksFailureReason(transaction Transaction) string {
	if raw := transaction.GetRawBytes(); len(raw) != 0 {
		if fields, err := binarycodec.DecodeBytes(raw); err == nil {
			return TransactionMapLocalChecksFailureReason(transaction.TxType(), fields)
		}
	}
	if reason := LocalChecksFailureReason(transaction.GetCommon()); reason != "" {
		return reason
	}

	fields, err := transaction.Flatten()
	if err != nil {
		return err.Error()
	}
	if reason := transactionFieldsLocalChecksFailureReason(transaction.TxType(), fields); reason != "" {
		return reason
	}
	if transaction.TxType() == TypeBatch {
		if signers, ok := transaction.(BatchSignerProvider); ok && len(signers.GetBatchSigners()) > 8 {
			return "Batch Signers array exceeds max entries."
		}
		if outer, ok := transaction.(interface{ InnerTransactions() []Transaction }); ok {
			inners := outer.InnerTransactions()
			if len(inners) > 8 {
				return "Raw Transactions array exceeds max entries."
			}
			for _, inner := range inners {
				if inner != nil && inner.TxType() == TypeBatch {
					return "Raw Transactions may not contain batch transactions."
				}
			}
		}
	}

	return ""
}

// TransactionMapLocalChecksFailureReason applies local-submission checks to a
// canonically parsed transaction map before it is converted to a Go transaction.
func TransactionMapLocalChecksFailureReason(txType Type, fields map[string]any) string {
	if memos, ok := fields["Memos"]; ok {
		if reason := localChecksMemosFromMap(memos); reason != "" {
			return reason
		}
	}
	if reason := transactionFieldsLocalChecksFailureReason(txType, fields); reason != "" {
		return reason
	}
	if txType == TypeBatch {
		if batchSigners, ok := fields["BatchSigners"].([]any); ok && len(batchSigners) > 8 {
			return "Batch Signers array exceeds max entries."
		}
		rawTransactions, ok := fields["RawTransactions"].([]any)
		if !ok {
			return ""
		}
		if len(rawTransactions) > 8 {
			return "Raw Transactions array exceeds max entries."
		}
		for _, raw := range rawTransactions {
			wrapper, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			inner, ok := wrapper["RawTransaction"].(map[string]any)
			if !ok {
				continue
			}
			innerTypeValue, present := inner["TransactionType"]
			if !present {
				return "Field not found: TransactionType"
			}
			innerType, ok := transactionTypeFromCanonicalMap(inner)
			if !ok {
				if code, numeric := innerTypeValue.(uint16); numeric {
					return fmt.Sprintf("Invalid transaction type %d", code)
				}
				return "Field 'TransactionType' has invalid data."
			}
			if innerType == TypeBatch {
				return "Raw Transactions may not contain batch transactions."
			}
			inner["TransactionType"] = innerType.String()
			if err := ValidateTemplateFields(innerType, inner); err != nil {
				return err.Error()
			}
		}
	}
	return ""
}

func localChecksMemosFromMap(memos any) string {
	full, err := binarycodec.EncodeBytes(map[string]any{"Memos": memos})
	if err != nil {
		return "The MemoType, MemoData and MemoFormat fields may only contain hex-encoded data."
	}
	overhead, err := binarycodec.EncodeBytes(map[string]any{"Memos": []map[string]any{}})
	if err != nil {
		return "The MemoType, MemoData and MemoFormat fields may only contain hex-encoded data."
	}
	if len(full)-len(overhead) > MaxSerializedMemosSize {
		return "The memo exceeds the maximum allowed size."
	}

	raw, err := json.Marshal(memos)
	if err != nil {
		return "The MemoType, MemoData and MemoFormat fields may only contain hex-encoded data."
	}
	var entries []map[string]map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "The MemoType, MemoData and MemoFormat fields may only contain hex-encoded data."
	}
	for _, wrapper := range entries {
		memo := wrapper["Memo"]
		for _, name := range []string{"MemoType", "MemoData", "MemoFormat"} {
			value, present := memo[name]
			if !present {
				continue
			}
			text, ok := value.(string)
			if !ok {
				return "The MemoType, MemoData and MemoFormat fields may only contain hex-encoded data."
			}
			decoded, err := hex.DecodeString(text)
			if err != nil {
				return "The MemoType, MemoData and MemoFormat fields may only contain hex-encoded data."
			}
			if name != "MemoData" && !isValidURLBytes(decoded) {
				return "The MemoType and MemoFormat fields may only contain characters that are allowed in URLs under RFC 3986."
			}
		}
	}
	return ""
}

func transactionFieldsLocalChecksFailureReason(txType Type, fields map[string]any) string {
	if hasDefaultAccountField(fields) {
		return "An account field is invalid."
	}
	if txType.IsPseudoTransaction() {
		return "Cannot submit pseudo transactions."
	}
	if hasUnsupportedMPTField(txType, fields) {
		return "Amount can not be MPT."
	}
	return ""
}

func transactionTypeFromCanonicalMap(fields map[string]any) (Type, bool) {
	switch value := fields["TransactionType"].(type) {
	case uint16:
		txType := Type(value)
		_, known := TypeFromName(txType.String())
		return txType, known
	case string:
		return TypeFromName(value)
	default:
		return 0, false
	}
}

func hasDefaultAccountField(fields map[string]any) bool {
	defs := definitions.Get()
	for name, value := range fields {
		field, err := defs.FieldInstanceByName(name)
		if err != nil || field.Type != "AccountID" {
			continue
		}
		account, ok := value.(string)
		if !ok {
			continue
		}
		if account == "" {
			return true
		}
		if decoded, err := hex.DecodeString(account); err == nil && len(decoded) == 20 {
			if bytes.Equal(decoded, make([]byte, 20)) {
				return true
			}
			continue
		}
		_, decoded, err := addresscodec.DecodeClassicAddressToAccountID(account)
		if err == nil && bytes.Equal(decoded, make([]byte, 20)) {
			return true
		}
	}
	return false
}

func hasUnsupportedMPTField(txType Type, fields map[string]any) bool {
	for name, value := range fields {
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if _, isMPT := object["mpt_issuance_id"]; !isMPT {
			continue
		}
		if !mptSupportedFields[txType][name] {
			return true
		}
	}
	return false
}

var mptSupportedFields = map[Type]map[string]bool{
	TypePayment:                 {"Amount": true, "SendMax": true, "DeliverMin": true},
	TypeEscrowCreate:            {"Amount": true},
	TypeOfferCreate:             {"TakerPays": true, "TakerGets": true},
	TypeCheckCreate:             {"SendMax": true},
	TypeCheckCash:               {"Amount": true, "DeliverMin": true},
	TypeClawback:                {"Amount": true},
	TypeAMMClawback:             {"Asset": true, "Asset2": true, "Amount": true},
	TypeAMMCreate:               {"Amount": true, "Amount2": true},
	TypeAMMDeposit:              {"Asset": true, "Asset2": true, "Amount": true, "Amount2": true},
	TypeAMMWithdraw:             {"Asset": true, "Asset2": true, "Amount": true, "Amount2": true},
	TypeAMMVote:                 {"Asset": true, "Asset2": true},
	TypeAMMBid:                  {"Asset": true, "Asset2": true},
	TypeAMMDelete:               {"Asset": true, "Asset2": true},
	TypeVaultCreate:             {"Asset": true},
	TypeVaultDeposit:            {"Amount": true},
	TypeVaultWithdraw:           {"Amount": true},
	TypeVaultClawback:           {"Amount": true},
	TypeLoanBrokerCoverDeposit:  {"Amount": true},
	TypeLoanBrokerCoverWithdraw: {"Amount": true},
	TypeLoanBrokerCoverClawback: {"Amount": true},
	TypeLoanPay:                 {"Amount": true},
}

func LocalChecksFailureReason(common *Common) string {
	if len(common.Memos) == 0 {
		return ""
	}

	serializedLen, err := serializedMemosLength(common.Memos)
	if err != nil {
		return "The MemoType, MemoData and MemoFormat fields may only contain hex-encoded data."
	}
	if serializedLen > MaxSerializedMemosSize {
		return "The memo exceeds the maximum allowed size."
	}

	for _, memoWrapper := range common.Memos {
		memo := memoWrapper.Memo
		for _, value := range []string{memo.MemoType, memo.MemoData, memo.MemoFormat} {
			if value == "" {
				continue
			}
			if _, err := hex.DecodeString(value); err != nil {
				return "The MemoType, MemoData and MemoFormat fields may only contain hex-encoded data."
			}
		}
		for _, value := range []string{memo.MemoType, memo.MemoFormat} {
			decoded, _ := hex.DecodeString(value)
			if !isValidURLBytes(decoded) {
				return "The MemoType and MemoFormat fields may only contain characters that are allowed in URLs under RFC 3986."
			}
		}
	}

	return ""
}

// serializedMemosLength returns the serialized length of the Memos array
// excluding the sfMemos field header and the array end marker, matching
// rippled's `Serializer s; memos.add(s); s.getDataLength()`. It measures the
// overhead by encoding an empty array with the same field so the header and end
// marker cancel out, leaving only the array contents.
func serializedMemosLength(memos []MemoWrapper) (int, error) {
	arr := flattenMemos(memos)
	full, err := binarycodec.EncodeBytes(map[string]any{"Memos": arr})
	if err != nil {
		return 0, err
	}
	overhead, err := binarycodec.EncodeBytes(map[string]any{"Memos": []map[string]any{}})
	if err != nil {
		return 0, err
	}
	return len(full) - len(overhead), nil
}

// isValidURLBytes reports whether every byte is allowed in URLs per RFC 3986:
// alphanumerics and -._~:/?#[]@!$&'()*+,;=%
func isValidURLBytes(data []byte) bool {
	for _, b := range data {
		if !isURLChar(b) {
			return false
		}
	}
	return true
}

// isURLChar reports whether the byte is a valid URL character per RFC 3986.
func isURLChar(c byte) bool {
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return true
	}
	switch c {
	case '-', '.', '_', '~', ':', '/', '?', '#', '[', ']', '@', '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=', '%':
		return true
	}
	return false
}
