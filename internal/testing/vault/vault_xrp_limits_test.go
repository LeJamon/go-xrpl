package vault_test

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	paytest "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestVaultWithdraw_XRPSharesBeyondNativeMaxIsException(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup=%t", cleanup), func(t *testing.T) {
			env := newVaultEnv(t)
			if cleanup {
				env.EnableFeature("fixCleanup3_1_3")
			} else {
				env.DisableFeature("fixCleanup3_1_3")
			}
			env.Close()
			require.Equal(t, cleanup, env.Rules().Enabled(amendment.FeatureFixCleanup3_1_3))

			owner := jtx.NewAccount("xrp-max-owner")
			depositor := jtx.NewAccount("xrp-max-depositor")
			env.Fund(owner, depositor)
			createSequence := env.Seq(owner)
			create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
			create.Common.Fee = createFee
			jtx.RequireTxSuccess(t, env.Submit(create))
			id := vaultID(owner, createSequence)
			jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(depositor.Address, id, tx.NewXRPAmount(1_000))))
			env.Close()

			vaultKey := keylet.Vault(owner.ID, createSequence)
			info, err := vault.ReadVaultInfo(env.Ledger(), vaultKey)
			require.NoError(t, err)
			require.NotNil(t, info)
			issuanceKey := keylet.MPTIssuance(info.ShareMPTID)
			issuanceData, err := env.LedgerEntry(issuanceKey)
			require.NoError(t, err)
			issuance, err := state.ParseMPTokenIssuance(issuanceData)
			require.NoError(t, err)
			beforeVault, err := env.LedgerEntry(vaultKey)
			require.NoError(t, err)

			// Each drop is worth OutstandingAmount/1000 shares, so this many
			// shares redeem 2e17 drops: above the XRP cap but within int64.
			shares := int64(issuance.OutstandingAmount/1_000) * 200_000_000_000_000_000
			beforeBalance := env.Balance(depositor)
			beforeSequence := env.Seq(depositor)
			withdraw := vault.NewVaultWithdraw(
				depositor.Address,
				id,
				state.NewMPTAmountWithIssuanceID(shares, "", hex.EncodeToString(info.ShareMPTID[:])),
			)
			result := env.Submit(withdraw)
			require.Equal(t, "tefEXCEPTION", result.Code, result.Message)
			require.False(t, result.Applied)
			require.Equal(t, beforeBalance, env.Balance(depositor))
			require.Equal(t, beforeSequence, env.Seq(depositor))

			afterVault, err := env.LedgerEntry(vaultKey)
			require.NoError(t, err)
			require.Equal(t, beforeVault, afterVault)
			afterIssuance, err := env.LedgerEntry(issuanceKey)
			require.NoError(t, err)
			require.Equal(t, issuanceData, afterIssuance)
		})
	}
}

func TestVaultDeposit_XRPFeeShortfallFailsProcessing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		open    bool
		want    string
		applied bool
	}{
		{name: "ClosedView", want: "tecFAILED_PROCESSING", applied: true},
		{name: "OpenView", open: true, want: "telFAILED_PROCESSING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newVaultEnv(t)
			owner := jtx.NewAccount("fee-drain-owner")
			depositor := jtx.NewAccount("fee-drain-depositor")
			env.Fund(owner, depositor)
			createSequence := env.Seq(owner)
			create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
			create.Common.Fee = createFee
			jtx.RequireTxSuccess(t, env.Submit(create))
			id := vaultID(owner, createSequence)
			// The first deposit creates the share holding so the drained
			// deposit needs no extra reserve.
			jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(depositor.Address, id, tx.NewXRPAmount(1_000))))

			// The whole liquid balance passes preclaim, but a fee above the
			// reserve leaves the post-fee balance one drop short of it.
			const amount = int64(100_000_000)
			reserve := env.ReserveBase() + uint64(env.OwnerCount(depositor))*env.ReserveIncrement()
			fee := reserve + 1
			drainTo(t, env, depositor, owner, reserve+uint64(amount))
			vaultKey := keylet.Vault(owner.ID, createSequence)
			beforeVault, err := env.LedgerEntry(vaultKey)
			require.NoError(t, err)
			beforeBalance := env.Balance(depositor)
			beforeSequence := env.Seq(depositor)

			env.SetOpenLedger(tc.open)
			deposit := vault.NewVaultDeposit(depositor.Address, id, tx.NewXRPAmount(amount))
			deposit.Common.Fee = fmt.Sprint(fee)
			result := env.Submit(deposit)
			env.SetOpenLedger(true)

			require.Equal(t, tc.want, result.Code, result.Message)
			require.Equal(t, tc.applied, result.Applied)
			wantBalance, wantSequence := beforeBalance, beforeSequence
			if tc.applied {
				wantBalance -= fee
				wantSequence++
			}
			require.Equal(t, wantBalance, env.Balance(depositor))
			require.Equal(t, wantSequence, env.Seq(depositor))
			afterVault, err := env.LedgerEntry(vaultKey)
			require.NoError(t, err)
			require.Equal(t, beforeVault, afterVault)
		})
	}
}

func drainTo(t *testing.T, env *jtx.TestEnv, account, sink *jtx.Account, target uint64) {
	t.Helper()
	balance := env.Balance(account)
	require.Greater(t, balance, target+env.BaseFee())
	jtx.RequireTxSuccess(t, env.Submit(paytest.Pay(account, sink, balance-target-env.BaseFee()).Build()))
	require.Equal(t, target, env.Balance(account))
}
