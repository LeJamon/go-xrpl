package amm_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestAMMAutoDeletionRejectsPinnedCredential(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup=%v", cleanup), func(t *testing.T) {
			env := setupAMM(t)
			if cleanup {
				env.EnableFeature("fixCleanup3_4_0")
			}
			env.DisableFeature("fixCleanup3_3_0")
			env.Close()
			pseudo := env.ReadAMMAccount(amm.XRP(), env.USD)
			require.NotNil(t, pseudo)
			jtx.RequireTxSuccess(t, env.Submit(credential.CredentialCreateText(env.Carol, pseudo, "pinned").Build()))
			env.EnableFeature("fixCleanup3_3_0")
			env.Close()
			ck := keylet.Credential(pseudo.ID, env.Carol.ID, []byte("pinned"))
			before, err := env.LedgerEntry(keylet.Account(pseudo.ID))
			require.NoError(t, err)
			result := env.Submit(amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).WithdrawAll().Build())
			jtx.RequireTxFail(t, result, "tecINTERNAL")
			after, err := env.LedgerEntry(keylet.Account(pseudo.ID))
			require.NoError(t, err)
			require.Equal(t, before, after)
			jtx.RequireLedgerEntryExists(t, env.TestEnv, ck)
			jtx.RequireOwnerDirectoryContains(t, env.TestEnv, pseudo, ck.Key, true)
		})
	}
}
