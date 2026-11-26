package paychan

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// A Channel naming a ledger entry of another type is treated as a missing
// channel: the transaction is included with a tec code, charges the fee and
// consumes the sequence, and leaves the named entry in place.
func TestPayChan_ChannelNamesOtherEntryType(t *testing.T) {
	targets := []struct {
		name string
		key  func(t *testing.T, env *jtx.TestEnv, alice, gw *jtx.Account) keylet.Keylet
	}{
		{
			name: "AccountRoot",
			key: func(_ *testing.T, _ *jtx.TestEnv, alice, _ *jtx.Account) keylet.Keylet {
				return keylet.Account(alice.ID)
			},
		},
		{
			name: "Offer",
			key: func(t *testing.T, env *jtx.TestEnv, alice, gw *jtx.Account) keylet.Keylet {
				seq := env.Seq(alice)
				jtx.RequireTxSuccess(t, env.CreateOffer(alice, tx.NewXRPAmount(xrp(10)), jtx.USD(gw, 10)))
				env.Close()
				return keylet.Offer(alice.ID, seq)
			},
		},
	}

	txs := []struct {
		name string
		code string
		tx   func(alice *jtx.Account, channel string) tx.Transaction
	}{
		{
			name: "Claim",
			code: "tecNO_TARGET",
			tx: func(alice *jtx.Account, channel string) tx.Transaction {
				return ChannelClaim(alice, channel).Build()
			},
		},
		{
			name: "ClaimClose",
			code: "tecNO_TARGET",
			tx: func(alice *jtx.Account, channel string) tx.Transaction {
				return ChannelClaim(alice, channel).Close().Build()
			},
		},
		{
			name: "Fund",
			code: "tecNO_ENTRY",
			tx: func(alice *jtx.Account, channel string) tx.Transaction {
				return ChannelFund(alice, channel, xrp(1)).Build()
			},
		},
	}

	for _, target := range targets {
		for _, tc := range txs {
			t.Run(target.name+"/"+tc.name, func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				alice := jtx.NewAccount("alice")
				gw := jtx.NewAccount("gw")
				env.FundAmount(alice, uint64(jtx.XRP(10000)))
				env.FundAmount(gw, uint64(jtx.XRP(10000)))
				env.Close()

				k := target.key(t, env, alice, gw)
				require.True(t, env.LedgerEntryExists(k))

				balance := env.Balance(alice)
				seq := env.Seq(alice)

				result := env.Submit(tc.tx(alice, hex.EncodeToString(k.Key[:])))
				jtx.RequireTxClaimed(t, result, tc.code)
				require.True(t, result.Applied, "transaction must be included")
				require.Equal(t, seq+1, env.Seq(alice), "sequence must be consumed")
				require.Equal(t, balance-result.Fee, env.Balance(alice), "fee must be charged")
				require.NotZero(t, result.Fee)

				env.Close()
				require.True(t, env.LedgerEntryExists(k), "named entry must survive")
			})
		}
	}
}
