package vault_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestVaultZeroSelfWithdrawalPreservesExpiredCredential(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		t.Run(kind, func(t *testing.T) {
			f := newZeroAssetWithdrawFixture(t, kind, true)
			expiration := f.env.NowRipple() + 1000
			jtx.RequireTxSuccess(t, f.env.Submit(credentialtest.CredentialCreateText(f.owner, f.holder, "withdraw").Expiration(expiration).Build()))
			jtx.RequireTxSuccess(t, f.env.Submit(credentialtest.CredentialAcceptText(f.holder, f.owner, "withdraw").Build()))
			f.env.CloseToParentCloseTime(expiration + 1)
			credentialKey := keylet.Credential(f.holder.ID, f.owner.ID, []byte("withdraw"))
			credentialBefore, err := f.env.LedgerEntry(credentialKey)
			require.NoError(t, err)
			balance, sequence, ownerCount := f.env.Balance(f.holder), f.env.Seq(f.holder), f.env.OwnerCount(f.holder)
			shares := vaultShareBalance(t, f.env, f.shareID, f.holder)
			withdraw := f.withdraw()
			withdraw.CredentialIDs = []string{hex.EncodeToString(credentialKey.Key[:])}
			result := f.env.Submit(withdraw)
			jtx.RequireTxSuccess(t, result)
			credentialAfter, err := f.env.LedgerEntry(credentialKey)
			require.NoError(t, err)
			require.Equal(t, credentialBefore, credentialAfter)
			require.False(t, f.env.LedgerEntryExists(f.assetHolding))
			require.Equal(t, shares-f.shareAmount, vaultShareBalance(t, f.env, f.shareID, f.holder))
			require.Equal(t, balance-f.env.BaseFee(), f.env.Balance(f.holder))
			require.Equal(t, sequence+1, f.env.Seq(f.holder))
			require.Equal(t, ownerCount, f.env.OwnerCount(f.holder))
			require.NotNil(t, result.Metadata)
			for _, node := range result.Metadata.AffectedNodes {
				require.False(t, strings.EqualFold(node.LedgerIndex, withdraw.CredentialIDs[0]), "self withdrawal changed expired credential")
			}
		})
	}
}
