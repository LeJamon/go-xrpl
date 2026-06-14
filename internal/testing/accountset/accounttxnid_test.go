package accountset

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/keylet"
)

// Regression for the seq-N account_hash fork found in the mixed soak:
// enabling asfAccountTxnID must make sfAccountTxnID PRESENT with value zero
// (rippled SetAccount.cpp makeFieldPresent(sfAccountTxnID)), and the account's
// next transaction must update that present-zero field to the tx id (rippled
// Transactor::apply isFieldPresent(sfAccountTxnID), Transactor.cpp:568-569).
//
// go-xrpl previously tracked presence as "non-zero", setting AccountTxnID to
// the enabling tx's hash and updating only when non-zero. That both diverged
// from rippled's present-zero value at enable time AND silently dropped the
// update for a present-zero field adopted from a rippled-built ledger — forking
// account_hash while transaction_hash matched.
func TestAccountSet_AccountTxnID_PresentZeroAndUpdate(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundNoRipple(alice)
	env.Close()

	readAR := func() *state.AccountRoot {
		t.Helper()
		data, err := env.LedgerEntry(keylet.Account(alice.ID))
		require.NoError(t, err)
		ar, err := state.ParseAccountRoot(data)
		require.NoError(t, err)
		return ar
	}
	var zero [32]byte

	// Enable asfAccountTxnID → field present, value zero.
	r := env.Submit(AccountSet(alice).SetFlag(accounttx.AccountSetFlagAccountTxnID).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	ar := readAR()
	require.True(t, ar.HasAccountTxnID, "enable must make sfAccountTxnID present")
	require.Equal(t, zero, ar.AccountTxnID,
		"enable must leave AccountTxnID zero, not the enabling tx hash (rippled makeFieldPresent)")

	// Next transaction must update the present-zero field to the tx id.
	r = env.Submit(AccountSet(alice).Build()) // noop AccountSet
	jtx.RequireTxSuccess(t, r)
	env.Close()

	ar = readAR()
	require.True(t, ar.HasAccountTxnID, "AccountTxnID must remain present after the update")
	require.NotEqual(t, zero, ar.AccountTxnID,
		"the account's next tx must update present-zero AccountTxnID to the tx id")

	// Clearing removes the field entirely.
	r = env.Submit(AccountSet(alice).ClearFlag(accounttx.AccountSetFlagAccountTxnID).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	ar = readAR()
	require.False(t, ar.HasAccountTxnID, "clear must remove sfAccountTxnID")
	require.Equal(t, zero, ar.AccountTxnID)
}
