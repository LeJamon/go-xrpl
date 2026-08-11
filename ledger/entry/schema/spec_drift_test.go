package schema

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
)

// TestSpecCoversRippledMacro fails when an entry type in Specs is missing a
// field that rippled's ledger_entries.macro lists for the same type. Catches
// the class of bug where a new optional field is added in rippled (or to an
// existing amendment) and ships on the wire without a matching arm on the
// typed Decode path — which would surface as runtime ErrUnknownField.
func TestSpecCoversRippledMacro(t *testing.T) {
	macroPath := requireRippledMacro(t)

	rippled, err := parseRippledMacro(macroPath, v32SchemaFieldName)
	if err != nil {
		t.Fatalf("parse %s: %v", macroPath, err)
	}

	if len(rippled)+1 != len(Specs) {
		t.Fatalf("rippled v3.2.0 has %d ledger templates, schema has %d (want the Sponsorship delta only)", len(rippled), len(Specs))
	}
	haveEntries := make(map[string]bool, len(Specs))
	for _, entry := range Specs {
		haveEntries[entry.Name] = true
		if entry.Name == "Sponsorship" {
			info, found := protocol.LedgerEntryTypeByName(entry.Name)
			if !found || info.Type != protocol.LedgerEntryTypeSponsorship || info.RPCName != "sponsorship" || info.Deprecated {
				t.Errorf("Sponsorship registry entry is missing or invalid")
			}
			continue
		}
		rEntry, found := rippled[entry.Name]
		if !found {
			t.Errorf("entry %q is absent from rippled", entry.Name)
			continue
		}
		info, found := protocol.LedgerEntryTypeByName(entry.Name)
		if !found || info.Deprecated {
			t.Errorf("entry %q is absent from the canonical registry", entry.Name)
			continue
		}
		if info.Type != rEntry.Type {
			t.Errorf("entry %q: registry code = %#04x, rippled code = %#04x", entry.Name, info.Type, rEntry.Type)
		}
		if info.RPCName != rEntry.RPCName {
			t.Errorf("entry %q: registry RPC selector = %q, rippled RPC selector = %q", entry.Name, info.RPCName, rEntry.RPCName)
		}

		have := make(map[string]bool, len(entry.Fields))
		for _, f := range entry.Fields {
			have[f.Name] = true
		}

		for _, rf := range rEntry.Fields {
			if !have[rf] {
				t.Errorf("entry %q: rippled lists field %q, schema.go does not", entry.Name, rf)
			}
		}
	}
	for name := range rippled {
		if !haveEntries[name] {
			t.Errorf("rippled entry %q is absent from schema", name)
		}
	}
	canonical := 0
	for _, info := range protocol.LedgerEntryTypes() {
		if info.Deprecated {
			continue
		}
		canonical++
		if info.Name == "Sponsorship" {
			continue
		}
		rEntry, found := rippled[info.Name]
		if !found {
			t.Errorf("registry entry %q is absent from rippled", info.Name)
			continue
		}
		if rEntry.Type != info.Type || rEntry.RPCName != info.RPCName {
			t.Errorf("registry entry %q does not match rippled", info.Name)
		}
	}
	if canonical != len(rippled)+1 {
		t.Errorf("registry has %d canonical entries, rippled v3.2.0 has %d plus Sponsorship", canonical, len(rippled))
	}
}

