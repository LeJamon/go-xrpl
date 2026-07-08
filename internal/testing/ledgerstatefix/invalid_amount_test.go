package ledgerstatefix

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

// A native amount above the isLegalNet cap (1e17 drops) is rejected by the
// universal preflight scan (temBAD_AMOUNT) once fixCleanup3_2_0 is active.
func TestUniversalPreflight_OversizedXRP(t *testing.T) {
	const overCap = int64(200_000_000_000_000_000) // 2e17 > 1e17 cap

	submit := func(env *jtx.TestEnv) jtx.TxResult {
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()
		p := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(overCap))
		return env.Submit(p)
	}

	t.Run("amendment on", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		res := submit(env)
		require.Equal(t, "temBAD_AMOUNT", res.Code, res.Message)
	})

	// With the amendment off the universal scan is skipped; Payment's own
	// isLegalNet check still rejects an oversized native Amount with the same
	// code, so the transaction can never apply either way.
	t.Run("amendment off", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixCleanup3_2_0")
		res := submit(env)
		require.Equal(t, "temBAD_AMOUNT", res.Code, res.Message)
	})
}
