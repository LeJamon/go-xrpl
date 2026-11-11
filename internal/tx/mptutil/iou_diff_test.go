package mptutil

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

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

func TestIOUDeepFrozenDifferential(t *testing.T) {
	for _, sc := range iouDiffScenarios(t) {
		view := newMPTTestView()
		view.data = sc.data
		if got, want := isIOUDeepFrozen(view, sc.holder, sc.issuer, sc.currency), tx.IsDeepFrozen(view, sc.holder, sc.issuer, sc.currency); got != want {
			t.Errorf("%s: mptutil=%v tx=%v", sc.name, got, want)
		}
	}
}

func TestIOURequireAuthDifferential(t *testing.T) {
	for _, rules := range []*amendment.Rules{amendment.AllSupportedRules(), amendment.GenesisRules()} {
		for _, sc := range iouDiffScenarios(t) {
			if sc.currency != "USD" {
				continue
			}
			view := newMPTTestView()
			view.data = sc.data
			view.rules = rules
			asset := tx.Asset{Currency: sc.currency, Issuer: state.EncodeAccountIDSafe(sc.issuer)}
			got := requireAssetAuthWithTypeAt(view, asset, sc.holder, LegacyAuth, 0, 0)
			want := tx.RequireAuth(fullDiffView{view}, asset, sc.holder)
			if got != want {
				t.Errorf("%s: mptutil=%v tx=%v", sc.name, got, want)
			}
			if weak := requireAssetAuthWithTypeAt(view, asset, sc.holder, WeakAuth, 0, 0); weak != got {
				t.Errorf("%s: weak=%v legacy=%v", sc.name, weak, got)
			}
			strong := requireAssetAuthWithTypeAt(view, asset, sc.holder, StrongAuth, 0, 0)
			_, lineExists := sc.data[keylet.Line(sc.holder, sc.issuer, "USD").Key]
			if sc.holder != sc.issuer && !lineExists {
				if strong != ter.TecNO_LINE {
					t.Errorf("%s: strong=%v without line", sc.name, strong)
				}
			} else if strong != got {
				t.Errorf("%s: strong=%v legacy=%v", sc.name, strong, got)
			}
		}
	}
}

type fullDiffView struct{ *mptTestView }

func (fullDiffView) ForEach(func([32]byte, []byte) bool) error { return nil }
func (fullDiffView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (fullDiffView) TxExists([32]byte) (bool, error) { return false, nil }
func (fullDiffView) LedgerSeq() uint32               { return 0 }
