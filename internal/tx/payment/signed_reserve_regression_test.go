package payment

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

func TestPaymentMissingDestinationSignedReserveBoundary(t *testing.T) {
	source := state.EncodeAccountIDSafe([20]byte{1})
	destination := state.EncodeAccountIDSafe([20]byte{2})

	tests := []struct {
		name        string
		reserve     int64
		amount      int64
		balance     uint64
		wantResult  ter.Result
		wantBalance uint64
	}{
		{
			name:        "exact reserve is sufficient",
			reserve:     10,
			amount:      10,
			balance:     20,
			wantResult:  ter.TesSUCCESS,
			wantBalance: 10,
		},
		{
			name:        "below reserve is rejected",
			reserve:     10,
			amount:      9,
			balance:     19,
			wantResult:  ter.TecNO_DST_INSUF_XRP,
			wantBalance: 19,
		},
		{
			name:        "negative reserve permits one drop account",
			reserve:     -10,
			amount:      1,
			balance:     1,
			wantResult:  ter.TesSUCCESS,
			wantBalance: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := newPaymentMockLedgerView()
			payment := NewPayment(source, destination, tx.NewXRPAmount(test.amount))
			payment.Fee = "0"
			account := &state.AccountRoot{
				Account:  source,
				Balance:  test.balance,
				Sequence: 1,
			}
			ctx := &tx.ApplyContext{
				View:      view,
				Account:   account,
				AccountID: [20]byte{1},
				Common:    payment.GetCommon(),
				Config: tx.EngineConfig{
					ReserveBase:    uint64(drops.XRPAmount(test.reserve)),
					LedgerSequence: 2,
					Rules:          amendment.EmptyRules(),
				},
				Metadata: &tx.Metadata{},
				Log:      xrpllog.Discard(),
				Ctx:      context.Background(),
			}

			result := payment.Apply(ctx)
			require.Equal(t, test.wantResult, result)
			require.Equal(t, test.wantBalance, account.Balance)
			exists, err := view.Exists(keylet.Account([20]byte{2}))
			require.NoError(t, err)
			require.Equal(t, test.wantResult == ter.TesSUCCESS, exists)
		})
	}
}

func TestPaymentNegativeReserveNeverAuthorizesBalanceUnderflow(t *testing.T) {
	source := state.EncodeAccountIDSafe([20]byte{3})
	destination := state.EncodeAccountIDSafe([20]byte{4})
	payment := NewPayment(source, destination, tx.NewXRPAmount(2))
	payment.Fee = "0"
	account := &state.AccountRoot{Account: source, Balance: 1, Sequence: 1}
	negativeReserve := drops.XRPAmount(-10)
	ctx := &tx.ApplyContext{
		View:      newPaymentMockLedgerView(),
		Account:   account,
		AccountID: [20]byte{3},
		Common:    payment.GetCommon(),
		Config: tx.EngineConfig{
			ReserveBase: uint64(negativeReserve),
			Rules:       amendment.EmptyRules(),
		},
		Metadata: &tx.Metadata{},
		Log:      xrpllog.Discard(),
		Ctx:      context.Background(),
	}

	require.Equal(t, ter.TecUNFUNDED_PAYMENT, payment.Apply(ctx))
	require.Equal(t, uint64(1), account.Balance)
}

func TestPaymentExternallyPaidNegativeReserveReturnsInvariantFailure(t *testing.T) {
	source := state.EncodeAccountIDSafe([20]byte{6})
	destination := state.EncodeAccountIDSafe([20]byte{7})
	negativeReserve := drops.XRPAmount(-10)

	p := NewPayment(source, destination, tx.NewXRPAmount(2))
	p.Fee = "0"
	p.Delegate = state.EncodeAccountIDSafe([20]byte{8})

	account := &state.AccountRoot{Account: source, Balance: 1, Sequence: 1}
	view := newPaymentMockLedgerView()

	ctx := &tx.ApplyContext{
		View:      view,
		Account:   account,
		AccountID: [20]byte{6},
		Common:    p.GetCommon(),
		Config: tx.EngineConfig{
			ReserveBase: uint64(negativeReserve),
		},
		Metadata: &tx.Metadata{},
		Log:      xrpllog.Discard(),
		Ctx:      context.Background(),
	}

	require.False(t, ctx.SourcePaysFee())
	require.Equal(t, ter.TecINVARIANT_FAILED, p.Apply(ctx))
	require.Equal(t, uint64(1), account.Balance)
}

func TestPaymentPreclaimMissingDestinationSignedReserveBoundary(t *testing.T) {
	destination := state.EncodeAccountIDSafe([20]byte{5})
	for _, test := range []struct {
		name       string
		reserve    int64
		amount     int64
		wantResult ter.Result
	}{
		{name: "exact", reserve: 10, amount: 10, wantResult: ter.TesSUCCESS},
		{name: "below", reserve: 10, amount: 9, wantResult: ter.TecNO_DST_INSUF_XRP},
		{name: "negative", reserve: -10, amount: 1, wantResult: ter.TesSUCCESS},
	} {
		t.Run(test.name, func(t *testing.T) {
			payment := NewPayment("rSource", destination, tx.NewXRPAmount(test.amount))
			result := payment.Preclaim(newPaymentMockLedgerView(), tx.EngineConfig{
				ReserveBase: uint64(drops.XRPAmount(test.reserve)),
			})
			require.Equal(t, test.wantResult, result)
		})
	}
}

func TestPaymentSourceFeePayerUsesCommonIdentityForZeroFee(t *testing.T) {
	payment := NewPayment("rSource", "rDestination", tx.NewXRPAmount(1))
	payment.Fee = "0"
	ctx := &tx.ApplyContext{Common: payment.GetCommon()}

	require.True(t, ctx.SourcePaysFee())
	require.Zero(t, ctx.SourceFeeCharged)

	ctx.Common.Delegate = "rDelegate"
	require.False(t, ctx.SourcePaysFee())

	ctx.Common.Delegate = ""
	ctx.Common.Sponsor = "rSponsor"
	feeFlags := tx.SpfSponsorFee
	ctx.Common.SponsorFlags = &feeFlags
	require.False(t, ctx.SourcePaysFee())

	reserveFlags := tx.SpfSponsorReserve
	ctx.Common.SponsorFlags = &reserveFlags
	require.True(t, ctx.SourcePaysFee())

	ctx.Common.Delegate = "rDelegate"
	ctx.Common.Sponsor = ctx.Common.Account
	ctx.Common.SponsorFlags = &feeFlags
	require.True(t, ctx.SourcePaysFee())
}
