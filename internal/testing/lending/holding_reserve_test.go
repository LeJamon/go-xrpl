package lending_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	paytest "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestLoanSetMPTokenFreeTier(t *testing.T) {
	for _, ownerSubmits := range []bool{false, true} {
		name := "borrower submits"
		if ownerSubmits {
			name = "owner submits"
		}
		t.Run(name, func(t *testing.T) {
			f := newLoanSetAssetFixtureWithEnv(t, newTwoHoldingLoanEnv(t, false, true), "MPT", false)
			f.createHolding(f.owner)
			require.Zero(t, f.env.OwnerCount(f.borrower))
			reserve := f.env.ReserveBase() + f.env.ReserveIncrement()
			jtx.RequireTxSuccess(t, f.env.Submit(paytest.Pay(f.borrower, f.issuer,
				f.env.Balance(f.borrower)-reserve-f.env.BaseFee()).Build()))
			submitter, counterparty := f.borrower, f.owner
			wantBalance := reserve - 20
			if ownerSubmits {
				submitter, counterparty = f.owner, f.borrower
				wantBalance = reserve
			}
			jtx.RequireTxSuccess(t, submitLoanSet(t, f, submitter, counterparty, false))
			require.Equal(t, wantBalance, f.env.Balance(f.borrower))
			require.Equal(t, uint32(2), f.env.OwnerCount(f.borrower))
			assertLoanSetOwnedObjects(t, f, f.borrower)
			raw, err := f.env.LedgerEntry(f.holdingKey(f.borrower))
			require.NoError(t, err)
			holding, err := state.ParseMPToken(raw)
			require.NoError(t, err)
			require.Equal(t, uint64(1_000), holding.MPTAmount)
		})
	}
}

func TestLoanSetHoldingReserveAfterFee(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		for _, ownerSubmits := range []bool{false, true} {
			name := kind + "/borrower submits"
			if ownerSubmits {
				name = kind + "/owner submits"
			}
			t.Run(name, func(t *testing.T) {
				f := newLoanSetAssetFixtureWithEnv(t, newTwoHoldingLoanEnv(t, false, true), kind, false)
				submitter, counterparty := f.borrower, f.owner
				newObjects := uint32(2)
				if ownerSubmits {
					submitter, counterparty = f.owner, f.borrower
					newObjects = 1
				} else {
					f.createHolding(f.owner)
					if kind == "MPT" {
						f.env.Trust(f.borrower, tx.NewIssuedAmountFromFloat64(1, "EUR", f.issuer.Address))
					}
				}
				owners := f.env.OwnerCount(submitter)
				reserve := f.env.ReserveBase() + uint64(owners+newObjects)*f.env.ReserveIncrement()
				jtx.RequireTxSuccess(t, f.env.Submit(paytest.Pay(submitter, f.issuer,
					f.env.Balance(submitter)-reserve-19-f.env.BaseFee()).Build()))
				require.Equal(t, reserve+19, f.env.Balance(submitter))
				beforeSequence := f.env.Seq(submitter)
				beforeBorrowerOwners := f.env.OwnerCount(f.borrower)
				beforeVault, err := f.env.LedgerEntry(f.vaultKey)
				require.NoError(t, err)
				brokerKey := keylet.LoanBrokerByID(f.brokerKey)
				beforeBroker, err := f.env.LedgerEntry(brokerKey)
				require.NoError(t, err)
				want := jtx.TecINSUFFICIENT_RESERVE
				if kind == "IOU" {
					want = jtx.TecNO_LINE_INSUF_RESERVE
				}
				result := submitLoanSet(t, f, submitter, counterparty, ownerSubmits)
				jtx.RequireTxClaimed(t, result, want)
				require.Equal(t, uint64(20), result.Fee)
				require.Equal(t, beforeSequence+1, f.env.Seq(submitter))
				require.Equal(t, reserve-1, f.env.Balance(submitter))
				require.Equal(t, owners, f.env.OwnerCount(submitter))
				require.Equal(t, beforeBorrowerOwners, f.env.OwnerCount(f.borrower))
				require.False(t, f.env.LedgerEntryExists(f.holdingKey(submitter)))
				require.False(t, f.env.LedgerEntryExists(f.holdingKey(f.borrower)))
				require.False(t, f.env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)))
				afterVault, err := f.env.LedgerEntry(f.vaultKey)
				require.NoError(t, err)
				require.Equal(t, beforeVault, afterVault)
				afterBroker, err := f.env.LedgerEntry(brokerKey)
				require.NoError(t, err)
				require.Equal(t, beforeBroker, afterBroker)

				f.env.Pay(submitter, 21)
				jtx.RequireTxSuccess(t, submitLoanSet(t, f, submitter, counterparty, ownerSubmits))
				require.Equal(t, reserve, f.env.Balance(submitter))
				require.Equal(t, owners+newObjects, f.env.OwnerCount(submitter))
				assertLoanSetOwnedObjects(t, f, f.borrower)
				require.True(t, ownerDirectoryContains(t, f.env, submitter.AccountID(), f.holdingKey(submitter)))
			})
		}
	}
}

func TestLoanBrokerCoverWithdrawCreatesHoldingOwnerCount(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		t.Run(kind, func(t *testing.T) {
			f := newLoanSetAssetFixtureWithEnv(t, newTwoHoldingLoanEnv(t, false, true), kind, false)
			f.createHolding(f.owner)
			f.fundHolding(f.owner, 100)
			jtx.RequireTxSuccess(t, f.env.Submit(lending.NewLoanBrokerCoverDeposit(f.owner.Address, f.brokerID, f.amount(100))))
			f.removeHolding(f.owner)
			require.False(t, f.env.LedgerEntryExists(f.holdingKey(f.owner)))
			owners := f.env.OwnerCount(f.owner)
			jtx.RequireTxSuccess(t, f.env.Submit(lending.NewLoanBrokerCoverWithdraw(f.owner.Address, f.brokerID, f.amount(50))))
			require.Equal(t, owners+1, f.env.OwnerCount(f.owner))
			require.True(t, ownerDirectoryContains(t, f.env, f.owner.AccountID(), f.holdingKey(f.owner)))
			raw, err := f.env.LedgerEntry(f.holdingKey(f.owner))
			require.NoError(t, err)
			if kind == "MPT" {
				holding, err := state.ParseMPToken(raw)
				require.NoError(t, err)
				require.Equal(t, uint64(50), holding.MPTAmount)
			} else {
				line, err := state.ParseRippleState(raw)
				require.NoError(t, err)
				want := "50"
				if !keylet.IsLowAccount(f.owner.AccountID(), f.issuer.AccountID()) {
					want = "-50"
				}
				require.Equal(t, want, line.Balance.Value())
			}
		})
	}
}
