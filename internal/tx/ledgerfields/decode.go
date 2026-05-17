package ledgerfields

import (
	"encoding/hex"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
)

// decodeBlob runs binarycodec.Decode against a binary ledger-entry blob and
// returns the field map. This wraps the existing generic decoder so typed
// entries can populate their slots from named field lookups while keeping the
// codec primitive choice (string vs uint32 vs map for IOU amounts) in one
// place.
//
// A follow-up optimization will replace this with a streaming per-entry-type
// decoder that writes directly into typed slots, eliminating the intermediate
// map allocation. The interface defined in ledgerfields.go is shaped so that
// migration is a drop-in change to each Entry.Decode implementation.
func decodeBlob(data []byte) (map[string]any, error) {
	return binarycodec.Decode(hex.EncodeToString(data))
}

// hexString safely extracts a hex-string field. Returns ("", false) if the
// field is absent or not a string.
func hexString(m map[string]any, name string) (string, bool) {
	v, ok := m[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// uint32Field safely extracts a uint32 field. Returns (0, false) if absent or
// of the wrong type.
func uint32Field(m map[string]any, name string) (uint32, bool) {
	v, ok := m[name]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case uint32:
		return x, true
	case int:
		return uint32(x), true
	}
	return 0, false
}

// intField safely extracts an `int` field (used for UInt16 / UInt8). Returns
// (0, false) if absent or of the wrong type.
func intField(m map[string]any, name string) (int, bool) {
	v, ok := m[name]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return x, true
	case uint32:
		return int(x), true
	}
	return 0, false
}

// amountField extracts an Amount: either a string (XRP drops) or a
// map[string]any for IOU. Returns the underlying any value as-is.
func amountField(m map[string]any, name string) (any, bool) {
	v, ok := m[name]
	return v, ok
}

// stringSliceField extracts a Vector256 ([]string of hex hashes).
func stringSliceField(m map[string]any, name string) ([]string, bool) {
	v, ok := m[name]
	if !ok {
		return nil, false
	}
	s, ok := v.([]string)
	return s, ok
}

// equalAmount compares two Amount values. XRP amounts are strings; IOU
// amounts are map[string]any with value/currency/issuer. Returns true if
// equivalent. nil values compare equal only if both are nil.
func equalAmount(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, va := range av {
			vb, ok := bv[k]
			if !ok {
				return false
			}
			sa, aOK := va.(string)
			sb, bOK := vb.(string)
			if !aOK || !bOK || sa != sb {
				return false
			}
		}
		return true
	}
	return false
}

// equalStringSlice compares two []string values element-wise.
func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
