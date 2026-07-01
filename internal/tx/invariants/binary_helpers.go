package invariants

import (
	"fmt"
)

// isXRPCurrency returns true if the given currency string represents XRP.
// XRP is the all-zero 160-bit currency, which the state amount parser may render
// as the empty string, the literal "XRP", or a 3-byte run of NUL bytes (the
// standard-code path reading an all-zero currency); it may also arrive as a
// 40-char hex string of all zeros. Besides the all-zero (native) currency, only
// the exact 3-char ISO "XRP" bad-currency (bytes 12-14 "XRP", every other byte
// zero) counts as XRP — a non-zero prefix (e.g. "Wellgistics XRP") is a valid IOU.
func isXRPCurrency(curr string) bool {
	if len(curr) == 0 || curr == "XRP" {
		return true
	}
	// All-NUL bytes (e.g. the 3-byte standard-code form of the zero currency).
	if isAllZeroBytes(curr) {
		return true
	}
	// Hex-encoded currency: 40 hex chars = 20 bytes
	if len(curr) == 40 {
		b, err := hexDecode20(curr)
		if err != nil {
			return false
		}
		// All zeros = XRP
		allZero := true
		for _, bb := range b {
			if bb != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return true
		}
		// rippled badCurrency() (UintTypes.cpp:132-137): bytes 12-14 "XRP" with
		// every other byte zero; "XRP" beside non-zero bytes is a valid IOU.
		if b[12] == 'X' && b[13] == 'R' && b[14] == 'P' {
			restZero := true
			for i := 0; i < 20; i++ {
				if (i < 12 || i > 14) && b[i] != 0 {
					restZero = false
					break
				}
			}
			if restZero {
				return true
			}
		}
	}
	return false
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
