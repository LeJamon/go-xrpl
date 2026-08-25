package vault_test

import (
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	paybuilder "github.com/LeJamon/go-xrpl/internal/testing/payment"
	trustbuilder "github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func TestVaultSharePaymentInheritsUnderlyingIOUChecks(t *testing.T) {
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
	require.NotNil(t, vaultShareIssuance(t, env, vaultInfo.ShareMPTID).ReferenceHolding)
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
	jtx.RequireTxSuccess(t, env.Submit(trustbuilder.TrustLine(issuer, currency, owner, "0").NoRipple().Build()))
	jtx.RequireTxSuccess(t, env.Submit(trustbuilder.TrustLine(issuer, currency, destination, "0").NoRipple().Build()))
	require.True(t, env.HasNoRipple(issuer, owner, currency))
	require.True(t, env.HasNoRipple(issuer, destination, currency))

	ownerXRP = env.Balance(owner)
	ownerSequence := env.Seq(owner)
	noRippleResult := env.Submit(payment())
	jtx.RequireTxFail(t, noRippleResult, jtx.TerNO_RIPPLE)
	require.False(t, noRippleResult.Applied)
	require.False(t, noRippleResult.Queued)
	require.Zero(t, noRippleResult.Fee)
	require.Nil(t, noRippleResult.Metadata)
	require.Equal(t, ownerXRP, env.Balance(owner))
	require.Equal(t, ownerSequence, env.Seq(owner))
	require.Equal(t, ownerShares, vaultShareBalance(t, env, vaultInfo.ShareMPTID, owner))
	require.Equal(t, destinationShares, vaultShareBalance(t, env, vaultInfo.ShareMPTID, destination))

	jtx.RequireTxSuccess(t, env.Submit(trustbuilder.TrustLine(issuer, currency, destination, "0").ClearNoRipple().Build()))
	require.False(t, env.HasNoRipple(issuer, destination, currency))
	ownerXRP = env.Balance(owner)
	jtx.RequireTxSuccess(t, env.Submit(payment()))
	require.Equal(t, ownerXRP-env.BaseFee(), env.Balance(owner))
	require.Equal(t, ownerShares-10, vaultShareBalance(t, env, vaultInfo.ShareMPTID, owner))
	require.Equal(t, destinationShares+10, vaultShareBalance(t, env, vaultInfo.ShareMPTID, destination))
}

func TestVaultSharePaymentRejectsFrozenReferenceHolding(t *testing.T) {
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
	env.Trust(destination, limit)
	env.AuthorizeTrustLine(issuer, destination, currency)
	jtx.RequireTxSuccess(t, env.Submit(mpt.NewMPTokenAuthorize(destination.Address, shareID)))

	pseudoAddress, err := state.EncodeAccountID(vaultInfo.Account)
	require.NoError(t, err)
	pseudo := jtx.NewAccountWithAddress("vault", pseudoAddress)
	env.FreezeTrustLine(issuer, pseudo, currency)

	issuance := vaultShareIssuance(t, env, vaultInfo.ShareMPTID)
	require.NotNil(t, issuance.ReferenceHolding)
	require.Zero(t, issuance.Flags&entry.LsfMPTLocked)
	require.Zero(t, vaultShareHolding(t, env, vaultInfo.ShareMPTID, owner).Flags&entry.LsfMPTLocked)
	require.Zero(t, vaultShareHolding(t, env, vaultInfo.ShareMPTID, destination).Flags&entry.LsfMPTLocked)

	ownerXRP := env.Balance(owner)
	ownerSequence := env.Seq(owner)
	ownerCount := env.AccountInfo(owner).OwnerCount
	destinationXRP := env.Balance(destination)
	destinationSequence := env.Seq(destination)
	ownerShares := vaultShareBalance(t, env, vaultInfo.ShareMPTID, owner)
	destinationShares := vaultShareBalance(t, env, vaultInfo.ShareMPTID, destination)

	amount := state.NewMPTAmountWithIssuanceID(10, "", shareID)
	result := env.Submit(paybuilder.PayIssued(owner, destination, amount).Build())
	requireFeeOnlyPaymentClaim(t, env, result, jtx.TecLOCKED)

	accountNode := result.Metadata.AffectedNodes[0]
	require.Equal(t, map[string]any{
		"Balance":  strconv.FormatUint(ownerXRP, 10),
		"Sequence": ownerSequence,
	}, accountNode.PreviousFields)
	require.Equal(t, owner.Address, accountNode.FinalFields["Account"])
	require.Equal(t, strconv.FormatUint(ownerXRP-env.BaseFee(), 10), accountNode.FinalFields["Balance"])
	require.Equal(t, ownerSequence+1, accountNode.FinalFields["Sequence"])
	require.Equal(t, ownerCount, accountNode.FinalFields["OwnerCount"])

	require.Equal(t, ownerXRP-env.BaseFee(), env.Balance(owner))
	require.Equal(t, ownerSequence+1, env.Seq(owner))
	require.Equal(t, ownerCount, env.AccountInfo(owner).OwnerCount)
	require.Equal(t, destinationXRP, env.Balance(destination))
	require.Equal(t, destinationSequence, env.Seq(destination))
	require.Equal(t, ownerShares, vaultShareBalance(t, env, vaultInfo.ShareMPTID, owner))
	require.Equal(t, destinationShares, vaultShareBalance(t, env, vaultInfo.ShareMPTID, destination))
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
	return vaultShareHolding(t, env, issuanceID, holder).MPTAmount
}

func vaultShareHolding(t *testing.T, env *jtx.TestEnv, issuanceID [24]byte, holder *jtx.Account) *state.MPTokenData {
	t.Helper()
	raw, err := env.Ledger().Read(keylet.MPTokenByID(issuanceID, holder.AccountID()))
	require.NoError(t, err)
	require.NotNil(t, raw)
	token, err := state.ParseMPToken(raw)
	require.NoError(t, err)
	return token
}

func vaultShareIssuance(t *testing.T, env *jtx.TestEnv, issuanceID [24]byte) *state.MPTokenIssuanceData {
	t.Helper()
	raw, err := env.Ledger().Read(keylet.MPTIssuance(issuanceID))
	require.NoError(t, err)
	require.NotNil(t, raw)
	issuance, err := state.ParseMPTokenIssuance(raw)
	require.NoError(t, err)
	return issuance
}
