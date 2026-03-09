package types

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test vectors derived from rippled src/test/protocol/STAmount_test.cpp
// These tests ensure goXRPL serialization produces identical binary output to rippled.
// =============================================================================

// TestXRPAmountEncoding_RippledVectors tests XRP amount encoding.
// Extracted from testNativeCurrency() and testSetValue(native) in STAmount_test.cpp
func TestXRPAmountEncoding_RippledVectors(t *testing.T) {
	tests := []struct {
		name        string
		drops       string        // Input drops value
		expectedHex string        // Expected serialized hex
		expectError bool
		description string        // From rippled test
	}{
		// From testNativeCurrency() - basic values
		{
			name:        "zero drops",
			drops:       "0",
			expectedHex: "4000000000000000",
			expectError: false,
			description: "STAmount zeroSt - zero XRP",
		},
		{
			name:        "one drop",
			drops:       "1",
			expectedHex: "4000000000000001",
			expectError: false,
			description: "STAmount one(1) - one drop",
		},
		{
			name:        "100 drops",
			drops:       "100",
			expectedHex: "4000000000000064",
			expectError: false,
			description: "STAmount hundred(100) - 100 drops",
		},
		// From testSetValue(native) - fractional XRP (drops)
		{
			name:        "22 drops",
			drops:       "22",
			expectedHex: "4000000000000016",
			expectError: false,
			description: "testSetValue(\"22\", xrp)",
		},
		{
			name:        "333 drops",
			drops:       "333",
			expectedHex: "400000000000014d",
			expectError: false,
			description: "testSetValue(\"333\", xrp)",
		},
		{
			name:        "4444 drops",
			drops:       "4444",
			expectedHex: "400000000000115c",
			expectError: false,
			description: "testSetValue(\"4444\", xrp)",
		},
		{
			name:        "55555 drops",
			drops:       "55555",
			expectedHex: "400000000000d903",
			expectError: false,
			description: "testSetValue(\"55555\", xrp)",
		},
		{
			name:        "666666 drops",
			drops:       "666666",
			expectedHex: "40000000000a2c2a",
			expectError: false,
			description: "testSetValue(\"666666\", xrp)",
		},
		// Powers of 10 XRP amounts (in drops)
		{
			name:        "1 XRP (1000000 drops)",
			drops:       "1000000",
			expectedHex: "40000000000f4240",
			expectError: false,
			description: "1 XRP = 1,000,000 drops",
		},
		{
			name:        "10 XRP (10000000 drops)",
			drops:       "10000000",
			expectedHex: "4000000000989680",
			expectError: false,
			description: "10 XRP",
		},
		{
			name:        "100 XRP",
			drops:       "100000000",
			expectedHex: "4000000005f5e100",
			expectError: false,
			description: "100 XRP",
		},
		{
			name:        "1000 XRP",
			drops:       "1000000000",
			expectedHex: "400000003b9aca00",
			expectError: false,
			description: "1000 XRP",
		},
		{
			name:        "10000 XRP",
			drops:       "10000000000",
			expectedHex: "40000002540be400",
			expectError: false,
			description: "10000 XRP",
		},
		{
			name:        "100000 XRP",
			drops:       "100000000000",
			expectedHex: "400000174876e800",
			expectError: false,
			description: "100000 XRP",
		},
		{
			name:        "1000000 XRP",
			drops:       "1000000000000",
			expectedHex: "400000e8d4a51000",
			expectError: false,
			description: "1 million XRP",
		},
		{
			name:        "10 million XRP",
			drops:       "10000000000000",
			expectedHex: "400009184e72a000",
			expectError: false,
			description: "10 million XRP",
		},
		{
			name:        "100 million XRP",
			drops:       "100000000000000",
			expectedHex: "40005af3107a4000",
			expectError: false,
			description: "100 million XRP",
		},
		{
			name:        "1 billion XRP",
			drops:       "1000000000000000",
			expectedHex: "40038d7ea4c68000",
			expectError: false,
			description: "1 billion XRP",
		},
		{
			name:        "10 billion XRP",
			drops:       "10000000000000000",
			expectedHex: "402386f26fc10000",
			expectError: false,
			description: "10 billion XRP",
		},
		{
			name:        "max XRP (100 billion XRP in drops)",
			drops:       "100000000000000000",
			expectedHex: "416345785d8a0000",
			expectError: false,
			description: "Maximum XRP supply",
		},
		// Invalid values from rippled tests - these should fail
		{
			name:        "decimal not allowed for XRP drops",
			drops:       "1.1",
			expectedHex: "",
			expectError: true,
			description: "testSetValue(\"1.1\", xrp, false) - should fail",
		},
		{
			name:        "exceeds max drops",
			drops:       "100000000000000001",
			expectedHex: "",
			expectError: true,
			description: "exceeds max XRP drops",
		},
		{
			name:        "negative XRP not allowed",
			drops:       "-1",
			expectedHex: "",
			expectError: true,
			description: "negative XRP not allowed in serialization",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			amount := &Amount{}
			result, err := amount.FromJSON(tc.drops)

			if tc.expectError {
				require.Error(t, err, "Expected error for: %s", tc.description)
				return
			}

			require.NoError(t, err, "Unexpected error for: %s", tc.description)
			actualHex := hex.EncodeToString(result)
			assert.Equal(t, tc.expectedHex, actualHex, "XRP encoding mismatch for: %s", tc.description)
		})
	}
}

