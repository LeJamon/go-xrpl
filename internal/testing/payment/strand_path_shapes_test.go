package payment

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txpayment "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

func TestStrandShape_XRPToXRPBook(t *testing.T) {
	env, alice, bob, gw := newXRPPathLoopEnv(t, "USD")
	jtx.RequireTxSuccess(t, env.CreatePassiveOffer(gw,
		tx.NewXRPAmount(jtx.XRP(100)),
		tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address),
	))
	env.Close()

	paths := [][]txpayment.PathStep{{currencyPath("XRP"), issuePath("USD", gw)}}
	result := env.Submit(PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)).
		SendMax(tx.NewXRPAmount(jtx.XRP(2))).
		Paths(paths).
		Build())
	jtx.RequireTxFail(t, result, jtx.TemBAD_PATH)
	require.False(t, result.Applied)
}

func TestStrandShape_SenderAsPathElement(t *testing.T) {
	env, alice, bob, gw := newXRPPathLoopEnv(t, "USD")
	paths := [][]txpayment.PathStep{{accountPath(alice)}}
	amount := tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)

	result := env.Submit(PayIssued(alice, bob, amount).Paths(paths).Build())
	jtx.RequireTxFail(t, result, jtx.TemBAD_PATH)
	require.False(t, result.Applied)

	result = env.Submit(PayIssued(alice, bob, amount).Paths(paths).NoDirectRipple().Build())
	jtx.RequireTxFail(t, result, jtx.TemBAD_PATH)
	require.False(t, result.Applied)
}

// The strand alice→gw→alice→bob revisits its own source.
func TestStrandShape_LoopBackThroughSender(t *testing.T) {
	env, alice, bob, gw := newXRPPathLoopEnv(t, "USD")
	jtx.RequireTxSuccess(t, env.Submit(trust(bob, alice, "USD", 1_000)))
	env.Close()
	paths := [][]txpayment.PathStep{{accountPath(gw), accountPath(alice)}}

	result := env.Submit(PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(1, "USD", bob.Address)).
		Paths(paths).
		Build())
	jtx.RequireTxFail(t, result, jtx.TemBAD_PATH_LOOP)
	require.False(t, result.Applied)
}

// NoRipple is checked before loop detection, so the looping strand is dropped
// with terNO_RIPPLE and the default path delivers.
func TestStrandShape_NoRippleBeforeLoop(t *testing.T) {
	env, alice, bob, gw := newXRPPathLoopEnv(t, "USD")
	carol := jtx.NewAccount("carol")
	env.FundAmount(carol, uint64(jtx.XRP(10_000)))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustLine(carol, "USD", gw, "1000").NoRipple().Build()))
	env.Close()

	paths := [][]txpayment.PathStep{{accountPath(gw), accountPath(carol)}}
	result := env.Submit(PayIssued(alice, bob, tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)).
		Paths(paths).
		Build())
	jtx.RequireTxSuccess(t, result)
	require.True(t, result.Applied)
	env.Close()

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 90)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 10)
}
