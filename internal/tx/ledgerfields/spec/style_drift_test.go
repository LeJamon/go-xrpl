package spec

import (
	"bufio"
	"bytes"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const rippledStyleTag = "3.2.0"
const rippledSponsorRef = "18e311e1e245bcc1813363bd1771b94931585d80"

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
	macroPath, ok := findRippledMacro()
	if !ok {
		t.Skip("rippled checkout not found; skip tagged style check")
	}
	rippledDir := filepath.Clean(filepath.Join(filepath.Dir(macroPath), "..", "..", "..", ".."))
	macro := gitShowTaggedFile(t, rippledDir, "include/xrpl/protocol/detail/ledger_entries.macro")
	tagged := parseTaggedStyles(t, macro)
	if len(tagged)+1 != len(Specs) {
		t.Fatalf("rippled %s has %d ledger templates, spec has %d (want the Sponsor delta only)", rippledStyleTag, len(tagged), len(Specs))
	}

	byEntry := make(map[string]Entry, len(Specs))
	for _, entry := range Specs {
		byEntry[entry.Name] = entry
	}
	for entryName, fields := range tagged {
		entry, ok := byEntry[entryName]
		if !ok {
			t.Errorf("rippled %s template %s is missing from spec", rippledStyleTag, entryName)
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
				t.Errorf("spec field %s.%s is absent from rippled %s", entryName, field.Name, rippledStyleTag)
			}
		}
	}

	common := gitShowTaggedFile(t, rippledDir, "src/libxrpl/protocol/LedgerFormats.cpp")
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

func TestSponsorSerializationStylesMatchRippledReference(t *testing.T) {
	macroPath, ok := findRippledMacro()
	if !ok {
		t.Skip("rippled checkout not found; skip Sponsor style check")
	}
	rippledDir := filepath.Clean(filepath.Join(filepath.Dir(macroPath), "..", "..", "..", ".."))
	macro := gitShowFileAtRef(t, rippledDir, rippledSponsorRef, "include/xrpl/protocol/detail/ledger_entries.macro")
	tagged := parseTaggedStyles(t, macro)

	byEntry := make(map[string]Entry, len(Specs))
	for _, entry := range Specs {
		byEntry[entry.Name] = entry
	}
	assertStyles := func(entryName string, want map[string]Style) {
		t.Helper()
		entry, ok := byEntry[entryName]
		if !ok {
			t.Fatalf("spec is missing %s", entryName)
		}
		allFields := entry.AllFields()
		got := make(map[string]Style, len(allFields))
		for _, field := range allFields {
			got[field.Name] = field.Style
		}
		for name, style := range want {
			if got[name] != style {
				t.Errorf("%s.%s style = %d, want %d", entryName, name, got[name], style)
			}
			if tagged[entryName][name] != style {
				t.Errorf("rippled %s %s.%s style = %d, want %d", rippledSponsorRef, entryName, name, tagged[entryName][name], style)
			}
		}
	}
	for entryName, additions := range sponsorStyleAdditions {
		assertStyles(entryName, additions)
	}
	assertStyles("Sponsorship", tagged["Sponsorship"])

	common := gitShowFileAtRef(t, rippledDir, rippledSponsorRef, "src/libxrpl/protocol/LedgerFormats.cpp")
	if !regexp.MustCompile(`\{sfSponsor,\s*(?:soeOPTIONAL|SoeOptional)\}`).Match(common) {
		t.Fatal("rippled Sponsor common ledger field is not optional")
	}
	if len(commonFields) != 1 || commonFields[0].Name != "Sponsor" || commonFields[0].Style != StyleOptional {
		t.Fatalf("common ledger fields = %#v, want optional Sponsor", commonFields)
	}
}

func gitShowTaggedFile(t *testing.T, repo, path string) []byte {
	return gitShowFileAtRef(t, repo, rippledStyleTag, path)
}

func gitShowFileAtRef(t *testing.T, repo, ref, path string) []byte {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "show", ref+":"+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git show rippled %s:%s: %v: %s", ref, path, err, out)
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
				entryName = match[1]
				out[entryName] = make(map[string]Style)
			}
			continue
		}
		if match := taggedStyleField.FindStringSubmatch(line); match != nil {
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
			out[entryName][match[1]] = style
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
