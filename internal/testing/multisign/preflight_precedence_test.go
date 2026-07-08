// Preflight TER-precedence pins for SignerListSet: the flags mask (preflight0)
// and the signer-entry validation (preflight body) both run before any
// ledger-state check, matching rippled's SetSignerList preflight ordering.
//
// Reference: rippled SetSignerList.cpp getFlagsMask() / preflight().
package multisign_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/signerlist"
)

// invalidTxFlag is a non-universal flag bit; with fixInvalidTxFlags active it is
// outside SignerListSet's allowed set and must be rejected as temINVALID_FLAG.
const invalidTxFlag uint32 = 0x00010000

// TestSignerListSet_FlagsMaskPrecedence pins finding SignerListSet-mask: with
// fixInvalidTxFlags active the flags mask is evaluated at preflight0, so it wins
// over the determineOperation shape check (temMALFORMED) and over the sequence
// gap (terPRE_SEQ) that a preclaim check would otherwise report.
func TestSignerListSet_FlagsMaskPrecedence(t *testing.T) {
	t.Run("mask beats malformed shape", func(t *testing.T) {
		env := jtx.NewTestEnv(t) // fixInvalidTxFlags active in the default preset
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		// quorum==0 with entries present is neither a set nor a destroy, so
		// Validate() reports temMALFORMED — but the invalid flag must win.
		sls := signerlist.NewSignerListSet(alice.Address, 0)
		sls.AddSigner(jtx.NewAccount("bob").Address, 1)
		sls.SetFlags(invalidTxFlag)
		jtx.RequireTxFail(t, env.Submit(sls), "temINVALID_FLAG")
	})

	t.Run("mask beats sequence gap", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		// A well-formed set list with an invalid flag and a future sequence:
		// without the preflight0 mask this would report terPRE_SEQ from preclaim.
		sls := signerlist.NewSignerListSet(alice.Address, 2)
		sls.AddSigner(jtx.NewAccount("bob").Address, 1)
		sls.AddSigner(jtx.NewAccount("carol").Address, 1)
		sls.SetFlags(invalidTxFlag)
		sls.SetSequence(env.Seq(alice) + 10)
		jtx.RequireTxFail(t, env.Submit(sls), "temINVALID_FLAG")
	})
}

// TestSignerListSet_EntryValidationPrecedence pins finding SignerListSet-order:
// the signer-entry validation runs in the preflight body, so an over-cap list
// (33 entries with ExpandedSignerList) reports temMALFORMED even when the
// transaction also carries a sequence gap that preclaim would flag as terPRE_SEQ.
func TestSignerListSet_EntryValidationPrecedence(t *testing.T) {
	env := jtx.NewTestEnv(t) // ExpandedSignerList active → cap is 32
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	sls := signerlist.NewSignerListSet(alice.Address, 1)
	for _, s := range signersOfWeightOne(33) {
		sls.AddSigner(s.Account.Address, s.Weight)
	}
	sls.SetSequence(env.Seq(alice) + 10)
	jtx.RequireTxFail(t, env.Submit(sls), "temMALFORMED")
}
