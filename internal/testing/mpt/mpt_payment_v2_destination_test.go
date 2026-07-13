package mpt_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	paybuilder "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestMPTPaymentV2DestinationChecks(t *testing.T) {
	t.Run("missing destination", func(t *testing.T) {
		fixture := newMPTPaymentFixture(t, true, 0, nil)
		missing := jtx.NewAccount("missing")
		result := fixture.env.Submit(
			paybuilder.PayIssued(
				fixture.issuer,
				missing,
				fixture.token.MPTAmount(10),
			).Build(),
		)

		jtx.RequireTxFail(t, result, jtx.TecNO_DST)
	})

	t.Run("destination tag required", func(t *testing.T) {
		fixture := newMPTPaymentFixture(t, true, 0, nil)
		fixture.env.EnableRequireDest(fixture.other)
		result := fixture.env.Submit(
			paybuilder.PayIssued(
				fixture.issuer,
				fixture.other,
				fixture.token.MPTAmount(10),
			).Build(),
		)

		jtx.RequireTxFail(t, result, jtx.TecDST_TAG_NEEDED)
		fixture.token.RequireMPTokenAmount(fixture.other, 0)
	})

	t.Run("deposit authorization", func(t *testing.T) {
		fixture := newMPTPaymentFixture(t, true, 0, nil)
		fixture.env.EnableDepositAuth(fixture.other)
		payment := func() tx.Transaction {
			return paybuilder.PayIssued(
				fixture.issuer,
				fixture.other,
				fixture.token.MPTAmount(10),
			).BuildPayment()
		}

		jtx.RequireTxFail(t, fixture.env.Submit(payment()), jtx.TecNO_PERMISSION)
		fixture.env.Preauthorize(fixture.other, fixture.issuer)
		jtx.RequireTxSuccess(t, fixture.env.Submit(payment()))
		fixture.token.RequireMPTokenAmount(fixture.other, 10)
	})
}
