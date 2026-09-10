package vault_test

import (
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func TestVaultDepositPrecisionPreservesOriginalSharesAndMetadata(t *testing.T) {
	env := newVaultEnv(t)
	env.EnableFeature("fixCleanup3_4_0")
	env.Close()
	issuer := jtx.NewAccount("precision-issuer")
	owner := jtx.NewAccount("precision-owner")
	depositor := jtx.NewAccount("precision-depositor")
	env.Fund(issuer)
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).DefaultRipple().Build()))
	env.Fund(owner, depositor)
	const currency = "USD"
	asset := tx.Asset{Currency: currency, Issuer: issuer.Address}
	env.Trust(depositor, tx.NewIssuedAmountFromFloat64(1, currency, issuer.Address))
	env.PayIOU(issuer, depositor, issuer, currency, 0.000000002)

	seq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, asset)
	scale := uint8(9)
	create.Scale = &scale
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))
	id := vaultID(owner, seq)
	seed := tx.NewIssuedAmountFromFloat64(1_000_000, currency, issuer.Address)
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(issuer.Address, id, seed)))
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(issuer.Address, id, seed)))

	vaultKey := keylet.Vault(owner.AccountID(), seq)
	info, err := vault.ReadVaultLending(env.Ledger(), vaultKey)
	require.NoError(t, err)
	require.NotNil(t, info)
	// Preserve the shares minted by the real deposits while reducing the stored
	// asset rail to exercise the cleanup clamp against an existing share supply.
	patchVaultNumbers(t, env, vaultKey, "1000000", "1000000")
	info, err = vault.ReadVaultLending(env.Ledger(), vaultKey)
	require.NoError(t, err)
	require.NotNil(t, info)
	patchLineBalance(t, env, keylet.Line(info.Account, issuer.ID, currency), issuer.ID, info.Account, state.NewIssuedAmountFromValue(1_000_000, 0, currency, issuer.Address))
	beforeTotal, err := state.ParseXRPLNumber(info.AssetsTotal, state.MantissaScaleLarge, state.RoundToNearest)
	require.NoError(t, err)
	beforeAvailable, err := state.ParseXRPLNumber(info.AssetsAvailable, state.MantissaScaleLarge, state.RoundToNearest)
	require.NoError(t, err)
	raw, err := env.LedgerEntry(keylet.MPTIssuance(info.ShareMPTID))
	require.NoError(t, err)
	issuance, err := state.ParseMPTokenIssuance(raw)
	require.NoError(t, err)
	require.Equal(t, uint64(2_000_000_000_000_000), issuance.OutstandingAmount)

	result := env.Submit(vault.NewVaultDeposit(
		depositor.Address, id, state.NewIssuedAmountFromValue(2, -9, currency, issuer.Address)))
	require.Equal(t, "tesSUCCESS", result.Code)
	require.True(t, result.Success)
	require.True(t, result.Applied)
	require.Equal(t, env.BaseFee(), result.Fee)
	require.NotNil(t, result.Metadata)
	requireMetadataEntries(t, result.Metadata, "Vault", "MPTokenIssuance", "MPToken", "RippleState")
	info, err = vault.ReadVaultLending(env.Ledger(), vaultKey)
	require.NoError(t, err)
	afterTotal, err := state.ParseXRPLNumber(info.AssetsTotal, state.MantissaScaleLarge, state.RoundToNearest)
	require.NoError(t, err)
	afterAvailable, err := state.ParseXRPLNumber(info.AssetsAvailable, state.MantissaScaleLarge, state.RoundToNearest)
	require.NoError(t, err)
	totalDelta := afterTotal.Sub(beforeTotal)
	require.True(t, totalDelta.Sub(state.NewXRPLNumber(2, -9)).IsZero())
	require.True(t, afterAvailable.Sub(beforeAvailable).Sub(totalDelta).IsZero())
	shareValue := beforeTotal.Mul(state.NewXRPLNumber(4, 0)).Div(state.NewXRPLNumber(2_000_000_000_000_000, 0))
	require.LessOrEqual(t, totalDelta.Sub(shareValue).Signum(), 0)
	ulp := state.NewXRPLNumber(1, afterTotal.AssetExponent(false, state.RoundToNearest))
	require.Less(t, shareValue.Sub(totalDelta).Sub(ulp).Signum(), 0)
	raw, err = env.LedgerEntry(keylet.MPTIssuance(info.ShareMPTID))
	require.NoError(t, err)
	issuance, err = state.ParseMPTokenIssuance(raw)
	require.NoError(t, err)
	require.Equal(t, uint64(2_000_000_000_000_004), issuance.OutstandingAmount)
	require.Equal(t, uint64(4), vaultShareBalance(t, env, info.ShareMPTID, depositor))
}

