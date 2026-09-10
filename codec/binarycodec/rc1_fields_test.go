package binarycodec

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRC1FieldEncodings(t *testing.T) {
	for _, field := range []struct {
		name   string
		header string
		width  int
		max    int64
	}{
		{"LEVersion", "0610", 2, math.MaxUint8},
		{"ContractResult", "001015", 2, math.MaxUint8},
		{"VaultKind", "001016", 2, math.MaxUint8},
		{"SubscriptionDate", "204B", 8, math.MaxUint32},
		{"RedemptionDate", "204C", 8, math.MaxUint32},
	} {
		t.Run(field.name, func(t *testing.T) {
			for _, value := range []int64{0, 1, field.max} {
				t.Run(fmt.Sprint(value), func(t *testing.T) {
					object := map[string]any{field.name: value}
					want := field.header + fmt.Sprintf("%0*X", field.width, value)
					encoded, err := Encode(object)
					require.NoError(t, err)
					require.Equal(t, want, encoded)
					decoded, err := Decode(want)
					require.NoError(t, err)
					require.Len(t, decoded, 1)
					require.EqualValues(t, value, decoded[field.name])
					roundTrip, err := Encode(decoded)
					require.NoError(t, err)
					require.Equal(t, want, roundTrip)
					signing, err := EncodeForSigning(object)
					require.NoError(t, err)
					require.Equal(t, "53545800"+want, signing)
				})
			}
			for _, value := range []any{int64(-1), field.max + 1, 0.5} {
				_, err := Encode(map[string]any{field.name: value})
				require.Error(t, err, "value %v", value)
			}
			for n := 0; n < field.width; n += 2 {
				_, err := Decode(field.header + strings.Repeat("00", n/2))
				require.Error(t, err, "truncated value with %d bytes", n/2)
			}
		})
	}
}

func TestRC1FieldCanonicalOrder(t *testing.T) {
	object := map[string]any{
		"VaultKind":        1,
		"SubscriptionDate": 2,
		"ContractResult":   3,
		"RedemptionDate":   4,
		"LEVersion":        5,
	}
	encoded, err := Encode(object)
	require.NoError(t, err)
	require.Equal(t, "204B00000002204C000000040610050010150300101601", encoded)
}

func TestRC1RemovedFields(t *testing.T) {
	for _, field := range []struct {
		name string
		blob string
	}{
		{"HookResult", "00101200"},
		{"HookStateChangeCount", "10110000"},
		{"EmitGeneration", "202E00000000"},
		{"HookInstructionCount", "30110000000000000000"},
		{"HookStateData", "701600"},
		{"Hook", "EEE1"},
		{"EmittedTxn", "E014E1"},
		{"Hooks", "FBF1"},
	} {
		t.Run(field.name, func(t *testing.T) {
			_, err := Encode(map[string]any{field.name: 0})
			require.ErrorIs(t, err, ErrUnknownField)
			_, err = Decode(field.blob)
			require.Error(t, err)
		})
	}
}

func TestRC1RetainsEmitDetails(t *testing.T) {
	zeroHash := strings.Repeat("0", 64)
	object := map[string]any{
		"EmitDetails": map[string]any{
			"EmitBurden":      "1",
			"EmitParentTxnID": zeroHash,
			"EmitNonce":       zeroHash,
			"EmitHookHash":    zeroHash,
			"EmitCallback":    "rrrrrrrrrrrrrrrrrrrrrhoLvTp",
		},
	}
	want := "ED3D0000000000000001" + "5B" + zeroHash + "5C" + zeroHash + "5D" + zeroHash + "8A14" + strings.Repeat("0", 40) + "E1"
	encoded, err := Encode(object)
	require.NoError(t, err)
	require.Equal(t, want, encoded)
	decoded, err := Decode(want)
	require.NoError(t, err)
	require.Equal(t, object, decoded)
	signing, err := EncodeForSigning(object)
	require.NoError(t, err)
	require.Equal(t, "53545800"+want, signing)
}
