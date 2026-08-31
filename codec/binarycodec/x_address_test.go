package binarycodec

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/stretchr/testify/require"
)

func TestXAddressTagPresenceRoundTrip(t *testing.T) {
	t.Parallel()

	const classicAddress = "r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59"
	zero := uint32(0)
	nonzero := uint32(22)

	tagCases := []struct {
		name string
		tag  *uint32
	}{
		{name: "absent", tag: nil},
		{name: "zero", tag: &zero},
		{name: "nonzero", tag: &nonzero},
	}
	fieldCases := []struct {
		address string
		tag     string
	}{
		{address: "Destination", tag: "DestinationTag"},
		{address: "Account", tag: "SourceTag"},
	}

	for _, fieldCase := range fieldCases {
		for _, tagCase := range tagCases {
			t.Run(fieldCase.address+"/"+tagCase.name, func(t *testing.T) {
				xAddress, err := addresscodec.ClassicAddressToXAddress(classicAddress, tagCase.tag, addresscodec.Mainnet)
				require.NoError(t, err)

				encoded, err := Encode(map[string]any{fieldCase.address: xAddress})
				require.NoError(t, err)
				expectedFields := map[string]any{fieldCase.address: classicAddress}
				if tagCase.tag != nil {
					expectedFields[fieldCase.tag] = *tagCase.tag
				}
				expected, err := Encode(expectedFields)
				require.NoError(t, err)
				require.Equal(t, expected, encoded)

				decoded, err := Decode(encoded)
				require.NoError(t, err)
				require.Equal(t, classicAddress, decoded[fieldCase.address])
				if tagCase.tag == nil {
					require.NotContains(t, decoded, fieldCase.tag)
				} else {
					require.Contains(t, decoded, fieldCase.tag)
					require.Equal(t, *tagCase.tag, decoded[fieldCase.tag])
				}

				reencoded, err := Encode(decoded)
				require.NoError(t, err)
				require.Equal(t, encoded, reencoded)
			})
		}
	}
}

func TestXAddressExplicitZeroTagConflicts(t *testing.T) {
	t.Parallel()

	zero := uint32(0)
	xAddress, err := addresscodec.ClassicAddressToXAddress(
		"r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59",
		&zero,
		addresscodec.Mainnet,
	)
	require.NoError(t, err)
	one := uint32(1)
	nonzeroXAddress, err := addresscodec.ClassicAddressToXAddress(
		"r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59",
		&one,
		addresscodec.Mainnet,
	)
	require.NoError(t, err)

	testcases := []struct {
		address string
		tag     string
	}{
		{address: "Destination", tag: "DestinationTag"},
		{address: "Account", tag: "SourceTag"},
	}
	matchingValues := []struct {
		name  string
		value any
	}{
		{name: "uint32", value: uint32(0)},
		{name: "int", value: int(0)},
		{name: "int64", value: int64(0)},
		{name: "uint64", value: uint64(0)},
		{name: "float64", value: float64(0)},
	}
	malformedValues := []struct {
		name  string
		value any
	}{
		{name: "negative wraps to zero", value: int64(-1 << 32)},
		{name: "overflow wraps to zero", value: uint64(1 << 32)},
		{name: "fraction truncates to zero", value: float64(0.5)},
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
	}
	malformedNonzeroValues := []struct {
		name  string
		value any
	}{
		{name: "fraction truncates to one", value: float64(1.5)},
		{name: "overflow wraps to one", value: (uint64(1) << 32) + 1},
	}
	for _, tc := range testcases {
		t.Run(tc.address, func(t *testing.T) {
			for _, matching := range matchingValues {
				t.Run("matching/"+matching.name, func(t *testing.T) {
					_, err := Encode(map[string]any{
						tc.address: xAddress,
						tc.tag:     matching.value,
					})
					require.NoError(t, err)
				})
			}

			_, err := Encode(map[string]any{
				tc.address: xAddress,
				tc.tag:     float64(1),
			})
			require.ErrorContains(t, err, "duplicate "+tc.tag)

			for _, malformed := range malformedValues {
				t.Run("malformed/"+malformed.name, func(t *testing.T) {
					_, err := Encode(map[string]any{
						tc.address: xAddress,
						tc.tag:     malformed.value,
					})
					require.ErrorContains(t, err, "out of range for UInt32")
				})
			}

			for _, malformed := range malformedNonzeroValues {
				t.Run("malformed/nonzero/"+malformed.name, func(t *testing.T) {
					_, err := Encode(map[string]any{
						tc.address: nonzeroXAddress,
						tc.tag:     malformed.value,
					})
					require.ErrorContains(t, err, "out of range for UInt32")
				})
			}
		})
	}
}
