package vault_test

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestVaultCleanupIOUDepositConservation(t *testing.T) {
	env := newVaultEnv(t)
	env.EnableFeature("fixCleanup3_4_0")
	env.Close()
	issuer, owner, depositor := jtx.NewAccount("precision-issuer"), jtx.NewAccount("precision-owner"), jtx.NewAccount("precision-depositor")
	env.Fund(issuer, owner, depositor)
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).DefaultRipple().Build()))
	for _, account := range []*jtx.Account{owner, depositor} {
		env.Trust(account, tx.NewIssuedAmountFromFloat64(1000000000, "USD", issuer.Address))
		env.PayIOU(issuer, account, issuer, "USD", 100000000)
	}
	sequence := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "USD", Issuer: issuer.Address})
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))
	id := vaultID(owner, sequence)
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(owner.Address, id, tx.NewIssuedAmountFromFloat64(1000, "USD", issuer.Address))))
	vaultKey := keylet.Vault(owner.ID, sequence)
	raw, err := env.LedgerEntry(vaultKey)
	require.NoError(t, err)
	fields, err := binarycodec.DecodeBytes(raw)
	require.NoError(t, err)
	fields["AssetsTotal"] = "1000.123456789012"
	raw, err = binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(vaultKey, raw))
	pseudo, err := state.DecodeAccountID(fields["Account"].(string))
	require.NoError(t, err)
	parse := func(value any) state.XRPLNumber {
		t.Helper()
		n, err := state.ParseXRPLNumber(fmt.Sprint(value), state.MantissaScaleLarge, state.RoundToNearest)
		require.NoError(t, err)
		return n
	}
	balance := func(account [20]byte) state.XRPLNumber {
		t.Helper()
		data, err := env.LedgerEntry(keylet.Line(account, issuer.ID, "USD"))
		require.NoError(t, err)
		line, err := state.ParseRippleState(data)
		require.NoError(t, err)
		n := parse(line.Balance.Value())
		if state.CompareAccountIDs(account, issuer.ID) > 0 {
			n = n.Negate()
		}
		return n
	}
	snapshot := func() (state.XRPLNumber, state.XRPLNumber, state.XRPLNumber, state.XRPLNumber) {
		t.Helper()
		data, err := env.LedgerEntry(vaultKey)
		require.NoError(t, err)
		fields, err := binarycodec.DecodeBytes(data)
		require.NoError(t, err)
		return parse(fields["AssetsTotal"]), parse(fields["AssetsAvailable"]), balance(pseudo), balance(depositor.ID)
	}
	shareBytes, err := hex.DecodeString(fields["ShareMPTID"].(string))
	require.NoError(t, err)
	var shareID [24]byte
	copy(shareID[:], shareBytes)
	shareSupply := func() state.XRPLNumber {
		t.Helper()
		data, err := env.LedgerEntry(keylet.MPTIssuance(shareID))
		require.NoError(t, err)
		issuance, err := state.ParseMPTokenIssuance(data)
		require.NoError(t, err)
		return parse(issuance.OutstandingAmount)
	}
	for _, amount := range []float64{1, 7, 1000, 10000000} {
		beforeTotal, beforeAvailable, beforePseudo, beforeDepositor := snapshot()
		beforeShares := shareSupply()
		jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(depositor.Address, id, tx.NewIssuedAmountFromFloat64(amount, "USD", issuer.Address))))
		total, available, pseudoBalance, depositorBalance := snapshot()
		delta := total.Sub(beforeTotal)
		minted := shareSupply().Sub(beforeShares)
		expectedShares := beforeShares.Mul(parse(amount)).Div(beforeTotal).Truncate()
		require.True(t, minted.Equal(expectedShares), "share count changed during clamp: %s vs %s", minted.String(), expectedShares.String())
		shareValue := beforeTotal.Mul(minted).Div(beforeShares)
		require.True(t, delta.Cmp(shareValue) <= 0, "debit exceeds share value")
		assetUnit := state.NewXRPLNumber(1, total.AssetExponent(false, state.RoundToNearest))
		for _, change := range []state.XRPLNumber{available.Sub(beforeAvailable), pseudoBalance.Sub(beforePseudo)} {
			gap := delta.Sub(change)
			if gap.Signum() < 0 {
				gap = gap.Negate()
			}
			require.True(t, gap.Cmp(assetUnit) <= 0, "delta %s vs %s exceeds asset unit %s", delta.String(), change.String(), assetUnit.String())
		}

		// The depositor balance is coarser than the vault in this fixture.
		ulp := state.NewXRPLNumber(1, beforeDepositor.AssetExponent(false, state.RoundToNearest))
		difference := beforeDepositor.Sub(depositorBalance).Sub(delta)
		if difference.Signum() < 0 {
			difference = difference.Negate()
		}
		require.True(t, difference.Cmp(ulp) <= 0)
	}
	before, err := env.LedgerEntry(vaultKey)
	require.NoError(t, err)
	sequenceBefore := env.Seq(depositor)
	jtx.RequireTxClaimed(t, env.Submit(vault.NewVaultDeposit(depositor.Address, id, tx.NewIssuedAmountFromFloat64(1e-10, "USD", issuer.Address))), "tecPRECISION_LOSS")
	after, err := env.LedgerEntry(vaultKey)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, sequenceBefore+1, env.Seq(depositor))
}