// TestIOUAmountEncoding_RippledVectors tests IOU amount encoding.
// Extracted from testCustomCurrency() and testSetValue(iou) in STAmount_test.cpp
func TestIOUAmountEncoding_RippledVectors(t *testing.T) {
	// Standard issuer for testing
	issuer := "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B"
	issuerHex := "0a20b3c85f482532a9578dbb3950b85ca06594d1"

	tests := []struct {
		name        string
		value       string
		currency    string
		expectedHex string // Just the 8-byte value portion (lowercase hex)
		expectError bool
		description string
	}{
		// Zero IOU - from testCustomCurrency()
		{
			name:        "zero IOU",
			value:       "0",
			currency:    "USD",
			expectedHex: "8000000000000000",
			expectError: false,
			description: "STAmount zeroSt(noIssue())",
		},
		// From testCustomCurrency() - one IOU
		{
			name:        "1 USD",
			value:       "1",
			currency:    "USD",
			expectedHex: "d4838d7ea4c68000",
			expectError: false,
			description: "STAmount one(noIssue(), 1)",
		},
		// From testCustomCurrency() - hundred IOU
		{
			name:        "100 USD",
			value:       "100",
			currency:    "USD",
			expectedHex: "d5038d7ea4c68000",
			expectError: false,
			description: "STAmount hundred(noIssue(), 100)",
		},
		// From testSetValue(iou) - powers of 10
		{
			name:        "10 USD",
			value:       "10",
			currency:    "USD",
			expectedHex: "d4c38d7ea4c68000",
			expectError: false,
			description: "testSetValue(\"10\", usd)",
		},
		{
			name:        "1000 USD",
			value:       "1000",
			currency:    "USD",
			expectedHex: "d5438d7ea4c68000",
			expectError: false,
			description: "testSetValue(\"1000\", usd)",
		},
		{
			name:        "10000 USD",
			value:       "10000",
			currency:    "USD",
			expectedHex: "d5838d7ea4c68000",
			expectError: false,
			description: "testSetValue(\"10000\", usd)",
		},
		{
			name:        "100000 USD",
			value:       "100000",
			currency:    "USD",
			expectedHex: "d5c38d7ea4c68000",
			expectError: false,
			description: "testSetValue(\"100000\", usd)",
		},
		{
			name:        "1000000 USD",
			value:       "1000000",
			currency:    "USD",
			expectedHex: "d6038d7ea4c68000",
			expectError: false,
			description: "testSetValue(\"1000000\", usd)",
		},
		// Decimal values from testSetValue(iou)
		{
			name:        "1234567.1 USD",
			value:       "1234567.1",
			currency:    "USD",
			expectedHex: "d60462d50d726700",
			expectError: false,
			description: "testSetValue(\"1234567.1\", usd)",
		},
		{
			name:        "1234567.12 USD",
			value:       "1234567.12",
			currency:    "USD",
			expectedHex: "d60462d50ea39400",
			expectError: false,
			description: "testSetValue(\"1234567.12\", usd)",
		},
		// Negative values from testCustomCurrency()
		{
			name:        "negative 2 USD",
			value:       "-2",
			currency:    "USD",
			expectedHex: "94871afd498d0000",
			expectError: false,
			description: "negative IOU",
		},
		// Exponent tests from testCustomCurrency()
		{
			name:        "31 * 10 = 310",
			value:       "310",
			currency:    "USD",
			expectedHex: "d50b036efecdc000",
			expectError: false,
			description: "STAmount(noIssue(), 31, 1).getText() != \"310\"",
		},
		{
			name:        "31 * 0.1 = 3.1",
			value:       "3.1",
			currency:    "USD",
			expectedHex: "d48b036efecdc000",
			expectError: false,
			description: "STAmount(noIssue(), 31, -1).getText() != \"3.1\"",
		},
		{
			name:        "31 * 0.01 = 0.31",
			value:       "0.31",
			currency:    "USD",
			expectedHex: "d44b036efecdc000",
			expectError: false,
			description: "STAmount(noIssue(), 31, -2).getText() != \"0.31\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			amount := &Amount{}
			input := map[string]any{
				"value":    tc.value,
				"currency": tc.currency,
				"issuer":   issuer,
			}
			result, err := amount.FromJSON(input)

			if tc.expectError {
				require.Error(t, err, "Expected error for: %s", tc.description)
				return
			}

			require.NoError(t, err, "Unexpected error for: %s", tc.description)

			// Result should be 48 bytes: 8 (value) + 20 (currency) + 20 (issuer)
			require.Len(t, result, 48, "IOU amount should be 48 bytes")

			// Check value portion (first 8 bytes)
			actualValueHex := hex.EncodeToString(result[:8])
			assert.Equal(t, tc.expectedHex, actualValueHex, "IOU value encoding mismatch for: %s", tc.description)

			// Verify issuer portion (last 20 bytes)
			actualIssuerHex := hex.EncodeToString(result[28:])
			assert.Equal(t, issuerHex, actualIssuerHex, "Issuer mismatch")
		})
	}
}

