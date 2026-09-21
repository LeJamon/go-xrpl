package lending

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	loan "github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func configureLoanPayRules(t *testing.T, env *jtx.TestEnv, lendingEnabled, v11Enabled, cleanupEnabled bool) {
	t.Helper()
	features := []struct {
		name string
		id   [32]byte
		want bool
	}{
		{"LendingProtocol", amendment.FeatureLendingProtocol, lendingEnabled},
		{"SingleAssetVault", amendment.FeatureSingleAssetVault, lendingEnabled},
		{"MPTokensV1", amendment.FeatureMPTokensV1, lendingEnabled},
		{"LendingProtocolV1_1", amendment.FeatureLendingProtocolV1_1, v11Enabled},
		{"fixCleanup3_4_0", amendment.FeatureFixCleanup3_4_0, cleanupEnabled},
	}
	for _, feature := range features {
		if feature.want {
			env.EnableFeature(feature.name)
		} else {
			env.DisableFeature(feature.name)
		}
	}
	env.Close()
	for _, feature := range features {
		if got := env.Rules().Enabled(feature.id); got != feature.want {
			t.Fatalf("%s enabled=%v, want %v", feature.name, got, feature.want)
		}
	}
}

func TestLoanPayInvalidAmountEngineRulesMatrix(t *testing.T) {
	for _, lendingEnabled := range []bool{false, true} {
		for _, v11Enabled := range []bool{false, true} {
			for _, cleanupEnabled := range []bool{false, true} {
				name := fmt.Sprintf("lending=%t/v11=%t/cleanup=%t", lendingEnabled, v11Enabled, cleanupEnabled)
				t.Run(name, func(t *testing.T) {
					env := jtx.NewTestEnv(t)
					borrower := jtx.NewAccount("borrower")
					env.Fund(borrower)
					env.Close()
					configureLoanPayRules(t, env, lendingEnabled, v11Enabled, cleanupEnabled)

					want := ter.TemBAD_AMOUNT
					if !lendingEnabled {
						want = ter.TemDISABLED
					}
					for _, amount := range []int64{0, -1} {
						t.Run(fmt.Sprintf("amount=%d", amount), func(t *testing.T) {
							pay := loan.NewLoanPay(
								borrower.Address,
								strings.Repeat("1", 64),
								tx.NewXRPAmount(amount),
							)
							pay.Fee = strconv.FormatUint(env.BaseFee(), 10)
							seq := env.Seq(borrower)
							pay.SetSequence(seq)

							fee, feeErr := sign.CalculateBaseFee(pay, env.Ledger(), tx.EngineConfig{
								BaseFee: env.BaseFee(),
								Rules:   env.Rules(),
							})
							if feeErr != nil || fee != env.BaseFee() {
								t.Fatalf("CalculateBaseFee = %d, %v; want normal fee %d", fee, feeErr, env.BaseFee())
							}

							balance := env.Balance(borrower)
							result := env.Submit(pay)
							if result.Result != want {
								t.Fatalf("result = %s, want %s", result.Result, want)
							}
							if result.Applied || result.Fee != 0 || result.Metadata != nil {
								t.Fatalf("applied/fee/metadata = %v/%d/%#v, want false/0/nil", result.Applied, result.Fee, result.Metadata)
							}
							if got := env.Balance(borrower); got != balance {
								t.Fatalf("borrower balance = %d, want unchanged %d", got, balance)
							}
							if got := env.Seq(borrower); got != seq {
								t.Fatalf("borrower sequence = %d, want unchanged %d", got, seq)
							}
						})
					}
				})
			}
		}
	}
}
