package sponsor_test

import (
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	signtx "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/stretchr/testify/require"
)

func roleRulesSponsorEnv(t *testing.T, cleanup bool) (*jtx.TestEnv, *jtx.Account, *jtx.Account) {
	t.Helper()
	env, source, _, sponsor, _ := sponsorEnv(t)
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	env.Close()
	if got := env.Rules().Enabled(amendment.FeatureFixCleanup3_4_0); got != cleanup {
		t.Fatalf("cleanup=%t: Rules().Enabled(fixCleanup3_4_0) = %t", cleanup, got)
	}
	return env, source, sponsor
}

func oppositeRoleRules(cleanup bool) *amendment.Rules {
	if cleanup {
		return amendment.EmptyRules()
	}
	return amendment.NewRulesBuilder().Enable(amendment.FeatureFixCleanup3_4_0).Build()
}

func TestRulesAccessorReflectsCommittedAmendments(t *testing.T) {
	env := jtx.NewTestEnv(t)
	require.False(t, env.Rules().Enabled(amendment.FeatureFixCleanup3_4_0))

	env.EnableFeature("fixCleanup3_4_0")
	require.False(t, env.Rules().Enabled(amendment.FeatureFixCleanup3_4_0))
	env.Close()
	require.True(t, env.Rules().Enabled(amendment.FeatureFixCleanup3_4_0))

	env.DisableFeature("fixCleanup3_4_0")
	require.True(t, env.Rules().Enabled(amendment.FeatureFixCleanup3_4_0))
	env.Close()
	require.False(t, env.Rules().Enabled(amendment.FeatureFixCleanup3_4_0))
}

func newRoleRulesSponsorFeeTransaction(source, sponsor *jtx.Account, fee string) tx.Transaction {
	transaction := accounttx.NewAccountSet(source.Address)
	transaction.Fee = fee
	transaction.Sponsor = sponsor.Address
	flags := tx.SpfSponsorFee
	transaction.SponsorFlags = &flags
	return transaction
}

func attachSponsorSingleWithRules(
	t *testing.T,
	env *jtx.TestEnv,
	transaction tx.Transaction,
	source, sponsor *jtx.Account,
	rules *amendment.Rules,
) {
	t.Helper()
	common := transaction.GetCommon()
	sequence := env.Seq(source)
	common.Sequence = &sequence
	common.SigningPubKey = source.PublicKeyHex()
	signature, err := signtx.SignSponsorWithRules(
		transaction,
		sponsor.PublicKeyHex(),
		signingPrivateKey(sponsor),
		rules,
	)
	require.NoError(t, err)
	common.SponsorSignature = signature
}

func attachSponsorMultiWithRules(
	t *testing.T,
	env *jtx.TestEnv,
	transaction tx.Transaction,
	source *jtx.Account,
	signers []*jtx.Account,
	rules *amendment.Rules,
) {
	t.Helper()
	common := transaction.GetCommon()
	sequence := env.Seq(source)
	common.Sequence = &sequence
	common.SigningPubKey = source.PublicKeyHex()

	wrappers := make([]tx.SignerWrapper, 0, len(signers))
	for _, signer := range signers {
		signature, err := signtx.SignTransactionForMultiSignRole(
			transaction,
			signer.Address,
			signingPrivateKey(signer),
			binarycodec.SponsorRole,
			rules,
		)
		require.NoError(t, err)
		wrappers = append(wrappers, tx.SignerWrapper{Signer: tx.Signer{
			Account:       signer.Address,
			SigningPubKey: signer.PublicKeyHex(),
			TxnSignature:  signature,
		}})
	}
	sort.Slice(wrappers, func(i, j int) bool {
		left, leftErr := state.DecodeAccountID(wrappers[i].Signer.Account)
		right, rightErr := state.DecodeAccountID(wrappers[j].Signer.Account)
		require.NoError(t, leftErr)
		require.NoError(t, rightErr)
		return string(left[:]) < string(right[:])
	})
	common.SponsorSignature = &tx.SponsorSignature{Signers: wrappers}
}

