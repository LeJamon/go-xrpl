package accountdelete_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	acctx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/stretchr/testify/require"
)

const credID = "ABABABABABABABAB" +
	"ABABABABABABABAB" +
	"ABABABABABABABAB" +
	"ABABABABABABABAB"

func newCredEnv(t *testing.T) (*jtx.TestEnv, *jtx.Account, *jtx.Account) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("Credentials")
	env.Close()
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	env.Fund(alice, becky)
	env.Close()
	return env, alice, becky
}

func TestAccountDelete_CredentialsDisabledPrecedence(t *testing.T) {
	decoded, err := hex.DecodeString(credID)
	require.NoError(t, err)
	require.Len(t, decoded, 32)

	t.Run("beats empty-present shape check", func(t *testing.T) {
		env, alice, becky := newCredEnv(t)
		d := acctx.NewAccountDelete(alice.Address, becky.Address)
		d.Fee = "10"
		d.CredentialIDs = []string{}
		jtx.RequireTxFail(t, env.Submit(d), jtx.TemDISABLED)
	})

	t.Run("beats duplicate-id shape check", func(t *testing.T) {
		env, alice, becky := newCredEnv(t)
		d := acctx.NewAccountDelete(alice.Address, becky.Address)
		d.Fee = "10"
		d.CredentialIDs = []string{credID, credID}
		jtx.RequireTxFail(t, env.Submit(d), jtx.TemDISABLED)
	})

	t.Run("beats self-delete check", func(t *testing.T) {
		env, alice, _ := newCredEnv(t)
		d := acctx.NewAccountDelete(alice.Address, alice.Address)
		d.Fee = "10"
		d.CredentialIDs = []string{credID}
		jtx.RequireTxFail(t, env.Submit(d), jtx.TemDISABLED)
	})

	t.Run("beats sequence gap", func(t *testing.T) {
		env, alice, becky := newCredEnv(t)
		d := acctx.NewAccountDelete(alice.Address, becky.Address)
		d.Fee = "10"
		d.CredentialIDs = []string{credID}
		d.SetSequence(env.Seq(alice) + 10)
		jtx.RequireTxFail(t, env.Submit(d), jtx.TemDISABLED)
	})
}

func TestAccountDelete_ValidCredentialFixtureReachesSequenceCheck(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	env.Fund(alice, becky)
	env.Close()

	d := acctx.NewAccountDelete(alice.Address, becky.Address)
	d.Fee = "10"
	d.CredentialIDs = []string{credID}
	d.SetSequence(env.Seq(alice) + 10)
	jtx.RequireTxFail(t, env.Submit(d), jtx.TerPRE_SEQ)
}

func TestAccountDelete_EmptyCredentialIDsMalformedWhenEnabled(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	env.Fund(alice, becky)
	env.Close()

	d := acctx.NewAccountDelete(alice.Address, becky.Address)
	d.Fee = "10"
	d.CredentialIDs = []string{}
	jtx.RequireTxFail(t, env.Submit(d), jtx.TemMALFORMED)
}

func TestAccountDelete_MixedCaseCredentialIDsAreDuplicates(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	env.Fund(alice, becky)
	env.Close()

	d := acctx.NewAccountDelete(alice.Address, becky.Address)
	d.Fee = "10"
	d.CredentialIDs = []string{credID, strings.ToLower(credID)}
	jtx.RequireTxFail(t, env.Submit(d), jtx.TemMALFORMED)
}
