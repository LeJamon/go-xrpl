// Preflight TER-precedence pin for SetRegularKey: the fixMasterKeyAsRegularKey
// RegularKey==Account rejection runs in the preflight body, before any
// ledger-state check.
//
// Reference: rippled SetRegularKey.cpp preflight().
package setregularkey_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/signerlist"
)

// TestSetRegularKey_BadRegKeyPrecedence pins finding SetRegularKey-order: setting
// the regular key to the account's own address is temBAD_REGKEY (preflight),
// which wins over the sequence gap that preclaim would otherwise report as
// terPRE_SEQ. fixMasterKeyAsRegularKey is active in the default preset.
func TestSetRegularKey_BadRegKeyPrecedence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	setKey := signerlist.NewSetRegularKey(alice.Address)
	setKey.SetKey(alice.Address) // RegularKey == Account
	setKey.SetSequence(env.Seq(alice) + 10)
	jtx.RequireTxFail(t, env.Submit(setKey), "temBAD_REGKEY")
}
