package offer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

func TestOfferCreateSignedReserveControlsPlacement(t *testing.T) {
	owner := [20]byte{1}
	issuer := [20]byte{2}
	ownerAddress := state.EncodeAccountIDSafe(owner)
	issuerAddress := state.EncodeAccountIDSafe(issuer)

	for _, test := range []struct {
		name       string
		reserve    int64
		wantResult ter.Result
		wantOffer  bool
		wantOwners uint32
	}{
		{
			name:       "positive reserve rejects underfunded placement",
			reserve:    10,
			wantResult: ter.TecINSUF_RESERVE_OFFER,
		},
		{
			name:       "negative reserve permits placement",
			reserve:    -10,
			wantResult: ter.TesSUCCESS,
			wantOffer:  true,
			wantOwners: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := newOfferMPTLedgerView()
			account := &state.AccountRoot{Account: ownerAddress, Balance: 1, Sequence: 1}
			accountData, err := state.SerializeAccountRoot(account)
			require.NoError(t, err)
			require.NoError(t, view.Insert(keylet.Account(owner), accountData))

			offer := NewOfferCreate(
				ownerAddress,
				tx.NewXRPAmount(1),
				tx.NewIssuedAmountFromFloat64(1, "USD", issuerAddress),
			)
			offer.SetSequence(1)
			ctx := &tx.ApplyContext{
				View:      view,
				Account:   account,
				AccountID: owner,
				Common:    offer.GetCommon(),
				Config: tx.EngineConfig{
					ReserveBase:    uint64(drops.XRPAmount(test.reserve)),
					LedgerSequence: 2,
					Rules:          amendment.EmptyRules(),
				},
				Log: xrpllog.Discard(),
				Ctx: context.Background(),
			}

			result := offer.ApplyCreate(ctx)
			require.Equal(t, test.wantResult, result)
			require.Equal(t, test.wantOwners, ctx.Account.OwnerCount)
			exists, err := view.Exists(keylet.Offer(owner, 1))
			require.NoError(t, err)
			require.Equal(t, test.wantOffer, exists)
			require.Equal(t, uint64(1), ctx.Account.Balance)
		})
	}
}
