package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
)

const (
	pinnedOracleRepository = "XRPLF/xrpld-private"
	pinnedOracleTag        = "3.4.1"
	pinnedOracleCommit     = "d147fccf54a500fce586522f28d6044c37fd8d29"
	pinnedFixtureVersion   = "v4"
	pinnedManifestSchema   = 3
)

var pinnedRecorderSources = []string{
	"scripts/oracle/record-v4.py",
	"scripts/oracle/record-v4.sh",
	"scripts/oracle/strict-corpus-config.json",
	"scripts/oracle/strict_recorder.cpp",
}

type pinnedManifest struct {
	Schema           int                         `json:"schema"`
	FixtureVersion   string                      `json:"fixture_version"`
	OracleRepository string                      `json:"oracle_repository"`
	RippledTag       string                      `json:"rippled_tag"`
	RippledCommit    string                      `json:"rippled_commit"`
	RecorderCommit   string                      `json:"recorder_commit"`
	RecorderSources  map[string]string           `json:"recorder_sources"`
	BinarySHA256     string                      `json:"binary_sha256"`
	BuildIdentity    string                      `json:"build_identity"`
	ConfigIdentity   string                      `json:"config_identity"`
	ConfigSource     string                      `json:"config_source"`
	AmendmentMatrix  []pinnedProfile             `json:"amendment_matrix"`
	FixtureCount     int                         `json:"fixture_count"`
	Fixtures         map[string]pinnedFixturePin `json:"fixtures"`
	CoverageLimits   []string                    `json:"coverage_limits"`
}

type pinnedProfile struct {
	ID                  string `json:"id"`
	FixCleanup3_4_0     *bool  `json:"fixCleanup3_4_0"`
	LendingProtocolV1_1 *bool  `json:"LendingProtocolV1_1"`
	BatchV1_1           *bool  `json:"BatchV1_1"`
	FixBatchV1_2        *bool  `json:"fixBatchV1_2"`
	Supported           *bool  `json:"supported"`
	UnsupportedReason   string `json:"unsupported_reason,omitempty"`
}

type pinnedFixturePin struct {
	SHA256        string `json:"sha256"`
	Suite         string `json:"suite"`
	Family        string `json:"family"`
	Profile       string `json:"profile"`
	ExcludeReason string `json:"exclude_reason,omitempty"`
}

type pinnedCase struct {
	Name    string
	Pin     pinnedFixturePin
	Fixture snapshotFixture
}

type pinnedCorpus struct {
	Root           string
	Manifest       pinnedManifest
	ManifestSHA256 string
	Cases          []pinnedCase
}

func conformanceRepositoryRoot() string {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		panic("cannot resolve conformance source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
}

func resolvePinnedCorpus(required bool) (*pinnedCorpus, error) {
	root := strings.TrimSpace(os.Getenv("GOXRPL_FIXTURES_DIR"))
	if root == "" {
		if required {
			return nil, fmt.Errorf("%w: set GOXRPL_FIXTURES_DIR", errCorpusNotConfigured)
		}
		root = filepath.Join(conformanceRepositoryRoot(), "internal/testing/conformance/testdata/rippled-3.4.1-v4")
	} else if !filepath.IsAbs(root) {
		root = filepath.Join(conformanceRepositoryRoot(), root)
	}
	return loadPinnedCorpus(root)
}

func loadPinnedCorpus(path string) (*pinnedCorpus, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open pinned corpus: %w", err)
	}
	defer root.Close()
	data, err := readRegularCorpusFile(root, corpusManifestName)
	if err != nil {
		return nil, fmt.Errorf("read pinned manifest: %w", err)
	}
	var manifest pinnedManifest
	if err := decodeStrictJSON(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode pinned manifest: %w", err)
	}
	if err := validatePinnedManifestKeys(data); err != nil {
		return nil, err
	}
	profiles, err := validatePinnedManifest(manifest)
	if err != nil {
		return nil, err
	}
	result := &pinnedCorpus{Root: path, Manifest: manifest, ManifestSHA256: sha256Hex(data)}
	seen := make(map[string]bool, len(manifest.Fixtures))
	executedProfiles := make(map[string]int)
	if err := fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("corpus symlink is not allowed: %s", path)
		}
		if entry.IsDir() || path == corpusManifestName || filepath.Ext(path) != ".json" {
			return nil
		}
		pin, ok := manifest.Fixtures[path]
		if !ok {
			return fmt.Errorf("discovered fixture has no manifest entry: %s", path)
		}
		data, err := readRegularCorpusFile(root, path)
		if err != nil {
			return fmt.Errorf("read fixture %s: %w", path, err)
		}
		if sha256Hex(data) != pin.SHA256 {
			return fmt.Errorf("fixture %s: SHA-256 mismatch", path)
		}
		fixture, err := decodeSnapshotFixture(data)
		if err != nil {
			return fmt.Errorf("fixture %s: %w", path, err)
		}
		if fixture.Suite != pin.Suite || fixture.Family != pin.Family || fixture.Profile != pin.Profile {
			return fmt.Errorf("fixture %s: suite/family/profile does not match manifest", path)
		}
		profile, ok := profiles[pin.Profile]
		if !ok || !*profile.Supported {
			return fmt.Errorf("fixture %s: unsupported profile %s", path, pin.Profile)
		}
		if err := profile.matchesRules(fixture.Parent.Rules); err != nil {
			return fmt.Errorf("fixture %s: %w", path, err)
		}
		seen[path] = true
		if pin.ExcludeReason == "" {
			executedProfiles[pin.Profile]++
		}
		result.Cases = append(result.Cases, pinnedCase{Name: strings.TrimSuffix(path, ".json"), Pin: pin, Fixture: fixture})
		return nil
	}); err != nil {
		return nil, err
	}
	if len(seen) != manifest.FixtureCount {
		return nil, fmt.Errorf("fixture count: manifest=%d discovered=%d", manifest.FixtureCount, len(seen))
	}
	for name := range manifest.Fixtures {
		if !seen[name] {
			return nil, fmt.Errorf("manifest fixture missing: %s", name)
		}
	}
	if len(executedProfiles) == 0 {
		return nil, errCorpusNoInScope
	}
	for name, profile := range profiles {
		if *profile.Supported && executedProfiles[name] == 0 {
			return nil, fmt.Errorf("supported profile %s has no executable fixtures", name)
		}
	}
	sort.Slice(result.Cases, func(i, j int) bool { return result.Cases[i].Name < result.Cases[j].Name })
	return result, nil
}

