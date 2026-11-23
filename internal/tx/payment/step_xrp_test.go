package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestXRPEndpointStep_CheckGlobalFreeze(t *testing.T) {
	view := newPaymentMockLedgerView()
	acct := [20]byte{1}
	root := &state.AccountRoot{
		Account:  state.EncodeAccountIDSafe(acct),
		Balance:  100_000_000,
		Flags:    state.LsfGlobalFreeze,
		Sequence: 1,
	}
	data, err := state.SerializeAccountRoot(root)
	require.NoError(t, err)
	view.data[keylet.Account(acct).Key] = data

	sb := NewPaymentSandbox(view)
	require.Equal(t, ter.TerNO_LINE, NewXRPEndpointStep(acct, true).Check(sb))
	require.Equal(t, ter.TesSUCCESS, NewXRPEndpointStep(acct, false).Check(sb))

	ctx := NewStrandContext(sb, [20]byte{2}, acct)
	ctx.SeenDirectIssues[0][Issue{Currency: "XRP"}] = true
	_, result := ctx.newXRPEndpointStep(acct, true, false)
	require.Equal(t, ter.TerNO_LINE, result)
}

func TestXRPEndpointStep_CheckUnresolvableAMM(t *testing.T) {
	tests := []struct {
		name     string
		disabled bool
		want     ter.Result
	}{
		{name: "enabled", want: ter.TecINTERNAL},
		{name: "disabled", disabled: true, want: ter.TesSUCCESS},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := newPaymentMockLedgerView()
			if test.disabled {
				view.rules = amendment.NewRulesBuilder().
					FromPreset(amendment.PresetAllSupported).
					DisableByName("fixFrozenLPTokenTransfer").
					Build()
			}
			acct := [20]byte{2}
			root := &state.AccountRoot{
				Account:  state.EncodeAccountIDSafe(acct),
				Balance:  100_000_000,
				Sequence: 1,
			}
			root.AMMID[0] = 1
			data, err := state.SerializeAccountRoot(root)
			require.NoError(t, err)
			view.data[keylet.Account(acct).Key] = data

			sb := NewPaymentSandbox(view)
			require.Equal(t, test.want, NewXRPEndpointStep(acct, true).Check(sb))
		})
	}
}

func TestXRPEndpointStep_CheckAccount(t *testing.T) {
	tests := []struct {
		name    string
		account [20]byte
		want    ter.Result
	}{
		{name: "zero", want: ter.TemBAD_PATH},
		{name: "missing", account: [20]byte{2}, want: ter.TerNO_ACCOUNT},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sb := NewPaymentSandbox(newPaymentMockLedgerView())
			require.Equal(t, test.want, NewXRPEndpointStep(test.account, true).Check(sb))
		})
	}
}

// TestXRPEndpointStep_xrpLiquid_DeferredCredit proves that XRP credited to an
// account earlier in the same payment is not double-counted as spendable
// liquidity. xrpLiquid applies the deferred-credit balance hook
// (rippled View.cpp: balanceHook(id, xrpAccount(), fullBalance)), so the credited
// drops are subtracted from the account's available balance.
func TestXRPEndpointStep_xrpLiquid_DeferredCredit(t *testing.T) {
	view := newPaymentMockLedgerView()
	var acct [20]byte
	copy(acct[:], []byte("alice12345678901234"))
	// 100 XRP, 0 owners -> base reserve 10 XRP -> liquid 90 XRP without any hook.
	view.createAccount(acct, 100_000_000, 0)

	step := NewXRPEndpointStep(acct, false)

	// Baseline: no deferred credit, full liquid balance.
	sbBase := NewPaymentSandbox(view)
	require.Equal(t, int64(90_000_000), step.xrpLiquid(sbBase),
		"baseline liquid = balance(100) - base reserve(10)")

	// Record a 40 XRP credit TO the account from the XRP pseudo-account, exactly
	// as transferXRP's credit-to-receiver path does mid-payment.
	sbCredited := NewPaymentSandbox(view)
	var xrpAccount [20]byte
	sbCredited.CreditHook(xrpAccount, acct, tx.NewXRPAmount(40_000_000), tx.NewXRPAmount(-60_000_000))

	// The 40 XRP received earlier must not be re-spendable: 90 - 40 = 50 XRP.
	require.Equal(t, int64(50_000_000), step.xrpLiquid(sbCredited),
		"deferred-credit hook must subtract the 40 XRP received earlier in the payment")
}

func TestXRPEndpointStep_xrpLiquid_PseudoAccountReserveExemption(t *testing.T) {
	view := newPaymentMockLedgerView()
	pseudoAccounts := []struct {
		name string
		set  func(*state.AccountRoot)
	}{
		{name: "AMM", set: func(account *state.AccountRoot) { account.AMMID[0] = 1 }},
		{name: "vault", set: func(account *state.AccountRoot) { account.VaultID[0] = 1 }},
		{name: "loan broker", set: func(account *state.AccountRoot) { account.LoanBrokerID[0] = 1 }},
	}

	for index, pseudo := range pseudoAccounts {
		t.Run(pseudo.name, func(t *testing.T) {
			var accountID [20]byte
			accountID[0] = byte(index + 1)
			account := &state.AccountRoot{
				Account:    state.EncodeAccountIDSafe(accountID),
				Balance:    100_000_000,
				OwnerCount: 5,
				Sequence:   1,
			}
			pseudo.set(account)
			data, err := state.SerializeAccountRoot(account)
			require.NoError(t, err)
			view.data[keylet.Account(accountID).Key] = data

			step := NewXRPEndpointStep(accountID, false)
			sb := NewPaymentSandbox(view)
			require.Equal(t, int64(100_000_000), step.xrpLiquid(sb),
				"pseudo-account must have zero reserve -> full balance liquid")
		})
	}

	var normal [20]byte
	copy(normal[:], []byte("normalacct123456789"))
	view.createAccount(normal, 100_000_000, 5) // base 10 + 5*2 = 20 XRP reserve
	normalStep := NewXRPEndpointStep(normal, false)
	require.Equal(t, int64(80_000_000), normalStep.xrpLiquid(NewPaymentSandbox(view)))
}
