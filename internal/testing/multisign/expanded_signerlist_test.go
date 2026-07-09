// Tests for SignerListSet: the 32-entry signer cap and WalletLocator
// persistence.
//
// Reference: rippled SetSignerList.cpp validateQuorumAndSignerEntries() and
// writeSignersToSLE(), STTx::maxMultiSigners (STTx.h:53-63).
package multisign_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/signerlist"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// signersOfWeightOne builds n distinct phantom signers, each with weight 1.
func signersOfWeightOne(n int) []jtx.TestSigner {
	signers := make([]jtx.TestSigner, n)
	for i := range signers {
		signers[i] = jtx.TestSigner{
			Account: jtx.NewAccount(fmt.Sprintf("phantom_signer_%d", i)),
			Weight:  1,
		}
	}
	return signers
}

// TestSignerList_EntryCap_WithExpandedAmendment asserts that up to 32 entries
// are accepted when featureExpandedSignerList is enabled (the default preset).
func TestSignerList_EntryCap_WithExpandedAmendment(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	// 32 signer entries cost up to reserveBase + 32*reserveIncrement of owner
	// reserve before featureMultiSignReserve collapses it to 1; the default
	// preset enables MultiSignReserve, but fund generously regardless.
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	// Nine entries — rejected without the amendment — are now accepted.
	result := env.Submit(jtx.NewSignerListSetTx(alice, 1, signersOfWeightOne(9)))
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireSignerListCount(t, env, alice, 1)

	// The full 32-entry list is accepted.
	result = env.Submit(jtx.NewSignerListSetTx(alice, 1, signersOfWeightOne(32)))
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireSignerListCount(t, env, alice, 1)

	// 33 entries exceed the absolute maximum and are rejected.
	result = env.Submit(jtx.NewSignerListSetTx(alice, 1, signersOfWeightOne(33)))
	jtx.RequireTxFail(t, result, "temMALFORMED")
}

// TestSignerList_EntryCapPrecedesWeightCheck guards the rippled check order: a
// transaction that both exceeds the entry cap (33 > 32) and contains a
// zero-weight signer must report temMALFORMED (the cap check), not temBAD_WEIGHT.
// rippled checks the count before the per-signer loop (SetSignerList.cpp:271-303).
func TestSignerList_EntryCapPrecedesWeightCheck(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	// 33 entries (over the cap of 32) with one zero weight.
	signers := signersOfWeightOne(33)
	signers[0].Weight = 0
	result := env.Submit(jtx.NewSignerListSetTx(alice, 1, signers))
	jtx.RequireTxFail(t, result, "temMALFORMED")
}

const testWalletLocator = "00000000000000000000000000000000000000000000000000000000DEADBEEF"

// signerListSetWithLocator builds a SignerListSet whose single entry carries a
// WalletLocator tag.
func signerListSetWithLocator(account, signer, locator string) *signerlist.SignerListSet {
	sl := signerlist.NewSignerListSet(account, 1)
	sl.SignerEntries = []signerlist.SignerEntry{
		{SignerEntry: signerlist.SignerEntryData{
			Account:       signer,
			SignerWeight:  1,
			WalletLocator: locator,
		}},
	}
	return sl
}

// TestSignerList_WalletLocator_Persisted asserts that, with the amendment
// enabled, a WalletLocator tag survives into the SignerList ledger entry.
// Reference: rippled SetSignerList.cpp:445-448.
func TestSignerList_WalletLocator_Persisted(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bogie := jtx.NewAccount("bogie")
	env.Fund(alice, bogie)
	env.Close()

	result := env.Submit(signerListSetWithLocator(alice.Address, bogie.Address, testWalletLocator))
	jtx.RequireTxSuccess(t, result)
	env.Close()

	data, err := env.LedgerEntry(keylet.SignerList(alice.ID))
	require.NoError(t, err, "signer list entry should exist")

	info, err := state.ParseSignerList(data)
	require.NoError(t, err)
	require.Len(t, info.SignerEntries, 1)
	require.Equal(t, testWalletLocator, info.SignerEntries[0].WalletLocator,
		"WalletLocator must be persisted in the SignerList entry")
}

// signerAccounts builds n distinct, funded signer accounts.
func signerAccounts(env *jtx.TestEnv, n int) []*jtx.Account {
	accts := make([]*jtx.Account, n)
	for i := range accts {
		a := jtx.NewAccount(fmt.Sprintf("msigner_%d", i))
		env.FundAmount(a, uint64(jtx.XRP(1000)))
		accts[i] = a
	}
	return accts
}

// TestMultiSign_ArrayBound_WithExpandedAmendment asserts the rules-gated cap on
// a transaction's Signers array: with featureExpandedSignerList enabled a 33-entry
// array is rejected (cap is 32). The bound is a structural multi-sign check, so
// it surfaces as temINVALID (rippled multiSignHelper's Unexpected → SigBad →
// temINVALID in preflight2), regardless of the SignerList/authorization.
// Reference: rippled STTx::checkMultiSign -> multiSignHelper size check.
func TestMultiSign_ArrayBound_WithExpandedAmendment(t *testing.T) {
	env := jtx.NewTestEnv(t)
	require.True(t, env.FeatureEnabled("ExpandedSignerList"))

	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(becky, uint64(jtx.XRP(10000)))
	env.Close()

	signers := signerAccounts(env, 33)
	env.Close()

	payTx := payment.Pay(alice, becky, uint64(jtx.XRP(10))).Build()
	result := env.SubmitMultiSigned(payTx, signers)
	jtx.RequireTxFail(t, result, "temINVALID")
}
