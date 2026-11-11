package mptutil

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

// TestRequireIOUAuthGolden freezes rippled's requireAuth(Issue) over issuer
// state x holder ordering x every trust-line flag combination x pseudo-account
// holder x AuthType x amendment set. An amendment that changes IOU
// authorization must update the golden deliberately
// (go test -run TestRequireIOUAuthGolden -update).
func TestRequireIOUAuthGolden(t *testing.T) {
	ruleSets := []struct {
		name  string
		rules *amendment.Rules
	}{{"all", amendment.AllSupportedRules()}, {"genesis", amendment.GenesisRules()}}
	var b strings.Builder
	for _, rs := range ruleSets {
		for _, sc := range iouScenarios(t) {
			if sc.currency != "USD" {
				continue
			}
			for _, pseudo := range []bool{false, true} {
				if pseudo && sc.holder == sc.issuer {
					continue
				}
				view := newMPTTestView()
				for k, v := range sc.data {
					view.data[k] = v
				}
				view.rules = rs.rules
				if pseudo {
					raw, err := state.SerializeAccountRoot(&state.AccountRoot{
						Account: state.EncodeAccountIDSafe(sc.holder), Balance: 1, Sequence: 0,
						VaultID: [32]byte{1},
					})
					if err != nil {
						t.Fatal(err)
					}
					view.data[keylet.Account(sc.holder).Key] = raw
				}
				fmt.Fprintf(&b, "%s %s pseudo=%d legacy=%s weak=%s strong=%s\n", rs.name, sc.name, b2i(pseudo),
					RequireIOUAuth(view, sc.issuer, sc.currency, sc.holder, LegacyAuth),
					RequireIOUAuth(view, sc.issuer, sc.currency, sc.holder, WeakAuth),
					RequireIOUAuth(view, sc.issuer, sc.currency, sc.holder, StrongAuth),
				)
			}
		}
	}
	checkGolden(t, "iou_require_auth.golden", b.String())
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

type iouScenario struct {
	name     string
	data     map[[32]byte][]byte
	holder   [20]byte
	issuer   [20]byte
	currency string
}

func iouScenarios(t *testing.T) []iouScenario {
	t.Helper()
	var issuer, low, high [20]byte
	issuer[0], low[0], high[0] = 0x50, 0x10, 0x90
	issuerFlags := []struct {
		missing bool
		flags   uint32
		rate    uint32
	}{{missing: true}, {}, {flags: state.LsfGlobalFreeze}, {flags: state.LsfRequireAuth}, {flags: state.LsfGlobalFreeze | state.LsfRequireAuth}, {flags: state.LsfRequireAuth, rate: 1_005_000_000}}
	lineFlags := []uint32{state.LsfLowFreeze, state.LsfHighFreeze, state.LsfLowDeepFreeze, state.LsfHighDeepFreeze, state.LsfLowAuth, state.LsfHighAuth}
	var out []iouScenario
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
					out = append(out, iouScenario{
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