func TestVaultWithdrawPrecisionLossClaimsFeeAndRollsBack(t *testing.T) {
	env := newVaultEnv(t)
	env.EnableFeature("fixCleanup3_4_0")
	env.Close()
	issuer := jtx.NewAccount("withdraw-precision-issuer")
	owner := jtx.NewAccount("withdraw-precision-owner")
	holder := jtx.NewAccount("withdraw-precision-holder")
	env.Fund(issuer)
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).DefaultRipple().Build()))
	env.Fund(owner, holder)
	const currency = "USD"
	asset := tx.Asset{Currency: currency, Issuer: issuer.Address}
	env.Trust(holder, tx.NewIssuedAmountFromFloat64(10, currency, issuer.Address))
	env.PayIOU(issuer, holder, issuer, currency, 3)

	seq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, asset)
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))
	id := vaultID(owner, seq)
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(
		holder.Address, id, tx.NewIssuedAmountFromFloat64(3, currency, issuer.Address))))

	vaultKey := keylet.Vault(owner.AccountID(), seq)
	info, err := vault.ReadVaultLending(env.Ledger(), vaultKey)
	require.NoError(t, err)
	require.NotNil(t, info)
	patchVaultNumbers(t, env, vaultKey, "3", "3")
	patchIssuanceAmount(t, env, info.ShareMPTID, 2)
	patchTokenAmount(t, env, info.ShareMPTID, holder.ID, 2)

	issuanceKey := keylet.MPTIssuance(info.ShareMPTID)
	holderTokenKey := keylet.MPTokenByID(info.ShareMPTID, holder.ID)
	pseudoLineKey := keylet.Line(info.Account, issuer.ID, currency)
	holderLineKey := keylet.Line(holder.ID, issuer.ID, currency)
	snapshots := map[keylet.Keylet][]byte{}
	for _, key := range []keylet.Keylet{vaultKey, issuanceKey, holderTokenKey, pseudoLineKey, holderLineKey} {
		raw, readErr := env.LedgerEntry(key)
		require.NoError(t, readErr)
		snapshots[key] = append([]byte(nil), raw...)
	}
	sequenceBefore := env.Seq(holder)
	balanceBefore := env.Balance(holder)

	result := env.Submit(vault.NewVaultWithdraw(
		holder.Address, id, tx.NewIssuedAmountFromFloat64(1, currency, issuer.Address)))
	require.Equal(t, ter.TecPRECISION_LOSS.String(), result.Code)
	require.True(t, result.Applied)
	require.Equal(t, env.BaseFee(), result.Fee)
	require.NotNil(t, result.Metadata)
	require.Len(t, result.Metadata.AffectedNodes, 1)
	accountNode := result.Metadata.AffectedNodes[0]
	require.Equal(t, "ModifiedNode", accountNode.NodeType)
	require.Equal(t, "AccountRoot", accountNode.LedgerEntryType)
	require.Equal(t, strconv.FormatUint(balanceBefore, 10), accountNode.PreviousFields["Balance"])
	require.Equal(t, strconv.FormatUint(balanceBefore-env.BaseFee(), 10), accountNode.FinalFields["Balance"])
	require.Equal(t, sequenceBefore, accountNode.PreviousFields["Sequence"])
	require.Equal(t, sequenceBefore+1, accountNode.FinalFields["Sequence"])
	require.Equal(t, sequenceBefore+1, env.Seq(holder))
	require.Equal(t, balanceBefore-env.BaseFee(), env.Balance(holder))
	for key, before := range snapshots {
		after, readErr := env.LedgerEntry(key)
		require.NoError(t, readErr)
		require.Equal(t, before, after, "precision-loss withdrawal changed %v", key)
	}
}

func requireMetadataEntries(t *testing.T, metadata *tx.Metadata, entries ...string) {
	t.Helper()
	got := make(map[string]bool, len(metadata.AffectedNodes))
	for _, node := range metadata.AffectedNodes {
		got[node.LedgerEntryType] = true
	}
	for _, entryType := range entries {
		require.True(t, got[entryType], "metadata missing %s", entryType)
	}
}

func patchIssuanceAmount(t *testing.T, env *jtx.TestEnv, issuanceID [24]byte, amount uint64) {
	t.Helper()
	key := keylet.MPTIssuance(issuanceID)
	raw, err := env.LedgerEntry(key)
	require.NoError(t, err)
	issuance, err := state.ParseMPTokenIssuance(raw)
	require.NoError(t, err)
	issuance.OutstandingAmount = amount
	raw, err = state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(key, raw))
}

func patchTokenAmount(t *testing.T, env *jtx.TestEnv, issuanceID [24]byte, holder [20]byte, amount uint64) {
	t.Helper()
	key := keylet.MPTokenByID(issuanceID, holder)
	raw, err := env.LedgerEntry(key)
	require.NoError(t, err)
	token, err := state.ParseMPToken(raw)
	require.NoError(t, err)
	token.MPTAmount = amount
	raw, err = state.SerializeMPToken(token)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(key, raw))
}

func patchVaultNumbers(t *testing.T, env *jtx.TestEnv, key keylet.Keylet, total, available string) {
	t.Helper()
	raw, err := env.LedgerEntry(key)
	require.NoError(t, err)
	var wire entry.Vault
	require.NoError(t, wire.Decode(raw))
	wire.SetAssetsTotal(total)
	wire.SetAssetsAvailable(available)
	raw, err = wire.Encode()
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(key, raw))
}

func patchLineBalance(t *testing.T, env *jtx.TestEnv, key keylet.Keylet, issuer, perspective [20]byte, amount state.Amount) {
	t.Helper()
	raw, err := env.LedgerEntry(key)
	require.NoError(t, err)
	line, err := state.ParseRippleState(raw)
	require.NoError(t, err)
	if !keylet.IsLowAccount(perspective, issuer) {
		amount = amount.Negate()
	}
	line.Balance = amount
	raw, err = state.SerializeRippleState(line)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(key, raw))
}
