package tx_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/testutil/rippled"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
)

type oracleTxSettings struct {
	name       string
	code       tx.Type
	delegable  bool
	amendment  string
	privileges []string
}

var (
	oracleTxPattern        = regexp.MustCompile(`(?s)TRANSACTION\((\w+),\s*(\d+),\s*(\w+),\s*\(\{(.*?)\}\)`)
	oracleLineComment      = regexp.MustCompile(`//[^\n]*`)
	oracleAmendmentPattern = regexp.MustCompile(`\.amendment\s*=\s*(\w+)`)
	oraclePrivilegePattern = regexp.MustCompile(`\.privileges\s*=\s*([^,}]+)`)
)

func readOracleTxSettings(t *testing.T) []oracleTxSettings {
	t.Helper()
	data, err := os.ReadFile(rippled.File(t, "include/xrpl/protocol/detail/transactions.macro"))
	if err != nil {
		t.Fatal(err)
	}
	var out []oracleTxSettings
	for _, m := range oracleTxPattern.FindAllStringSubmatch(string(data), -1) {
		code, err := strconv.ParseUint(m[2], 10, 16)
		if err != nil {
			t.Fatalf("parse code for %s: %v", m[3], err)
		}
		settings := oracleLineComment.ReplaceAllString(m[4], "")
		row := oracleTxSettings{
			name:      m[3],
			code:      tx.Type(code),
			delegable: strings.Contains(settings, "Delegation::Delegable"),
		}
		if a := oracleAmendmentPattern.FindStringSubmatch(settings); a != nil {
			row.amendment = a[1]
		}
		if p := oraclePrivilegePattern.FindStringSubmatch(settings); p != nil {
			for _, part := range strings.Split(p[1], "|") {
				row.privileges = append(row.privileges, strings.TrimPrefix(strings.TrimSpace(part), "Privilege::"))
			}
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		t.Fatal("no TRANSACTION entries parsed from transactions.macro")
	}
	return out
}

func oracleAmendmentID(t *testing.T, name string) [32]byte {
	t.Helper()
	switch {
	case strings.HasPrefix(name, "feature"):
		return amendment.FeatureID(strings.TrimPrefix(name, "feature"))
	case strings.HasPrefix(name, "fix"):
		return amendment.FeatureID(name)
	}
	t.Fatalf("unrecognised amendment identifier %q", name)
	return [32]byte{}
}

// TestDescriptorsMatchOracleTxSettings checks every registered transaction
// type's identity and per-type settings against rippled's transactions.macro.
func TestDescriptorsMatchOracleTxSettings(t *testing.T) {
	all.RegisterAll()
	rows := readOracleTxSettings(t)
	if got := len(tx.SupportedTypes()); got != len(rows) {
		t.Errorf("registered %d transaction types, oracle declares %d", got, len(rows))
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if got := row.code.String(); got != row.name {
				t.Fatalf("type %d name = %q, oracle %q", row.code, got, row.name)
			}
			instance, err := tx.NewFromType(row.code)
			if err != nil {
				t.Fatalf("type %s is not registered: %v", row.name, err)
			}
			if got := tx.IsTransactionDelegable(row.code); got != row.delegable {
				t.Errorf("delegable = %t, oracle %t", got, row.delegable)
			}
			// Retired amendments are permanently enabled, so gating on one is
			// equivalent to the oracle's "no amendment".
			var required [][32]byte
			for _, id := range instance.RequiredAmendments() {
				if f := amendment.FeatureByID(id); f == nil || !f.Retired {
					required = append(required, id)
				}
			}
			switch {
			case row.amendment == "" && len(required) != 0:
				t.Errorf("requires %x, oracle declares no amendment", required[0])
			case row.amendment != "" && len(required) == 0:
				t.Errorf("requires no amendment, oracle declares %s", row.amendment)
			case row.amendment != "" && required[0] != oracleAmendmentID(t, row.amendment):
				t.Errorf("first required amendment %x, oracle declares %s", required[0], row.amendment)
			}
		})
	}
}
