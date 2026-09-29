package conformance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func copyPinnedCorpus(t *testing.T) (string, pinnedManifest) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(conformanceRepositoryRoot(), "internal/testing/conformance/testdata/rippled-3.4.1-v4")
	if err := os.CopyFS(root, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, corpusManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest pinnedManifest
	if err := decodeStrictJSON(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return root, manifest
}

func writePinnedManifest(t *testing.T, root string, manifest pinnedManifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, corpusManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func firstPinnedFixture(manifest pinnedManifest) string {
	names := make([]string, 0, len(manifest.Fixtures))
	for name := range manifest.Fixtures {
		names = append(names, name)
	}
	sort.Strings(names)
	return names[0]
}

func TestPinnedCorpusAcceptsRecordedCorpus(t *testing.T) {
	root, manifest := copyPinnedCorpus(t)
	corpus, err := loadPinnedCorpus(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) != manifest.FixtureCount || len(corpus.Cases) == 0 {
		t.Fatalf("case count=%d, manifest=%d", len(corpus.Cases), manifest.FixtureCount)
	}
}

func TestPinnedCorpusResolvesRelativeSelectionFromRepository(t *testing.T) {
	t.Setenv("GOXRPL_FIXTURES_DIR", "internal/testing/conformance/testdata/rippled-3.4.1-v4")
	t.Chdir(t.TempDir())
	corpus, err := resolvePinnedCorpus(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("selected corpus has no cases")
	}
}

func TestPinnedCorpusAcceptsExplicitUnsupportedProfile(t *testing.T) {
	root, manifest := copyPinnedCorpus(t)
	const id = "c0-l0-b0-f1"
	for i := range manifest.AmendmentMatrix {
		if manifest.AmendmentMatrix[i].ID == id {
			no := false
			manifest.AmendmentMatrix[i].Supported = &no
			manifest.AmendmentMatrix[i].UnsupportedReason = "negative-control corpus deliberately omits this optional combination"
		}
	}
	for name, pin := range manifest.Fixtures {
		if pin.Profile == id {
			if err := os.Remove(filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			delete(manifest.Fixtures, name)
		}
	}
	manifest.FixtureCount = len(manifest.Fixtures)
	writePinnedManifest(t, root, manifest)
	if _, err := loadPinnedCorpus(root); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedCorpusNegativeControls(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*testing.T, string, *pinnedManifest)
		want   string
	}{
		{"old format", func(_ *testing.T, _ string, m *pinnedManifest) { m.FixtureVersion = "v3" }, "required corpus format"},
		{"public repository", func(_ *testing.T, _ string, m *pinnedManifest) { m.OracleRepository = "XRPLF/rippled" }, "required oracle"},
		{"stale version", func(_ *testing.T, _ string, m *pinnedManifest) { m.RippledTag = "3.4.0" }, "required oracle"},
		{"stale commit", func(_ *testing.T, _ string, m *pinnedManifest) { m.RippledCommit = legacyRippledCommit }, "required oracle"},
		{"missing recorder commit", func(_ *testing.T, _ string, m *pinnedManifest) { m.RecorderCommit = "" }, "recorder_commit"},
		{"missing binary", func(_ *testing.T, _ string, m *pinnedManifest) { m.BinarySHA256 = "" }, "binary_sha256"},
		{"missing config", func(_ *testing.T, _ string, m *pinnedManifest) { m.ConfigIdentity = "" }, "config_identity"},
		{"missing recorder source", func(_ *testing.T, _ string, m *pinnedManifest) { m.RecorderSources = nil }, "recorder_sources"},
		{"incomplete recorder source", func(_ *testing.T, _ string, m *pinnedManifest) {
			delete(m.RecorderSources, "scripts/oracle/strict_recorder.cpp")
		}, "recorder_sources"},
		{"tampered recorder source", func(_ *testing.T, _ string, m *pinnedManifest) {
			for name := range m.RecorderSources {
				if name != m.ConfigSource {
					m.RecorderSources[name] = strings.Repeat("a", 64)
				}
			}
		}, "SHA-256 mismatch"},
		{"empty corpus", func(_ *testing.T, _ string, m *pinnedManifest) {
			m.FixtureCount = 0
			m.Fixtures = map[string]pinnedFixturePin{}
		}, "empty"},
		{"wrong count", func(_ *testing.T, _ string, m *pinnedManifest) { m.FixtureCount++ }, "fixture_count"},
		{"incomplete matrix", func(_ *testing.T, _ string, m *pinnedManifest) { m.AmendmentMatrix = m.AmendmentMatrix[:1] }, "all 16"},
		{"duplicate matrix", func(_ *testing.T, _ string, m *pinnedManifest) { m.AmendmentMatrix[1] = m.AmendmentMatrix[0] }, "duplicate"},
		{"missing amendment state", func(_ *testing.T, _ string, m *pinnedManifest) { m.AmendmentMatrix[0].FixBatchV1_2 = nil }, "fixBatchV1_2 must not be null"},
		{"required profile excluded", func(_ *testing.T, _ string, m *pinnedManifest) {
			for i := range m.AmendmentMatrix {
				if *m.AmendmentMatrix[i].BatchV1_1 {
					no := false
					m.AmendmentMatrix[i].Supported = &no
					m.AmendmentMatrix[i].UnsupportedReason = "test"
					break
				}
			}
		}, "must execute"},
		{"missing fixture", func(t *testing.T, root string, m *pinnedManifest) {
			if err := os.Remove(filepath.Join(root, firstPinnedFixture(*m))); err != nil {
				t.Fatal(err)
			}
		}, "fixture count"},
		{"unreadable fixture", func(t *testing.T, root string, m *pinnedManifest) {
			if err := os.Chmod(filepath.Join(root, firstPinnedFixture(*m)), 0); err != nil {
				t.Fatal(err)
			}
		}, "readable regular file"},
		{"symlink fixture", func(t *testing.T, root string, m *pinnedManifest) {
			path := filepath.Join(root, firstPinnedFixture(*m))
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, corpusManifestName), path); err != nil {
				t.Fatal(err)
			}
		}, "symlink"},
		{"unlisted fixture", func(t *testing.T, root string, _ *pinnedManifest) {
			if err := os.WriteFile(filepath.Join(root, "extra.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "no manifest entry"},
		{"byte mismatch", func(t *testing.T, root string, m *pinnedManifest) {
			if err := os.WriteFile(filepath.Join(root, firstPinnedFixture(*m)), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "SHA-256 mismatch"},
		{"wholly excluded", func(_ *testing.T, _ string, m *pinnedManifest) {
			for name, pin := range m.Fixtures {
				pin.ExcludeReason = "negative control"
				m.Fixtures[name] = pin
			}
		}, "no in-scope"},
		{"profile zero executed", func(_ *testing.T, _ string, m *pinnedManifest) {
			profile := m.Fixtures[firstPinnedFixture(*m)].Profile
			for name, pin := range m.Fixtures {
				if pin.Profile == profile {
					pin.ExcludeReason = "negative control"
					m.Fixtures[name] = pin
				}
			}
		}, "no executable fixtures"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, manifest := copyPinnedCorpus(t)
			test.modify(t, root, &manifest)
			writePinnedManifest(t, root, manifest)
			_, err := loadPinnedCorpus(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestPinnedCorpusRejectsMalformedFixtureAfterChecksum(t *testing.T) {
	for _, test := range []struct {
		name, field string
		value       any
		want        string
	}{
		{"unknown field", "silently_ignored", true, "unknown field"},
		{"dependency", "depends_on", "missing.json", "unknown field"},
		{"unknown step", "steps", []any{map[string]any{"op": "invent_state"}}, "unknown field"},
		{"mixed repository", "oracle_repository", "XRPLF/rippled", "oracle"},
		{"mixed tag", "oracle_tag", "3.4.0", "oracle"},
		{"mixed commit", "oracle_commit", legacyRippledCommit, "oracle"},
		{"wrong family", "family", "UnknownTransaction", "family"},
		{"wrong profile", "profile", "unknown", "profile"},
		{"unknown amendment", "parent.rules", []string{strings.Repeat("f", 64)}, "unknown amendment"},
		{"malformed header", "parent.header", "00", "header"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, manifest := copyPinnedCorpus(t)
			name := firstPinnedFixture(manifest)
			path := filepath.Join(root, name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fixture map[string]any
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			fields := strings.Split(test.field, ".")
			object := fixture
			for _, field := range fields[:len(fields)-1] {
				object = object[field].(map[string]any)
			}
			object[fields[len(fields)-1]] = test.value
			data, err = json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			pin := manifest.Fixtures[name]
			pin.SHA256 = sha256Hex(data)
			manifest.Fixtures[name] = pin
			writePinnedManifest(t, root, manifest)
			if _, err := loadPinnedCorpus(root); err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestPinnedCorpusRejectsMalformedExcludedResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*snapshotSubmit)
		want   string
	}{
		{"unknown TER", func(s *snapshotSubmit) { s.EngineResult, s.EngineResultCode = "-", 9999 }, "unknown submit TER"},
		{"unapplied success", func(s *snapshotSubmit) { s.Applied = false }, "submit.applied"},
		{"applied malformed", func(s *snapshotSubmit) {
			s.EngineResult, s.EngineResultCode = ter.TemMALFORMED.String(), int(ter.TemMALFORMED)
		}, "submit.applied"},
		{"unapplied fee", func(s *snapshotSubmit) {
			s.EngineResult, s.EngineResultCode = ter.TemMALFORMED.String(), int(ter.TemMALFORMED)
			s.Applied, s.Fee = false, 1
		}, "submit.fee"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, manifest := copyPinnedCorpus(t)
			name := "c0-l0-b1-f1-Batch-canonical.json"
			path := filepath.Join(root, name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			fixture, err := decodeSnapshotFixture(data)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&fixture.Submit)
			data, err = json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			pin := manifest.Fixtures[name]
			pin.SHA256 = sha256Hex(data)
			pin.ExcludeReason = "explicit exclusion must not hide malformed results"
			manifest.Fixtures[name] = pin
			writePinnedManifest(t, root, manifest)
			if _, err := loadPinnedCorpus(root); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestPinnedStrictJSONRejectsDuplicates(t *testing.T) {
	for _, data := range []string{`{"schema":3,"schema":2}`, `{"a":[{"applied":true,"applied":false}]}`} {
		var decoded any
		if err := decodeStrictJSON([]byte(data), &decoded); err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
			t.Fatalf("accepted ambiguous JSON: %s: %v", data, err)
		}
	}
}

func TestPinnedManifestRejectsAliasedAndNullFields(t *testing.T) {
	for _, change := range []struct {
		field string
		value any
		want  string
	}{{"SCHEMA", 3, "unknown field"}, {"binary_sha256", nil, "must not be null"}} {
		t.Run(change.field, func(t *testing.T) {
			root, _ := copyPinnedCorpus(t)
			path := filepath.Join(root, corpusManifestName)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest[change.field] = change.value
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadPinnedCorpus(root); err == nil || !strings.Contains(err.Error(), change.want) {
				t.Fatalf("error=%v, want %q", err, change.want)
			}
		})
	}
}

func TestPinnedRequiredEntrypointsRejectAbsentCorpus(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"TestConformance", "FuzzEngineDifferential"} {
		cmd := exec.CommandContext(t.Context(), executable, "-test.run=^"+test+"$")
		report := filepath.Join(t.TempDir(), "report.json")
		if err := os.WriteFile(report, []byte(`{"total":{"passed":999}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd.Env = append(os.Environ(), "GOXRPL_FIXTURES_DIR=", "GOXRPL_CONFORMANCE_REQUIRED=1", "GOXRPL_CONFORMANCE_REPORT="+report)
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "corpus is not configured") {
			t.Fatalf("%s accepted an absent required corpus: %v\n%s", test, err, output)
		}
		if test == "TestConformance" {
			data, err := os.ReadFile(report)
			if err != nil || len(data) != 0 {
				t.Fatalf("failed setup retained stale execution report: %s: %v", data, err)
			}
		}
	}
}

func TestPinnedRequiredEntrypointRejectsFilteredExecution(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestConformance$/^does-not-exist$")
	cmd.Env = append(os.Environ(),
		"GOXRPL_FIXTURES_DIR=internal/testing/conformance/testdata/rippled-3.4.1-v4",
		"GOXRPL_CONFORMANCE_REQUIRED=1", "GOXRPL_CONFORMANCE_REPORT="+path)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "incomplete conformance execution") {
		t.Fatalf("filtered corpus accepted: %v\n%s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report corpusReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if err := report.validate(); err != nil {
		t.Fatal(err)
	}
	if report.Total.Discovered == 0 || report.Total.Executed != 0 || report.Total.Skipped == 0 || report.Total.Discovered != report.Total.Skipped+report.Total.Excluded {
		t.Fatalf("incorrect filtered execution accounting: %+v", report.Total)
	}
	for _, group := range []map[string]*corpusCounts{report.Suites, report.Families, report.Profiles} {
		var total corpusCounts
		for _, count := range group {
			total.Discovered += count.Discovered
			total.Executed += count.Executed
			total.Passed += count.Passed
			total.Failed += count.Failed
			total.Skipped += count.Skipped
			total.Excluded += count.Excluded
		}
		if total != report.Total {
			t.Fatalf("group counts=%+v, total=%+v", total, report.Total)
		}
	}
	for _, result := range report.Cases {
		if result.Reason == "" {
			t.Fatalf("case %s has no non-execution reason", result.Name)
		}
	}
	for _, count := range report.Families {
		count.Discovered++
		if err := report.validate(); err == nil {
			t.Fatal("accepted corrupted family accounting")
		}
		break
	}
}
