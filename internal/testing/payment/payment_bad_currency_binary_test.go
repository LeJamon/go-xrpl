package payment

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// badCurrencyHex is the reserved ISO "XRP" 160-bit code as the binary codec
// renders it.
const badCurrencyHex = "0000000000000000000000005852500000000000"

func TestPayment_BadCurrencySignedBinary(t *testing.T) {
	tests := []struct {
		name  string
		build func(alice, bob, gw *jtx.Account) tx.Transaction
	}{
		{
			name: "Amount",
			build: func(alice, bob, gw *jtx.Account) tx.Transaction {
				return PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(10, badCurrencyHex, gw.Address)).Build()
			},
		},
		{
			name: "SendMax",
			build: func(alice, bob, gw *jtx.Account) tx.Transaction {
				return Pay(alice, bob, 1_000_000).
					SendMax(tx.NewIssuedAmountFromFloat64(10, badCurrencyHex, gw.Address)).
					Build()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			gw := jtx.NewAccount("gateway")
			env.Fund(alice, bob, gw)
			env.Close()

			seq, balance := env.Seq(alice), env.Balance(alice)
			result := env.SubmitSignedBinary(tc.build(alice, bob, gw))

			jtx.RequireTxFail(t, result, jtx.TemBAD_CURRENCY)
			require.False(t, result.Applied)
			require.Equal(t, seq, env.Seq(alice))
			require.Equal(t, balance, env.Balance(alice))
		})
	}
}
