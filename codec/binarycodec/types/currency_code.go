package types

import (
	"bytes"
	"encoding/hex"
	"strings"
)

// DecodeCurrencyCode decodes a 20-byte currency into its canonical string form
// using the same rippled to_string semantics as the amount codec. It is exported
// so state-layer SLE parsers render currencies byte-identically to the binary
// codec (e.g. a standard-form code with a non-ISO byte renders as full hex, not
// as a raw 3-char string), keeping parsed amounts comparable across the codebase.
func DecodeCurrencyCode(data []byte) (string, error) {
	return decodeCurrencyCode(data)
}

// decodeCurrencyCode decodes a 20-byte currency into its string representation,
// matching rippled's to_string(Currency) (UintTypes.cpp:53-81) in order: the
// all-zero code is "XRP", the noCurrency() sentinel is "1", a standard-form code
// (bytes 0-11 and 15-19 zero) is the 3-char ISO code only when those bytes are a
// printable code other than "XRP", and everything else renders as full hex.
// The ISO characters are returned unmodified — lowercase codes are legal and
// must round-trip byte-for-byte.
func decodeCurrencyCode(data []byte) (string, error) {
	if len(data) != 20 {
		return "", errInvalidCurrencyCode
	}

	if bytes.Equal(data, zeroByteArray) {
		return "XRP", nil
	}

	if bytes.Equal(data, noCurrencyBytes) {
		return "1", nil
	}

	// rippled forbids the ISO-style representation of the system code, so a
	// standard-form "XRP" falls through to hex (UintTypes.cpp:73).
	if isAllZero(data[0:12]) && isAllZero(data[15:20]) &&
		isISOCode(data[12:15]) &&
		!bytes.Equal(data[12:15], []byte("XRP")) {
		return string(data[12:15]), nil
	}

	return strings.ToUpper(hex.EncodeToString(data)), nil
}

// A decoded noCurrency value renders as "1", which is not valid user input.
func (source fieldSource) currencyText(code string) string {
	if source != jsonFields && code == "1" {
		return "0000000000000000000000000000000000000001"
	}
	return code
}

func encodeCurrencyCode(currency string) ([]byte, error) {
	switch {
	case currency == "" || currency == "XRP" || currency == "0":
		return make([]byte, 20), nil

	case len(currency) == 3:
		if !isISOCode(currency) {
			return nil, errInvalidCurrencyCode
		}
		currencyBytes := make([]byte, 20)
		copy(currencyBytes[12:], currency)
		return currencyBytes, nil

	case len(currency) == 40:
		decodedHex, err := hex.DecodeString(currency)
		if err != nil {
			return nil, err
		}
		// rippled treats a 40-char hex currency as opaque 160 bits
		// (to_currency → parseHex, UintTypes.cpp): the bytes are stored verbatim.
		// Canonicalization to a 3-char ISO code applies only to standard-form bytes
		// with a printable code, which the decoder already renders as 3 chars rather
		// than hex — so a hex string here must round-trip to exactly its bytes,
		// including non-printable or non-standard-position content.
		return decodedHex, nil

	default:
		return nil, &InvalidCodeError{Disallowed: currency}
	}
}

// isISOCode reports whether code is exactly three bytes from rippled's
// isoCharSet, the set IOUCodeRegex describes: ASCII letters and digits plus
// <>(){}[]|?!@#$%^&*.
func isISOCode[T string | []byte](code T) bool {
	if len(code) != 3 {
		return false
	}
	for i := range len(code) {
		if !isISOChar(code[i]) {
			return false
		}
	}
	return true
}

func isISOChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("<>(){}[]|?!@#$%^&*", c) >= 0
}
