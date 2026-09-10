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

func TestOfferAcceptanceRetryResults(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, issuerHigh := range []bool{false, true} {
			for _, tc := range []struct {
				name          string
				missingIssuer bool
				mpt           bool
				line          bool
				authorized    bool
				final, retry  ter.Result
			}{
				{name: "missing IOU issuer", missingIssuer: true, final: ter.TecNO_ISSUER, retry: ter.TerNO_ACCOUNT},
				{name: "missing MPT issuer", missingIssuer: true, mpt: true, final: ter.TecNO_ISSUER, retry: ter.TerNO_ACCOUNT},
				{name: "missing authorization line", final: ter.TecNO_LINE, retry: ter.TerNO_LINE},
				{name: "unauthorized line", line: true, final: ter.TecNO_AUTH, retry: ter.TerNO_AUTH},
				{name: "authorized line", line: true, authorized: true, final: ter.TesSUCCESS, retry: ter.TesSUCCESS},
			} {
				t.Run(fmt.Sprintf("%s/retry=%t/issuerHigh=%t", tc.name, retry, issuerHigh), func(t *testing.T) {
					view := newOfferMPTLedgerView()
					issuer, holder := [20]byte{1}, [20]byte{2}
					if issuerHigh {
						issuer, holder = holder, issuer
					}
					issuerAddr, holderAddr := state.EncodeAccountIDSafe(issuer), state.EncodeAccountIDSafe(holder)
					putOfferMPTAccount(t, view, holder)
					if !tc.missingIssuer {
						raw, err := state.SerializeAccountRoot(&state.AccountRoot{Account: issuerAddr, Balance: 100_000_000, Sequence: 2, Flags: state.LsfRequireAuth})
						require.NoError(t, err)
						view.data[keylet.Account(issuer).Key] = raw
					}
					if tc.line {
						low, high := issuerAddr, holderAddr
						auth := uint32(state.LsfLowAuth)
						if issuerHigh {
							low, high, auth = holderAddr, issuerAddr, state.LsfHighAuth
						}
						if !tc.authorized {
							auth = 0
						}
						raw, err := state.SerializeRippleState(&state.RippleState{
							Balance:   state.NewIssuedAmountFromValue(0, 0, "USD", state.AccountOneAddress),
							LowLimit:  state.NewIssuedAmountFromValue(100, 0, "USD", low),
							HighLimit: state.NewIssuedAmountFromValue(100, 0, "USD", high),
							Flags:     auth,
						})
						require.NoError(t, err)
						view.data[keylet.Line(holder, issuer, "USD").Key] = raw
					}
					amount := state.NewIssuedAmountFromValue(1, 0, "USD", issuerAddr)
					if tc.mpt {
						amount = offerMPTAmount(keylet.MakeMPTID(1, issuer), 1)
					}
					config := tx.EngineConfig{Rules: amendment.AllSupportedRules()}
					want := tc.final
					if retry {
						config.ApplyFlags = tx.TapRETRY
						want = tc.retry
					}
					offer := NewOfferCreate(holderAddr, tx.NewXRPAmount(1), amount)
					require.Equal(t, want, offer.Preclaim(view, config))
				})
			}
		}
	}
}
