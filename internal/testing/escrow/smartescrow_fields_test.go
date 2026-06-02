// SmartEscrow field-pairing validation tests. The FinishFunction/
// ComputationAllowance pairing is checked in preclaim, before the WASM engine
// runs, so these reject without invoking the engine and build in the default
// (non-wasmi) toolchain.
package escrow_test

import (
	"strings"
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

	allowance := uint32(1_000_000)
	ef := escrow.EscrowFinish(bob, alice, seq).
		Fee(baseFee * 150).
		BuildEscrowFinish()
	ef.ComputationAllowance = &allowance
	require.Equal(t, "tefNO_WASM", env.Submit(ef).Code)
}

// TestSmartEscrow_FinishDepositAuth: under SmartEscrow, deposit-authorization is
// still enforced — rippled relocates verifyDepositPreauth to before the WASM
// step, it does not skip it. An unauthorized finisher is rejected with
// tecNO_PERMISSION before the finish function runs, so this builds in the
// default (non-wasmi) toolchain.
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
	// the WASM function runs.
	allowance := uint32(1_000_000)
	ef := escrow.EscrowFinish(zelda, alice, seq).
		Fee(baseFee * 150).
		BuildEscrowFinish()
	ef.ComputationAllowance = &allowance
	require.Equal(t, "tecNO_PERMISSION", env.Submit(ef).Code)
}

// TestSmartEscrow_FinishFunctionReserve: a FinishFunction escrow consumes
// additional owner reserve — one slot plus one per 500 bytes of code — matching
// rippled's calculateAdditionalReserve. A ~600-byte FinishFunction therefore
// costs two owner-count slots on create and releases both on cancel. The blob is
// not validated as WASM at this layer (that lands later in the stack), so it
// only exercises the reserve math and builds without the engine.
// Reference: rippled-smart-escrow EscrowHelpers.h:232-239
func TestSmartEscrow_FinishFunctionReserve(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SmartEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	require.Equal(t, uint32(0), env.OwnerCount(alice))

	ff := strings.Repeat("00", 600) // 600 bytes → 1 + 600/500 = 2 reserve slots
	ts := env.Now().Add(20 * time.Second)
	seq := env.Seq(alice)
	ec := escrow.EscrowCreate(alice, bob, xrp(1000)).
		CancelTime(ts).
		Fee(baseFee * 150).
		BuildEscrowCreate()
	ec.FinishFunction = &ff
	jtx.RequireTxSuccess(t, env.Submit(ec))
	env.Close()

	require.Equal(t, uint32(2), env.OwnerCount(alice))

	// Advance strictly past the cancel time, then cancel — releasing the full
	// reserve. The margin clears the close-time resolution rounding so the
	// parent close time is strictly greater than CancelAfter.
	for !env.Now().After(ts.Add(10 * time.Second)) {
		env.Close()
	}
	jtx.RequireTxSuccess(t, env.Submit(
		escrow.EscrowCancel(alice, alice, seq).Fee(baseFee*150).Build()))
	env.Close()

	require.Equal(t, uint32(0), env.OwnerCount(alice))
}
