package types

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test vectors derived from rippled src/test/protocol/STAmount_test.cpp
// These tests ensure goXRPL serialization matches rippled exactly.

// TestXRPAmountSerialization tests XRP amount encoding (drops).
// Derived from testNativeCurrency() in STAmount_test.cpp
func TestXRPAmountSerialization(t *testing.T) {
	tests := []struct {
		name        string
		drops       string
		expectedHex string
		expectError bool
	}{
		// Basic values from rippled tests
		{
			name:        "zero XRP",
			drops:       "0",
			expectedHex: "4000000000000000",
			expectError: false,
		},
		{
			name:        "one drop",
			drops:       "1",
			expectedHex: "4000000000000001",
			expectError: false,
		},
		{
			name:        "100 drops",
			drops:       "100",
			expectedHex: "4000000000000064",
			expectError: false,
		},
		// Powers of 10 from testSetValue native currency tests
		{
			name:        "1 XRP in drops (1000000)",
			drops:       "1000000",
			expectedHex: "40000000000f4240",
			expectError: false,
		},
		{
			name:        "10 XRP in drops",
			drops:       "10000000",
			expectedHex: "4000000000989680",
			expectError: false,
		},
		{
			name:        "100 XRP in drops",
			drops:       "100000000",
			expectedHex: "4000000005f5e100",
			expectError: false,
		},
		{
			name:        "1000 XRP in drops",
			drops:       "1000000000",
			expectedHex: "400000003b9aca00",
			expectError: false,
		},
		{
			name:        "10000 XRP in drops",
			drops:       "10000000000",
			expectedHex: "40000002540be400",
			expectError: false,
		},
		// Edge cases
		{
			name:        "max XRP supply (100 billion XRP in drops)",
			drops:       "100000000000000000",
			expectedHex: "416345785d8a0000",
			expectError: false,
		},
		// Negative XRP drops are valid in the codec: ledger objects such as
		// NFToken sell offers may carry signed amounts. The absolute value is
		// encoded without the positive sign bit (matches rippled STAmount).
		{
			name:        "negative one drop",
			drops:       "-1",
			expectedHex: "0000000000000001",
			expectError: false,
		},
		// Invalid values
		{
			name:        "decimal XRP - should fail",
			drops:       "1.5",
			expectedHex: "",
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			amount := &Amount{}
			result, err := amount.FromJSON(tc.drops)

			if tc.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			actualHex := hex.EncodeToString(result)
			assert.Equal(t, tc.expectedHex, actualHex, "XRP amount encoding mismatch")
		})
	}
}

// TestXRPAmountDeserialization tests XRP amount decoding.
func TestXRPAmountDeserialization(t *testing.T) {
	defs := definitions.Get()

	tests := []struct {
		name          string
		inputHex      string
		expectedDrops string
	}{
		{
			name:          "zero XRP",
			inputHex:      "4000000000000000",
			expectedDrops: "0",
		},
		{
			name:          "one drop",
			inputHex:      "4000000000000001",
			expectedDrops: "1",
		},
		{
			name:          "100 drops",
			inputHex:      "4000000000000064",
			expectedDrops: "100",
		},
		{
			name:          "1 XRP",
			inputHex:      "40000000000F4240",
			expectedDrops: "1000000",
		},
		{
			name:          "10 million drops",
			inputHex:      "4000000000989680",
			expectedDrops: "10000000",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := hex.DecodeString(tc.inputHex)
			require.NoError(t, err)

			parser := serdes.NewBinaryParser(data, defs)
			amount := &Amount{}
			result, err := amount.ToJSON(parser)

			require.NoError(t, err)
			assert.Equal(t, tc.expectedDrops, result)
		})
	}
}

