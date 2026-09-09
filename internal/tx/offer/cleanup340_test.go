package offer

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"

	"github.com/stretchr/testify/require"
)

func TestOfferCleanup340TrustlineRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, open := range []bool{false, true} {
			t.Run(fmt.Sprintf("retry=%t/open=%t", retry, open), func(t *testing.T) {
				view := newOfferMPTLedgerView()
				issuer, holder := [20]byte{1}, [20]byte{2}
				raw, err := state.SerializeAccountRoot(&state.AccountRoot{Account: state.EncodeAccountIDSafe(issuer), Balance: 100_000_000, Sequence: 2, Flags: state.LsfDisallowIncomingTrustline})
				require.NoError(t, err)
				view.data[keylet.Account(issuer).Key] = raw
				config := tx.EngineConfig{ViewOpen: open, Rules: amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})}
				if retry {
					config.ApplyFlags = tx.TapRETRY
				}
				expected := ter.TecNO_LINE
				if retry {
					expected = ter.TerNO_LINE
				}
				require.Equal(t, expected, checkAcceptAsset(view, holder, issuer, "USD", config))
			})
		}
	}
}
