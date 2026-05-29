// Tests for featureExpandedSignerList behavior in SignerListSet: the
// signer-entry cap (8 without the amendment, 32 with) and WalletLocator
// validation/persistence.
//
// Reference: rippled SetSignerList.cpp validateQuorumAndSignerEntries() and
// writeSignersToSLE(), STTx::maxMultiSigners (STTx.h:53-63).
package multisign_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx/signerlist"
	"github.com/LeJamon/goXRPLd/keylet"
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

// TestSignerList_EntryCap_WithoutExpandedAmendment asserts the 8-entry cap is
// enforced when featureExpandedSignerList is disabled.
func TestSignerList_EntryCap_WithoutExpandedAmendment(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("ExpandedSignerList")
	env.Close()

	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	// Nine entries exceed the pre-amendment maximum of 8.
	result := env.Submit(jtx.NewSignerListSetTx(alice, 1, signersOfWeightOne(9)))
	jtx.RequireTxFail(t, result, "temMALFORMED")

	// Eight entries are the pre-amendment maximum and must succeed.
	result = env.Submit(jtx.NewSignerListSetTx(alice, 1, signersOfWeightOne(8)))
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireSignerListCount(t, env, alice, 1)
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

// TestSignerList_WalletLocator_RejectedWithoutAmendment asserts that a
// WalletLocator tag is rejected with temMALFORMED when the amendment is off.
// Reference: rippled SetSignerList.cpp:313-318.
func TestSignerList_WalletLocator_RejectedWithoutAmendment(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("ExpandedSignerList")
	env.Close()

	alice := jtx.NewAccount("alice")
	bogie := jtx.NewAccount("bogie")
	env.Fund(alice, bogie)
	env.Close()

	result := env.Submit(signerListSetWithLocator(alice.Address, bogie.Address, testWalletLocator))
	jtx.RequireTxFail(t, result, "temMALFORMED")
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