// TestIOUAmountSerialization tests IOU amount encoding with currency and issuer.
// Derived from testCustomCurrency() and testSetValue(iou) in STAmount_test.cpp
func TestIOUAmountSerialization(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		currency    string
		issuer      string
		expectedHex string
		expectError bool
	}{
		// Basic IOU values - from testSetValue(iou)
		{
			name:        "1 USD",
			value:       "1",
			currency:    "USD",
			issuer:      "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			expectedHex: "d4838d7ea4c680000000000000000000000000005553440000000000" + "0a20b3c85f482532a9578dbb3950b85ca06594d1",
			expectError: false,
		},
		{
			name:        "10 USD",
			value:       "10",
			currency:    "USD",
			issuer:      "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			expectedHex: "d4c38d7ea4c680000000000000000000000000005553440000000000" + "0a20b3c85f482532a9578dbb3950b85ca06594d1",
			expectError: false,
		},
		{
			name:        "100 USD",
			value:       "100",
			currency:    "USD",
			issuer:      "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			expectedHex: "d5038d7ea4c680000000000000000000000000005553440000000000" + "0a20b3c85f482532a9578dbb3950b85ca06594d1",
			expectError: false,
		},
		// Zero IOU
		{
			name:        "zero USD",
			value:       "0",
			currency:    "USD",
			issuer:      "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			expectedHex: "80000000000000000000000000000000000000005553440000000000" + "0a20b3c85f482532a9578dbb3950b85ca06594d1",
			expectError: false,
		},
		// Negative IOU - from testCustomCurrency
		{
			name:        "negative 2 USD",
			value:       "-2",
			currency:    "USD",
			issuer:      "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			expectedHex: "94871afd498d00000000000000000000000000005553440000000000" + "0a20b3c85f482532a9578dbb3950b85ca06594d1",
			expectError: false,
		},
		// Decimal values - from testSetValue(iou)
		{
			name:        "3.1 USD",
			value:       "3.1",
			currency:    "USD",
			issuer:      "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			expectedHex: "d48b036efecdc00000000000000000000000000055534400000000000a20b3c85f482532a9578dbb3950b85ca06594d1",
			expectError: false,
		},
		{
			name:        "0.31 USD",
			value:       "0.31",
			currency:    "USD",
			issuer:      "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			expectedHex: "d44b036efecdc00000000000000000000000000055534400000000000a20b3c85f482532a9578dbb3950b85ca06594d1",
			expectError: false,
		},
		// Different currency codes
		{
			name:        "1 EUR",
			value:       "1",
			currency:    "EUR",
			issuer:      "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			expectedHex: "d4838d7ea4c680000000000000000000000000004555520000000000" + "0a20b3c85f482532a9578dbb3950b85ca06594d1",
			expectError: false,
		},
		{
			name:        "1 BTC",
			value:       "1",
			currency:    "BTC",
			issuer:      "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			expectedHex: "d4838d7ea4c680000000000000000000000000004254430000000000" + "0a20b3c85f482532a9578dbb3950b85ca06594d1",
			expectError: false,
		},
		// XRP currency code should fail for IOU
		{
			name:        "XRP currency code - should fail",
			value:       "1",
			currency:    "XRP",
			issuer:      "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			expectedHex: "",
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			amount := &Amount{}
			input := map[string]any{
				"value":    tc.value,
				"currency": tc.currency,
				"issuer":   tc.issuer,
			}
			result, err := amount.FromJSON(input)

			if tc.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			actualHex := hex.EncodeToString(result)
			assert.Equal(t, tc.expectedHex, actualHex, "IOU amount encoding mismatch")
		})
	}
}

// TestIOUAmountDeserialization tests IOU amount decoding.
func TestIOUAmountDeserialization(t *testing.T) {
	defs := definitions.Get()

	tests := []struct {
		name             string
		inputHex         string
		expectedValue    string
		expectedCurrency string
		expectedIssuer   string
	}{
		{
			name:             "1 USD",
			inputHex:         "D4838D7EA4C680000000000000000000000000005553440000000000" + "0A20B3C85F482532A9578DBB3950B85CA06594D1",
			expectedValue:    "1",
			expectedCurrency: "USD",
			expectedIssuer:   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
		},
		{
			name:             "zero USD",
			inputHex:         "80000000000000000000000000000000000000005553440000000000" + "0A20B3C85F482532A9578DBB3950B85CA06594D1",
			expectedValue:    "0",
			expectedCurrency: "USD",
			expectedIssuer:   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
		},
		{
			name:             "negative 2 USD",
			inputHex:         "94871AFD498D00000000000000000000000000005553440000000000" + "0A20B3C85F482532A9578DBB3950B85CA06594D1",
			expectedValue:    "-2",
			expectedCurrency: "USD",
			expectedIssuer:   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := hex.DecodeString(tc.inputHex)
			require.NoError(t, err)

			parser := serdes.NewBinaryParser(data, defs)
			amount := &Amount{}
			result, err := amount.ToJSON(parser)

			require.NoError(t, err)

			resultMap, ok := result.(map[string]any)
			require.True(t, ok, "result should be a map for IOU amounts")

			assert.Equal(t, tc.expectedValue, resultMap["value"])
			assert.Equal(t, tc.expectedCurrency, resultMap["currency"])
			assert.Equal(t, tc.expectedIssuer, resultMap["issuer"])
		})
	}
}

