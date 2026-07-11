package tx

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// MaxSerializedMemosSize is the maximum serialized length of a transaction's
// Memos array. rippled isMemoOkay serializes the array with per-object field
// headers and end markers and rejects it above 1024 bytes; there are no
// per-field size caps. Reference: rippled STTx.cpp isMemoOkay.
const MaxSerializedMemosSize = 1024

// PassesLocalChecks runs the submission-only validation rippled performs in
// STTx::passesLocalChecks (invoked from NetworkOPs at the ingress), NOT in the
// consensus-critical preflight. It must be applied only on the local submission
// path: a relayed or consensus-applied transaction carrying the same memo still
// applies, because rippled treats a memo violation as a local, non-TER refusal
// (Validity::SigGoodOnly), which the engine preflight accepts. Applying it in
// preflight would exclude a memo-violating transaction from an agreed tx set
// that rippled applies with tesSUCCESS — a ledger fork.
//
// It returns TemMALFORMED on any violation, matching rippled's rejection of a
// locally-submitted transaction that fails passesLocalChecks.
func PassesLocalChecks(common *Common) ter.Result {
	return validateMemosLocal(common)
}

// validateMemosLocal mirrors rippled isMemoOkay: the whole Memos array must
// serialize to at most 1024 bytes (field headers included), every present
// MemoType/MemoData/MemoFormat field must be valid hex, and the decoded
// MemoType/MemoFormat bytes may contain only RFC 3986 URL characters (MemoData
// is unrestricted). There are no per-field size caps.
func validateMemosLocal(common *Common) ter.Result {
	if len(common.Memos) == 0 {
		return ter.TesSUCCESS
	}

	serializedLen, err := serializedMemosLength(common.Memos)
	if err != nil {
		return ter.TemMALFORMED
	}
	if serializedLen > MaxSerializedMemosSize {
		return ter.TemMALFORMED
	}

	for _, memoWrapper := range common.Memos {
		memo := memoWrapper.Memo
		// MemoType and MemoFormat are URL-charset-restricted; MemoData is not.
		if !memoHexFieldOkay(memo.MemoType, true) ||
			!memoHexFieldOkay(memo.MemoData, false) ||
			!memoHexFieldOkay(memo.MemoFormat, true) {
			return ter.TemMALFORMED
		}
	}

	return ter.TesSUCCESS
}

// memoHexFieldOkay reports whether a memo field is absent, or is valid hex whose
// decoded bytes satisfy the RFC 3986 URL charset when urlRestricted is set.
func memoHexFieldOkay(value string, urlRestricted bool) bool {
	if value == "" {
		return true
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return false
	}
	if urlRestricted && !isValidURLBytes(decoded) {
		return false
	}
	return true
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
