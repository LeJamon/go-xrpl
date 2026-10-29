package lending

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

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
			for _, scale := range []state.MantissaScale{
				state.MantissaScaleLargeLegacy,
				state.MantissaScaleLarge320,
				state.MantissaScaleLarge330,
			} {
				n, err := state.ParseXRPLNumber(tc.value, scale, state.RoundToNearest)
				require.NoError(t, err)
				require.Equal(t, tc.want, checkAssetValue(n, tx.Asset{Currency: "XRP"}), "scale %d", scale)
			}
		})
	}
}
