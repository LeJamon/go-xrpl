package check_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/check"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// strayFlag is a non-universal, non-type-specific bit invalid on every Check
// transaction (0x08000000 is above the highest type-specific flag and below the
// universal bits).
const strayFlag = uint32(0x08000000)

// someCheckID is any well-formed 256-bit ID; the flag mask fires at preflight0
// before the ledger-stage check lookup, so the ID need not resolve.
const someCheckID = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"

// TestCheck_FlagMaskBeatsBadFee pins that the Check FlagsMasker adoption places
// the invalid-flags rejection at preflight0, ahead of the fee check: a stray flag
// combined with a malformed (negative) fee surfaces temINVALID_FLAG, not
// temBAD_FEE. Reference: rippled preflight0 getFlagsMask precedes preflight1's fee
// check for CreateCheck/CashCheck/CancelCheck (base Transactor::getFlagsMask).
func TestCheck_FlagMaskBeatsBadFee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	cases := []struct {
		name string
		txn  tx.Transaction
	}{
		{"CheckCreate", check.CheckCreate(alice, bob, tx.NewXRPAmount(100)).Flags(strayFlag).Build()},
		{"CheckCash", check.CheckCashAmount(alice, someCheckID, tx.NewXRPAmount(100)).Flags(strayFlag).Build()},
		{"CheckCancel", check.CheckCancel(alice, someCheckID).Flags(strayFlag).Build()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.txn.GetCommon().Fee = "-10" // malformed fee, reached only if the flag check passes
			require.Equal(t, "temINVALID_FLAG", env.Submit(c.txn).Code)
		})
	}
}
