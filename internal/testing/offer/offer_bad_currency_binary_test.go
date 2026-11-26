package offer

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// badCurrencyHex is the reserved ISO "XRP" 160-bit code as the binary codec
// renders it.
const badCurrencyHex = "0000000000000000000000005852500000000000"

func TestOfferCreate_BadCurrencySignedBinary(t *testing.T) {
	tests := []struct {
		name      string
		takerPays func(gw *jtx.Account) tx.Amount
		takerGets func(gw *jtx.Account) tx.Amount
	}{
		{
			name:      "TakerPays",
			takerPays: func(gw *jtx.Account) tx.Amount { return tx.NewIssuedAmountFromFloat64(10, badCurrencyHex, gw.Address) },
			takerGets: func(*jtx.Account) tx.Amount { return tx.NewXRPAmount(10_000_000) },
		},
		{
			name:      "TakerGets",
			takerPays: func(*jtx.Account) tx.Amount { return tx.NewXRPAmount(10_000_000) },
			takerGets: func(gw *jtx.Account) tx.Amount { return tx.NewIssuedAmountFromFloat64(10, badCurrencyHex, gw.Address) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice := jtx.NewAccount("alice")
			gw := jtx.NewAccount("gateway")
			env.Fund(alice, gw)
			env.Close()

			seq, balance := env.Seq(alice), env.Balance(alice)
			result := env.SubmitSignedBinary(OfferCreate(alice, tc.takerPays(gw), tc.takerGets(gw)).Build())

			jtx.RequireTxFail(t, result, jtx.TemBAD_CURRENCY)
			require.False(t, result.Applied)
			require.Equal(t, seq, env.Seq(alice))
			require.Equal(t, balance, env.Balance(alice))
			RequireOfferCount(t, env, alice, 0)
		})
	}
}
