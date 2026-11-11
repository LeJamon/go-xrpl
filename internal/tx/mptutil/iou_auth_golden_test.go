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
		for _, sc := range iouDiffScenarios(t) {
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
