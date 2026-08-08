package accountset

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestAccountSet_AccountTxnIDTransitions(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	ghost := jtx.NewAccount("ghost")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	readAccount := func() *state.AccountRoot {
		t.Helper()
		data, err := env.LedgerEntry(keylet.Account(alice.ID))
		require.NoError(t, err)
		account, err := state.ParseAccountRoot(data)
		require.NoError(t, err)
		return account
	}

	origFlags := readAccount().Flags
	var zero [32]byte

	result := env.Submit(AccountSet(alice).SetFlag(accounttx.AccountSetFlagAccountTxnID).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	account := readAccount()
	require.True(t, account.HasAccountTxnID)
	require.Equal(t, zero, account.AccountTxnID)
	require.Equal(t, origFlags, account.Flags)

	update := AccountSet(alice).Build()
	result = env.Submit(update)
	jtx.RequireTxSuccess(t, result)
	expectedHash, err := tx.ComputeTransactionHash(update)
	require.NoError(t, err)
	env.Close()
	account = readAccount()
	require.True(t, account.HasAccountTxnID)
	require.Equal(t, expectedHash, account.AccountTxnID)

	balanceBefore := env.Balance(alice)
	sequenceBefore := env.Seq(alice)
	claimed := payment.Pay(alice, ghost, uint64(jtx.XRP(1))).Build()
	result = env.Submit(claimed)
	jtx.RequireTxFail(t, result, "tecNO_DST_INSUF_XRP")
	fee, err := strconv.ParseUint(claimed.GetCommon().Fee, 10, 64)
	require.NoError(t, err)
	env.Close()
	require.Equal(t, balanceBefore-fee, env.Balance(alice))
	require.Equal(t, sequenceBefore+1, env.Seq(alice))
	account = readAccount()
	require.True(t, account.HasAccountTxnID)
	require.Equal(t, expectedHash, account.AccountTxnID)

	result = env.Submit(AccountSet(alice).ClearFlag(accounttx.AccountSetFlagAccountTxnID).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	account = readAccount()
	require.False(t, account.HasAccountTxnID)
	require.Equal(t, zero, account.AccountTxnID)
	require.Equal(t, origFlags, account.Flags)
}
