package offer

import (
	"encoding/binary"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// TestOffer_TickSizeQuality_Issue1012 pins the book-directory quality an offer
// receives when a tick size applies. rippled rounds the tick-adjusted side with
// plain multiply()/divide() — round to nearest — so the resulting quality matches
// Quality::round(). goXRPL previously rounded that side away from zero, producing a
// quality one ULP off and keying the offer into a different BookDirectory.
//
// Replays mainnet ledger 99226373's OfferCreate c1d3b833…: a passive
// XRP↔RLUSD offer, TakerPays 971829440 drops, TakerGets 2854.83644, against an
// issuer with TickSize 7. Mainnet stored TakerGets 2854.835624261196 and
// ExchangeRate 5a0c180ee6b8b000; the away-from-zero rounding yielded
// 2854.835624261197 / 5a0c180ee6b8afff.
func TestOffer_TickSizeQuality_Issue1012(t *testing.T) {
	const (
		wantExchangeRate uint64 = 0x5a0c180ee6b8b000
		wantTakerGets           = "2854.835624261196"
	)

	env := newEnvWithFeatures(t, nil)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	env.FundAmount(gw, uint64(jtx.XRP(100000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(gw).TickSize(7).Build()))

	env.Trust(alice, jtx.USD(gw, 1000000))
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, alice, jtx.USD(gw, 1000000)).Build()))
	env.Close()

	// TakerGets = 2854.83644 USD (== mantissa 2854836440000000e-12), TakerPays = XRP drops.
	takerGets := tx.NewIssuedAmount(2854836440000000, -12, "USD", gw.Address)
	takerPays := tx.NewXRPAmount(971829440)

	seq := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(OfferCreate(alice, takerPays, takerGets).Passive().Build()))
	env.Close()

	offer := GetOffer(env, alice, seq)
	require.NotNil(t, offer, "offer should be placed")

	gotRate := binary.BigEndian.Uint64(offer.BookDirectory[24:32])
	require.Equalf(t, wantExchangeRate, gotRate,
		"book-directory ExchangeRate: got %016x want %016x", gotRate, wantExchangeRate)
	require.Equal(t, wantTakerGets, offer.TakerGets.Value(), "tick-rounded TakerGets")
}
