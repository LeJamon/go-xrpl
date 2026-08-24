package vault_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	paybuilder "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestVaultSharePaymentRequiresUnderlyingIOUAuthorization(t *testing.T) {
	env := newVaultEnv(t)
	env.DisableFeature("MPTokensV2")
	env.Close()
	issuer := jtx.NewAccount("issuer")
	owner := jtx.NewAccount("owner")
	destination := jtx.NewAccount("destination")
	env.Fund(issuer, owner, destination)
	env.EnableRequireAuth(issuer)

	const currency = "USD"
	limit := tx.NewIssuedAmountFromFloat64(1_000, currency, issuer.Address)
	env.Trust(owner, limit)
	env.AuthorizeTrustLine(issuer, owner, currency)
	env.PayIOU(issuer, owner, issuer, currency, 100)

	sequence := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: currency, Issuer: issuer.Address})
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))

	vaultKey := keylet.Vault(owner.AccountID(), sequence)
	vaultInfo, err := vault.ReadVaultInfo(env.Ledger(), vaultKey)
	require.NoError(t, err)
	require.NotNil(t, vaultInfo)
	shareID := mptutil.EncodeID(vaultInfo.ShareMPTID)

	deposit := vault.NewVaultDeposit(
		owner.Address,
		vaultID(owner, sequence),
		tx.NewIssuedAmountFromFloat64(100, currency, issuer.Address),
	)
	jtx.RequireTxSuccess(t, env.Submit(deposit))
	jtx.RequireTxSuccess(t, env.Submit(mpt.NewMPTokenAuthorize(destination.Address, shareID)))

	shareAmount := state.NewMPTAmountWithIssuanceID(10, "", shareID)
	payment := func() tx.Transaction {
		return paybuilder.PayIssued(owner, destination, shareAmount).Build()
	}
	ownerShares := vaultShareBalance(t, env, vaultInfo.ShareMPTID, owner)
	destinationShares := vaultShareBalance(t, env, vaultInfo.ShareMPTID, destination)
	require.Greater(t, ownerShares, uint64(10))
	require.Zero(t, destinationShares)

	ownerXRP := env.Balance(owner)
	missingLineResult := env.Submit(payment())
	requireFeeOnlyPaymentClaim(t, env, missingLineResult, jtx.TecNO_LINE)
	require.Equal(t, ownerXRP-env.BaseFee(), env.Balance(owner))
	require.False(t, env.TrustLineExists(destination, issuer, currency))
	require.Equal(t, ownerShares, vaultShareBalance(t, env, vaultInfo.ShareMPTID, owner))
	require.Equal(t, destinationShares, vaultShareBalance(t, env, vaultInfo.ShareMPTID, destination))

	env.Trust(destination, limit)
	ownerXRP = env.Balance(owner)
	unauthorizedResult := env.Submit(payment())
	requireFeeOnlyPaymentClaim(t, env, unauthorizedResult, jtx.TecNO_AUTH)
	require.Equal(t, ownerXRP-env.BaseFee(), env.Balance(owner))
	require.Equal(t, ownerShares, vaultShareBalance(t, env, vaultInfo.ShareMPTID, owner))
	require.Equal(t, destinationShares, vaultShareBalance(t, env, vaultInfo.ShareMPTID, destination))

	env.AuthorizeTrustLine(issuer, destination, currency)
	ownerXRP = env.Balance(owner)
	jtx.RequireTxSuccess(t, env.Submit(payment()))
	require.Equal(t, ownerXRP-env.BaseFee(), env.Balance(owner))
	require.Equal(t, ownerShares-10, vaultShareBalance(t, env, vaultInfo.ShareMPTID, owner))
	require.Equal(t, destinationShares+10, vaultShareBalance(t, env, vaultInfo.ShareMPTID, destination))
}

func requireFeeOnlyPaymentClaim(t *testing.T, env *jtx.TestEnv, result jtx.TxResult, code string) {
	t.Helper()
	jtx.RequireTxClaimed(t, result, code)
	require.True(t, result.Applied)
	require.Equal(t, env.BaseFee(), result.Fee)
	require.NotNil(t, result.Metadata)
	require.Len(t, result.Metadata.AffectedNodes, 1)
	require.Equal(t, "ModifiedNode", result.Metadata.AffectedNodes[0].NodeType)
	require.Equal(t, "AccountRoot", result.Metadata.AffectedNodes[0].LedgerEntryType)
}

func vaultShareBalance(t *testing.T, env *jtx.TestEnv, issuanceID [24]byte, holder *jtx.Account) uint64 {
	t.Helper()
	raw, err := env.Ledger().Read(keylet.MPTokenByID(issuanceID, holder.AccountID()))
	require.NoError(t, err)
	require.NotNil(t, raw)
	token, err := state.ParseMPToken(raw)
	require.NoError(t, err)
	return token.MPTAmount
}
