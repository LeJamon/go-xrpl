// Preflight TER-precedence pins for AccountDelete: the CredentialIDs amendment
// gate is a checkExtraFeatures rejection that runs before preflight1 and the
// AccountDelete preflight body, so temDISABLED wins over the shape check
// (temMALFORMED), the self-delete check (temDST_IS_SRC), and every ledger-state
// TER (terPRE_SEQ).
//
// Reference: rippled DeleteAccount.cpp checkExtraFeatures().
package accountdelete_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	acctx "github.com/LeJamon/go-xrpl/internal/tx/account"
)

// credID is a well-formed 256-bit credential id (64 hex chars).
const credID = "ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB0"

// newCredEnv builds an environment with featureCredentials disabled and one
// funded account, so a CredentialIDs-bearing AccountDelete hits the
// checkExtraFeatures temDISABLED gate.
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

// TestAccountDelete_CredentialsDisabledPrecedence pins finding AccountDelete-order
// across its three illustrations: the temDISABLED gate beats the duplicate-id
// shape check, the self-delete check, and a sequence gap.
func TestAccountDelete_CredentialsDisabledPrecedence(t *testing.T) {
	t.Run("beats duplicate-id shape check", func(t *testing.T) {
		env, alice, becky := newCredEnv(t)
		d := acctx.NewAccountDelete(alice.Address, becky.Address)
		d.Fee = "10"
		d.CredentialIDs = []string{credID, credID} // duplicate → temMALFORMED if reached
		jtx.RequireTxFail(t, env.Submit(d), "temDISABLED")
	})

	t.Run("beats self-delete check", func(t *testing.T) {
		env, alice, _ := newCredEnv(t)
		d := acctx.NewAccountDelete(alice.Address, alice.Address) // dest==src → temDST_IS_SRC if reached
		d.Fee = "10"
		d.CredentialIDs = []string{credID}
		jtx.RequireTxFail(t, env.Submit(d), "temDISABLED")
	})

	t.Run("beats sequence gap", func(t *testing.T) {
		env, alice, becky := newCredEnv(t)
		d := acctx.NewAccountDelete(alice.Address, becky.Address)
		d.Fee = "10"
		d.CredentialIDs = []string{credID}
		d.SetSequence(env.Seq(alice) + 10) // future sequence → terPRE_SEQ if reached
		jtx.RequireTxFail(t, env.Submit(d), "temDISABLED")
	})
}
