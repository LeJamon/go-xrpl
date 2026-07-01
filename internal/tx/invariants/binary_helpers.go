package invariants

import (
	"fmt"
)

// badCurrencyBytes is rippled's badCurrency() (UintTypes.cpp:132-137): the
// forbidden 3-char ISO "XRP" code — bytes 12-14 "XRP", every other byte zero.
var badCurrencyBytes = [20]byte{12: 'X', 13: 'R', 14: 'P'}

// isNativeXRPCurrency returns true if the currency string renders the all-zero
// 160-bit (native XRP) currency: the empty string (a missing or native-form
// field), the literal "XRP", a run of NUL bytes, or all-zero hex. A currency
// whose bytes merely contain "XRP" beside non-zero bytes (e.g. "Wellgistics
// XRP") is a valid IOU, as is badCurrency.
func isNativeXRPCurrency(curr string) bool {
	if len(curr) == 0 || curr == "XRP" {
		return true
	}
	if isAllZeroBytes(curr) {
		return true
	}
	if len(curr) == 40 {
		b, err := hexDecode20(curr)
		if err != nil {
			return false
		}
		return b == [20]byte{}
	}
	return false
}

// isBadCurrency returns true iff the currency string is the hex rendering of
// badCurrencyBytes — the only form the canonical codec produces for it
// (to_string refuses the ISO "XRP" representation, so it never arrives as a
// 3-char string).
func isBadCurrency(curr string) bool {
	if len(curr) != 40 {
		return false
	}
	b, err := hexDecode20(curr)
	if err != nil {
		return false
	}
	return b == badCurrencyBytes
}

// isAllZeroBytes reports whether s is non-empty and every byte is NUL.
func isAllZeroBytes(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != 0 {
			return false
		}
	}
	return true
}

func hexDecode20(s string) ([20]byte, error) {
	var b [20]byte
	if len(s) != 40 {
		return b, fmt.Errorf("expected 40 hex chars, got %d", len(s))
	}
	for i := range 20 {
		hi := hexVal(s[i*2])
		lo := hexVal(s[i*2+1])
		if hi < 0 || lo < 0 {
			return b, fmt.Errorf("invalid hex char")
		}
		b[i] = byte(hi<<4 | lo)
	}
	return b, nil
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
