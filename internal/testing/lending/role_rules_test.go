package lending_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func oppositeLendingRoleRules(cleanup bool) *amendment.Rules {
	if cleanup {
		return amendment.EmptyRules()
	}
	return amendment.NewRulesBuilder().Enable(amendment.FeatureFixCleanup3_4_0).Build()
}

func roleRulesLoanSet(t *testing.T, f *loanSetAssetFixture, rules *amendment.Rules) *lending.LoanSet {
	t.Helper()
	loanSet := lending.NewLoanSet(f.borrower.Address, f.brokerID, "1000")
	loanSet.Counterparty = f.owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = f.borrower.PublicKeyHex()
	sequence := f.env.Seq(f.borrower)
	loanSet.GetCommon().Sequence = &sequence
	signature, err := txsign.SignCounterpartyWithRules(
		loanSet,
		f.owner.PublicKeyHex(),
		"00"+f.owner.PrivateKeyHex(),
		rules,
	)
	require.NoError(t, err)
	loanSet.GetCommon().CounterpartySignature = signature
	return loanSet
}

func requireLoanSetRoleSuccess(t *testing.T, f *loanSetAssetFixture, loanSet *lending.LoanSet) {
	t.Helper()
	borrowerBefore := f.env.Balance(f.borrower)
	ownerBefore := f.env.Balance(f.owner)
	sequenceBefore := f.env.Seq(f.borrower)

	result := f.env.SubmitSigned(loanSet)
	jtx.RequireTxSuccess(t, result)
	require.True(t, result.Applied)
	require.Equal(t, uint64(20), result.Fee)
	require.Equal(t, borrowerBefore-20, f.env.Balance(f.borrower))
	require.Equal(t, ownerBefore, f.env.Balance(f.owner))
	require.Equal(t, sequenceBefore+1, f.env.Seq(f.borrower))
}

func requireLoanSetRoleMismatch(t *testing.T, f *loanSetAssetFixture, loanSet *lending.LoanSet) {
	t.Helper()
	borrowerBefore := f.env.Balance(f.borrower)
	ownerBefore := f.env.Balance(f.owner)
	sequenceBefore := f.env.Seq(f.borrower)

	result := f.env.SubmitSigned(loanSet)
	jtx.RequireTxFail(t, result, "temINVALID")
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Equal(t, borrowerBefore, f.env.Balance(f.borrower))
	require.Equal(t, ownerBefore, f.env.Balance(f.owner))
	require.Equal(t, sequenceBefore, f.env.Seq(f.borrower))
	require.False(t, f.env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)))
}

func TestLoanCounterpartyRoleRulesFullEngine(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "cleanup-off"
		if cleanup {
			name = "cleanup-on"
		}
		t.Run(name, func(t *testing.T) {
			env := newLegacyLendingEnvWithCleanup(t, cleanup)
			f := newLoanSetAssetFixtureWithEnv(t, env, "IOU", false)
			f.createHolding(f.owner)
			env.SetVerifySignatures(true)

			requireLoanSetRoleSuccess(t, f, roleRulesLoanSet(t, f, env.Rules()))

			env = newLegacyLendingEnvWithCleanup(t, cleanup)
			f = newLoanSetAssetFixtureWithEnv(t, env, "IOU", false)
			f.createHolding(f.owner)
			env.SetVerifySignatures(true)
			requireLoanSetRoleMismatch(t, f, roleRulesLoanSet(t, f, oppositeLendingRoleRules(cleanup)))
		})
	}
}
