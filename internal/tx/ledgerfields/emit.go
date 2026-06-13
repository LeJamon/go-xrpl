package ledgerfields

import (
	"reflect"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
)

// Helpers shared by all generated entry implementations. They keep the
// presence-aware change-detection logic in one place so the per-entry-type
// code can stay mechanical.

// emitIfChangedString writes name=prevVal to out if the field's
// presence-or-value differs between the original (p*) and the current (c*).
// Mirrors the rippled rule: if the field changed (present->absent, absent->
// present, or different value), record the original value in PreviousFields.
func emitIfChangedString(out map[string]any, name, prevVal, currVal string, pPresent, cPresent uint64) {
	if pPresent == 0 && cPresent == 0 {
		return
	}
	if pPresent != 0 && cPresent != 0 && prevVal == currVal {
		return
	}
	if pPresent != 0 {
		out[name] = prevVal
	}
}

func emitIfChangedUint32(out map[string]any, name string, prevVal, currVal uint32, pPresent, cPresent uint64) {
	if pPresent == 0 && cPresent == 0 {
		return
	}
	if pPresent != 0 && cPresent != 0 && prevVal == currVal {
		return
	}
	if pPresent != 0 {
		out[name] = prevVal
	}
}

// emitIfChangedInt is the int counterpart (UInt8/UInt16).
func emitIfChangedInt(out map[string]any, name string, prevVal, currVal int, pPresent, cPresent uint64) {
	if pPresent == 0 && cPresent == 0 {
		return
	}
	if pPresent != 0 && cPresent != 0 && prevVal == currVal {
		return
	}
	if pPresent != 0 {
		out[name] = prevVal
	}
}

// emitIfChangedAmount handles Amount values, which are either string (XRP) or
// map[string]any (IOU/MPT).
func emitIfChangedAmount(out map[string]any, name string, prevVal, currVal any, pPresent, cPresent uint64) {
	if pPresent == 0 && cPresent == 0 {
		return
	}
	if pPresent != 0 && cPresent != 0 && equalAmount(prevVal, currVal) {
		return
	}
	if pPresent != 0 {
		out[name] = prevVal
	}
}

// emitIfChangedDeep handles compound values (STObject map[string]any,
// STArray []any, Issue/Number/XChainBridge map[string]any). reflect.DeepEqual
// is used because nested maps/arrays don't permit a typed shortcut.
func emitIfChangedDeep(out map[string]any, name string, prevVal, currVal any, pPresent, cPresent uint64) {
	if pPresent == 0 && cPresent == 0 {
		return
	}
	if pPresent != 0 && cPresent != 0 && reflect.DeepEqual(prevVal, currVal) {
		return
	}
	if pPresent != 0 {
		out[name] = prevVal
	}
}

// emitIfChangedStringSlice handles Vector256 ([]string of hex hashes).
func emitIfChangedStringSlice(out map[string]any, name string, prevVal, currVal []string, pPresent, cPresent uint64) {
	if pPresent == 0 && cPresent == 0 {
		return
	}
	if pPresent != 0 && cPresent != 0 && equalStringSlice(prevVal, currVal) {
		return
	}
	if pPresent != 0 {
		out[name] = prevVal
	}
}

// isZeroHexString reports whether s is a non-empty string consisting only of
// '0' characters. Used to filter default values for hash fields in
// CreatedNode.NewFields.
func isZeroHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

// amountIsDefault reports whether v is a default STAmount. rippled
// STAmount::isDefault() is `(mValue == 0) && native()` — only a zero XRP amount
// is default. An IOU or MPT amount decodes to a map and is never default.
func amountIsDefault(v any) bool {
	s, ok := v.(string)
	return ok && s == "0"
}

// issueIsDefault reports whether v is a default STIssue. rippled
// STIssue::isDefault() is `asset_ == xrpIssue()`; the codec decodes the XRP
// issue as the single-key map {"currency": "XRP"}.
func issueIsDefault(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	c, _ := m["currency"].(string)
	return len(m) == 1 && c == "XRP"
}

// numberIsDefault reports whether v is a default STNumber (zero). rippled
// STNumber::isDefault() is `value_ == Number()`; the codec decodes a zero
// Number as the string "0".
func numberIsDefault(v any) bool {
	s, ok := v.(string)
	return ok && s == "0"
}

// zeroAccountAddr is the base58 encoding of the all-zero AccountID, the value
// a default STAccount / XChainBridge door decodes to.
var zeroAccountAddr = func() string {
	s, _ := addresscodec.Encode(make([]byte, 20), []byte{addresscodec.AccountAddressPrefix}, addresscodec.AccountAddressLength)
	return s
}()

// xchainBridgeIsDefault reports whether v is a default STXChainBridge. rippled
// STXChainBridge::isDefault() requires all four doors/issues to be default
// (zero account / XRP issue); a real bridge always carries non-zero doors, so
// this only fires on a genuinely empty bridge. Doors decode to address
// strings; issues decode to Issue-shaped maps.
func xchainBridgeIsDefault(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, k := range [...]string{"LockingChainDoor", "IssuingChainDoor"} {
		s, _ := m[k].(string)
		if s != zeroAccountAddr {
			return false
		}
	}
	for _, k := range [...]string{"LockingChainIssue", "IssuingChainIssue"} {
		if !issueIsDefault(m[k]) {
			return false
		}
	}
	return true
}