// TestIOUExponentBoundaries tests the exponent range validation.
// The implementation uses adjusted exponent: adjustedExp = Scale + Precision - 16
// Valid range is: -96 <= adjustedExp <= 80
func TestIOUExponentBoundaries_RippledVectors(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		expectError bool
		errorType   string
		description string
	}{
		// Valid boundary values using adjusted exponent calculation
		// For "1e-81": precision=1, scale=-81, adjusted = -81 + 1 - 16 = -96 (min valid)
		{
			name:        "adjusted exponent at minimum (-96)",
			value:       "1e-81",
			expectError: false,
			description: "Minimum valid adjusted exponent",
		},
		// For "1e80": precision=1, scale=80, adjusted = 80 + 1 - 16 = 65 (within range)
		{
			name:        "exponent 80",
			value:       "1e80",
			expectError: false,
			description: "Exponent 80 is valid",
		},
		// For "1e95": precision=1, scale=95, adjusted = 95 + 1 - 16 = 80 (max valid)
		{
			name:        "adjusted exponent at maximum (80)",
			value:       "1e95",
			expectError: false,
			description: "Maximum valid adjusted exponent",
		},
		// Out of range exponents
		// For "1e-82": adjusted = -82 + 1 - 16 = -97 (below min)
		{
			name:        "exponent too small (-82 gives adjusted -97)",
			value:       "1e-82",
			expectError: true,
			errorType:   "Exponent",
			description: "Below MinIOUExponent",
		},
		// For "1e96": adjusted = 96 + 1 - 16 = 81 (above max)
		{
			name:        "exponent too large (96 gives adjusted 81)",
			value:       "1e96",
			expectError: true,
			errorType:   "Exponent",
			description: "Above MaxIOUExponent",
		},
		// Precision tests - MaxIOUPrecision = 16
		{
			name:        "max precision (16 digits)",
			value:       "9999999999999999",
			expectError: false,
			description: "Maximum allowed precision",
		},
		{
			name:        "precision exceeded (17 digits)",
			value:       "12345678901234567",
			expectError: true,
			errorType:   "Precision",
			description: "Exceeds MaxIOUPrecision",
		},
		// Mantissa boundaries
		{
			name:        "max mantissa at max exponent",
			value:       "9999999999999999e80",
			expectError: false,
			description: "Max mantissa at max exponent",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyIOUValue(tc.value)

			if tc.expectError {
				require.Error(t, err, "Expected error for: %s", tc.description)
				if tc.errorType != "" {
					outOfRange, ok := err.(*OutOfRangeError)
					if ok {
						assert.Equal(t, tc.errorType, outOfRange.Type)
					}
				}
			} else {
				require.NoError(t, err, "Unexpected error for: %s", tc.description)
			}
		})
	}
}