// TestIOUExponentRange tests IOU amount exponent boundaries.
// The implementation uses adjusted exponent: adjustedExp = Scale + Precision - 16
// Valid range is: -96 <= adjustedExp <= 80
func TestIOUExponentRange(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		expectError bool
		errorType   string
	}{
		// Valid adjusted exponent range
		// For "1e-81": precision=1, scale=-81, adjusted = -81 + 1 - 16 = -96 (min valid)
		{
			name:        "minimum adjusted exponent value",
			value:       "1e-81",
			expectError: false,
		},
		// For "1e80": precision=1, scale=80, adjusted = 80 + 1 - 16 = 65 (within valid range)
		{
			name:        "maximum exponent value",
			value:       "1e80",
			expectError: false,
		},
		// For "1e95": precision=1, scale=95, adjusted = 95 + 1 - 16 = 80 (max valid)
		{
			name:        "maximum adjusted exponent value",
			value:       "1e95",
			expectError: false,
		},
		// Out of range - too small exponent
		// For "1e-82": adjusted = -82 + 1 - 16 = -97 (below min)
		{
			name:        "exponent too small",
			value:       "1e-82",
			expectError: true,
			errorType:   "Exponent",
		},
		// Out of range - too large exponent
		// For "1e96": adjusted = 96 + 1 - 16 = 81 (above max)
		{
			name:        "exponent too large",
			value:       "1e96",
			expectError: true,
			errorType:   "Exponent",
		},
		// Maximum precision (16 significant digits)
		{
			name:        "max precision 16 digits",
			value:       "9999999999999999",
			expectError: false,
		},
		// Exceeding maximum precision
		{
			name:        "precision exceeded - 17 digits",
			value:       "12345678901234567",
			expectError: true,
			errorType:   "Precision",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyIOUValue(tc.value)

			if tc.expectError {
				require.Error(t, err)
				if tc.errorType != "" {
					outOfRange, ok := err.(*OutOfRangeError)
					if ok {
						assert.Equal(t, tc.errorType, outOfRange.Type)
					}
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestAmountSerializationRoundtrip tests that amounts can be serialized and deserialized
// back to their original values. Derived from serializeAndDeserialize() in STAmount_test.cpp
func TestAmountSerializationRoundtrip(t *testing.T) {
	defs := definitions.Get()

	tests := []struct {
		name  string
		input any
	}{
		// XRP amounts
		{"zero XRP", "0"},
		{"1 drop", "1"},
		{"100 drops", "100"},
		{"1 XRP", "1000000"},
		{"1000 XRP", "1000000000"},
		// IOU amounts
		{
			"1 USD",
			map[string]any{
				"value":    "1",
				"currency": "USD",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
		{
			"100 USD",
			map[string]any{
				"value":    "100",
				"currency": "USD",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
		{
			"zero USD",
			map[string]any{
				"value":    "0",
				"currency": "USD",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
		{
			"negative 100 USD",
			map[string]any{
				"value":    "-100",
				"currency": "USD",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
		{
			"decimal 3.14159 EUR",
			map[string]any{
				"value":    "3.14159",
				"currency": "EUR",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			amount := &Amount{}

			// Serialize
			serialized, err := amount.FromJSON(tc.input)
			require.NoError(t, err)

			// Deserialize
			parser := serdes.NewBinaryParser(serialized, defs)
			deserialized, err := amount.ToJSON(parser)
			require.NoError(t, err)

			// Compare
			switch expected := tc.input.(type) {
			case string:
				// XRP amount
				assert.Equal(t, expected, deserialized)
			case map[string]any:
				// IOU amount
				result, ok := deserialized.(map[string]any)
				require.True(t, ok)
				assert.Equal(t, expected["value"], result["value"])
				assert.Equal(t, expected["currency"], result["currency"])
				assert.Equal(t, expected["issuer"], result["issuer"])
			}
		})
	}
}

// TestIOUConstants validates IOU encoding constants match rippled.
// From STAmount_test.cpp constants
func TestIOUConstants(t *testing.T) {
	// These constants must match rippled exactly for binary compatibility
	assert.Equal(t, -96, MinIOUExponent, "MinIOUExponent mismatch with rippled")
	assert.Equal(t, 80, MaxIOUExponent, "MaxIOUExponent mismatch with rippled")
	assert.Equal(t, 16, MaxIOUPrecision, "MaxIOUPrecision mismatch with rippled")
	assert.Equal(t, uint64(1000000000000000), uint64(MinIOUMantissa), "MinIOUMantissa mismatch with rippled")
	assert.Equal(t, uint64(9999999999999999), uint64(MaxIOUMantissa), "MaxIOUMantissa mismatch with rippled")
}

// TestXRPDropsValidation tests XRP drops value validation.
// Derived from testSetValue(native) in STAmount_test.cpp
func TestXRPDropsValidation(t *testing.T) {
	tests := []struct {
		name        string
		drops       string
		expectError bool
	}{
		// Valid values - single digits to large amounts
		{"1 drop", "1", false},
		{"22 drops", "22", false},
		{"333 drops", "333", false},
		{"4444 drops", "4444", false},
		{"55555 drops", "55555", false},
		{"666666 drops", "666666", false},
		// Powers of 10 XRP amounts
		{"1 XRP", "1000000", false},
		{"10 XRP", "10000000", false},
		{"100 XRP", "100000000", false},
		{"1000 XRP", "1000000000", false},
		{"10000 XRP", "10000000000", false},
		{"100000 XRP", "100000000000", false},
		{"1 million XRP", "1000000000000", false},
		{"10 million XRP", "10000000000000", false},
		{"100 million XRP", "100000000000000", false},
		{"1 billion XRP", "1000000000000000", false},
		{"10 billion XRP", "10000000000000000", false},
		{"100 billion XRP", "100000000000000000", false},
		// Invalid values from rippled tests
		{"decimal not allowed", "1.1", true},
		{"exceeds max drops", "100000000000000001", true},
		{"way over max", "1000000000000000000", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyXrpValue(tc.drops)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestCurrencyCodeValidation tests currency code validation.
// Related to testNativeCurrency() currency string tests
func TestCurrencyCodeValidation(t *testing.T) {
	tests := []struct {
		name        string
		currency    string
		expectError bool
	}{
		// Valid 3-character codes
		{"USD", "USD", false},
		{"EUR", "EUR", false},
		{"BTC", "BTC", false},
		{"JPY", "JPY", false},
		// Valid custom characters
		{"special chars A*B", "A*B", false},
		// Invalid - XRP is reserved
		{"XRP reserved", "XRP", true},
		// Invalid - wrong length
		{"too short", "US", true},
		{"too long", "USDD", true},
		// Valid 40-character hex codes
		{"hex USD", "0000000000000000000000005553440000000000", false},
		{"hex EUR", "0000000000000000000000004555520000000000", false},
		// Invalid hex - XRP code
		{"hex XRP reserved", "0000000000000000000000005852500000000000", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := serializeIssuedCurrencyCode(tc.currency)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestAmountBitMasks validates the bit manipulation constants used in amount encoding.
func TestAmountBitMasks(t *testing.T) {
	// NotXRPBitMask: If bit 0x80 is set, it's not XRP (it's an IOU)
	assert.Equal(t, byte(0x80), byte(NotXRPBitMask), "NotXRPBitMask should be 0x80")

	// PosSignBitMask: For positive amounts
	assert.Equal(t, uint64(0x4000000000000000), uint64(PosSignBitMask), "PosSignBitMask mismatch")

	// ZeroCurrencyAmountHex: For zero IOU amounts
	assert.Equal(t, uint64(0x8000000000000000), uint64(ZeroCurrencyAmountHex), "ZeroCurrencyAmountHex mismatch")
}

// TestIsNativeFunction tests the isNative helper function.
func TestIsNativeFunction(t *testing.T) {
	tests := []struct {
		name      string
		firstByte byte
		expected  bool
	}{
		{"XRP positive (0x40)", 0x40, true},
		{"XRP zero (0x00)", 0x00, true},
		{"IOU (0x80)", 0x80, false},
		{"IOU positive (0xC0)", 0xC0, false},
		{"IOU negative (0x80)", 0x80, false},
		{"IOU zero (0x80)", 0x80, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isNative(tc.firstByte)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestIsPositiveFunction tests the isPositive helper function.
func TestIsPositiveFunction(t *testing.T) {
	tests := []struct {
		name      string
		firstByte byte
		expected  bool
	}{
		{"positive XRP (0x40)", 0x40, true},
		{"zero XRP (0x00)", 0x00, false},
		{"positive IOU (0xC0)", 0xC0, true},
		{"negative IOU (0x80)", 0x80, false},
		{"positive (0x7F)", 0x7F, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isPositive(tc.firstByte)
			assert.Equal(t, tc.expected, result)
		})
	}
}
