package state

import "testing"

func TestRoundToAssetScale_IOUTwoStageRounding(t *testing.T) {
	tests := []struct {
		name     string
		mantissa int64
		exponent int
		scale    MantissaScale
		mode     RoundingMode
		want     string
	}{
		{"large nearest 1.49996", 149996, -5, MantissaScaleLarge, RoundToNearest, "2"},
		{"large nearest 2.50004", 250004, -5, MantissaScaleLarge, RoundToNearest, "2"},
		{"large nearest -1.49996", -149996, -5, MantissaScaleLarge, RoundToNearest, "-2"},
		{"large nearest -2.50004", -250004, -5, MantissaScaleLarge, RoundToNearest, "-2"},
		{"large nearest 1.4994", 14994, -4, MantissaScaleLarge, RoundToNearest, "1"},
		{"large nearest 2.501", 2501, -3, MantissaScaleLarge, RoundToNearest, "3"},
		{"legacy nearest 1.49996", 149996, -5, MantissaScaleLargeLegacy, RoundToNearest, "2"},
		{"large downward 1.49996", 149996, -5, MantissaScaleLarge, RoundDownward, "1"},
		{"large upward 1.49996", 149996, -5, MantissaScaleLarge, RoundUpward, "2"},
		{"large toward zero 2.50004", 250004, -5, MantissaScaleLarge, RoundTowardsZero, "2"},
		{"large upward 2.50004", 250004, -5, MantissaScaleLarge, RoundUpward, "3"},
		{"small nearest 1.49996", 149996, -5, MantissaScaleSmall, RoundToNearest, "1"},
		{"small nearest 2.50004", 250004, -5, MantissaScaleSmall, RoundToNearest, "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := NewXRPLNumberScaled(tt.mantissa, tt.exponent, tt.scale, RoundToNearest)
			got := n.RoundToAssetScale(false, 0, tt.mode)
			if got.String() != tt.want {
				t.Fatalf("RoundToAssetScale(%s, 0, %v) = %s, want %s", n, tt.mode, got, tt.want)
			}
		})
	}
}
