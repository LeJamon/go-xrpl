package payment

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txpayment "github.com/LeJamon/go-xrpl/internal/tx/payment"
)

func newXRPPathLoopEnv(t *testing.T, fix1781 bool, currencies ...string) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account) {
	t.Helper()

	env := jtx.NewTestEnv(t)
	if fix1781 {
		env.EnableFeature("fix1781")
	} else {
		env.DisableFeature("fix1781")
	}
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")

	env.FundAmount(alice, uint64(jtx.XRP(10_000)))
	env.FundAmount(bob, uint64(jtx.XRP(10_000)))
	env.FundAmount(gw, uint64(jtx.XRP(10_000)))
	env.Close()

	for _, currency := range currencies {
		result := env.Submit(trust(alice, gw, currency, 1_000))
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(trust(bob, gw, currency, 1_000))
		jtx.RequireTxSuccess(t, result)
	}
	env.Close()

	for _, currency := range currencies {
		amount := tx.NewIssuedAmountFromFloat64(100, currency, gw.Address)
		result := env.Submit(PayIssued(gw, alice, amount).Build())
		jtx.RequireTxSuccess(t, result)
	}
	env.Close()

	return env, alice, bob, gw
}

func TestXRPPathLoop_Start(t *testing.T) {
	for _, test := range []struct {
		name    string
		fix1781 bool
	}{
		{name: "enabled", fix1781: true},
		{name: "disabled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, alice, bob, gw := newXRPPathLoopEnv(t, test.fix1781, "USD", "EUR")
			xrp100 := tx.NewXRPAmount(int64(jtx.XRP(100)))
			usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
			eur100 := tx.NewIssuedAmountFromFloat64(100, "EUR", gw.Address)

			jtx.RequireTxSuccess(t, env.CreatePassiveOffer(alice, usd100, xrp100))
			jtx.RequireTxSuccess(t, env.CreatePassiveOffer(alice, xrp100, usd100))
			jtx.RequireTxSuccess(t, env.CreatePassiveOffer(alice, eur100, xrp100))
			env.Close()

			paths := [][]txpayment.PathStep{{
				issuePath("USD", gw),
				currencyPath("XRP"),
				issuePath("EUR", gw),
			}}
			result := env.Submit(PayIssued(alice, bob,
				tx.NewIssuedAmountFromFloat64(1, "EUR", gw.Address),
			).SendMax(tx.NewXRPAmount(int64(jtx.XRP(1)))).Paths(paths).NoDirectRipple().Build())
			if test.fix1781 {
				jtx.RequireTxFail(t, result, jtx.TemBAD_PATH_LOOP)
			} else {
				jtx.RequireTxSuccess(t, result)
			}
		})
	}
}

func TestXRPPathLoop_End(t *testing.T) {
	env, alice, bob, gw := newXRPPathLoopEnv(t, true, "USD", "EUR")
	xrp100 := tx.NewXRPAmount(int64(jtx.XRP(100)))
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	eur100 := tx.NewIssuedAmountFromFloat64(100, "EUR", gw.Address)

	jtx.RequireTxSuccess(t, env.CreatePassiveOffer(alice, usd100, xrp100))
	jtx.RequireTxSuccess(t, env.CreatePassiveOffer(alice, xrp100, eur100))
	env.Close()

	paths := [][]txpayment.PathStep{{
		currencyPath("XRP"),
		issuePath("USD", gw),
		currencyPath("XRP"),
	}}
	result := env.Submit(Pay(alice, bob, uint64(jtx.XRP(1))).
		SendMax(tx.NewIssuedAmountFromFloat64(1, "EUR", gw.Address)).
		Paths(paths).
		NoDirectRipple().
		Build())
	jtx.RequireTxFail(t, result, jtx.TemBAD_PATH_LOOP)
}

func TestXRPPathLoop_Middle(t *testing.T) {
	env, alice, bob, gw := newXRPPathLoopEnv(t, true, "USD", "EUR", "JPY")
	xrp100 := tx.NewXRPAmount(int64(jtx.XRP(100)))
	usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
	eur100 := tx.NewIssuedAmountFromFloat64(100, "EUR", gw.Address)
	jpy100 := tx.NewIssuedAmountFromFloat64(100, "JPY", gw.Address)

	jtx.RequireTxSuccess(t, env.CreatePassiveOffer(alice, xrp100, usd100))
	jtx.RequireTxSuccess(t, env.CreatePassiveOffer(alice, eur100, xrp100))
	jtx.RequireTxSuccess(t, env.CreatePassiveOffer(alice, xrp100, eur100))
	jtx.RequireTxSuccess(t, env.CreatePassiveOffer(alice, jpy100, xrp100))
	env.Close()

	paths := [][]txpayment.PathStep{{
		currencyPath("XRP"),
		issuePath("EUR", gw),
		currencyPath("XRP"),
		issuePath("JPY", gw),
	}}
	result := env.Submit(PayIssued(alice, bob,
		tx.NewIssuedAmountFromFloat64(1, "JPY", gw.Address),
	).SendMax(tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)).Paths(paths).NoDirectRipple().Build())
	jtx.RequireTxFail(t, result, jtx.TemBAD_PATH_LOOP)
}

func TestXRPAccountStepOnlyAllowedAtEndpoint(t *testing.T) {
	env, alice, bob, gw := newXRPPathLoopEnv(t, true, "USD")
	carol := jtx.NewAccount("carol")
	env.FundAmount(carol, uint64(jtx.XRP(10_000)))
	env.Close()

	paths := [][]txpayment.PathStep{{
		currencyPath("XRP"),
		accountPath(carol),
	}}
	result := env.Submit(Pay(alice, bob, uint64(jtx.XRP(1))).
		SendMax(tx.NewIssuedAmountFromFloat64(1, "USD", gw.Address)).
		Paths(paths).
		NoDirectRipple().
		Build())
	jtx.RequireTxFail(t, result, jtx.TemBAD_PATH)
}
