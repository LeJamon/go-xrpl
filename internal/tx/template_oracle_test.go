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

// rc1StagedTemplateTypes are the rippled 3.5.0-rc1 transaction types whose
// templates are staged on top of the pinned oracle. None of them has an
// MPT-supported field.
var rc1StagedTemplateTypes = map[Type]bool{
	TypeConfidentialMPTMirrorUpdate:    true,
	TypeConfidentialMPTHolderKeyUpdate: true,
	TypeTransactionProposalCancel:      true,
	TypeTransactionProposalCreate:      true,
}

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
		if rc1StagedTemplateTypes[Type(code)] {
			t.Errorf("staged rc1 transaction type %s already exists in the pinned oracle", m[2])
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
	for staged := range rc1StagedTemplateTypes {
		if _, ok := txTemplates[staged]; !ok {
			t.Errorf("staged rc1 transaction type %s has no template", staged)
		}
		if got := txTemplateMPTFields[staged]; len(got) != 0 {
			t.Errorf("staged rc1 transaction type %s MPT-supported fields = %v, want none", staged, got)
		}
	}
	if entries+len(rc1StagedTemplateTypes) != len(txTemplates) {
		t.Errorf("oracle declares %d transaction types and rc1 stages %d, templates %d", entries, len(rc1StagedTemplateTypes), len(txTemplates))
	}
}
