package accountset

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/keylet"
)

// TestAccountSet_NFTokenMinterPrecedence pins finding AccountSet-order: setting
// asfAuthorizedNFTokenMinter without an sfNFTokenMinter field is temMALFORMED,
// enforced in the preflight body. It wins over the sequence gap that preclaim
// would otherwise report as terPRE_SEQ.
func TestAccountSet_NFTokenMinterPrecedence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()
	balance := env.Balance(alice)
	sequence := env.Seq(alice)

	// SetFlag asfAuthorizedNFTokenMinter with no NFTokenMinter present, plus a
	// future sequence: the field-pairing tem must beat the preclaim seq check.
	as := AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagAuthorizedNFTokenMinter).
		Sequence(env.Seq(alice) + 10).
		Build()
	jtx.RequireTxFail(t, env.Submit(as), "temMALFORMED")
	require.Equal(t, balance, env.Balance(alice))
	require.Equal(t, sequence, env.Seq(alice))
	data, err := env.LedgerEntry(keylet.Account(alice.ID))
	require.NoError(t, err)
	account, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	require.Empty(t, account.NFTokenMinter)
}
