package spec

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestEmitFinalFieldsSubsetOfEmitPreviousFields pins the assumption the
// empty-PreviousFields heuristic in internal/tx/apply_state_table.go relies
// on: for every spec.Field whose Meta is MetaDefault, the generated
// EmitFinalFields and EmitPreviousFields code paths in <entry>_gen.go MUST
// both reference the field by name. The heuristic detects rippled's
// STI_NOTPRESENT-in-prevs emission as "name in cur.FinalFields but not in
// orig.FinalFields" and assumes that any such field would also have been
// emplaced into rippled's prevs object (i.e. carries sMD_ChangeOrig).
//
// If a future spec entry declares a MetaDefault field that the codegen
// drops from one of the two functions, the heuristic would either over-fire
// (emitting an empty PreviousFields wrapper rippled does not emit) or
// under-fire (missing the wrapper rippled does emit). Either way the
// tx-tree leaf bytes diverge from rippled.
//
// MetaAlways fields (e.g. RootIndex) and MetaDeleteFinal fields (e.g.
// PreviousTxnID, PreviousTxnLgrSeq) are excluded from the assertion —
// rippled's prevs loop does not emit them either, and they are kept out
// of the heuristic by the existing implementation. MetaNever fields are
// skipped entirely.
func TestEmitFinalFieldsSubsetOfEmitPreviousFields(t *testing.T) {
	genDir, ok := findLedgerfieldsDir()
	if !ok {
		t.Skip("ledgerfields generated dir not found")
	}

	for _, entry := range Specs {
		t.Run(entry.Name, func(t *testing.T) {
			path := filepath.Join(genDir, fileNameForEntry(entry.Name))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}

			emitAllFields := extractEmitAllFields(string(data))
			emitPrevFields := extractEmitPreviousFields(string(data))

			for _, f := range entry.Fields {
				if f.Meta != MetaDefault {
					continue
				}
				if !emitAllFields[f.Name] {
					t.Errorf("%s.emitAll does not write field %q — codegen drift breaks the empty-PreviousFields heuristic",
						entry.Name, f.Name)
				}
				if !emitPrevFields[f.Name] {
					t.Errorf("%s.EmitPreviousFields does not handle field %q — codegen drift breaks the empty-PreviousFields heuristic",
						entry.Name, f.Name)
				}
			}
		})
	}
}

var (
	emitAllLine     = regexp.MustCompile(`out\["([A-Za-z][A-Za-z0-9]*)"\]\s*=`)
	emitChangedLine = regexp.MustCompile(`emitIfChanged[A-Za-z0-9]*\(\s*out\s*,\s*"([A-Za-z][A-Za-z0-9]*)"\s*,`)
)

func extractEmitAllFields(src string) map[string]bool {
	out := make(map[string]bool)
	body, ok := functionBody(src, "emitAll(out map[string]any, skipDefault bool)")
	if !ok {
		return out
	}
	for _, m := range emitAllLine.FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out
}

func extractEmitPreviousFields(src string) map[string]bool {
	out := make(map[string]bool)
	body, ok := functionBody(src, "EmitPreviousFields(prev Entry, out map[string]any)")
	if !ok {
		return out
	}
	for _, m := range emitChangedLine.FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out
}

// functionBody returns the substring spanning a function whose signature
// contains the given marker. We do not need a real Go parser — the codegen
// output is uniform.
func functionBody(src, marker string) (string, bool) {
	idx := strings.Index(src, marker)
	if idx < 0 {
		return "", false
	}
	open := strings.IndexByte(src[idx:], '{')
	if open < 0 {
		return "", false
	}
	start := idx + open + 1
	depth := 1
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start:i], true
			}
		}
	}
	return "", false
}

// fileNameForEntry mirrors the snake() helper in cmd/ledgerfieldsgen:
// runs of consecutive uppercase letters are treated as one acronym, so
// "NFTokenOffer" → "nf_token_offer" and "XChainOwnedClaimID" →
// "x_chain_owned_claim_id".
func fileNameForEntry(name string) string {
	bytes := []byte(name)
	var b strings.Builder
	for i, c := range bytes {
		isUpper := c >= 'A' && c <= 'Z'
		if i > 0 && isUpper {
			prev := bytes[i-1]
			var next byte
			if i+1 < len(bytes) {
				next = bytes[i+1]
			}
			prevLower := prev >= 'a' && prev <= 'z'
			prevUpper := prev >= 'A' && prev <= 'Z'
			nextLower := next >= 'a' && next <= 'z'
			if prevLower || (prevUpper && nextLower) {
				b.WriteByte('_')
			}
		}
		if isUpper {
			b.WriteByte(c + ('a' - 'A'))
		} else {
			b.WriteByte(c)
		}
	}
	b.WriteString("_gen.go")
	return b.String()
}

// findLedgerfieldsDir returns the directory containing the codegen output
// (the parent of this test file).
func findLedgerfieldsDir() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	parent := filepath.Dir(filepath.Dir(file))
	if _, err := os.Stat(filepath.Join(parent, "ledgerfields.go")); err != nil {
		return "", false
	}
	return parent, true
}
