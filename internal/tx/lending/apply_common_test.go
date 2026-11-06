package lending

import (
	"math"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	codecTypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

// assetValueRules returns rules with fixCleanup3_5_0 enabled or not.
func assetValueRules(cleanup bool) *amendment.Rules {
	if cleanup {
		return amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_5_0})
	}
	return amendment.EmptyRules()
}

func TestCheckAssetValueNativeBoundary(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  ter.Result
	}{
		{"0", ter.TesSUCCESS},
		{"100000000000000000", ter.TesSUCCESS},
		{"100000000000000001", ter.TefEXCEPTION},
		{"100000000000000000.4", ter.TecPRECISION_LOSS},
		{"100000000000000000.5", ter.TecPRECISION_LOSS},
		{"100000000000000000.6", ter.TefEXCEPTION},
		{"-100000000000000000", ter.TesSUCCESS},
		{"-100000000000000001", ter.TefEXCEPTION},
		{"-100000000000000000.4", ter.TecPRECISION_LOSS},
		{"-100000000000000000.5", ter.TecPRECISION_LOSS},
		{"-100000000000000000.6", ter.TefEXCEPTION},
	} {
		t.Run(tc.value, func(t *testing.T) {
			for _, cleanup := range []bool{false, true} {
				// Under fixCleanup3_5_0 a value outside the native range is
				// tecPRECISION_LOSS instead of tefEXCEPTION.
				want := tc.want
				if cleanup && want == ter.TefEXCEPTION {
					want = ter.TecPRECISION_LOSS
				}
				for _, scale := range []state.MantissaScale{
					state.MantissaScaleLargeLegacy,
					state.MantissaScaleLarge320,
					state.MantissaScaleLarge330,
				} {
					n, err := state.ParseXRPLNumber(tc.value, scale, state.RoundToNearest)
					require.NoError(t, err)
					require.Equal(t, want, checkAssetValue(n, tx.Asset{Currency: "XRP"}, assetValueRules(cleanup)),
						"scale %d, fixCleanup3_5_0 %t", scale, cleanup)
				}
			}
		})
	}
}

func TestCheckAssetValueMPTWireBoundaries(t *testing.T) {
	asset := tx.Asset{MPTIssuanceID: strings.Repeat("A", 48)}
	cases := []struct {
		name  string
		value string
	}{
		{name: "exact signed wire maximum", value: "9223372036854775807"},
		{name: "near maximum", value: "9223372036854775806"},
		// This is a valid scientific-form wire value just below 2^63, not
		// the rejected raw 2^63 string.
		{name: "valid scientific nearby value", value: "922337203685477580e1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := new(codecTypes.Number).FromJSON(tc.value)
			require.NoError(t, err)
			require.NotEmpty(t, wire)

			for _, scale := range []state.MantissaScale{
				state.MantissaScaleLargeLegacy,
				state.MantissaScaleLarge320,
				state.MantissaScaleLarge330,
			} {
				n, err := state.ParseXRPLNumber(tc.value, scale, state.RoundToNearest)
				require.NoError(t, err)
				for _, cleanup := range []bool{false, true} {
					require.Equal(t, ter.TesSUCCESS, checkAssetValue(n, asset, assetValueRules(cleanup)),
						"scale %d, fixCleanup3_5_0 %t", scale, cleanup)
				}
			}
		})
	}

	_, err := new(codecTypes.Number).FromJSON("9223372036854775808")
	require.Error(t, err, "the raw 2^63 JSON string is not a valid STNumber wire input")

	// The exact signed-wire maximum is also the normalized Large330 endpoint.
	// A sub-unit cusp operand keeps the result at that legal MPT value.
	max := state.NewXRPLNumberScaled(math.MaxInt64, 0, state.MantissaScaleLarge330, state.RoundToNearest)
	cusp := max.Add(state.NewXRPLNumberScaled(3, -1, state.MantissaScaleLarge330, state.RoundToNearest))
	require.Equal(t, max, cusp)
	for _, cleanup := range []bool{false, true} {
		require.Equal(t, ter.TesSUCCESS, checkAssetValue(cusp, asset, assetValueRules(cleanup)))
	}
}

func TestCheckAssetValueIOUPrecisionAndUnderflow(t *testing.T) {
	asset := tx.Asset{Currency: "USD", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}
	cases := []struct {
		name     string
		mantissa int64
		exponent int
		want     ter.Result
	}{
		{
			name:     "exact sixteen digit precision",
			mantissa: 1234567890123456,
			exponent: -6,
			want:     ter.TesSUCCESS,
		},
		{
			name:     "seventeenth digit loses precision",
			mantissa: 12345678901234567,
			exponent: -6,
			want:     ter.TecPRECISION_LOSS,
		},
		{
			name:     "below IOU exponent range underflows",
			mantissa: 1,
			exponent: -82,
			want:     ter.TecPRECISION_LOSS,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, scale := range []state.MantissaScale{
				state.MantissaScaleLargeLegacy,
				state.MantissaScaleLarge320,
				state.MantissaScaleLarge330,
			} {
				value := state.NewXRPLNumberScaled(tc.mantissa, tc.exponent, scale, state.RoundToNearest)
				for _, cleanup := range []bool{false, true} {
					require.Equal(t, tc.want, checkAssetValue(value, asset, assetValueRules(cleanup)),
						"scale %d, fixCleanup3_5_0 %t", scale, cleanup)
				}
			}
		})
	}
}
