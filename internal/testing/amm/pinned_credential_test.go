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
			env.DisableFeature("fixCleanup3_5_0")
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

// Reference: rippled Credentials_test.cpp testCreateFailed (pseudo-account subject).
func TestCredentialCreateRejectsPseudoAccountSubject(t *testing.T) {
	cases := []struct {
		name    string
		fix330  bool
		fix350  bool
		want    string
		pinning bool
	}{
		{name: "fixCleanup3_3_0", fix330: true, fix350: true, want: "tecPSEUDO_ACCOUNT"},
		{name: "fixCleanup3_5_0 invariant", fix350: true, want: "tecINVARIANT_FAILED"},
		{name: "legacy", want: "tesSUCCESS", pinning: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := setupAMM(t)
			for name, on := range map[string]bool{"fixCleanup3_3_0": c.fix330, "fixCleanup3_5_0": c.fix350} {
				if on {
					env.EnableFeature(name)
				} else {
					env.DisableFeature(name)
				}
			}
			env.Close()
			pseudo := env.ReadAMMAccount(amm.XRP(), env.USD)
			require.NotNil(t, pseudo)
			result := env.Submit(credential.CredentialCreateText(env.Carol, pseudo, "pinned").Build())
			require.Equal(t, c.want, result.Code)
			ck := keylet.Credential(pseudo.ID, env.Carol.ID, []byte("pinned"))
			jtx.RequireOwnerDirectoryContains(t, env.TestEnv, pseudo, ck.Key, c.pinning)
		})
	}
}
