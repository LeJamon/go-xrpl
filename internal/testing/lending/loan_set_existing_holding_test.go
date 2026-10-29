package lending_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestLoanSetExistingHoldingsAfterDefaultRippleCleared(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "cleanup-off"
		if cleanup {
			name = "cleanup-on"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name            string
				borrowerHolding bool
				ownerHolding    bool
				originationFee  bool
			}{
				{"both holdings without fee", true, true, false},
				{"both holdings with fee", true, true, true},
				{"owner missing without fee reaches authorization", true, false, false},
				{"owner holding required for fee", true, false, true},
				{"borrower holding required", false, true, false},
				{"both holdings missing", false, false, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					env := newTwoHoldingLoanEnv(t, false, cleanup)
					f := newLoanSetAssetFixtureWithEnv(t, env, "IOU", false)
					if tc.borrowerHolding {
						f.fundHolding(f.borrower, 1000)
					}
					if tc.ownerHolding {
						f.fundHolding(f.owner, 1000)
					}
					env.Close()
					require.Equal(t, tc.borrowerHolding, env.LedgerEntryExists(f.holdingKey(f.borrower)))
					require.Equal(t, tc.ownerHolding, env.LedgerEntryExists(f.holdingKey(f.owner)))

					clear := account.NewAccountSet(f.issuer.Address)
					flag := account.AccountSetFlagDefaultRipple
					clear.ClearFlag = &flag
					jtx.RequireTxSuccess(t, env.Submit(clear))
					env.Close()

					before := loanSetLedgerState(t, env)
					balance, sequence, owners := env.Balance(f.borrower), env.Seq(f.borrower), env.OwnerCount(f.borrower)
					ownerCount := env.OwnerCount(f.owner)
					result := submitLoanSet(t, f, f.borrower, f.owner, tc.originationFee)
					if !cleanup || !tc.borrowerHolding || tc.originationFee && !tc.ownerHolding {
						require.Equal(t, ter.TerNO_RIPPLE.String(), result.Code)
						require.False(t, result.Applied)
						require.Zero(t, result.Fee)
						require.Nil(t, result.Metadata)
						require.Equal(t, before, loanSetLedgerState(t, env))
						return
					}

					require.True(t, result.Applied)
					require.Equal(t, uint64(20), result.Fee)
					require.NotNil(t, result.Metadata)
					require.Equal(t, balance-result.Fee, env.Balance(f.borrower))
					require.Equal(t, sequence+1, env.Seq(f.borrower))
					if !tc.ownerHolding {
						require.Equal(t, ter.TecNO_LINE.String(), result.Code)
						require.Equal(t, owners, env.OwnerCount(f.borrower))
						requireLoanSetLedgerRollback(t, f, before)
						return
					}
					jtx.RequireTxSuccess(t, result)
					require.Equal(t, owners+1, env.OwnerCount(f.borrower))
					require.Equal(t, ownerCount, env.OwnerCount(f.owner))
					require.True(t, env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)))
					borrowerBalance := float64(2000)
					if tc.originationFee {
						borrowerBalance--
					}
					jtx.RequireIOUBalance(t, env, f.borrower, f.issuer, "USD", borrowerBalance)
					ownerBalance := float64(1000)
					if tc.originationFee {
						ownerBalance++
					}
					jtx.RequireIOUBalance(t, env, f.owner, f.issuer, "USD", ownerBalance)
					env.Close()
					require.True(t, env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)))
					jtx.RequireIOUBalance(t, env, f.borrower, f.issuer, "USD", borrowerBalance)
				})
			}
		})
	}
}
