package vault_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestVaultCreateClosedEndedDateBoundaries(t *testing.T) {
	const investmentPeriod uint32 = 180
	for _, tc := range []struct {
		name   string
		delta  int64
		expect string
	}{
		{name: "one second before subscription", delta: -1, expect: jtx.TecEXPIRED},
		{name: "at subscription", delta: 0, expect: jtx.TecEXPIRED},
		{name: "one second after subscription", delta: 1, expect: jtx.TesSUCCESS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newVaultEnv(t)
			env.EnableFeature("LendingProtocolV1_1")
			env.Close()
			owner := jtx.NewAccount("owner")
			env.Fund(owner)

			beforeBalance := env.Balance(owner)
			beforeSequence := env.Seq(owner)
			subscription := uint32(int64(env.NowRipple()) + tc.delta)
			redemption := subscription + investmentPeriod
			kind := vault.VaultKindClosedEnded
			create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
			create.Common.Fee = createFee
			create.VaultKind = &kind
			create.SubscriptionDate = &subscription
			create.RedemptionDate = &redemption

			result := env.Submit(create)
			if tc.expect == jtx.TesSUCCESS {
				jtx.RequireTxSuccess(t, result)
				require.Equal(t, uint64(50_000_000), result.Fee)
				require.Equal(t, beforeBalance-result.Fee, env.Balance(owner))
				require.Equal(t, beforeSequence+1, env.Seq(owner))

				info, err := vault.ReadVaultInfo(env.Ledger(), keylet.Vault(owner.AccountID(), beforeSequence))
				require.NoError(t, err)
				require.NotNil(t, info)
				require.Equal(t, vault.VaultKindClosedEnded, info.VaultKind)
				require.NotNil(t, info.SubscriptionDate)
				require.Equal(t, subscription, *info.SubscriptionDate)
				require.NotNil(t, info.RedemptionDate)
				require.Equal(t, redemption, *info.RedemptionDate)
				return
			}

			jtx.RequireTxClaimed(t, result, tc.expect)
			require.Equal(t, uint64(50_000_000), result.Fee)
			require.Equal(t, beforeBalance-result.Fee, env.Balance(owner))
			require.Equal(t, beforeSequence+1, env.Seq(owner))
			require.False(t, env.LedgerEntryExists(keylet.Vault(owner.AccountID(), beforeSequence)))
		})
	}
}

func TestVaultClosedEndedDepositWithdrawPhases(t *testing.T) {
	env := newVaultEnv(t)
	env.EnableFeature("LendingProtocolV1_1")
	env.Close()
	owner := jtx.NewAccount("owner")
	depositor := jtx.NewAccount("depositor")
	env.Fund(owner, depositor)

	createSequence := env.Seq(owner)
	subscription := env.NowRipple() + 60
	redemption := subscription + 180
	kind := vault.VaultKindClosedEnded
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = createFee
	create.VaultKind = &kind
	create.SubscriptionDate = &subscription
	create.RedemptionDate = &redemption
	createResult := env.Submit(create)
	jtx.RequireTxSuccess(t, createResult)
	vaultID := vaultID(owner, createSequence)

	deposit := func() jtx.TxResult {
		return env.Submit(vault.NewVaultDeposit(depositor.Address, vaultID, tx.NewXRPAmount(1_000_000)))
	}

	// Subscription includes both the pre-date and exact-date boundaries.
	jtx.RequireTxSuccess(t, deposit())
	env.CloseToParentCloseTime(subscription)
	sequenceAtSubscription := env.Seq(depositor)
	jtx.RequireTxSuccess(t, deposit())
	if got := env.Seq(depositor); got != sequenceAtSubscription+1 {
		t.Fatalf("depositor sequence after subscription deposit = %d, want %d", got, sequenceAtSubscription+1)
	}

	// Investment rejects deposits and withdrawals before amount/asset work.
	env.CloseToParentCloseTime(subscription + 1)
	depositResult := deposit()
	jtx.RequireTxClaimed(t, depositResult, jtx.TecEXPIRED)
	require.Equal(t, env.BaseFee(), depositResult.Fee)
	withdrawResult := env.Submit(vault.NewVaultWithdraw(depositor.Address, vaultID, tx.NewXRPAmount(1_000_000)))
	jtx.RequireTxClaimed(t, withdrawResult, "tecTOO_SOON")
	require.Equal(t, env.BaseFee(), withdrawResult.Fee)

	// Redemption continues to reject deposits while permitting withdrawals.
	env.CloseToParentCloseTime(redemption)
	depositResult = deposit()
	jtx.RequireTxClaimed(t, depositResult, jtx.TecEXPIRED)
	withdrawResult = env.Submit(vault.NewVaultWithdraw(depositor.Address, vaultID, tx.NewXRPAmount(1_000_000)))
	jtx.RequireTxSuccess(t, withdrawResult)

	info, err := vault.ReadVaultInfo(env.Ledger(), keylet.VaultByID(keylet.Vault(owner.AccountID(), createSequence).Key))
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, vault.VaultKindClosedEnded, info.VaultKind)
	require.NotNil(t, info.SubscriptionDate)
	require.Equal(t, subscription, *info.SubscriptionDate)
	require.NotNil(t, info.RedemptionDate)
	require.Equal(t, redemption, *info.RedemptionDate)
}
