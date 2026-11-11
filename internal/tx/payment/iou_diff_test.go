package payment

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestPaymentIOUPredicatesDifferential(t *testing.T) {
	scenarios := iouDiffScenarios(t)
	if len(scenarios) < 600 {
		t.Fatalf("matrix too small: %d", len(scenarios))
	}
	step := &BookStep{}
	for _, sc := range scenarios {
		view := newPaymentMockLedgerView()
		view.data = sc.data
		sb := NewPaymentSandbox(view)
		got, gotErr := step.isDeepFrozen(sb, sc.holder, sc.currency, sc.issuer)
		want, wantErr := tx.IsIOUDeepFrozen(sb, sc.holder, sc.issuer, sc.currency)
		if gotErr != nil || wantErr != nil || got != want {
			t.Errorf("%s deep: payment=%v/%v tx=%v/%v", sc.name, got, gotErr, want, wantErr)
		}
		if got, want := GetTransferRate(sb, sc.issuer), tx.GetTransferRateByID(sb, sc.issuer); got != want {
			t.Errorf("%s rate: payment=%v tx=%v", sc.name, got, want)
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
