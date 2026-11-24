package offer_test

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

// An offer whose TakerPays/TakerGets ratio underflows the amount range has rate
// zero, so its book directory key encodes quality zero. The new directory root
// must still carry sfExchangeRate (value 0); omitting it changes the SLE bytes
// and, under fixCleanup3_2_0, trips the ValidBookDirectory invariant.
func TestOfferCreate_ZeroQualityBookDirectoryKeepsExchangeRate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cleanup bool
	}{
		{name: "fixCleanup3_2_0", cleanup: true},
		{name: "pre_fixCleanup3_2_0", cleanup: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if tc.cleanup {
				env.EnableFeature("fixCleanup3_2_0")
			} else {
				env.DisableFeature("fixCleanup3_2_0")
			}
			alice := jtx.NewAccount("alice")
			env.FundAmount(alice, uint64(jtx.XRP(1000)))
			env.Close()

			seq := env.Seq(alice)
			gets := jtx.IssuedCurrencyFromMantissa(alice, "RAA", 2500000000000000, 77)
			pays := jtx.XRPTxAmountFromXRP(50)
			jtx.RequireTxSuccess(t, env.Submit(offerbuild.OfferCreate(alice, pays, gets).Build()))

			offerData, err := env.LedgerEntry(keylet.Offer(alice.ID, seq))
			require.NoError(t, err)
			offer, err := state.ParseLedgerOffer(offerData)
			require.NoError(t, err)
			require.Zero(t, binary.BigEndian.Uint64(offer.BookDirectory[24:]), "book directory quality")

			dirData, err := env.LedgerEntry(keylet.Keylet{Type: entry.TypeDirectoryNode, Key: offer.BookDirectory})
			require.NoError(t, err)
			var exchangeRate []byte
			require.NoError(t, state.WalkFields(dirData, func(f state.Field) error {
				if f.TypeCode == 3 && f.FieldCode == 6 {
					exchangeRate = f.Value
				}
				return nil
			}))
			require.Equal(t, make([]byte, 8), exchangeRate, "book directory root must store ExchangeRate 0")
		})
	}
}
