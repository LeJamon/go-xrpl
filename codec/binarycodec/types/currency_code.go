package types

import (
	"bytes"
	"encoding/hex"
	"strings"
)

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
		iouCodeRegex.Match(data[12:15]) &&
		!bytes.Equal(data[12:15], []byte("XRP")) {
		return string(data[12:15]), nil
	}

	return strings.ToUpper(hex.EncodeToString(data)), nil
}

// encodeCurrencyCode encodes a currency string into its 20-byte representation:
// "XRP" is the all-zero code, "1" is rippled's noCurrency() sentinel as rendered
// by to_string (UintTypes.cpp:59-60; rippled reaches noCurrency via to_currency's
// parse-failure fallback — the codec special-cases "1" only, keeping other
// unparseable codes an error), a 3-char code in the legal ISO alphabet fills
// bytes 12-14, and a 40-char hex string is stored verbatim.
func encodeCurrencyCode(currency string) ([]byte, error) {
	currency = strings.TrimPrefix(currency, "0x")

	switch {
	case currency == "XRP":
		return make([]byte, 20), nil

	case currency == "1":
		return append([]byte(nil), noCurrencyBytes...), nil

	case len(currency) == 3:
		if len(iouCodeRegex.FindAllString(currency, -1)) != 1 {
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
