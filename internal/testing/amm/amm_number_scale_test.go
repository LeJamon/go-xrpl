package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
)

func TestAMMDepositUsesRulesSelectedNumberScale(t *testing.T) {
	tests := []struct {
		name            string
		largeScale      bool
		wantPoolDrops   uint64
		wantSenderDelta uint64
		wantLPTMantissa int64
		wantLPTExponent int
	}{
		{
			name:            "Large",
			largeScale:      true,
			wantPoolDrops:   15_000_000,
			wantSenderDelta: 5_000_010,
			wantLPTMantissa: 1_224_744_871_391_589,
			wantLPTExponent: -11,
		},
		{
			name:            "Small",
			largeScale:      false,
			wantPoolDrops:   14_999_999,
			wantSenderDelta: 5_000_009,
			wantLPTMantissa: 1_224_744_830_566_758,
			wantLPTExponent: -11,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			if !test.largeScale {
				env.DisableFeature("SingleAssetVault")
				env.DisableFeature("LendingProtocol")
			}
			env.FundWithIOUs(30, 0)

			create := amm.AMMCreate(
				env.Alice,
				amm.XRPAmount(10),
				amm.IOUAmount(env.GW, "USD", 10),
			).Build()
			jtx.RequireTxSuccess(t, env.Submit(create))
			env.Close()

			ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
			if ammAccount == nil {
				t.Fatal("AMM account not found")
			}
			senderBefore := env.Balance(env.Carol)

			deposit := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
				Amount(amm.XRPAmount(5)).
				SingleAsset().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(deposit))

			if got := env.AMMPoolXRP(ammAccount); got != test.wantPoolDrops {
				t.Fatalf("AMM XRP balance = %d, want %d", got, test.wantPoolDrops)
			}
			if got := senderBefore - env.Balance(env.Carol); got != test.wantSenderDelta {
				t.Fatalf("sender balance delta = %d, want %d", got, test.wantSenderDelta)
			}

			ammData := env.ReadAMMData(amm.XRP(), env.USD)
			if ammData == nil {
				t.Fatal("AMM data not found")
			}
			if got := ammData.LPTokenBalance.Mantissa(); got != test.wantLPTMantissa {
				t.Fatalf("LPTokenBalance mantissa = %d, want %d", got, test.wantLPTMantissa)
			}
			if got := ammData.LPTokenBalance.Exponent(); got != test.wantLPTExponent {
				t.Fatalf("LPTokenBalance exponent = %d, want %d", got, test.wantLPTExponent)
			}
		})
	}
}
