package tx

import (
	"maps"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/testutil/rippled"
)

// TestTemplateMPTSupportMatchesOracle pins the per-field SoeMptSupported
// marks of every transaction template against transactions.macro.
func TestTemplateMPTSupportMatchesOracle(t *testing.T) {
	data, err := os.ReadFile(rippled.File(t, "include/xrpl/protocol/detail/transactions.macro"))
	if err != nil {
		t.Fatal(err)
	}
	header := regexp.MustCompile(`^\w+,\s*(\d+),\s*(\w+),`)
	mptField := regexp.MustCompile(`\{sf(\w+),[^}]*SoeMptSupported`)
	entries := 0
	for _, chunk := range strings.Split(string(data), "TRANSACTION(") {
		m := header.FindStringSubmatch(chunk)
		if m == nil {
			continue
		}
		entries++
		code, err := strconv.ParseUint(m[1], 10, 16)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for _, f := range mptField.FindAllStringSubmatch(chunk, -1) {
			want[f[1]] = true
		}
		got := txTemplateMPTFields[Type(code)]
		if got == nil {
			got = map[string]bool{}
		}
		if !maps.Equal(got, want) {
			t.Errorf("%s MPT-supported fields = %v, oracle %v", m[2], got, want)
		}
	}
	if entries != len(txTemplates) {
		t.Errorf("oracle declares %d transaction types, templates %d", entries, len(txTemplates))
	}
}
