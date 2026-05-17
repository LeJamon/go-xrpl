package ledgerfields

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

// emitIfChangedUint32 is the uint32 counterpart of emitIfChangedString.
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
