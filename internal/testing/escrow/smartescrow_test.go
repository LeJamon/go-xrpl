//go:build cgo && wasmi

// SmartEscrow (computational escrow) end-to-end tests. Built with -tags wasmi so
// the real wasmi engine runs the escrow's FinishFunction.
package escrow_test

import (
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/stretchr/testify/require"
)

// Minimal finish functions compiled with wat2wasm:
//
//	(module (func (export "finish") (result i32) (i32.const N)))
const (
	finishReturns1 = "0061736d010000000105016000017f03020100070a010666696e69736800000a0601040041010b"
	finishReturns0 = "0061736d010000000105016000017f03020100070a010666696e69736800000a0601040041000b"
)

func runComputationalEscrow(t *testing.T, finishFunctionHex string) string {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SmartEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	// A FinishFunction requires a CancelAfter; no FinishAfter, so the escrow can
	// be finished immediately (the finish function governs).
	seq := env.Seq(alice)
	ec := escrow.EscrowCreate(alice, bob, xrp(1000)).
		CancelTime(env.Now().Add(1000 * time.Second)).
		Fee(baseFee * 150).
		BuildEscrowCreate()
	ec.FinishFunction = &finishFunctionHex
	createRes := env.Submit(ec)
	t.Logf("create result: %s", createRes.Code)
	jtx.RequireTxSuccess(t, createRes)
	env.Close()

	// A 1,000,000-unit ComputationAllowance costs ~1,000,001 drops at the default
	// gas price (#717), so the finish must pay well above the base fee.
	allowance := uint32(1_000_000)
	ef := escrow.EscrowFinish(bob, alice, seq).
		Fee(2_000_000).
		BuildEscrowFinish()
	ef.ComputationAllowance = &allowance
	return env.Submit(ef).Code
}

// TestSmartEscrow_FinishAccepts: a finish function returning 1 lets the escrow
// finish.
func TestSmartEscrow_FinishAccepts(t *testing.T) {
	require.Equal(t, "tesSUCCESS", runComputationalEscrow(t, finishReturns1))
}

// TestSmartEscrow_FinishRejects: a finish function returning 0 rejects the
// finish with tecWASM_REJECTED.
func TestSmartEscrow_FinishRejects(t *testing.T) {
	require.Equal(t, "tecWASM_REJECTED", runComputationalEscrow(t, finishReturns0))
}
