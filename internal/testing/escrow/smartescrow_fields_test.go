// SmartEscrow field-pairing validation tests. The FinishFunction/
// ComputationAllowance pairing is checked in preclaim, before the WASM engine
// runs, so these reject without invoking the engine and build in the default
// (non-wasmi) toolchain.
package escrow_test

import (
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/stretchr/testify/require"
)

// A trivial finish function returning 1; never executed in these tests since
// the finish is rejected at field validation before the engine runs.
const finishFnTrivial = "0061736d010000000105016000017f03020100070a010666696e69736800000a0601040041010b"

// TestSmartEscrow_FinishMissingAllowance: finishing an escrow that carries a
// FinishFunction without a ComputationAllowance is rejected with
// tefWASM_FIELD_NOT_INCLUDED.
// Reference: rippled EscrowSmart_test.cpp line 470
func TestSmartEscrow_FinishMissingAllowance(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SmartEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	seq := env.Seq(alice)
	ff := finishFnTrivial
	ec := escrow.EscrowCreate(alice, bob, xrp(1000)).
		CancelTime(env.Now().Add(1000 * time.Second)).
		Fee(baseFee * 150).
		BuildEscrowCreate()
	ec.FinishFunction = &ff
	jtx.RequireTxSuccess(t, env.Submit(ec))
	env.Close()

	// Finish without a ComputationAllowance.
	ef := escrow.EscrowFinish(bob, alice, seq).
		Fee(baseFee * 150).
		BuildEscrowFinish()
	require.Equal(t, "tefWASM_FIELD_NOT_INCLUDED", env.Submit(ef).Code)
}

// TestSmartEscrow_FinishNoFunction: supplying a ComputationAllowance when the
// escrow has no FinishFunction is rejected with tefNO_WASM.
// Reference: rippled EscrowSmart_test.cpp line 514
func TestSmartEscrow_FinishNoFunction(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SmartEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	// A plain time-based escrow, no FinishFunction.
	seq := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			FinishTime(env.Now().Add(1*time.Second)).
			Build()))
	env.Close()

	// The ComputationAllowance fee (#717) applies even though the escrow has no
	// FinishFunction; pay above it so the finish reaches the tefNO_WASM check.
	allowance := uint32(1_000_000)
	ef := escrow.EscrowFinish(bob, alice, seq).
		Fee(2_000_000).
		BuildEscrowFinish()
	ef.ComputationAllowance = &allowance
	require.Equal(t, "tefNO_WASM", env.Submit(ef).Code)
}

// finishPlainEscrowAllowance creates a plain time-based escrow and finishes it
// with the given ComputationAllowance, returning the finish result code. Used to
// exercise the ComputationAllowance bound checks, which run before the escrow's
// FinishFunction is consulted.
func finishPlainEscrowAllowance(t *testing.T, allowance uint32, fee uint64) string {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SmartEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	seq := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(
		escrow.EscrowCreate(alice, bob, xrp(1000)).
			FinishTime(env.Now().Add(1*time.Second)).
			Build()))
	env.Close()

	ef := escrow.EscrowFinish(bob, alice, seq).
		Fee(fee).
		BuildEscrowFinish()
	ef.ComputationAllowance = &allowance
	return env.Submit(ef).Code
}

// TestSmartEscrow_AllowanceZero: a zero ComputationAllowance is temBAD_LIMIT.
// Reference: rippled EscrowFinish.cpp preflight lines 109-112.
func TestSmartEscrow_AllowanceZero(t *testing.T) {
	require.Equal(t, "temBAD_LIMIT", finishPlainEscrowAllowance(t, 0, baseFee*150))
}

// TestSmartEscrow_AllowanceTooLarge: a ComputationAllowance above the extension
// compute limit (default 1,000,000) is temBAD_LIMIT.
// Reference: rippled EscrowFinish.cpp preflight lines 113-116.
func TestSmartEscrow_AllowanceTooLarge(t *testing.T) {
	// Fee must cover the gas fee for the (over-limit) allowance so the finish
	// reaches the bound check rather than failing on fee adequacy first.
	require.Equal(t, "temBAD_LIMIT", finishPlainEscrowAllowance(t, 1_000_001, 2_000_000))
}

// TestSmartEscrow_FinishDepositAuth: under SmartEscrow, deposit-authorization is
// still enforced — rippled relocates verifyDepositPreauth to before the WASM
// step, it does not skip it. An unauthorized finisher is rejected with
// tecNO_PERMISSION before the finish function runs, so this builds in the
// default (non-wasmi) toolchain too.
// Reference: rippled-smart-escrow EscrowFinish.cpp:328-338
func TestSmartEscrow_FinishDepositAuth(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SmartEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	zelda := jtx.NewAccount("zelda")
	fund5000(env, alice, bob, zelda)

	// Bob (the escrow destination) requires deposit authorization.
	env.EnableDepositAuth(bob)
	env.Close()

	seq := env.Seq(alice)
	ff := finishFnTrivial
	ec := escrow.EscrowCreate(alice, bob, xrp(1000)).
		CancelTime(env.Now().Add(1000 * time.Second)).
		Fee(baseFee * 150).
		BuildEscrowCreate()
	ec.FinishFunction = &ff
	jtx.RequireTxSuccess(t, env.Submit(ec))
	env.Close()

	// Zelda is not authorized to deposit to Bob: the finish is rejected before
	// the WASM function runs. The allowance fee (#717) applies, so pay above it.
	allowance := uint32(1_000_000)
	ef := escrow.EscrowFinish(zelda, alice, seq).
		Fee(2_000_000).
		BuildEscrowFinish()
	ef.ComputationAllowance = &allowance
	require.Equal(t, "tecNO_PERMISSION", env.Submit(ef).Code)
}
