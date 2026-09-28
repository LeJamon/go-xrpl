package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

type corpusCounts struct {
	Discovered int `json:"discovered"`
	Executed   int `json:"executed"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
	Excluded   int `json:"excluded"`
}

type caseResult struct {
	Name    string `json:"name"`
	Suite   string `json:"suite"`
	Family  string `json:"family"`
	Profile string `json:"profile"`
	SHA256  string `json:"sha256"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
}

type corpusReport struct {
	OracleRepository    string                   `json:"oracle_repository"`
	OracleTag           string                   `json:"oracle_tag"`
	OracleCommit        string                   `json:"oracle_commit"`
	ManifestSHA256      string                   `json:"manifest_sha256"`
	Total               corpusCounts             `json:"total"`
	Suites              map[string]*corpusCounts `json:"suites"`
	Families            map[string]*corpusCounts `json:"families"`
	Profiles            map[string]*corpusCounts `json:"profiles"`
	UnsupportedProfiles map[string]string        `json:"unsupported_profiles"`
	Cases               []caseResult             `json:"cases"`
	CoverageLimits      []string                 `json:"coverage_limits"`
}

func (r *corpusReport) add(c pinnedCase, status, reason string) {
	r.Cases = append(r.Cases, caseResult{Name: c.Name, Suite: c.Pin.Suite, Family: c.Pin.Family, Profile: c.Pin.Profile, SHA256: c.Pin.SHA256, Status: status, Reason: reason})
	counts := make([]*corpusCounts, 1, 4)
	counts[0] = &r.Total
	for _, group := range []struct {
		values map[string]*corpusCounts
		name   string
	}{{r.Suites, c.Pin.Suite}, {r.Families, c.Pin.Family}, {r.Profiles, c.Pin.Profile}} {
		if group.values[group.name] == nil {
			group.values[group.name] = &corpusCounts{}
		}
		counts = append(counts, group.values[group.name])
	}
	for _, count := range counts {
		count.Discovered++
		switch status {
		case "passed":
			count.Executed++
			count.Passed++
		case "failed":
			count.Executed++
			count.Failed++
		case "skipped":
			count.Skipped++
		case "excluded":
			count.Excluded++
		}
	}
}

func (r corpusReport) validate() error {
	valid := func(c corpusCounts) bool {
		return c.Discovered >= 0 && c.Executed >= 0 && c.Passed >= 0 && c.Failed >= 0 && c.Skipped >= 0 && c.Excluded >= 0 &&
			c.Discovered == c.Executed+c.Skipped+c.Excluded && c.Executed == c.Passed+c.Failed
	}
	if !valid(r.Total) || r.Total.Discovered != len(r.Cases) {
		return fmt.Errorf("invalid total accounting: %+v", r.Total)
	}
	for name, group := range map[string]map[string]*corpusCounts{"suites": r.Suites, "families": r.Families, "profiles": r.Profiles} {
		var total corpusCounts
		for key, c := range group {
			if key == "" || c == nil || !valid(*c) {
				return fmt.Errorf("invalid %s accounting for %q", name, key)
			}
			total.Discovered += c.Discovered
			total.Executed += c.Executed
			total.Passed += c.Passed
			total.Failed += c.Failed
			total.Skipped += c.Skipped
			total.Excluded += c.Excluded
		}
		if total != r.Total {
			return fmt.Errorf("%s accounting does not reconcile with total", name)
		}
	}
	for profile, reason := range r.UnsupportedProfiles {
		if r.Profiles[profile] == nil || *r.Profiles[profile] != (corpusCounts{}) || reason == "" {
			return fmt.Errorf("unsupported profile %q has observations or no reason", profile)
		}
	}
	seen := make(map[string]bool)
	for _, c := range r.Cases {
		if c.Name == "" || seen[c.Name] || r.Suites[c.Suite] == nil || r.Families[c.Family] == nil || r.Profiles[c.Profile] == nil {
			return fmt.Errorf("invalid case dimensions: %q", c.Name)
		}
		seen[c.Name] = true
		switch c.Status {
		case "passed", "failed":
		case "skipped", "excluded":
			if c.Reason == "" {
				return fmt.Errorf("case %q has no non-execution reason", c.Name)
			}
		default:
			return fmt.Errorf("case %q has unknown status %q", c.Name, c.Status)
		}
	}
	return nil
}

func TestConformance(t *testing.T) {
	if path := os.Getenv("GOXRPL_CONFORMANCE_REPORT"); path != "" {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("initialize conformance report: %v", err)
		}
	}
	corpus, err := resolvePinnedCorpus(conformanceCorpusRequired())
	if err != nil {
		t.Fatalf("conformance corpus rejected: %v", err)
	}
	report := corpusReport{
		OracleRepository:    corpus.Manifest.OracleRepository,
		OracleTag:           corpus.Manifest.RippledTag,
		OracleCommit:        corpus.Manifest.RippledCommit,
		ManifestSHA256:      corpus.ManifestSHA256,
		Suites:              make(map[string]*corpusCounts),
		Families:            make(map[string]*corpusCounts),
		Profiles:            make(map[string]*corpusCounts),
		UnsupportedProfiles: make(map[string]string),
		CoverageLimits:      corpus.Manifest.CoverageLimits,
	}
	for _, profile := range corpus.Manifest.AmendmentMatrix {
		report.Profiles[profile.ID] = &corpusCounts{}
		if !*profile.Supported {
			report.UnsupportedProfiles[profile.ID] = profile.UnsupportedReason
		}
	}
	t.Cleanup(func() {
		if err := report.validate(); err != nil {
			t.Error(err)
		}
		data, err := json.Marshal(report)
		if err != nil {
			t.Errorf("encode conformance report: %v", err)
			return
		}
		t.Logf("CONFORMANCE_REPORT %s", data)
		if path := os.Getenv("GOXRPL_CONFORMANCE_REPORT"); path != "" {
			if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
				t.Errorf("write conformance report: %v", err)
			}
		}
		if report.Total.Executed == 0 || report.Total.Skipped != 0 || report.Total.Discovered != len(corpus.Cases) || report.Total.Discovered != report.Total.Executed+report.Total.Skipped+report.Total.Excluded {
			t.Errorf("incomplete conformance execution: %+v", report.Total)
		}
	})
	for _, c := range corpus.Cases {
		if c.Pin.ExcludeReason != "" {
			report.add(c, "excluded", c.Pin.ExcludeReason)
			continue
		}
		executed := false
		reason := ""
		passed := t.Run(c.Name, func(t *testing.T) {
			executed = true
			defer func() {
				if recovered := recover(); recovered != nil {
					reason = fmt.Sprintf("panic: %v", recovered)
					t.Error(reason)
				}
			}()
			if err := runSnapshotFixture(c.Fixture); err != nil {
				reason = err.Error()
				t.Error(reason)
			}
		})
		switch {
		case !executed:
			report.add(c, "skipped", "filtered by go test -run; the required corpus must execute completely")
		case passed:
			report.add(c, "passed", "")
		default:
			report.add(c, "failed", reason)
		}
	}
}
