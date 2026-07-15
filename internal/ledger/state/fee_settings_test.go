package state

import (
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
)

func TestFeeSettingsFees(t *testing.T) {
	tests := []struct {
		name string
		in   FeeSettings
		want drops.Fees
	}{
		{
			name: "modern",
			in: FeeSettings{
				XRPFeesMode:           true,
				BaseFeeDrops:          12,
				ReserveBaseDrops:      8_000_000,
				ReserveIncrementDrops: 3_000_000,
			},
			want: drops.Fees{Base: 12, Reserve: 8_000_000, Increment: 3_000_000},
		},
		{
			name: "legacy",
			in: FeeSettings{
				BaseFee:          11,
				ReserveBase:      7_000_000,
				ReserveIncrement: 1_000_000,
			},
			want: drops.Fees{Base: 11, Reserve: 7_000_000, Increment: 1_000_000},
		},
		{name: "defaults", want: drops.DefaultFees()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.in.Fees(); got != test.want {
				t.Fatalf("Fees() = %+v, want %+v", got, test.want)
			}
		})
	}
}
