package accountset

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestAccountSet_TickSizeTransitions(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	readTickSize := func() uint8 {
		t.Helper()
		data, err := env.LedgerEntry(keylet.Account(alice.ID))
		require.NoError(t, err)
		account, err := state.ParseAccountRoot(data)
		require.NoError(t, err)
		return account.TickSize
	}

	tests := []struct {
		name   string
		value  uint8
		code   string
		stored uint8
	}{
		{name: "minimum", value: 3, code: "tesSUCCESS", stored: 3},
		{name: "below minimum", value: 2, code: "temBAD_TICK_SIZE", stored: 3},
		{name: "ordinary maximum", value: 15, code: "tesSUCCESS", stored: 15},
		{name: "above maximum", value: 17, code: "temBAD_TICK_SIZE", stored: 15},
		{name: "maximum clears", value: 16, code: "tesSUCCESS", stored: 0},
		{name: "zero clears", value: 0, code: "tesSUCCESS", stored: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := env.Submit(AccountSet(alice).TickSize(tt.value).Build())
			require.Equal(t, tt.code, result.Code)
			env.Close()
			require.Equal(t, tt.stored, readTickSize())
		})
	}
}

func TestAccountSet_NFTokenMinterTransitions(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.Fund(alice, bob, carol)

	result := env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagAuthorizedNFTokenMinter).
		Build())
	jtx.RequireTxFail(t, result, "temMALFORMED")
	require.Empty(t, env.AccountInfo(alice).NFTokenMinter)

	badClear := AccountSet(alice).
		ClearFlag(accounttx.AccountSetFlagAuthorizedNFTokenMinter).
		Build()
	badClear.NFTokenMinter = bob.Address
	result = env.Submit(badClear)
	jtx.RequireTxFail(t, result, "temMALFORMED")
	require.Empty(t, env.AccountInfo(alice).NFTokenMinter)

	stray := AccountSet(alice).Build()
	stray.NFTokenMinter = bob.Address
	result = env.Submit(stray)
	jtx.RequireTxSuccess(t, result)
	require.Empty(t, env.AccountInfo(alice).NFTokenMinter)

	result = env.Submit(AccountSet(alice).AuthorizedMinter(bob).Build())
	jtx.RequireTxSuccess(t, result)
	require.Equal(t, bob.Address, env.AccountInfo(alice).NFTokenMinter)

	result = env.Submit(AccountSet(alice).AuthorizedMinter(carol).Build())
	jtx.RequireTxSuccess(t, result)
	require.Equal(t, carol.Address, env.AccountInfo(alice).NFTokenMinter)

	result = env.Submit(AccountSet(alice).
		ClearFlag(accounttx.AccountSetFlagAuthorizedNFTokenMinter).
		Build())
	jtx.RequireTxSuccess(t, result)
	require.Empty(t, env.AccountInfo(alice).NFTokenMinter)

	result = env.Submit(AccountSet(alice).
		ClearFlag(accounttx.AccountSetFlagAuthorizedNFTokenMinter).
		Build())
	jtx.RequireTxSuccess(t, result)
	require.Empty(t, env.AccountInfo(alice).NFTokenMinter)
}

func TestAccountSet_DisallowIncomingFlags(t *testing.T) {
	tests := []struct {
		name        string
		accountFlag uint32
		ledgerFlag  uint32
	}{
		{name: "NFTokenOffer", accountFlag: accounttx.AccountSetFlagDisallowIncomingNFTokenOffer, ledgerFlag: state.LsfDisallowIncomingNFTokenOffer},
		{name: "Check", accountFlag: accounttx.AccountSetFlagDisallowIncomingCheck, ledgerFlag: state.LsfDisallowIncomingCheck},
		{name: "PayChan", accountFlag: accounttx.AccountSetFlagDisallowIncomingPayChan, ledgerFlag: state.LsfDisallowIncomingPayChan},
		{name: "Trustline", accountFlag: accounttx.AccountSetFlagDisallowIncomingTrustline, ledgerFlag: state.LsfDisallowIncomingTrustline},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice := jtx.NewAccount("alice")
			env.Fund(alice)

			result := env.Submit(AccountSet(alice).SetFlag(tt.accountFlag).Build())
			jtx.RequireTxSuccess(t, result)
			jtx.RequireFlagSet(t, env, alice, tt.ledgerFlag)

			result = env.Submit(AccountSet(alice).ClearFlag(tt.accountFlag).Build())
			jtx.RequireTxSuccess(t, result)
			jtx.RequireFlagNotSet(t, env, alice, tt.ledgerFlag)
		})
	}
}

func TestAccountSet_ClawbackInteractions(t *testing.T) {
	t.Run("retired amendment remains active", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.DisableFeature("Clawback")
		env.Close()

		result := env.Submit(AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)
	})

	t.Run("set is irreversible and excludes no freeze", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		result := env.Submit(AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		result = env.Submit(AccountSet(alice).
			ClearFlag(accounttx.AccountSetFlagAllowTrustLineClawback).
			Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineClawback)

		result = env.Submit(AccountSet(alice).NoFreeze().Build())
		jtx.RequireTxFail(t, result, "tecNO_PERMISSION")
		jtx.RequireFlagNotSet(t, env, alice, state.LsfNoFreeze)
	})

	t.Run("no freeze excludes clawback", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		result := env.Submit(AccountSet(alice).NoFreeze().Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxFail(t, result, "tecNO_PERMISSION")
		jtx.RequireFlagNotSet(t, env, alice, state.LsfAllowTrustLineClawback)
	})
}

func TestAccountSet_TrustLineLockingAmendment(t *testing.T) {
	t.Run("enabled set and clear", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		result := env.Submit(AccountSet(alice).AllowTrustLineLocking().Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineLocking)

		result = env.Submit(AccountSet(alice).
			ClearFlag(accounttx.AccountSetFlagAllowTrustLineLocking).
			Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireFlagNotSet(t, env, alice, state.LsfAllowTrustLineLocking)
	})

	t.Run("disabled set and clear are no-ops", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.DisableFeature("TokenEscrow")
		env.Close()

		result := env.Submit(AccountSet(alice).AllowTrustLineLocking().Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireFlagNotSet(t, env, alice, state.LsfAllowTrustLineLocking)

		env.EnableFeature("TokenEscrow")
		env.Close()
		result = env.Submit(AccountSet(alice).AllowTrustLineLocking().Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineLocking)

		env.DisableFeature("TokenEscrow")
		env.Close()
		result = env.Submit(AccountSet(alice).
			ClearFlag(accounttx.AccountSetFlagAllowTrustLineLocking).
			Build())
		jtx.RequireTxSuccess(t, result)
		jtx.RequireFlagSet(t, env, alice, state.LsfAllowTrustLineLocking)
	})
}

func TestAccountSet_RequireAuthTapRetry(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bob, Weight: 1}})
	env.Close()

	accountSet := AccountSet(alice).RequireAuth().Build()
	require.Equal(t, ter.TecOWNERS, accountSet.Preclaim(env.Ledger(), tx.EngineConfig{}))
	require.Equal(t, ter.TerOWNERS, accountSet.Preclaim(env.Ledger(), tx.EngineConfig{ApplyFlags: tx.TapRETRY}))
}