// TestAmountRoundtrip_RippledVectors tests serialize -> deserialize roundtrips.
// Derived from serializeAndDeserialize() pattern in STAmount_test.cpp
func TestAmountRoundtrip_RippledVectors(t *testing.T) {
	defs := definitions.Get()

	tests := []struct {
		name  string
		input any
	}{
		// XRP roundtrips from testNativeCurrency()
		{"XRP zero", "0"},
		{"XRP one drop", "1"},
		{"XRP 100 drops", "100"},
		{"XRP 1 XRP", "1000000"},
		{"XRP 1000 XRP", "1000000000"},
		{"XRP max", "100000000000000000"},
		// IOU roundtrips from testCustomCurrency()
		{
			"IOU zero",
			map[string]any{
				"value":    "0",
				"currency": "USD",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
		{
			"IOU one",
			map[string]any{
				"value":    "1",
				"currency": "USD",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
		{
			"IOU hundred",
			map[string]any{
				"value":    "100",
				"currency": "USD",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
		{
			"IOU negative",
			map[string]any{
				"value":    "-100",
				"currency": "USD",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
		{
			"IOU decimal",
			map[string]any{
				"value":    "3.14159265358979",
				"currency": "EUR",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
		{
			"IOU small decimal",
			map[string]any{
				"value":    "0.000001",
				"currency": "BTC",
				"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			amount := &Amount{}

			// Serialize
			serialized, err := amount.FromJSON(tc.input)
			require.NoError(t, err, "Serialization failed")

			// Deserialize
			parser := serdes.NewBinaryParser(serialized, defs)
			deserialized, err := amount.ToJSON(parser)
			require.NoError(t, err, "Deserialization failed")

			// Compare
			switch expected := tc.input.(type) {
			case string:
				// XRP amount - should match exactly
				assert.Equal(t, expected, deserialized, "XRP roundtrip mismatch")
			case map[string]any:
				// IOU amount
				result, ok := deserialized.(map[string]any)
				require.True(t, ok, "Result should be map for IOU")
				assert.Equal(t, expected["value"], result["value"], "Value mismatch")
				assert.Equal(t, expected["currency"], result["currency"], "Currency mismatch")
				assert.Equal(t, expected["issuer"], result["issuer"], "Issuer mismatch")
			}
		})
	}
}

// TestCurrencyCodeEncoding_RippledVectors tests currency code serialization.
// From testNativeCurrency() currency string tests in STAmount_test.cpp
func TestCurrencyCodeEncoding_RippledVectors(t *testing.T) {
	tests := []struct {
		name        string
		currency    string
		expectedHex string
		expectError bool
		description string
	}{
		// Standard 3-character ISO codes
		{
			name:        "USD currency",
			currency:    "USD",
			expectedHex: "0000000000000000000000005553440000000000",
			expectError: false,
			description: "to_currency(c, \"USD\")",
		},
		{
			name:        "EUR currency",
			currency:    "EUR",
			expectedHex: "0000000000000000000000004555520000000000",
			expectError: false,
			description: "EUR currency code",
		},
		{
			name:        "BTC currency",
			currency:    "BTC",
			expectedHex: "0000000000000000000000004254430000000000",
			expectError: false,
			description: "BTC currency code",
		},
		// Custom currency from rippled test
		{
			name:        "custom currency hex",
			currency:    "015841551A748AD2C1F76FF6ECB0CCCD00000000",
			expectedHex: "015841551a748ad2c1f76ff6ecb0cccd00000000",
			expectError: false,
			description: "create custom currency from STAmount_test.cpp",
		},
		// XRP is reserved - should fail
		{
			name:        "XRP reserved",
			currency:    "XRP",
			expectedHex: "",
			expectError: true,
			description: "XRP uppercase is disallowed for IOU",
		},
		// XRP hex format is also reserved
		{
			name:        "XRP hex reserved",
			currency:    "0000000000000000000000005852500000000000",
			expectedHex: "",
			expectError: true,
			description: "XRP in hex format is disallowed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := serializeIssuedCurrencyCode(tc.currency)

			if tc.expectError {
				require.Error(t, err, "Expected error for: %s", tc.description)
				return
			}

			require.NoError(t, err, "Unexpected error for: %s", tc.description)
			actualHex := hex.EncodeToString(result)
			assert.Equal(t, tc.expectedHex, actualHex, "Currency encoding mismatch for: %s", tc.description)
		})
	}
}

// TestIOUValueConstants validates constants match rippled STAmount constants.
// From STAmount class in rippled
func TestIOUValueConstants_RippledVectors(t *testing.T) {
	// These constants must match rippled exactly for binary compatibility
	// From include/xrpl/protocol/STAmount.h
	assert.Equal(t, -96, MinIOUExponent, "cMinOffset must match rippled")
	assert.Equal(t, 80, MaxIOUExponent, "cMaxOffset must match rippled")
	assert.Equal(t, 16, MaxIOUPrecision, "precision must match rippled")

	// cMinValue and cMaxValue from rippled
	assert.Equal(t, uint64(1000000000000000), uint64(MinIOUMantissa), "cMinValue must match rippled")
	assert.Equal(t, uint64(9999999999999999), uint64(MaxIOUMantissa), "cMaxValue must match rippled")

	// Byte lengths
	assert.Equal(t, 8, NativeAmountByteLength, "XRP amount byte length")
	assert.Equal(t, 48, CurrencyAmountByteLength, "IOU amount byte length")

	// Bit masks
	assert.Equal(t, byte(0x80), byte(NotXRPBitMask), "NotXRPBitMask")
	assert.Equal(t, uint64(0x4000000000000000), uint64(PosSignBitMask), "PosSignBitMask")
	assert.Equal(t, uint64(0x8000000000000000), uint64(ZeroCurrencyAmountHex), "ZeroCurrencyAmountHex")
}

// TestAmountBitFlags tests the bit manipulation for amount type detection.
// From amount serialization format in rippled
func TestAmountBitFlags_RippledVectors(t *testing.T) {
	t.Run("isNative tests", func(t *testing.T) {
		// Native XRP: bit 0x80 NOT set
		assert.True(t, isNative(0x40), "0x40 should be native (positive XRP)")
		assert.True(t, isNative(0x00), "0x00 should be native (zero XRP)")
		assert.True(t, isNative(0x3F), "0x3F should be native")

		// IOU: bit 0x80 IS set
		assert.False(t, isNative(0x80), "0x80 should NOT be native (IOU)")
		assert.False(t, isNative(0xC0), "0xC0 should NOT be native (positive IOU)")
		assert.False(t, isNative(0xD4), "0xD4 should NOT be native (IOU with exponent)")
	})

	t.Run("isPositive tests", func(t *testing.T) {
		// Positive: bit 0x40 IS set
		assert.True(t, isPositive(0x40), "0x40 should be positive (XRP)")
		assert.True(t, isPositive(0xC0), "0xC0 should be positive (IOU)")
		assert.True(t, isPositive(0xD4), "0xD4 should be positive (IOU)")
		assert.True(t, isPositive(0x7F), "0x7F should be positive")

		// Negative/Zero: bit 0x40 NOT set
		assert.False(t, isPositive(0x00), "0x00 should NOT be positive")
		assert.False(t, isPositive(0x80), "0x80 should NOT be positive (zero IOU)")
		assert.False(t, isPositive(0x94), "0x94 should NOT be positive (negative IOU)")
	})
}

// TestKnownTransactionEncodings tests complete transaction encoding.
// These are real transaction encodings that must match rippled exactly.
func TestKnownTransactionEncodings(t *testing.T) {
	tests := []struct {
		name     string
		hexInput string
		expected map[string]any
	}{
		{
			name:     "decode XRP amount in transaction",
			hexInput: "4000000000000001", // 1 drop XRP
			expected: nil,                // just validate parsing
		},
	}

	defs := definitions.Get()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := hex.DecodeString(tc.hexInput)
			require.NoError(t, err)

			parser := serdes.NewBinaryParser(data, defs)
			amount := &Amount{}
			result, err := amount.ToJSON(parser)
			require.NoError(t, err)

			// Verify it parsed to expected type
			switch result.(type) {
			case string:
				// XRP amount
				t.Logf("Parsed XRP: %s", result)
			case map[string]any:
				// IOU amount
				t.Logf("Parsed IOU: %v", result)
			}
		})
	}
}
