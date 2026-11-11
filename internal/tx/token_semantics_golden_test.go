package tx

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

type iouScenario struct {
	name     string
	view     *mockBaseView
	holder   [20]byte
	issuer   [20]byte
	currency string
}

// iouScenarios enumerates issuer state x holder ordering x every trust-line
// flag combination x currency.
func iouScenarios(t *testing.T) []iouScenario {
	t.Helper()
	var issuer, low, high [20]byte
	issuer[0], low[0], high[0] = 0x50, 0x10, 0x90
	issuerStates := []struct {
		missing bool
		flags   uint32
		rate    uint32
	}{
		{missing: true},
		{},
		{flags: state.LsfGlobalFreeze},
		{flags: state.LsfRequireAuth},
		{flags: state.LsfGlobalFreeze | state.LsfRequireAuth},
		{flags: state.LsfRequireAuth, rate: 1_005_000_000},
	}
	lineFlags := []uint32{
		state.LsfLowFreeze, state.LsfHighFreeze,
		state.LsfLowDeepFreeze, state.LsfHighDeepFreeze,
		state.LsfLowAuth, state.LsfHighAuth,
	}
	var out []iouScenario
	for _, is := range issuerStates {
		for _, holder := range [][20]byte{low, high, issuer} {
			lines := []int{-1}
			if holder != issuer {
				for m := 0; m < 1<<len(lineFlags); m++ {
					lines = append(lines, m)
				}
			}
			for _, m := range lines {
				for _, currency := range []string{"USD", "XRP"} {
					view := newMockBaseView()
					if !is.missing {
						raw, err := state.SerializeAccountRoot(&state.AccountRoot{
							Account: state.EncodeAccountIDSafe(issuer), Balance: 1, Sequence: 1,
							Flags: is.flags, TransferRate: is.rate,
						})
						if err != nil {
							t.Fatal(err)
						}
						view.data[keylet.Account(issuer).Key] = raw
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
						view.data[keylet.Line(holder, issuer, "USD").Key] = raw
					}
					issuerState := fmt.Sprintf("%#x", is.flags)
					if is.missing {
						issuerState = "missing"
					}
					out = append(out, iouScenario{
						name:     fmt.Sprintf("issuer=%s/%d holder=%02x line=%d/%#x %s", issuerState, is.rate, holder[0], m, flags, currency),
						view:     view,
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

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if string(want) != got {
		wantLines, gotLines := strings.Split(string(want), "\n"), strings.Split(got, "\n")
		for i := 0; i < len(wantLines) && i < len(gotLines); i++ {
			if wantLines[i] != gotLines[i] {
				t.Fatalf("golden %s differs at line %d:\nwant %s\ngot  %s", name, i+1, wantLines[i], gotLines[i])
			}
		}
		t.Fatalf("golden %s differs in length: want %d lines, got %d", name, len(wantLines), len(gotLines))
	}
}

// TestIOUTokenSemanticsGolden freezes the results of the shared IOU freeze and
// transfer-rate predicates. An amendment that changes any of them must update
// the golden deliberately (go test -run TestIOUTokenSemanticsGolden -update).
func TestIOUTokenSemanticsGolden(t *testing.T) {
	var b strings.Builder
	for _, sc := range iouScenarios(t) {
		asset := Asset{Currency: sc.currency, Issuer: state.EncodeAccountIDSafe(sc.issuer)}
		iouFrozen, iouFrozenErr := IsIOUFrozen(sc.view, sc.holder, sc.issuer, sc.currency)
		deep, deepErr := IsIOUDeepFrozen(sc.view, sc.holder, sc.issuer, sc.currency)
		if iouFrozenErr != nil || deepErr != nil {
			t.Fatalf("%s: unexpected errors %v %v", sc.name, iouFrozenErr, deepErr)
		}
		if IsDeepFrozen(sc.view, sc.holder, sc.issuer, sc.currency) != deep {
			t.Fatalf("%s: IsDeepFrozen disagrees with IsIOUDeepFrozen", sc.name)
		}
		if rate, err := ReadTransferRate(sc.view, sc.issuer); err != nil || rate != GetTransferRateByID(sc.view, sc.issuer) {
			t.Fatalf("%s: ReadTransferRate = %d, %v", sc.name, rate, err)
		}
		fmt.Fprintf(&b, "%s global=%d individual=%d frozen=%d iouFrozen=%d deep=%d rate=%d\n",
			sc.name,
			b2i(IsGlobalFrozen(sc.view, asset.Issuer)),
			b2i(IsIndividualFrozen(sc.view, sc.holder, asset)),
			b2i(IsFrozen(sc.view, sc.holder, asset)),
			b2i(iouFrozen),
			b2i(deep),
			GetTransferRateByID(sc.view, sc.issuer),
		)
	}
	checkGolden(t, "iou_token_semantics.golden", b.String())
}
