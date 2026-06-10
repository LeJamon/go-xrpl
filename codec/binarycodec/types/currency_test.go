package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCurrency_ToJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected any
		err      bool
	}{
		{
			name: "pass - XRP currency",
			input: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
			expected: "XRP",
		},
		{
			name: "pass - 3 letter currency code",
			input: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x55, 0x53, 0x44, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
			expected: "USD",
		},
		{
			name: "pass - lowercase 3 letter currency round-trips unmodified",
			input: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x75, 0x73, 0x64, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
			expected: "usd",
		},
		{
			name: "pass - hex currency code",
			input: []byte{
				0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
				0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14,
			},
			expected: "0102030405060708090A0B0C0D0E0F1011121314",
		},
		{
			name: "pass - noCurrency sentinel renders 1",
			input: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
			},
			expected: "1",
		},
		{
			name: "pass - non-printable standard-form code renders hex",
			input: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x01, 0x02, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
			expected: "0000000000000000000000000102030000000000",
		},
		{
			name: "pass - standard-form XRP chars render hex",
			input: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x58, 0x52, 0x50, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
			expected: "0000000000000000000000005852500000000000",
		},
		{
			name:  "fail - truncated data",
			input: []byte{0x00, 0x01},
			err:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			currency := &Currency{}
			actual, err := currency.ToJSON(testParser(tc.input))

			if tc.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}

func TestCurrency_FromJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected []byte
		err      bool
	}{
		{
			name:  "pass - XRP currency",
			input: "XRP",
			expected: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
		},
		{
			name:  "pass - 3 letter currency code",
			input: "USD",
			expected: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x55, 0x53, 0x44, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
		},
		{
			name:  "pass - lowercase 3 letter currency code",
			input: "usd",
			expected: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x75, 0x73, 0x64, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
		},
		{
			name:  "pass - hex currency code",
			input: "0102030405060708090A0B0C0D0E0F1011121314",
			expected: []byte{
				0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
				0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14,
			},
		},
		{
			name:  "pass - noCurrency sentinel from 1",
			input: "1",
			expected: []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
			},
		},
		{
			name:  "fail - invalid currency type",
			input: 123,
			err:   true,
		},
		{
			name: "fail - short hex no longer accepted",
			// A 10-char hex string used to silently serialize 5 bytes and shift
			// every following field; it must be rejected.
			input: "0102030405",
			err:   true,
		},
		{
			name:  "fail - 3-char code outside the ISO alphabet",
			input: "U+D",
			err:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			currency := &Currency{}
			actual, err := currency.FromJSON(tc.input)

			if tc.err {
				require.Error(t, err)
				require.Nil(t, actual)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}

// TestCurrencyCode_AlphabetRoundTrip asserts encode→decode→encode is the
// identity for every character in the legal ISO alphabet, in every code
// position — including lowercase letters.
func TestCurrencyCode_AlphabetRoundTrip(t *testing.T) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz?!@#$%^&*<>(){}[]|"
	for _, c := range alphabet {
		for pos := range 3 {
			code := []byte{'A', 'A', 'A'}
			code[pos] = byte(c)
			if string(code) == "XRP" {
				continue
			}
			encoded, err := encodeCurrencyCode(string(code))
			require.NoError(t, err, "encode %q", code)
			decoded, err := decodeCurrencyCode(encoded)
			require.NoError(t, err, "decode %q", code)
			require.Equal(t, string(code), decoded)
			reencoded, err := encodeCurrencyCode(decoded)
			require.NoError(t, err)
			require.Equal(t, encoded, reencoded)
		}
	}
}
