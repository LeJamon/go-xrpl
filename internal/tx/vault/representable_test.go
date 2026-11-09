package vault

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestIsRepresentable(t *testing.T) {
	xrp := tx.Asset{Currency: "XRP"}
	mpt := tx.Asset{MPTIssuanceID: "00000001AE123A8556F3CF91154711376AFB0F894F832B3D"}
	iou := tx.Asset{Currency: "USD", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}
	cases := []struct {
		name  string
		asset tx.Asset
		value string
		want  bool
	}{
		{"xrp_maximum", xrp, "100000000000000000", true},
		{"xrp_above_maximum", xrp, "100000000000000001", false},
		{"xrp_below_negative_maximum", xrp, "-100000000000000001", false},
		{"xrp_int64_maximum", xrp, "9223372036854775807", false},
		{"mpt_int64_maximum", mpt, "9223372036854775807", true},
		{"mpt_above_int64", mpt, "1e19", false},
		{"iou_maximum", iou, "9999999999999999e80", true},
		{"iou_above_maximum_exponent", iou, "1e96", false},
		{"iou_rounds_above_maximum", iou, "99999999999999995e79", false},
		{"iou_below_minimum_is_zero", iou, "1e-200", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := state.ParseXRPLNumber(c.value, state.MantissaScaleLarge, state.RoundToNearest)
			require.NoError(t, err)
			require.Equal(t, c.want, IsRepresentable(c.asset, n))
		})
	}
}