func readRegularCorpusFile(root *os.Root, path string) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o444 == 0 {
		return nil, fmt.Errorf("%s is not a readable regular file", path)
	}
	return readRootFile(root, path)
}

func validatePinnedManifestKeys(data []byte) error {
	object, err := exactJSONKeys(data, "manifest", "schema", "fixture_version", "oracle_repository", "rippled_tag", "rippled_commit", "recorder_commit", "recorder_sources", "binary_sha256", "build_identity", "config_identity", "config_source", "amendment_matrix", "fixture_count", "fixtures", "coverage_limits")
	if err != nil {
		return err
	}
	var profiles []json.RawMessage
	if err := json.Unmarshal(object["amendment_matrix"], &profiles); err != nil {
		return err
	}
	for _, profile := range profiles {
		if _, err := exactJSONKeys(profile, "amendment profile", "id", "fixCleanup3_4_0", "LendingProtocolV1_1", "BatchV1_1", "fixBatchV1_2", "supported", "unsupported_reason"); err != nil {
			return err
		}
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(object["fixtures"], &fixtures); err != nil {
		return err
	}
	for name, fixture := range fixtures {
		if _, err := exactJSONKeys(fixture, name, "sha256", "suite", "family", "profile", "exclude_reason"); err != nil {
			return err
		}
	}
	return nil
}

func exactJSONKeys(data []byte, context string, allowed ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	for name, value := range object {
		known := false
		for _, key := range allowed {
			known = known || name == key
		}
		if !known {
			return nil, fmt.Errorf("%s: unknown field %q", context, name)
		}
		if strings.TrimSpace(string(value)) == "null" {
			return nil, fmt.Errorf("%s.%s must not be null", context, name)
		}
	}
	return object, nil
}

func validatePinnedManifest(manifest pinnedManifest) (map[string]pinnedProfile, error) {
	if manifest.Schema != pinnedManifestSchema || manifest.FixtureVersion != pinnedFixtureVersion {
		return nil, fmt.Errorf("required corpus format is schema %d/%s", pinnedManifestSchema, pinnedFixtureVersion)
	}
	if manifest.OracleRepository != pinnedOracleRepository || manifest.RippledTag != pinnedOracleTag || manifest.RippledCommit != pinnedOracleCommit {
		return nil, fmt.Errorf("required oracle is %s tag %s commit %s", pinnedOracleRepository, pinnedOracleTag, pinnedOracleCommit)
	}
	if !validGitCommit(manifest.RecorderCommit) || strings.Trim(manifest.RecorderCommit, "0") == "" {
		return nil, errors.New("recorder_commit must identify the recorder source commit")
	}
	if !validSHA256(manifest.BinarySHA256) || strings.TrimSpace(manifest.BuildIdentity) == "" || !validSHA256(manifest.ConfigIdentity) {
		return nil, errors.New("binary_sha256, build_identity, and SHA-256 config_identity are required")
	}
	if len(manifest.RecorderSources) != len(pinnedRecorderSources) {
		return nil, errors.New("recorder_sources must contain the complete recorder source inventory")
	}
	for _, name := range pinnedRecorderSources {
		if _, ok := manifest.RecorderSources[name]; !ok {
			return nil, fmt.Errorf("recorder_sources is missing %s", name)
		}
	}
	if manifest.ConfigSource != "scripts/oracle/strict-corpus-config.json" || manifest.RecorderSources[manifest.ConfigSource] != manifest.ConfigIdentity {
		return nil, errors.New("config_source must identify a checksummed recorder source matching config_identity")
	}
	repo, err := os.OpenRoot(conformanceRepositoryRoot())
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	for name, checksum := range manifest.RecorderSources {
		if !fs.ValidPath(name) || !validSHA256(checksum) {
			return nil, fmt.Errorf("invalid recorder source %q", name)
		}
		data, err := readRegularCorpusFile(repo, name)
		if err != nil {
			return nil, fmt.Errorf("recorder source %s: %w", name, err)
		}
		if sha256Hex(data) != checksum {
			return nil, fmt.Errorf("recorder source %s: SHA-256 mismatch", name)
		}
	}
	if manifest.FixtureCount == 0 {
		return nil, errCorpusEmpty
	}
	if manifest.FixtureCount < 0 || len(manifest.Fixtures) != manifest.FixtureCount {
		return nil, errors.New("fixture_count does not match fixture inventory")
	}
	for name, pin := range manifest.Fixtures {
		if !fs.ValidPath(name) || filepath.Ext(name) != ".json" || name == corpusManifestName || !validSHA256(pin.SHA256) {
			return nil, fmt.Errorf("invalid fixture pin %q", name)
		}
		if strings.TrimSpace(pin.Suite) == "" || strings.TrimSpace(pin.Family) == "" || strings.TrimSpace(pin.Profile) == "" {
			return nil, fmt.Errorf("fixture %s: suite, family and profile are required", name)
		}
		if pin.ExcludeReason != "" && strings.TrimSpace(pin.ExcludeReason) == "" {
			return nil, fmt.Errorf("fixture %s: exclusion reason must not be whitespace", name)
		}
	}
	if len(manifest.CoverageLimits) == 0 {
		return nil, errors.New("coverage_limits must explicitly state the recorded scope")
	}
	for _, limit := range manifest.CoverageLimits {
		if strings.TrimSpace(limit) == "" {
			return nil, errors.New("coverage_limits contains an empty statement")
		}
	}
	return validatePinnedProfiles(manifest.AmendmentMatrix)
}

func validatePinnedProfiles(matrix []pinnedProfile) (map[string]pinnedProfile, error) {
	if len(matrix) != 16 {
		return nil, errors.New("amendment_matrix must explicitly describe all 16 cleanup/lending/batch/fix combinations")
	}
	profiles := make(map[string]pinnedProfile, len(matrix))
	for _, profile := range matrix {
		if profile.FixCleanup3_4_0 == nil || profile.LendingProtocolV1_1 == nil || profile.BatchV1_1 == nil || profile.FixBatchV1_2 == nil || profile.Supported == nil {
			return nil, errors.New("each profile must specify all four amendments and supported")
		}
		id := fmt.Sprintf("c%d-l%d-b%d-f%d", boolInt(*profile.FixCleanup3_4_0), boolInt(*profile.LendingProtocolV1_1), boolInt(*profile.BatchV1_1), boolInt(*profile.FixBatchV1_2))
		if profile.ID != id {
			return nil, fmt.Errorf("profile %s: amendment states require ID %s", profile.ID, id)
		}
		if _, exists := profiles[id]; exists {
			return nil, fmt.Errorf("duplicate amendment profile %s", id)
		}
		if !*profile.Supported {
			if *profile.BatchV1_1 || !*profile.FixBatchV1_2 || strings.TrimSpace(profile.UnsupportedReason) == "" {
				return nil, fmt.Errorf("profile %s must execute; only fix-enabled/batch-disabled profiles may be unsupported with a reason", id)
			}
		} else if profile.UnsupportedReason != "" {
			return nil, fmt.Errorf("supported profile %s has an unsupported reason", id)
		}
		profiles[id] = profile
	}
	return profiles, nil
}

func (p pinnedProfile) matchesRules(ids []string) error {
	states := map[string]*bool{
		"fixCleanup3_4_0":     p.FixCleanup3_4_0,
		"LendingProtocolV1_1": p.LendingProtocolV1_1,
		"BatchV1_1":           p.BatchV1_1,
		"fixBatchV1_2":        p.FixBatchV1_2,
	}
	enabled := make(map[string]bool, len(ids))
	for _, id := range ids {
		enabled[strings.ToLower(id)] = true
	}
	for name, want := range states {
		feature := amendment.FeatureByName(name)
		if feature == nil {
			return fmt.Errorf("unknown profile amendment %s", name)
		}
		if got := enabled[fmt.Sprintf("%x", feature.ID)]; got != *want {
			return fmt.Errorf("profile %s: %s=%t but parent rules enable=%t", p.ID, name, *want, got)
		}
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) || strings.Trim(value, "0") == "" {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sha256Hex(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
