package offer

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

// TestOffer_ZeroQualityBookDirectory pins offers whose rate is 0. rippled's
// getRate returns 0 when in/out underflows or overflows, places the offer in the
// quality-0 directory and writes sfExchangeRate = 0 with setFieldU64, so the
// field stays present. goXRPL dropped a zero ExchangeRate when serializing the
// new root, which tripped ValidBookDirectory (tecINVARIANT_FAILED) where rippled
// returns tesSUCCESS.
func TestOffer_ZeroQualityBookDirectory(t *testing.T) {
	env := newEnvWithFeatures(t, nil)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	env.FundAmount(gw, uint64(jtx.XRP(100000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(gw).DefaultRipple().Build()))
	env.Trust(alice, jtx.USD(gw, 1000000))
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, alice, jtx.USD(gw, 1000000)).Build()))
	env.Close()

	cases := []struct {
		name      string
		takerPays tx.Amount
		takerGets tx.Amount
	}{
		{
			// 1e-81 USD / 1e10 drops underflows to a zero quotient.
			name:      "underflow",
			takerPays: tx.NewIssuedAmount(1000000000000000, -96, "USD", gw.Address),
			takerGets: tx.NewXRPAmount(10_000_000_000),
		},
		{
			// 9999999999999999e80 EUR / 1e-70 USD overflows the quotient.
			name:      "overflow",
			takerPays: tx.NewIssuedAmount(9999999999999999, 80, "EUR", gw.Address),
			takerGets: tx.NewIssuedAmount(1000000000000000, -85, "USD", gw.Address),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seq := env.Seq(alice)
			jtx.RequireTxSuccess(t, env.Submit(OfferCreate(alice, tc.takerPays, tc.takerGets).Build()))
			env.Close()

			offer := GetOffer(env, alice, seq)
			require.NotNil(t, offer, "offer should be placed")
			require.Zero(t, binary.BigEndian.Uint64(offer.BookDirectory[24:32]), "book quality")

			data, err := env.LedgerEntry(keylet.Keylet{Type: entry.TypeDirectoryNode, Key: offer.BookDirectory})
			require.NoError(t, err)
			rate, present := bookExchangeRate(t, data)
			require.True(t, present, "sfExchangeRate must stay present at value 0")
			require.Zero(t, rate)
		})
	}
}

var errFound = errors.New("found")

func bookExchangeRate(t *testing.T, data []byte) (uint64, bool) {
	t.Helper()
	var rate uint64
	var present bool
	err := state.WalkFields(data, func(f state.Field) error {
		if f.TypeCode == 3 && f.FieldCode == 6 && len(f.Value) == 8 {
			rate = binary.BigEndian.Uint64(f.Value)
			present = true
			return errFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, errFound) {
		require.NoError(t, err)
	}
	return rate, present
}
