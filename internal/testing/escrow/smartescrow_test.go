//go:build cgo && wasmi

// SmartEscrow (computational escrow) end-to-end tests. Built with -tags wasmi so
// the real wasmi engine runs the escrow's FinishFunction.
package escrow_test

import (
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/keylet"
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

// Finish functions that call update_data("smartescrow-data") (16 bytes) and then
// return — one rejecting (0), one accepting (1). Compiled with wat2wasm from:
//
//	(module
//	  (import "host" "update_data" (func $u (param i32 i32) (result i32)))
//	  (memory (export "memory") 1)
//	  (data (i32.const 0) "smartescrow-data")
//	  (func (export "finish") (result i32)
//	    (drop (call $u (i32.const 0) (i32.const 16)))
//	    (i32.const N)))
const (
	finishUpdateDataReject = "0061736d01000000010b0260027f7f017f6000017f02140104686f73740b7570646174655f646174610000030201010503010001071302066d656d6f727902000666696e69736800010a0d010b004100411010001a41000b0b16010041000b10736d617274657363726f772d64617461"
	finishUpdateDataAccept = "0061736d01000000010b0260027f7f017f6000017f02140104686f73740b7570646174655f646174610000030201010503010001071302066d656d6f727902000666696e69736800010a0d010b004100411010001a41010b0b16010041000b10736d617274657363726f772d64617461"
	// hex of the bytes the finish functions write via update_data.
	updateDataValueHex = "736d617274657363726f772d64617461"
)

// TestSmartEscrow_FinishAccepts: a finish function returning 1 lets the escrow
// finish.
func TestSmartEscrow_FinishAccepts(t *testing.T) {
	require.Equal(t, "tesSUCCESS", runComputationalEscrow(t, finishReturns1))
}

// TestSmartEscrow_RejectPersistsData: a finish function that mutates the escrow's
// Data via update_data and then rejects (returns 0) yields tecWASM_REJECTED, and
// the escrow survives carrying the new Data. The in-doApply Data write rides in
// the discarded sandbox, so this only holds if the write is re-applied after the
// sandbox is reset. Reference: rippled Transactor.cpp modifyWasmDataFields.
func TestSmartEscrow_RejectPersistsData(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SmartEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	seq := env.Seq(alice)
	ec := escrow.EscrowCreate(alice, bob, xrp(1000)).
		CancelTime(env.Now().Add(1000 * time.Second)).
		Fee(baseFee * 150).
		BuildEscrowCreate()
	ff := finishUpdateDataReject
	ec.FinishFunction = &ff
	jtx.RequireTxSuccess(t, env.Submit(ec))
	env.Close()

	allowance := uint32(1_000_000)
	ef := escrow.EscrowFinish(bob, alice, seq).
		Fee(2_000_000).
		BuildEscrowFinish()
	ef.ComputationAllowance = &allowance
	require.Equal(t, "tecWASM_REJECTED", env.Submit(ef).Code)
	env.Close()

	// The escrow must survive the rejected finish, now carrying the mutated Data.
	escrowKey := keylet.Escrow(alice.ID, seq)
	require.True(t, env.LedgerEntryExists(escrowKey), "escrow must survive tecWASM_REJECTED")
	raw, err := env.LedgerEntry(escrowKey)
	require.NoError(t, err)
	decoded, err := binarycodec.DecodeBytes(raw)
	require.NoError(t, err)
	data, ok := decoded["Data"].(string)
	require.True(t, ok, "escrow Data field must be present after update_data")
	require.True(t, strings.EqualFold(updateDataValueHex, data), "escrow Data must hold the finish function's write, got %q", data)
}

// TestSmartEscrow_AcceptWithDataMutation: update_data followed by an accepting
// finish still succeeds and deletes the escrow (the Data write is moot once the
// escrow is erased), confirming the Data path does not disturb the success flow.
func TestSmartEscrow_AcceptWithDataMutation(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SmartEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	seq := env.Seq(alice)
	ec := escrow.EscrowCreate(alice, bob, xrp(1000)).
		CancelTime(env.Now().Add(1000 * time.Second)).
		Fee(baseFee * 150).
		BuildEscrowCreate()
	ff := finishUpdateDataAccept
	ec.FinishFunction = &ff
	jtx.RequireTxSuccess(t, env.Submit(ec))
	env.Close()

	allowance := uint32(1_000_000)
	ef := escrow.EscrowFinish(bob, alice, seq).
		Fee(2_000_000).
		BuildEscrowFinish()
	ef.ComputationAllowance = &allowance
	require.Equal(t, "tesSUCCESS", env.Submit(ef).Code)
	env.Close()

	require.False(t, env.LedgerEntryExists(keylet.Escrow(alice.ID, seq)), "escrow must be deleted on a successful finish")
}

// TestSmartEscrow_FinishRejects: a finish function returning 0 rejects the
// finish with tecWASM_REJECTED.
func TestSmartEscrow_FinishRejects(t *testing.T) {
	require.Equal(t, "tecWASM_REJECTED", runComputationalEscrow(t, finishReturns0))
}

// Malformed FinishFunction WASM rejected at create time (preflightEscrowWasm),
// only validated when the wasmi engine is linked.
const (
	// Not a valid WASM module (no magic header).
	finishGarbage = "deadbeefdeadbeef"
	// Valid module, but the export is named "fonish" — no "finish" entry point.
	finishWrongExport = "0061736d010000000105016000017f03020100070a0106666f6e69736800000a0601040041010b"
)

// createWithFinishFunction creates an escrow carrying finishFunctionHex and
// returns the EscrowCreate result code.
func createWithFinishFunction(t *testing.T, finishFunctionHex string) string {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SmartEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	ec := escrow.EscrowCreate(alice, bob, xrp(1000)).
		CancelTime(env.Now().Add(1000 * time.Second)).
		Fee(baseFee * 150).
		BuildEscrowCreate()
	ec.FinishFunction = &finishFunctionHex
	return env.Submit(ec).Code
}

// TestSmartEscrow_CreateBadWasm: a FinishFunction that is not a well-formed
// module is rejected at create time with temBAD_WASM.
func TestSmartEscrow_CreateBadWasm(t *testing.T) {
	require.Equal(t, "temBAD_WASM", createWithFinishFunction(t, finishGarbage))
}

// TestSmartEscrow_CreateMissingFinishExport: a module that does not export
// finish() is rejected at create time with temBAD_WASM.
func TestSmartEscrow_CreateMissingFinishExport(t *testing.T) {
	require.Equal(t, "temBAD_WASM", createWithFinishFunction(t, finishWrongExport))
}