func requireSponsorRoleSuccess(t *testing.T, env *jtx.TestEnv, transaction tx.Transaction, source, sponsor *jtx.Account, fee uint64, attach func()) {
	t.Helper()
	sourceBefore := env.Balance(source)
	sponsorBefore := env.Balance(sponsor)
	sequenceBefore := env.Seq(source)
	attach()

	result := env.SubmitSigned(transaction)
	jtx.RequireTxSuccess(t, result)
	require.True(t, result.Applied)
	require.Equal(t, fee, result.Fee)
	require.Equal(t, sourceBefore, env.Balance(source))
	require.Equal(t, sponsorBefore-fee, env.Balance(sponsor))
	require.Equal(t, sequenceBefore+1, env.Seq(source))
}

func requireSponsorRoleMismatch(t *testing.T, env *jtx.TestEnv, transaction tx.Transaction, source, sponsor *jtx.Account, attach func()) {
	t.Helper()
	sourceBefore := env.Balance(source)
	sponsorBefore := env.Balance(sponsor)
	sequenceBefore := env.Seq(source)
	attach()

	result := env.SubmitSigned(transaction)
	jtx.RequireTxFail(t, result, "temINVALID")
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Equal(t, sourceBefore, env.Balance(source))
	require.Equal(t, sponsorBefore, env.Balance(sponsor))
	require.Equal(t, sequenceBefore, env.Seq(source))
}

func TestSponsorRoleRulesSingleSignatureFullEngine(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "cleanup-off"
		if cleanup {
			name = "cleanup-on"
		}
		t.Run(name, func(t *testing.T) {
			env, source, sponsor := roleRulesSponsorEnv(t, cleanup)
			env.SetVerifySignatures(true)

			valid := newRoleRulesSponsorFeeTransaction(source, sponsor, "10")
			requireSponsorRoleSuccess(t, env, valid, source, sponsor, 10, func() {
				attachSponsorSignature(t, env, valid, source, sponsor)
			})

			env, source, sponsor = roleRulesSponsorEnv(t, cleanup)
			env.SetVerifySignatures(true)
			mismatched := newRoleRulesSponsorFeeTransaction(source, sponsor, "10")
			requireSponsorRoleMismatch(t, env, mismatched, source, sponsor, func() {
				attachSponsorSingleWithRules(t, env, mismatched, source, sponsor, oppositeRoleRules(cleanup))
			})
		})
	}
}

func TestSponsorRoleRulesMultisignatureFullEngine(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "cleanup-off"
		if cleanup {
			name = "cleanup-on"
		}
		t.Run(name, func(t *testing.T) {
			env, source, sponsor := roleRulesSponsorEnv(t, cleanup)
			signer1 := jtx.NewAccount("role-rules-sponsor-signer-1")
			signer2 := jtx.NewAccount("role-rules-sponsor-signer-2")
			signers := []*jtx.Account{signer1, signer2}
			env.SetSignerList(sponsor, 2, []jtx.TestSigner{
				{Account: signer1, Weight: 1},
				{Account: signer2, Weight: 1},
			})
			env.SetVerifySignatures(true)

			valid := newRoleRulesSponsorFeeTransaction(source, sponsor, "30")
			requireSponsorRoleSuccess(t, env, valid, source, sponsor, 30, func() {
				attachSponsorMultiSignature(t, env, valid, source, signers...)
			})

			env, source, sponsor = roleRulesSponsorEnv(t, cleanup)
			env.SetSignerList(sponsor, 2, []jtx.TestSigner{
				{Account: signer1, Weight: 1},
				{Account: signer2, Weight: 1},
			})
			env.SetVerifySignatures(true)
			mismatched := newRoleRulesSponsorFeeTransaction(source, sponsor, "30")
			requireSponsorRoleMismatch(t, env, mismatched, source, sponsor, func() {
				attachSponsorMultiWithRules(t, env, mismatched, source, signers, oppositeRoleRules(cleanup))
			})
		})
	}
}
