package vault_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// A share-denominated withdrawal from an empty IOU vault converts to zero
// assets in preclaim and fails in apply with tecINSUFFICIENT_FUNDS, claiming
// the fee.
func TestVaultWithdraw_SharesFromEmptyIOUVault(t *testing.T) {
	env := newVaultEnv(t)
	issuer := jtx.NewAccount("empty-vault-issuer")
	owner := jtx.NewAccount("empty-vault-owner")
	env.Fund(issuer)
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).DefaultRipple().Build()))
	env.Fund(owner)

	sequence := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "USD", Issuer: issuer.Address})
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))

	info, err := vault.ReadVaultLending(env.Ledger(), keylet.Vault(owner.AccountID(), sequence))
	require.NoError(t, err)
	require.NotNil(t, info)
	shares := state.NewMPTAmountWithIssuanceID(1, "", mptutil.EncodeID(info.ShareMPTID))

	jtx.RequireTxClaimed(t, env.Submit(vault.NewVaultWithdraw(owner.Address, vaultID(owner, sequence), shares)), jtx.TecINSUFFICIENT_FUNDS)
}
