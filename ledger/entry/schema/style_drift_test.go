package schema

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var sponsorStyleAdditions = map[string]map[string]Style{
	"AccountRoot": {
		"SponsoredOwnerCount":    StyleDefault,
		"SponsoringOwnerCount":   StyleDefault,
		"SponsoringAccountCount": StyleDefault,
	},
	"RippleState": {
		"HighSponsor": StyleOptional,
		"LowSponsor":  StyleOptional,
	},
}
var taggedStyleField = regexp.MustCompile(`^\s*\{\s*sf(\w+)\s*,\s*(?:soe|Soe)(REQUIRED|OPTIONAL|DEFAULT|Required|Optional|Default)\s*\}`)

func TestSponsorCommonFieldAppliedToEveryEntry(t *testing.T) {
	for _, entry := range Specs {
		count := 0
		for _, field := range entry.AllFields() {
			if field.Name != "Sponsor" {
				continue
			}
			count++
			if field.Style != StyleOptional {
				t.Errorf("%s.Sponsor style = %d, want optional", entry.Name, field.Style)
			}
		}
		if count != 1 {
			t.Errorf("%s has %d Sponsor fields, want 1", entry.Name, count)
		}
	}
}

func TestSerializationStylesMatchRippledTag(t *testing.T) {
	macroPath := requireRippledMacro(t)
	rippledDir := filepath.Clean(filepath.Join(filepath.Dir(macroPath), "..", "..", "..", ".."))
	macro := readRippledFile(t, rippledDir, "include/xrpl/protocol/detail/ledger_entries.macro")
	tagged := parseTaggedStyles(t, macro)
	if len(tagged)+1 != len(Specs) {
		t.Fatalf("rippled v3.2.0 has %d ledger templates, schema has %d (want the Sponsorship delta only)", len(tagged), len(Specs))
	}

	byEntry := make(map[string]Entry, len(Specs))
	for _, entry := range Specs {
		byEntry[entry.Name] = entry
	}
	for entryName, fields := range tagged {
		entry, ok := byEntry[entryName]
		if !ok {
			t.Errorf("rippled v3.2.0 template %s is missing from schema", entryName)
			continue
		}
		specFields := make(map[string]Field, len(entry.Fields))
		for _, field := range entry.Fields {
			specFields[field.Name] = field
		}
		for fieldName, want := range fields {
			got, ok := specFields[fieldName]
			if !ok {
				t.Errorf("%s.%s is missing from spec", entryName, fieldName)
				continue
			}
			if got.Style != want {
				t.Errorf("%s.%s style = %d, want %d", entryName, fieldName, got.Style, want)
			}
			wantDeferred := want == StyleRequired && (fieldName == "PreviousTxnID" || fieldName == "PreviousTxnLgrSeq")
			if got.DeferredRequired != wantDeferred {
				t.Errorf("%s.%s DeferredRequired = %v, want %v", entryName, fieldName, got.DeferredRequired, wantDeferred)
			}
		}
		for _, field := range entry.Fields {
			if field.Name == "Flags" || field.DecodeOnly {
				continue
			}
			if additions := sponsorStyleAdditions[entryName]; additions != nil {
				if _, ok := additions[field.Name]; ok {
					continue
				}
			}
			if _, ok := fields[field.Name]; !ok {
				t.Errorf("schema field %s.%s is absent from rippled v3.2.0", entryName, field.Name)
			}
		}
	}

	common := readRippledFile(t, rippledDir, "src/libxrpl/protocol/LedgerFormats.cpp")
	if !regexp.MustCompile(`\{sfLedgerEntryType,\s*(?:soeREQUIRED|SoeRequired)\}`).Match(common) {
		t.Fatal("rippled common LedgerEntryType field is not required")
	}
	if !regexp.MustCompile(`\{sfFlags,\s*(?:soeREQUIRED|SoeRequired)\}`).Match(common) {
		t.Fatal("rippled common Flags field is not required")
	}
	for _, entry := range Specs {
		found := false
		for _, field := range entry.Fields {
			if field.Name == "Flags" {
				found = true
				if field.Style != StyleRequired {
					t.Errorf("%s.Flags style = %d, want required", entry.Name, field.Style)
				}
			}
		}
		if !found {
			t.Errorf("%s does not declare common Flags", entry.Name)
		}
	}
}

func readRippledFile(t *testing.T, repo, path string) []byte {
	t.Helper()
	out, err := os.ReadFile(filepath.Join(repo, path))
	if err != nil {
		t.Fatalf("read rippled v3.2.0 %s: %v", path, err)
	}
	return out
}

func parseTaggedStyles(t *testing.T, data []byte) map[string]map[string]Style {
	t.Helper()
	out := make(map[string]map[string]Style)
	var entryName string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if entryName == "" {
			if match := macroEntryStart.FindStringSubmatch(line); match != nil {
				entryName = match[2]
				out[entryName] = make(map[string]Style)
			}
			continue
		}
		if match := taggedStyleField.FindStringSubmatch(line); match != nil {
			fieldName := v32SchemaFieldName(match[1])
			var style Style
			switch strings.ToLower(match[2]) {
			case "required":
				style = StyleRequired
			case "optional":
				style = StyleOptional
			case "default":
				style = StyleDefault
			default:
				t.Fatalf("unknown style %q", match[2])
			}
			out[entryName][fieldName] = style
			continue
		}
		if strings.Contains(line, "}))") {
			entryName = ""
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan tagged macro: %v", err)
	}
	return out
}
