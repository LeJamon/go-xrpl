package types

import (
	"bytes"
	"encoding/hex"
	"regexp"
	"strings"
)

// Frozen copies of the original regexp-based currency codec. They are the
// oracle for the differential tests and fuzz targets; do not modify them.

var refIOUCodeRegex = regexp.MustCompile(IOUCodeRegex)

func refDecodeCurrencyCode(data []byte) (string, error) {
	if len(data) != 20 {
		return "", errInvalidCurrencyCode
	}
	if bytes.Equal(data, zeroByteArray) {
		return "XRP", nil
	}
	if bytes.Equal(data, noCurrencyBytes) {
		return "1", nil
	}
	if isAllZero(data[0:12]) && isAllZero(data[15:20]) &&
		refIOUCodeRegex.Match(data[12:15]) &&
		!bytes.Equal(data[12:15], []byte("XRP")) {
		return string(data[12:15]), nil
	}
	return strings.ToUpper(hex.EncodeToString(data)), nil
}

func refEncodeCurrencyCode(currency string) ([]byte, error) {
	currency = strings.TrimPrefix(currency, "0x")

	switch {
	case currency == "XRP":
		return make([]byte, 20), nil

	case currency == "1":
		return append([]byte(nil), noCurrencyBytes...), nil

	case len(currency) == 3:
		if len(refIOUCodeRegex.FindAllString(currency, -1)) != 1 {
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
		return decodedHex, nil

	default:
		return nil, &InvalidCodeError{Disallowed: currency}
	}
}
