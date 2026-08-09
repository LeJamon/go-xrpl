package schema

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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

	rippled, err := parseRippledMacro(macroPath)
	if err != nil {
		t.Fatalf("parse %s: %v", macroPath, err)
	}

	if len(rippled) != len(Specs) {
		t.Fatalf("rippled v3.3.0 has %d ledger templates, schema has %d", len(rippled), len(Specs))
	}
	haveEntries := make(map[string]bool, len(Specs))
	for _, entry := range Specs {
		haveEntries[entry.Name] = true
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
		rEntry, found := rippled[info.Name]
		if !found {
			t.Errorf("registry entry %q is absent from rippled", info.Name)
			continue
		}
		if rEntry.Type != info.Type || rEntry.RPCName != info.RPCName {
			t.Errorf("registry entry %q does not match rippled", info.Name)
		}
	}
	if canonical != len(rippled) {
		t.Errorf("registry has %d canonical entries, rippled v3.3.0 has %d", canonical, len(rippled))
	}
}

func requireRippledMacro(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve schema test source path")
	}
	dir := filepath.Dir(file)
	for range 12 {
		candidate := filepath.Join(dir, "rippled-worktrees", "v3.3.0-oracle", "include", "xrpl", "protocol", "detail", "ledger_entries.macro")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("required rippled v3.3.0 oracle not found from %s", file)
	return ""
}

var (
	macroEntryStart = regexp.MustCompile(`^LEDGER_ENTRY(?:_DUPLICATE)?\(\s*lt\w+\s*,\s*(0x[0-9a-fA-F]+)\s*,\s*(\w+)\s*,\s*(\w+)\s*,`)
	macroFieldLine  = regexp.MustCompile(`^\s*\{\s*sf(\w+)\s*,`)
)

type rippledEntry struct {
	Type    protocol.LedgerEntryType
	RPCName string
	Fields  []string
}

// parseRippledMacro returns the canonical identity and fields for every
// ledger-entry type in rippled's macro.
func parseRippledMacro(path string) (map[string]rippledEntry, error) {
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
			currentFields = append(currentFields, m[1])
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
