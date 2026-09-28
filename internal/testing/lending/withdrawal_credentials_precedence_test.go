package lending_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestWithdrawalCredentialsPseudoAccountPrecedence(t *testing.T) {
	env := newLendingEnv(t)
	env.EnableFeature("Credentials")
	env.EnableFeature("fixCleanup3_4_0")
	env.Close()
	owner := jtx.NewAccount("owner")
	env.FundAmount(owner, 10_000_000_000)
	seq := env.Seq(owner)
	id := setupXRPVault(t, env, owner, 2_000_000_000)
	raw, err := env.LedgerEntry(keylet.Vault(owner.AccountID(), seq))
	require.NoError(t, err)
	fields, err := binarycodec.DecodeBytes(raw)
	require.NoError(t, err)
	destination := fields["Account"].(string)

	withdraw := vault.NewVaultWithdraw(owner.Address, id, tx.NewXRPAmount(100_000_000))
	withdraw.Destination = destination
	withdraw.CredentialIDs = []string{fxHash}
	jtx.RequireTxFail(t, env.Submit(withdraw), ter.TecBAD_CREDENTIALS.String())

	cover := lending.NewLoanBrokerCoverWithdraw(owner.Address, fxHash, tx.NewXRPAmount(100_000_000))
	cover.Destination = destination
	cover.CredentialIDs = []string{fxHash}
	jtx.RequireTxFail(t, env.Submit(cover), ter.TecPSEUDO_ACCOUNT.String())
}