func TestDynamicMPTSchemaMatchesRippledV3_3(t *testing.T) {
	macroPath := requireRippledMacroVersion(t, "v3.3.0-oracle")
	rippled, err := parseRippledMacro(macroPath, nil)
	if err != nil {
		t.Fatalf("parse %s: %v", macroPath, err)
	}
	issuance, ok := rippled["MPTokenIssuance"]
	if !ok {
		t.Fatal("rippled v3.3.0 has no MPTokenIssuance template")
	}
	wantSequence := []string{"DomainID", "ImmutableFlags", "ReferenceHolding"}
	containsSequence := func(fields []string) bool {
		for i := 0; i+len(wantSequence) <= len(fields); i++ {
			if slices.Equal(fields[i:i+len(wantSequence)], wantSequence) {
				return true
			}
		}
		return false
	}
	if !containsSequence(issuance.Fields) {
		t.Fatalf("rippled v3.3.0 MPTokenIssuance fields do not contain %v in order", wantSequence)
	}
	macro, err := os.ReadFile(macroPath)
	if err != nil {
		t.Fatalf("read %s: %v", macroPath, err)
	}
	styles := parseTaggedStyles(t, macro)
	if got := styles["MPTokenIssuance"]["ImmutableFlags"]; got != StyleDefault {
		t.Fatalf("rippled v3.3.0 ImmutableFlags style = %d, want default", got)
	}

	for _, entry := range Specs {
		if entry.Name != "MPTokenIssuance" {
			continue
		}
		fields := make([]string, len(entry.Fields))
		for i, field := range entry.Fields {
			fields[i] = field.Name
			if field.Name == "ImmutableFlags" && field.Style != StyleDefault {
				t.Fatalf("Go ImmutableFlags style = %d, want default", field.Style)
			}
		}
		if !containsSequence(fields) {
			t.Fatalf("Go MPTokenIssuance fields do not contain %v in order", wantSequence)
		}
		return
	}
	t.Fatal("Go schema has no MPTokenIssuance template")
}

func requireRippledMacro(t *testing.T) string {
	return requireRippledMacroVersion(t, "v3.2.0-oracle")
}

func requireRippledMacroVersion(t *testing.T, oracle string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve schema test source path")
	}
	dir := filepath.Dir(file)
	for range 12 {
		candidate := filepath.Join(dir, "rippled-worktrees", oracle, "include", "xrpl", "protocol", "detail", "ledger_entries.macro")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("required rippled %s not found from %s", oracle, file)
	return ""
}

var (
	macroEntryStart = regexp.MustCompile(`^LEDGER_ENTRY(?:_DUPLICATE)?\(\s*lt\w+\s*,\s*(0x[0-9a-fA-F]+)\s*,\s*(\w+)\s*,\s*(\w+)\s*,`)
	macroFieldLine  = regexp.MustCompile(`^\s*\{\s*sf(\w+)\s*,`)
)

func v32SchemaFieldName(name string) string {
	if name == "MutableFlags" {
		return "ImmutableFlags"
	}
	return name
}

type rippledEntry struct {
	Type    protocol.LedgerEntryType
	RPCName string
	Fields  []string
}

// parseRippledMacro returns the canonical identity and fields for every
// ledger-entry type in rippled's macro.
func parseRippledMacro(path string, normalizeFieldName func(string) string) (map[string]rippledEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make(map[string]rippledEntry)
	var currentName string
	var currentType protocol.LedgerEntryType
	var currentRPCName string
	var currentFields []string
	inBlock := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !inBlock {
			if m := macroEntryStart.FindStringSubmatch(line); m != nil {
				code, err := strconv.ParseUint(m[1], 0, 16)
				if err != nil {
					return nil, err
				}
				currentType = protocol.LedgerEntryType(code)
				currentName = m[2]
				currentRPCName = m[3]
				currentFields = currentFields[:0]
				inBlock = true
			}
			continue
		}
		if m := macroFieldLine.FindStringSubmatch(line); m != nil {
			name := m[1]
			if normalizeFieldName != nil {
				name = normalizeFieldName(name)
			}
			currentFields = append(currentFields, name)
			continue
		}
		if strings.Contains(line, "}))") {
			out[currentName] = rippledEntry{
				Type:    currentType,
				RPCName: currentRPCName,
				Fields:  append([]string(nil), currentFields...),
			}
			currentName = ""
			currentType = 0
			currentRPCName = ""
			currentFields = currentFields[:0]
			inBlock = false
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
