package accountset

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
)

// TestAccountSet_LegacyTxFlags verifies that the legacy transaction-level Flags
// bits (tfRequireDestTag/tfOptionalDestTag, tfDisallowXRP/tfAllowXRP,
// tfOptionalAuth) drive the corresponding ledger flags, with no asf
// SetFlag/ClearFlag field present.
//
// Reference: rippled SetAccount.cpp doApply() lines 326-339 — bSetRequireDest,
// bClearRequireDest, bSetDisallowXRP, bClearDisallowXRP, bClearRequireAuth all
// OR the legacy tx Flags bit with the asf form.
func TestAccountSet_LegacyTxFlags(t *testing.T) {
	t.Run("RequireDestTag", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.FundNoRipple(alice)
		env.Close()

		jtx.RequireFlagNotSet(t, env, alice, state.LsfRequireDestTag)

		// tfRequireDestTag sets lsfRequireDestTag with no SetFlag field.
		result := env.Submit(AccountSet(alice).
			TxFlags(accounttx.AccountSetTxFlagRequireDestTag).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfRequireDestTag)

		// tfOptionalDestTag clears lsfRequireDestTag with no ClearFlag field.
		result = env.Submit(AccountSet(alice).
			TxFlags(accounttx.AccountSetTxFlagOptionalDestTag).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagNotSet(t, env, alice, state.LsfRequireDestTag)
	})

	t.Run("DisallowXRP", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.FundNoRipple(alice)
		env.Close()

		jtx.RequireFlagNotSet(t, env, alice, state.LsfDisallowXRP)

		// tfDisallowXRP sets lsfDisallowXRP with no SetFlag field.
		result := env.Submit(AccountSet(alice).
			TxFlags(accounttx.AccountSetTxFlagDisallowXRP).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfDisallowXRP)

		// tfAllowXRP clears lsfDisallowXRP with no ClearFlag field.
		result = env.Submit(AccountSet(alice).
			TxFlags(accounttx.AccountSetTxFlagAllowXRP).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagNotSet(t, env, alice, state.LsfDisallowXRP)
	})

	t.Run("RequireAuthClearViaTxFlag", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.FundNoRipple(alice)
		env.Close()

		// Set RequireAuth via the asf form (directory is empty so it succeeds).
		result := env.Submit(AccountSet(alice).
			SetFlag(accounttx.AccountSetFlagRequireAuth).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagSet(t, env, alice, state.LsfRequireAuth)

		// tfOptionalAuth clears lsfRequireAuth with no ClearFlag field.
		result = env.Submit(AccountSet(alice).
			TxFlags(accounttx.AccountSetTxFlagOptionalAuth).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireFlagNotSet(t, env, alice, state.LsfRequireAuth)
	})
}

// TestAccountSet_ZeroFlagsNoOp verifies that an AccountSet carrying explicit
// SetFlag:0 and ClearFlag:0 is a valid no-op (tesSUCCESS), not a temINVALID_FLAG
// "set and clear same flag" rejection.
//
// Reference: rippled SetAccount.cpp preflight() lines 80-84 — the contradiction
// check only fires when uSetFlag != 0.
func TestAccountSet_ZeroFlagsNoOp(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundNoRipple(alice)
	env.Close()

	origFlags := env.AccountInfo(alice).Flags

	result := env.Submit(AccountSet(alice).SetFlag(0).ClearFlag(0).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	nowFlags := env.AccountInfo(alice).Flags
	if nowFlags != origFlags {
		t.Fatalf("flags changed by no-op AccountSet: want 0x%x, got 0x%x", origFlags, nowFlags)
	}
}
