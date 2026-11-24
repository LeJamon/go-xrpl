package vault_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	paytest "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/stretchr/testify/require"
)

func TestVaultWithdrawMPTokenFreeTier(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "before cleanup"
		if cleanup {
			name = "after cleanup"
		}
		t.Run(name, func(t *testing.T) {
			f := newVaultWithdrawMPTFixture(t, false, cleanup, true, false)
			require.Equal(t, uint32(1), f.env.OwnerCount(f.depositor))
			require.False(t, f.env.LedgerEntryExists(f.assetHold))
			sink := jtx.NewAccount("free-tier-reserve-sink")
			f.env.Fund(sink)
			reserve := f.env.ReserveBase() + f.env.ReserveIncrement()
			jtx.RequireTxSuccess(t, f.env.Submit(paytest.Pay(f.depositor, sink,
				f.env.Balance(f.depositor)-reserve-f.env.BaseFee()).Build()))
			require.Equal(t, reserve, f.env.Balance(f.depositor))
			sharesBefore := vaultShareBalance(t, f.env, f.shareID, f.depositor)
			withdraw := vault.NewVaultWithdraw(f.depositor.Address, f.vaultID,
				state.NewMPTAmountWithIssuanceID(50, "", mptutil.EncodeID(f.assetID)))
			jtx.RequireTxSuccess(t, f.env.Submit(withdraw))
			require.Equal(t, uint32(2), f.env.OwnerCount(f.depositor))
			require.Equal(t, reserve-f.env.BaseFee(), f.env.Balance(f.depositor))
			require.Less(t, vaultShareBalance(t, f.env, f.shareID, f.depositor), sharesBefore)
			raw, err := f.env.LedgerEntry(f.assetHold)
			require.NoError(t, err)
			holding, err := state.ParseMPToken(raw)
			require.NoError(t, err)
			require.Equal(t, uint64(50), holding.MPTAmount)
		})
	}
}
