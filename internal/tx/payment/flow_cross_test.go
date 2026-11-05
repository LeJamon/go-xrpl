package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestFlowCrossReserveBoundary(t *testing.T) {
	for _, sponsored := range []bool{false, true} {
		name := "owner"
		if sponsored {
			name = "sponsor"
		}
		t.Run(name, func(t *testing.T) {
			view := newPaymentMockLedgerView()
			taker, issuer, owner := [20]byte{1}, [20]byte{2}, [20]byte{3}
			view.createAccount(taker, 29_743_827, 130)
			view.createAccount(issuer, 100_000_000, 0)
			cancelOwner, sponsor := taker, ""
			if sponsored {
				account, err := tx.ReadAccountRoot(view, taker)
				require.NoError(t, err)
				account.OwnerCount = 129
				account.SponsoringOwnerCount = 1
				data, err := state.SerializeAccountRoot(account)
				require.NoError(t, err)
				require.NoError(t, view.Update(keylet.Account(taker), data))
				view.createAccount(owner, 1_000_000, 1)
				account, err = tx.ReadAccountRoot(view, owner)
				require.NoError(t, err)
				account.SponsoredOwnerCount = 1
				data, err = state.SerializeAccountRoot(account)
				require.NoError(t, err)
				require.NoError(t, view.Update(keylet.Account(owner), data))
				cancelOwner, sponsor = owner, state.EncodeAccountIDSafe(taker)
			}

			outer := NewPaymentSandbox(view)
			cancel := NewChildSandbox(outer)
			require.NoError(t, tx.DecreaseOwnerCountOnView(cancel, cancelOwner, sponsor, 1))
			require.NoError(t, cancel.Apply(outer))
			gets := tx.NewXRPAmount(3_000_000)
			funds, result := AccountFundsInSandbox(outer, taker, gets, true, 1_000_000, 200_000)
			require.Equal(t, ter.TesSUCCESS, result)
			require.Equal(t, int64(2_743_827), funds.Drops())

			cross := FlowCross(outer, taker, gets,
				tx.NewIssuedAmount(5, 0, "USD", state.EncodeAccountIDSafe(issuer)),
				[32]byte{}, 1, FlowCrossParams{ReserveBase: 1_000_000, ReserveIncrement: 200_000})
			require.Equal(t, ter.TecPATH_DRY, cross.Result)
			require.NotNil(t, cross.Sandbox)
			funds, result = AccountFundsInSandbox(cross.Sandbox, taker, gets, true, 1_000_000, 200_000)
			require.Equal(t, ter.TesSUCCESS, result)
			require.Equal(t, int64(2_943_827), funds.Drops())

			// A deletion inside crossing must not release another reserve.
			child := NewChildSandbox(cross.Sandbox)
			require.NoError(t, tx.DecreaseOwnerCountOnView(child, taker, "", 1))
			require.NoError(t, child.Apply(cross.Sandbox))
			funds, result = AccountFundsInSandbox(cross.Sandbox, taker, gets, true, 1_000_000, 200_000)
			require.Equal(t, ter.TesSUCCESS, result)
			require.Equal(t, int64(2_943_827), funds.Drops())
		})
	}
}
