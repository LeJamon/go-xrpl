//go:build !wasmi

// SmartEscrow owner-reserve test. The reserve a FinishFunction escrow consumes
// is engine-independent, and the test uses a non-WASM blob as the FinishFunction
// to drive the reserve math without standing up the engine — so it runs only in
// the default (non-wasmi) build, where create-time WASM validation is skipped.
package escrow_test

import (
	"strings"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/stretchr/testify/require"
)

// TestSmartEscrow_FinishFunctionReserve: a FinishFunction escrow consumes
// additional owner reserve — one slot plus one per 500 bytes of code — matching
// rippled's calculateAdditionalReserve. A ~600-byte FinishFunction therefore
// costs two owner-count slots on create and releases both on cancel.
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
		Fee(baseFee * 1000). // covers the per-byte create fee for a 600-byte FinishFunction
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
