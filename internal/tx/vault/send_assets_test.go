package vault

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestSendAssetsIOUNoAccountIssuer(t *testing.T) {
	for _, senderHigh := range []bool{false, true} {
		name := "sender-low"
		if senderHigh {
			name = "sender-high"
		}
		t.Run(name, func(t *testing.T) {
			sender := [20]byte{0: 0x11}
			if senderHigh {
				sender[0] = 0x44
			}
			receivers := [][20]byte{{0: 0x22}, {0: 0x33}}
			view := newMPTArmsView()
			ctx := buildArmsCtx(t, view, sender, rulesWithFix(true))
			for _, receiver := range receivers {
				receiverAddress, err := state.EncodeAccountID(receiver)
				require.NoError(t, err)
				account, err := state.SerializeAccountRoot(&state.AccountRoot{
					Account: receiverAddress, Balance: 100_000_000, Sequence: 1,
				})
				require.NoError(t, err)
				require.NoError(t, view.Insert(keylet.Account(receiver), account))
				low, high := ctx.Account.Account, receiverAddress
				balance := state.NewIssuedAmountFromValue(100, 0, "USD", state.AccountOneAddress)
				if senderHigh {
					low, high = high, low
					balance = balance.Negate()
				}
				line, err := state.SerializeRippleState(&state.RippleState{
					Balance:   balance,
					LowLimit:  state.NewIssuedAmountFromValue(1000, 0, "USD", low),
					HighLimit: state.NewIssuedAmountFromValue(1000, 0, "USD", high),
				})
				require.NoError(t, err)
				require.NoError(t, view.Insert(keylet.Line(sender, receiver, "USD"), line))
			}

			asset := tx.Asset{Currency: "USD", Issuer: state.AccountOneAddress}
			result := SendAssets(ctx, sender, asset, []AssetPayment{
				{Account: receivers[0], Amount: ctx.NumberContext().Int(3)},
				{Account: receivers[1], Amount: ctx.NumberContext().Int(4)},
				{Account: receivers[0], Amount: ctx.NumberContext().Int(2)},
			})
			require.Equal(t, ter.TesSUCCESS, result)
			for i, receiver := range receivers {
				line, err := tx.ReadRippleState(view, sender, receiver, "USD")
				require.NoError(t, err)
				require.NotNil(t, line)
				balance := line.Balance
				if senderHigh {
					balance = balance.Negate()
				}
				want := state.NewIssuedAmountFromValue(int64(95+i), 0, "USD", state.AccountOneAddress)
				require.Equal(t, 0, balance.Compare(want))
			}
			require.Len(t, view.data, 5)
		})
	}
}
