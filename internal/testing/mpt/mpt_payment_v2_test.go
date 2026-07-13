package mpt_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	paybuilder "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

type mptPaymentFixture struct {
	env    *jtx.TestEnv
	token  *mpt.MPTTester
	issuer *jtx.Account
	holder *jtx.Account
	other  *jtx.Account
}

func newMPTPaymentFixture(
	t *testing.T,
	v2 bool,
	flags uint32,
	transferFee *uint16,
) mptPaymentFixture {
	t.Helper()

	env := jtx.NewTestEnv(t)
	if v2 {
		env.EnableFeature("MPTokensV2")
	} else {
		env.DisableFeature("MPTokensV2")
	}
	env.Close()

	issuer := jtx.NewAccount("issuer")
	holder := jtx.NewAccount("holder")
	other := jtx.NewAccount("other")
	token := mpt.NewMPTTester(t, env, issuer, mpt.MPTInit{
		Holders: []*jtx.Account{holder, other},
	})
	token.Create(mpt.CreateOpts{
		TransferFee: transferFee,
		OwnerCount:  mpt.PtrUint32(1),
		HolderCount: mpt.PtrUint32(0),
		Flags:       flags,
	})
	token.Authorize(mpt.AuthorizeOpts{Account: holder})
	token.Authorize(mpt.AuthorizeOpts{Account: other})

	return mptPaymentFixture{
		env:    env,
		token:  token,
		issuer: issuer,
		holder: holder,
		other:  other,
	}
}

func TestMPTPaymentV2Flags(t *testing.T) {
	for _, test := range []struct {
		name string
		v2   bool
	}{
		{name: "V1", v2: false},
		{name: "V2", v2: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMPTPaymentFixture(t, test.v2, 0, nil)
			amount := fixture.token.MPTAmount(10)
			if test.v2 {
				amount = state.NewMPTAmountWithIssuanceID(10, "", fixture.token.IssuanceID())
			}

			noDirect := fixture.env.Submit(
				paybuilder.PayIssued(fixture.issuer, fixture.holder, amount).
					NoDirectRipple().
					Build(),
			)
			if test.v2 {
				jtx.RequireTxFail(t, noDirect, jtx.TemRIPPLE_EMPTY)
			} else {
				jtx.RequireTxFail(t, noDirect, jtx.TemINVALID_FLAG)
			}
			fixture.token.RequireMPTokenAmount(fixture.holder, 0)

			limitQuality := fixture.env.Submit(
				paybuilder.PayIssued(fixture.issuer, fixture.holder, amount).
					LimitQuality().
					Build(),
			)
			if test.v2 {
				jtx.RequireTxSuccess(t, limitQuality)
				fixture.token.RequireMPTokenAmount(fixture.holder, 10)
			} else {
				jtx.RequireTxFail(t, limitQuality, jtx.TemINVALID_FLAG)
				fixture.token.RequireMPTokenAmount(fixture.holder, 0)
			}
		})
	}
}

func TestMPTPaymentV2AssetCombinations(t *testing.T) {
	type shapeCase struct {
		name   string
		v2Code string
		build  func(mptPaymentFixture) tx.Transaction
	}

	cases := []shapeCase{
		{
			name:   "MPTAmountWithXRPSendMax",
			v2Code: jtx.TecPATH_PARTIAL,
			build: func(fixture mptPaymentFixture) tx.Transaction {
				return paybuilder.PayIssued(
					fixture.issuer,
					fixture.other,
					fixture.token.MPTAmount(100),
				).SendMax(tx.NewXRPAmount(jtx.XRP(100))).Build()
			},
		},
		{
			name:   "IOUAmountWithMPTSendMax",
			v2Code: jtx.TecPATH_DRY,
			build: func(fixture mptPaymentFixture) tx.Transaction {
				usd := tx.NewIssuedAmountFromFloat64(100, "USD", fixture.issuer.Address)
				return paybuilder.PayIssued(fixture.issuer, fixture.other, usd).
					SendMax(fixture.token.MPTAmount(100)).
					Build()
			},
		},
		{
			name:   "XRPAmountWithMPTSendMax",
			v2Code: jtx.TecPATH_PARTIAL,
			build: func(fixture mptPaymentFixture) tx.Transaction {
				return paybuilder.Pay(
					fixture.issuer,
					fixture.other,
					uint64(jtx.XRP(100)),
				).SendMax(fixture.token.MPTAmount(100)).Build()
			},
		},
		{
			name:   "DifferentMPTIssuances",
			v2Code: jtx.TecOBJECT_NOT_FOUND,
			build: func(fixture mptPaymentFixture) tx.Transaction {
				missingID := mpt.MakeMPTIDHexFromAddr(
					fixture.env.Seq(fixture.issuer)+10,
					fixture.issuer.Address,
				)
				amount := state.NewMPTAmountWithIssuanceID(100, "", missingID)
				return paybuilder.PayIssued(fixture.issuer, fixture.other, amount).
					SendMax(fixture.token.MPTAmount(100)).
					Build()
			},
		},
		{
			name:   "ExplicitIOUPath",
			v2Code: jtx.TesSUCCESS,
			build: func(fixture mptPaymentFixture) tx.Transaction {
				return paybuilder.PayIssued(
					fixture.issuer,
					fixture.other,
					fixture.token.MPTAmount(100),
				).PathsCurrency("USD", fixture.issuer).Build()
			},
		},
	}

	for _, version := range []struct {
		name string
		v2   bool
	}{
		{name: "V1", v2: false},
		{name: "V2", v2: true},
	} {
		t.Run(version.name, func(t *testing.T) {
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					fixture := newMPTPaymentFixture(
						t,
						version.v2,
						mpt.TfMPTCanTransfer|mpt.TfMPTCanTrade,
						nil,
					)
					result := fixture.env.Submit(test.build(fixture))
					if !version.v2 {
						jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
						fixture.token.RequireMPTokenAmount(fixture.other, 0)
						return
					}

					if test.v2Code == jtx.TesSUCCESS {
						jtx.RequireTxSuccess(t, result)
						fixture.token.RequireMPTokenAmount(fixture.other, 100)
						return
					}
					jtx.RequireTxClaimed(t, result, test.v2Code)
					fixture.token.RequireMPTokenAmount(fixture.other, 0)
				})
			}
		})
	}
}

func TestMPTPaymentV2TransferFeeRounding(t *testing.T) {
	for _, test := range []struct {
		name      string
		v2        bool
		delivered int64
	}{
		{name: "V1", v2: false, delivered: 82},
		{name: "V2", v2: true, delivered: 81},
	} {
		t.Run(test.name, func(t *testing.T) {
			transferFee := uint16(10_000)
			fixture := newMPTPaymentFixture(
				t,
				test.v2,
				mpt.TfMPTCanTransfer,
				&transferFee,
			)
			fixture.token.Pay(fixture.issuer, fixture.holder, 1_000)

			result := fixture.env.Submit(
				paybuilder.PayIssued(
					fixture.holder,
					fixture.other,
					fixture.token.MPTAmount(100),
				).
					SendMax(fixture.token.MPTAmount(90)).
					PartialPayment().
					Build(),
			)
			jtx.RequireTxSuccess(t, result)
			fixture.token.RequireMPTokenAmount(fixture.holder, 910)
			fixture.token.RequireMPTokenAmount(fixture.other, test.delivered)
		})
	}
}
