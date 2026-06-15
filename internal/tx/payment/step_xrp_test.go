package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

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

// TestXRPEndpointStep_xrpLiquid_AMMReserveExemption proves that an AMM
// pseudo-account (sfAMMID present) has no reserve requirement, so its entire
// balance is liquid. Reference: rippled View.cpp xrpLiquid() lines 631-633.
func TestXRPEndpointStep_xrpLiquid_AMMReserveExemption(t *testing.T) {
	view := newPaymentMockLedgerView()
	var amm [20]byte
	copy(amm[:], []byte("ammacct123456789012"))

	// Build an AMM pseudo-account: non-zero AMMID, a non-trivial owner count to
	// prove the reserve is waived regardless of owners.
	acct := &state.AccountRoot{
		Account:    state.EncodeAccountIDSafe(amm),
		Balance:    100_000_000, // 100 XRP
		OwnerCount: 5,
		Sequence:   1,
	}
	acct.AMMID[0] = 0x01 // mark sfAMMID present
	data, err := state.SerializeAccountRoot(acct)
	require.NoError(t, err)
	view.data[keylet.Account(amm).Key] = data

	step := NewXRPEndpointStep(amm, false)
	sb := NewPaymentSandbox(view)

	// No reserve: full 100 XRP is liquid (base 10 + 5*owner reserve would be
	// withheld for a normal account, but an AMM has zero reserve).
	require.Equal(t, int64(100_000_000), step.xrpLiquid(sb),
		"AMM pseudo-account must have zero reserve -> full balance liquid")

	// Sanity: a non-AMM account with the same owner count withholds reserve.
	var normal [20]byte
	copy(normal[:], []byte("normalacct123456789"))
	view.createAccount(normal, 100_000_000, 5) // base 10 + 5*2 = 20 XRP reserve
	normalStep := NewXRPEndpointStep(normal, false)
	require.Equal(t, int64(80_000_000), normalStep.xrpLiquid(sb),
		"non-AMM account withholds base + owner*inc reserve")
}
