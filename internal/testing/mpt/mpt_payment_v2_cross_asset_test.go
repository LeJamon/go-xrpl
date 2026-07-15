package mpt_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	offertest "github.com/LeJamon/go-xrpl/internal/testing/offer"
	paybuilder "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
)

func newMPTCrossAssetFixture(t *testing.T) mptPaymentFixture {
	return newMPTPaymentFixture(
		t,
		true,
		mpt.TfMPTCanTransfer|mpt.TfMPTCanTrade,
		nil,
	)
}

func mptPath(id string) [][]paymenttx.PathStep {
	return [][]paymenttx.PathStep{{{MPTIssuanceID: id}}}
}

func TestMPTPaymentV2CrossAssetSuccess(t *testing.T) {
	t.Run("XRP to MPT", func(t *testing.T) {
		fixture := newMPTCrossAssetFixture(t)
		mptAmount := fixture.token.MPTAmount(100)
		xrpAmount := tx.NewXRPAmount(jtx.XRP(100))
		fixture.token.Pay(fixture.issuer, fixture.holder, 100)
		jtx.RequireTxSuccess(t, fixture.env.CreatePassiveOffer(fixture.holder, mptAmount, xrpAmount))
		holderXRP := fixture.env.Balance(fixture.holder)

		result := fixture.env.Submit(
			paybuilder.PayIssued(fixture.issuer, fixture.other, mptAmount).
				SendMax(xrpAmount).
				Paths(mptPath(fixture.token.IssuanceID())).
				NoDirectRipple().
				Build(),
		)

		jtx.RequireTxSuccess(t, result)
		fixture.token.RequireMPTokenAmount(fixture.holder, 0)
		fixture.token.RequireMPTokenAmount(fixture.other, 100)
		jtx.RequireBalance(t, fixture.env, fixture.holder, holderXRP+uint64(jtx.XRP(100)))
		offertest.RequireOfferCount(t, fixture.env, fixture.holder, 0)
	})

	t.Run("MPT to XRP", func(t *testing.T) {
		fixture := newMPTCrossAssetFixture(t)
		recipient := jtx.NewAccount("recipient")
		fixture.env.Fund(recipient)
		mptAmount := fixture.token.MPTAmount(100)
		xrpAmount := tx.NewXRPAmount(jtx.XRP(100))
		fixture.token.Pay(fixture.issuer, fixture.other, 100)
		jtx.RequireTxSuccess(t, fixture.env.CreatePassiveOffer(fixture.holder, xrpAmount, mptAmount))
		recipientXRP := fixture.env.Balance(recipient)

		result := fixture.env.Submit(
			paybuilder.Pay(fixture.other, recipient, uint64(jtx.XRP(100))).
				SendMax(mptAmount).
				PathsXRP().
				NoDirectRipple().
				Build(),
		)

		jtx.RequireTxSuccess(t, result)
		fixture.token.RequireMPTokenAmount(fixture.other, 0)
		fixture.token.RequireMPTokenAmount(fixture.holder, 100)
		jtx.RequireBalance(t, fixture.env, recipient, recipientXRP+uint64(jtx.XRP(100)))
		offertest.RequireOfferCount(t, fixture.env, fixture.holder, 0)
	})

	t.Run("IOU to MPT", func(t *testing.T) {
		fixture := newMPTCrossAssetFixture(t)
		mptAmount := fixture.token.MPTAmount(100)
		usdAmount := tx.NewIssuedAmountFromFloat64(100, "USD", fixture.issuer.Address)
		fixture.env.Trust(
			fixture.holder,
			tx.NewIssuedAmountFromFloat64(1_000, "USD", fixture.issuer.Address),
		)
		fixture.token.Pay(fixture.issuer, fixture.holder, 100)
		jtx.RequireTxSuccess(t, fixture.env.CreatePassiveOffer(fixture.holder, mptAmount, usdAmount))

		result := fixture.env.Submit(
			paybuilder.PayIssued(fixture.issuer, fixture.other, mptAmount).
				SendMax(usdAmount).
				Paths(mptPath(fixture.token.IssuanceID())).
				NoDirectRipple().
				Build(),
		)

		jtx.RequireTxSuccess(t, result)
		fixture.token.RequireMPTokenAmount(fixture.holder, 0)
		fixture.token.RequireMPTokenAmount(fixture.other, 100)
		jtx.RequireIOUBalance(t, fixture.env, fixture.holder, fixture.issuer, "USD", 100)
		offertest.RequireOfferCount(t, fixture.env, fixture.holder, 0)
	})

	t.Run("MPT to IOU", func(t *testing.T) {
		fixture := newMPTCrossAssetFixture(t)
		recipient := jtx.NewAccount("recipient")
		fixture.env.Fund(recipient)
		mptAmount := fixture.token.MPTAmount(100)
		usdAmount := tx.NewIssuedAmountFromFloat64(100, "USD", fixture.issuer.Address)
		usdLimit := tx.NewIssuedAmountFromFloat64(1_000, "USD", fixture.issuer.Address)
		fixture.env.Trust(fixture.holder, usdLimit)
		fixture.env.Trust(recipient, usdLimit)
		fixture.env.PayIOU(fixture.issuer, fixture.holder, fixture.issuer, "USD", 100)
		fixture.token.Pay(fixture.issuer, fixture.other, 100)
		jtx.RequireTxSuccess(t, fixture.env.CreatePassiveOffer(fixture.holder, usdAmount, mptAmount))

		result := fixture.env.Submit(
			paybuilder.PayIssued(fixture.other, recipient, usdAmount).
				SendMax(mptAmount).
				PathsCurrency("USD", fixture.issuer).
				NoDirectRipple().
				Build(),
		)

		jtx.RequireTxSuccess(t, result)
		fixture.token.RequireMPTokenAmount(fixture.other, 0)
		fixture.token.RequireMPTokenAmount(fixture.holder, 100)
		jtx.RequireIOUBalance(t, fixture.env, fixture.holder, fixture.issuer, "USD", 0)
		jtx.RequireIOUBalance(t, fixture.env, recipient, fixture.issuer, "USD", 100)
		offertest.RequireOfferCount(t, fixture.env, fixture.holder, 0)
	})

	t.Run("MPT to different MPT", func(t *testing.T) {
		fixture := newMPTCrossAssetFixture(t)
		recipient := jtx.NewAccount("recipient")
		fixture.env.Fund(recipient)
		otherToken := mpt.NewMPTTester(t, fixture.env, fixture.issuer, mpt.MPTInit{
			Holders: []*jtx.Account{fixture.holder, recipient},
		})
		otherToken.Create(mpt.CreateOpts{
			Flags: mpt.TfMPTCanTransfer | mpt.TfMPTCanTrade,
		})
		otherToken.Authorize(mpt.AuthorizeOpts{Account: fixture.holder})
		otherToken.Authorize(mpt.AuthorizeOpts{Account: recipient})

		input := fixture.token.MPTAmount(100)
		output := otherToken.MPTAmount(100)
		fixture.token.Pay(fixture.issuer, fixture.other, 100)
		otherToken.Pay(fixture.issuer, fixture.holder, 100)
		jtx.RequireTxSuccess(t, fixture.env.CreatePassiveOffer(fixture.holder, output, input))

		result := fixture.env.Submit(
			paybuilder.PayIssued(fixture.other, recipient, output).
				SendMax(input).
				Paths(mptPath(otherToken.IssuanceID())).
				NoDirectRipple().
				Build(),
		)

		jtx.RequireTxSuccess(t, result)
		fixture.token.RequireMPTokenAmount(fixture.other, 0)
		fixture.token.RequireMPTokenAmount(fixture.holder, 100)
		otherToken.RequireMPTokenAmount(fixture.holder, 0)
		otherToken.RequireMPTokenAmount(recipient, 100)
		offertest.RequireOfferCount(t, fixture.env, fixture.holder, 0)
	})
}
