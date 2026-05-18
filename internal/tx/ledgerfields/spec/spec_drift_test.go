package spec

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestSpecCoversRippledMacro fails when an entry type in Specs is missing a
// field that rippled's ledger_entries.macro lists for the same type. Catches
// the class of bug where a new optional field is added in rippled (or to an
// existing amendment) and ships on the wire without a matching arm on the
// typed Decode path — which would surface as runtime ErrUnknownField rather
// than a build-time failure.
//
// The macro is in the rippled repo sitting next to the goXRPL checkout. If it
// can't be located the test skips: this protects local devs without rippled
// checked out, while CI (where rippled is present) still enforces.
func TestSpecCoversRippledMacro(t *testing.T) {
	macroPath, ok := findRippledMacro()
	if !ok {
		t.Skip("rippled ledger_entries.macro not found; skip drift check")
	}

	rippled, err := parseRippledMacro(macroPath)
	if err != nil {
		t.Fatalf("parse %s: %v", macroPath, err)
	}

	for _, entry := range Specs {
		rFields, found := rippled[entry.Name]
		if !found {
			t.Errorf("entry %q present in spec.Specs but absent from rippled macro", entry.Name)
			continue
		}

		have := make(map[string]bool, len(entry.Fields))
		for _, f := range entry.Fields {
			have[f.Name] = true
		}

		for _, rf := range rFields {
			if !have[rf] {
				t.Errorf("entry %q: rippled lists field %q, spec.go does not", entry.Name, rf)
			}
		}
	}
}

func findRippledMacro() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	dir := filepath.Dir(file)
	for i := 0; i < 12; i++ {
		candidate := filepath.Join(dir, "rippled", "include", "xrpl", "protocol", "detail", "ledger_entries.macro")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

var (
	macroEntryStart = regexp.MustCompile(`^LEDGER_ENTRY(?:_DUPLICATE)?\(\s*lt\w+\s*,\s*0x[0-9a-fA-F]+\s*,\s*(\w+)\s*,`)
	macroFieldLine  = regexp.MustCompile(`^\s*\{\s*sf(\w+)\s*,`)
)

// parseRippledMacro returns a map from ledger-entry-type name to the list of
// field names rippled's macro carries for it. The macro's grammar is regular
// enough that a tiny line scanner handles it without pulling in a C parser.
func parseRippledMacro(path string) (map[string][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make(map[string][]string)
	var currentName string
	var currentFields []string
	inBlock := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !inBlock {
			if m := macroEntryStart.FindStringSubmatch(line); m != nil {
				currentName = m[1]
				currentFields = currentFields[:0]
				inBlock = true
			}
			continue
		}
		if m := macroFieldLine.FindStringSubmatch(line); m != nil {
			currentFields = append(currentFields, m[1])
			continue
		}
		if strings.Contains(line, "}))") {
			out[currentName] = append([]string(nil), currentFields...)
			currentName = ""
			currentFields = currentFields[:0]
			inBlock = false
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
