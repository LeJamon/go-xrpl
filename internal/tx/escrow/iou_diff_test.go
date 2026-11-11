package escrow

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type rulesMapView struct {
	*mapView
	rules *amendment.Rules
}

func (v rulesMapView) Rules() *amendment.Rules { return v.rules }

func TestEscrowIOUPredicatesDifferential(t *testing.T) {
	scenarios := iouDiffScenarios(t)
	if len(scenarios) < 600 {
		t.Fatalf("matrix too small: %d", len(scenarios))
	}
	for _, rules := range []*amendment.Rules{amendment.AllSupportedRules(), amendment.GenesisRules()} {
		for _, sc := range scenarios {
			if sc.currency != "USD" {
				continue
			}
			view := rulesMapView{mapView: &mapView{data: sc.data}, rules: rules}

			gotDeep, gotDeepResult := isIOUDeepFrozen(view, sc.holder, sc.issuer, sc.currency)
			wantDeep, err := tx.IsIOUDeepFrozen(view, sc.holder, sc.issuer, sc.currency)
			if err != nil || gotDeepResult != ter.TesSUCCESS || gotDeep != wantDeep {
				t.Errorf("%s deep: escrow=%v/%v tx=%v/%v", sc.name, gotDeep, gotDeepResult, wantDeep, err)
			}

			_, issuerExists := sc.data[keylet.Account(sc.issuer).Key]
			gotFrozen, gotFrozenResult := isIOUFrozen(view, sc.holder, sc.issuer, sc.currency)
			wantFrozen, err := tx.IsIOUFrozen(view, sc.holder, sc.issuer, sc.currency)
			if !issuerExists {
				if gotFrozenResult != ter.TecNO_ISSUER || err != nil {
					t.Errorf("%s frozen missing issuer: escrow=%v tx=%v/%v", sc.name, gotFrozenResult, wantFrozen, err)
				}
			} else if err != nil || gotFrozenResult != ter.TesSUCCESS || gotFrozen != wantFrozen {
				t.Errorf("%s frozen: escrow=%v/%v tx=%v/%v", sc.name, gotFrozen, gotFrozenResult, wantFrozen, err)
			}

			asset := tx.Asset{Currency: sc.currency, Issuer: state.EncodeAccountIDSafe(sc.issuer)}
			gotAuth := requireAuthIOU(view, sc.issuer, sc.holder, sc.currency)
			wantAuth := mptutil.RequireAssetAuthAt(view, asset, sc.holder, mptutil.LegacyAuth, 0)
			if gotAuth != wantAuth {
				t.Errorf("%s auth: escrow=%v mptutil=%v", sc.name, gotAuth, wantAuth)
			}

			gotRate, gotRateResult := getIOUTransferRate(view, sc.issuer)
			wantRate := tx.GetTransferRateByID(view, sc.issuer)
			if !issuerExists {
				if gotRateResult != ter.TecNO_ISSUER {
					t.Errorf("%s rate missing issuer: escrow=%v", sc.name, gotRateResult)
				}
			} else if gotRateResult != ter.TesSUCCESS || gotRate != wantRate {
				t.Errorf("%s rate: escrow=%v/%v tx=%v", sc.name, gotRate, gotRateResult, wantRate)
			}
		}
	}
}

type iouDiffScenario struct {
	name     string
	data     map[[32]byte][]byte
	holder   [20]byte
	issuer   [20]byte
	currency string
}

func iouDiffScenarios(t *testing.T) []iouDiffScenario {
	t.Helper()
	var issuer, low, high [20]byte
	issuer[0], low[0], high[0] = 0x50, 0x10, 0x90
	issuerFlags := []struct {
		missing bool
		flags   uint32
		rate    uint32
	}{{missing: true}, {}, {flags: state.LsfGlobalFreeze}, {flags: state.LsfRequireAuth}, {flags: state.LsfGlobalFreeze | state.LsfRequireAuth}, {flags: state.LsfRequireAuth, rate: 1_005_000_000}}
	lineFlags := []uint32{state.LsfLowFreeze, state.LsfHighFreeze, state.LsfLowDeepFreeze, state.LsfHighDeepFreeze, state.LsfLowAuth, state.LsfHighAuth}
	var out []iouDiffScenario
	for _, is := range issuerFlags {
		for _, holder := range [][20]byte{low, high, issuer} {
			lines := []int{-1}
			if holder != issuer {
				for m := 0; m < 1<<len(lineFlags); m++ {
					lines = append(lines, m)
				}
			}
			for _, m := range lines {
				for _, currency := range []string{"USD", "XRP"} {
					data := map[[32]byte][]byte{}
					if !is.missing {
						raw, err := state.SerializeAccountRoot(&state.AccountRoot{Account: state.EncodeAccountIDSafe(issuer), Balance: 1, Sequence: 1, Flags: is.flags, TransferRate: is.rate})
						if err != nil {
							t.Fatal(err)
						}
						data[keylet.Account(issuer).Key] = raw
					}
					var flags uint32
					if m >= 0 {
						for i, f := range lineFlags {
							if m&(1<<i) != 0 {
								flags |= f
							}
						}
						lowID, highID := holder, issuer
						if state.CompareAccountIDs(lowID, highID) > 0 {
							lowID, highID = highID, lowID
						}
						raw, err := state.SerializeRippleState(&state.RippleState{
							Balance:   state.NewIssuedAmountFromValue(0, -100, "USD", state.EncodeAccountIDSafe(lowID)),
							LowLimit:  state.NewIssuedAmountFromValue(0, -100, "USD", state.EncodeAccountIDSafe(lowID)),
							HighLimit: state.NewIssuedAmountFromValue(0, -100, "USD", state.EncodeAccountIDSafe(highID)),
							Flags:     flags,
						})
						if err != nil {
							t.Fatal(err)
						}
						data[keylet.Line(holder, issuer, "USD").Key] = raw
					}
					out = append(out, iouDiffScenario{
						name:     fmt.Sprintf("issuer=%v/%#x holder=%x line=%d/%#x %s", is.missing, is.flags, holder[0], m, flags, currency),
						data:     data,
						holder:   holder,
						issuer:   issuer,
						currency: currency,
					})
				}
			}
		}
	}
	return out
}
