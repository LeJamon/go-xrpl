package conformance

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

const (
	// The release gate is deliberately pinned to the final rippled source. A
	// corpus from an RC or an older release is useful for debugging, but cannot
	// prove final-release conformance.
	expectedRippledTag     = "3.4.0"
	expectedRippledCommit  = "4a4fded2eba11427c48ce3f24d9c1aea5e7a9d17"
	expectedRippledRepo    = "XRPLF/rippled"
	expectedFixtureVersion = "v3"
	expectedManifestSchema = 2
	corpusManifestName     = "manifest.json"
)

var (
	errCorpusNotConfigured = errors.New("conformance corpus is not configured")
	errCorpusEmpty         = errors.New("conformance corpus is empty")
	errCorpusNoInScope     = errors.New("conformance corpus has no in-scope fixtures")
)

// corpusManifest is the only metadata accepted at the corpus root. The
// manifest is external to this repository because the final oracle fixture
// corpus is intentionally not checked in.
type corpusManifest struct {
	Schema              int
	FixtureVersion      string
	OracleRepository    string
	RippledTag          string
	RippledCommit       string
	RecorderCommit      string
	BuildIdentity       string
	ConfigIdentity      string
	AmendmentMatrix     []amendmentMatrixEntry
	FixtureCount        int
	InScopeFixtureCount int
	SkippedFixtureCount int
	SkipReasons         map[string]string
}

type amendmentMatrixEntry struct {
	FixCleanup3_4_0     *bool `json:"fixCleanup3_4_0"`
	LendingProtocolV1_1 *bool `json:"LendingProtocolV1_1"`
}

type corpusFixture struct {
	Name    string
	Path    string
	InScope bool
}

type corpus struct {
	Root     string
	Manifest corpusManifest
	Fixtures []corpusFixture
	InScope  []corpusFixture
}

func conformanceCorpusRequired() bool {
	return os.Getenv("GOXRPL_CONFORMANCE_REQUIRED") != "" || strings.TrimSpace(os.Getenv("GOXRPL_FIXTURES_DIR")) != ""
}

// resolveCorpus is shared by TestConformance and FuzzEngineDifferential. An
// unset path is the one optional case: ordinary package tests may run without
// the large external corpus. Once a path is supplied, every validation error
// is fatal to the caller, including a missing path.
func resolveCorpus(required bool) (*corpus, error) {
	configured := strings.TrimSpace(os.Getenv("GOXRPL_FIXTURES_DIR"))
	if configured == "" {
		if required {
			return nil, fmt.Errorf("%w: set GOXRPL_FIXTURES_DIR", errCorpusNotConfigured)
		}
		return nil, errCorpusNotConfigured
	}

	root, err := filepath.Abs(configured)
	if err != nil {
		return nil, fmt.Errorf("resolve GOXRPL_FIXTURES_DIR %q: %w", configured, err)
	}
	return resolveCorpusPath(root)
}

func resolveCorpusPath(root string) (*corpus, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat conformance corpus %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("conformance corpus %q is not a directory", root)
	}

	// Opening the directory catches unreadable roots before WalkDir starts.
	dir, err := os.Open(root)
	if err != nil {
		return nil, fmt.Errorf("open conformance corpus %q: %w", root, err)
	}
	if _, err := dir.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		_ = dir.Close()
		return nil, fmt.Errorf("read conformance corpus %q: %w", root, err)
	}
	if err := dir.Close(); err != nil {
		return nil, fmt.Errorf("close conformance corpus %q: %w", root, err)
	}

	manifestPath, err := findCorpusManifest(root)
	if err != nil {
		return nil, err
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read conformance manifest %q: %w", manifestPath, err)
	}
	manifest, err := decodeCorpusManifest(manifestData)
	if err != nil {
		return nil, fmt.Errorf("decode conformance manifest %q: %w", manifestPath, err)
	}
	if err := validateCorpusManifest(manifest); err != nil {
		return nil, fmt.Errorf("validate conformance manifest %q: %w", manifestPath, err)
	}

	scope, err := outOfScopeSuitesStrict()
	if err != nil {
		return nil, err
	}

	result := &corpus{Root: root, Manifest: manifest}
	skipped := make(map[string]bool)
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %q: %w", path, walkErr)
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Clean(path) == filepath.Clean(manifestPath) {
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat fixture %q: %w", path, err)
		}
		if !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("fixture %q is not a regular file", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read fixture %q: %w", path, err)
		}
		fixture, err := decodeFixture(data)
		if err != nil {
			return fmt.Errorf("decode fixture %q: %w", path, err)
		}
		if err := validateFixture(&fixture, path); err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("relativize fixture %q: %w", path, err)
		}
		name := strings.TrimSuffix(filepath.ToSlash(rel), ".json")
		skipReason := skipTests[name]
		if skipReason == "" {
			if retired := fixtureDisablesRetiredAmendments(&fixture); len(retired) > 0 {
				skipReason = fmt.Sprintf("fixture disables retired amendment(s): %s", strings.Join(retired, ", "))
			}
		}
		inScope := !scope[suiteOf(name)] && skipReason == ""
		fixtureInfo := corpusFixture{Name: name, Path: path, InScope: inScope}
		result.Fixtures = append(result.Fixtures, fixtureInfo)
		if inScope {
			result.InScope = append(result.InScope, fixtureInfo)
		} else {
			skipped[name] = true
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(result.Fixtures, func(i, j int) bool { return result.Fixtures[i].Name < result.Fixtures[j].Name })
	sort.Slice(result.InScope, func(i, j int) bool { return result.InScope[i].Name < result.InScope[j].Name })

	if len(result.Fixtures) == 0 {
		return nil, fmt.Errorf("%w: %q contains no fixture JSON files", errCorpusEmpty, root)
	}
	if manifest.FixtureCount != len(result.Fixtures) {
		return nil, fmt.Errorf("conformance manifest fixture_count=%d, found %d", manifest.FixtureCount, len(result.Fixtures))
	}
	if manifest.InScopeFixtureCount != len(result.InScope) {
		return nil, fmt.Errorf("conformance manifest in_scope_fixture_count=%d, found %d", manifest.InScopeFixtureCount, len(result.InScope))
	}
	if manifest.SkippedFixtureCount != len(skipped) {
		return nil, fmt.Errorf("conformance manifest skipped_fixture_count=%d, found %d", manifest.SkippedFixtureCount, len(skipped))
	}
	for fixture := range skipped {
		if _, ok := manifest.SkipReasons[fixture]; !ok {
			return nil, fmt.Errorf("conformance manifest has no skip reason for %q", fixture)
		}
	}
	for fixture := range manifest.SkipReasons {
		if !skipped[fixture] {
			return nil, fmt.Errorf("conformance manifest skip reason references non-skipped fixture %q", fixture)
		}
	}
	if len(result.InScope) == 0 {
		return nil, fmt.Errorf("%w: %q", errCorpusNoInScope, root)
	}

	return result, nil
}

func findCorpusManifest(root string) (string, error) {
	path := filepath.Join(root, corpusManifestName)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("conformance corpus manifest %q is missing", path)
		}
		return "", fmt.Errorf("stat conformance corpus manifest %q: %w", path, err)
	}
	return path, nil
}

func decodeCorpusManifest(data []byte) (corpusManifest, error) {
	var manifest struct {
		Schema              int                    `json:"schema"`
		FixtureVersion      string                 `json:"fixture_version"`
		OracleRepository    string                 `json:"oracle_repository"`
		RippledTag          string                 `json:"rippled_tag"`
		RippledCommit       string                 `json:"rippled_commit"`
		RecorderCommit      string                 `json:"recorder_commit"`
		BuildIdentity       string                 `json:"build_identity"`
		ConfigIdentity      string                 `json:"config_identity"`
		AmendmentMatrix     []amendmentMatrixEntry `json:"amendment_matrix"`
		FixtureCount        int                    `json:"fixture_count"`
		InScopeFixtureCount int                    `json:"in_scope_fixture_count"`
		SkippedFixtureCount int                    `json:"skipped_fixture_count"`
		SkipReasons         map[string]string      `json:"skip_reasons"`
	}
	if err := decodeStrictJSON(data, &manifest); err != nil {
		return corpusManifest{}, err
	}
	return corpusManifest{
		Schema:              manifest.Schema,
		FixtureVersion:      manifest.FixtureVersion,
		OracleRepository:    manifest.OracleRepository,
		RippledTag:          manifest.RippledTag,
		RippledCommit:       manifest.RippledCommit,
		RecorderCommit:      manifest.RecorderCommit,
		BuildIdentity:       manifest.BuildIdentity,
		ConfigIdentity:      manifest.ConfigIdentity,
		AmendmentMatrix:     manifest.AmendmentMatrix,
		FixtureCount:        manifest.FixtureCount,
		InScopeFixtureCount: manifest.InScopeFixtureCount,
		SkippedFixtureCount: manifest.SkippedFixtureCount,
		SkipReasons:         manifest.SkipReasons,
	}, nil
}

func validateCorpusManifest(manifest corpusManifest) error {
	if manifest.Schema != expectedManifestSchema {
		return fmt.Errorf("schema=%d, want %d", manifest.Schema, expectedManifestSchema)
	}
	if normalizeFixtureVersion(manifest.FixtureVersion) != expectedFixtureVersion {
		return fmt.Errorf("fixture_version=%q, want %q", manifest.FixtureVersion, expectedFixtureVersion)
	}
	if normalizeOracleRepository(manifest.OracleRepository) != strings.ToLower(expectedRippledRepo) {
		return fmt.Errorf("oracle_repository=%q, want %q", manifest.OracleRepository, expectedRippledRepo)
	}
	if normalizeRippledVersion(manifest.RippledTag) != expectedRippledTag {
		return fmt.Errorf("rippled_tag=%q, want %q", manifest.RippledTag, expectedRippledTag)
	}
	if !strings.EqualFold(manifest.RippledCommit, expectedRippledCommit) {
		return fmt.Errorf("rippled_commit=%q, want %q", manifest.RippledCommit, expectedRippledCommit)
	}
	if !validGitCommit(manifest.RecorderCommit) {
		return fmt.Errorf("recorder_commit=%q is not a full git commit", manifest.RecorderCommit)
	}
	if strings.TrimSpace(manifest.BuildIdentity) == "" {
		return errors.New("build_identity is required")
	}
	if strings.TrimSpace(manifest.ConfigIdentity) == "" {
		return errors.New("config_identity is required")
	}
	if err := validateAmendmentMatrix(manifest.AmendmentMatrix); err != nil {
		return err
	}
	if manifest.FixtureCount < 0 {
		return fmt.Errorf("fixture_count=%d, want non-negative", manifest.FixtureCount)
	}
	if manifest.InScopeFixtureCount < 0 || manifest.InScopeFixtureCount > manifest.FixtureCount {
		return fmt.Errorf("in_scope_fixture_count=%d, want 0..%d", manifest.InScopeFixtureCount, manifest.FixtureCount)
	}
	if manifest.SkippedFixtureCount < 0 {
		return fmt.Errorf("skipped_fixture_count=%d, want non-negative", manifest.SkippedFixtureCount)
	}
	if manifest.InScopeFixtureCount+manifest.SkippedFixtureCount != manifest.FixtureCount {
		return fmt.Errorf("fixture counts do not add up: in_scope=%d skipped=%d total=%d", manifest.InScopeFixtureCount, manifest.SkippedFixtureCount, manifest.FixtureCount)
	}
	if manifest.SkipReasons == nil {
		return errors.New("skip_reasons is required, including when empty")
	}
	if len(manifest.SkipReasons) != manifest.SkippedFixtureCount {
		return fmt.Errorf("skip_reasons has %d entries, want %d", len(manifest.SkipReasons), manifest.SkippedFixtureCount)
	}
	for fixture, reason := range manifest.SkipReasons {
		if strings.TrimSpace(fixture) == "" || strings.TrimSpace(reason) == "" {
			return errors.New("skip_reasons requires non-empty fixture names and reasons")
		}
	}
	return nil
}

func normalizeOracleRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "git@github.com:"} {
		repository = strings.TrimPrefix(repository, prefix)
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(repository, "/"), ".git"))
}

func validGitCommit(commit string) bool {
	if len(commit) != 40 {
		return false
	}
	for _, c := range commit {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func validateAmendmentMatrix(matrix []amendmentMatrixEntry) error {
	if len(matrix) != 4 {
		return fmt.Errorf("amendment_matrix has %d entries, want 4", len(matrix))
	}
	seen := make(map[[2]bool]bool, len(matrix))
	for i, entry := range matrix {
		if entry.FixCleanup3_4_0 == nil || entry.LendingProtocolV1_1 == nil {
			return fmt.Errorf("amendment_matrix entry %d must specify both amendments", i)
		}
		key := [2]bool{*entry.FixCleanup3_4_0, *entry.LendingProtocolV1_1}
		if seen[key] {
			return fmt.Errorf("amendment_matrix duplicates combination cleanup=%t lending=%t", key[0], key[1])
		}
		seen[key] = true
	}
	for _, cleanup := range []bool{false, true} {
		for _, lending := range []bool{false, true} {
			if !seen[[2]bool{cleanup, lending}] {
				return fmt.Errorf("amendment_matrix missing combination cleanup=%t lending=%t", cleanup, lending)
			}
		}
	}
	return nil
}

func normalizeRippledVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

func normalizeFixtureVersion(version string) string {
	version = strings.TrimSpace(version)
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return version
}

func decodeStrictJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func decodeFixture(data []byte) (Fixture, error) {
	var fixture Fixture
	if err := decodeStrictJSON(data, &fixture); err != nil {
		return Fixture{}, err
	}
	return fixture, nil
}

func validateFixture(fixture *Fixture, fixturePath string) error {
	if fixture.RippledVersion == "" {
		return fmt.Errorf("fixture %q: missing rippled_version", fixturePath)
	}
	if normalizeRippledVersion(fixture.RippledVersion) != expectedRippledTag {
		return fmt.Errorf("fixture %q: rippled_version=%q, want %q", fixturePath, fixture.RippledVersion, expectedRippledTag)
	}
	if strings.TrimSpace(fixture.Suite) == "" {
		return fmt.Errorf("fixture %q: missing suite", fixturePath)
	}
	if strings.TrimSpace(fixture.Testcase) == "" {
		return fmt.Errorf("fixture %q: missing testcase", fixturePath)
	}
	if len(fixture.Steps) == 0 {
		return fmt.Errorf("fixture %q: steps must not be empty", fixturePath)
	}
	if fixture.DependsOn != "" {
		dep := filepath.ToSlash(fixture.DependsOn)
		if filepath.IsAbs(fixture.DependsOn) || dep == "." || strings.HasPrefix(dep, "../") || strings.Contains(dep, "/../") {
			return fmt.Errorf("fixture %q: invalid depends_on path %q", fixturePath, fixture.DependsOn)
		}
	}
	if err := validateEnvConfig(fixture.Env, fixturePath, "env"); err != nil {
		return err
	}
	for i := range fixture.Steps {
		if err := validateStep(&fixture.Steps[i], fixturePath, i); err != nil {
			return err
		}
		if err := validatePostState(fixture.Steps[i].PostState, fixturePath, i); err != nil {
			return err
		}
	}
	return nil
}

func validatePostState(postState *PostState, fixturePath string, stepIndex int) error {
	if postState == nil {
		return nil
	}
	if len(postState.Accounts) == 0 {
		return fmt.Errorf("fixture %q step %d: post_state.accounts must not be empty", fixturePath, stepIndex)
	}
	seen := make(map[string]bool, len(postState.Accounts))
	for _, account := range postState.Accounts {
		if strings.TrimSpace(account.Name) == "" || strings.TrimSpace(account.Address) == "" {
			return fmt.Errorf("fixture %q step %d: post_state account name and address are required", fixturePath, stepIndex)
		}
		if seen[account.Name] {
			return fmt.Errorf("fixture %q step %d: duplicate post_state account %q", fixturePath, stepIndex, account.Name)
		}
		seen[account.Name] = true
		if _, err := strconv.ParseUint(account.XRPBalance, 10, 64); err != nil {
			return fmt.Errorf("fixture %q step %d: invalid post_state balance for %q: %w", fixturePath, stepIndex, account.Name, err)
		}
	}
	return nil
}

func validateEnvConfig(env *EnvConfig, fixturePath, context string) error {
	if env == nil {
		return nil
	}
	if env.InitialLedgerSeq != nil && *env.InitialLedgerSeq < 3 {
		return fmt.Errorf("fixture %q: %s initial_ledger_seq=%d is below the supported genesis boundary", fixturePath, context, *env.InitialLedgerSeq)
	}
	seen := make(map[string]bool, len(env.AmendmentsEnabled))
	for _, name := range env.AmendmentsEnabled {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("fixture %q: %s contains an empty amendment name", fixturePath, context)
		}
		if amendment.FeatureByName(name) == nil {
			return fmt.Errorf("fixture %q: %s contains unknown amendment %q", fixturePath, context, name)
		}
		if seen[name] {
			return fmt.Errorf("fixture %q: %s contains duplicate amendment %q", fixturePath, context, name)
		}
		seen[name] = true
	}
	return nil
}

func validateStep(step *Step, fixturePath string, index int) error {
	context := fmt.Sprintf("fixture %q step %d", fixturePath, index)
	if strings.TrimSpace(step.Op) == "" {
		return fmt.Errorf("%s: missing op", context)
	}
	switch step.Op {
	case "fund":
		if step.Account == "" || step.Address == "" || len(step.Amount) == 0 {
			return fmt.Errorf("%s (fund): account, address, and amount are required", context)
		}
		if _, err := parseDropsAmount(step.Amount); err != nil {
			return fmt.Errorf("%s (fund): invalid amount: %w", context, err)
		}
	case "trust":
		if step.Account == "" || step.LimitAmount == nil {
			return fmt.Errorf("%s (trust): account and limit_amount are required", context)
		}
		if strings.TrimSpace(step.LimitAmount.Currency) == "" ||
			strings.TrimSpace(step.LimitAmount.Issuer) == "" ||
			strings.TrimSpace(step.LimitAmount.Value) == "" {
			return fmt.Errorf("%s (trust): limit_amount currency, issuer, and value are required", context)
		}
	case "close":
	case "tx", "retry":
		if step.TxBlob == "" {
			return fmt.Errorf("%s (%s): tx_blob is required", context, step.Op)
		}
		if _, err := hex.DecodeString(step.TxBlob); err != nil {
			return fmt.Errorf("%s (%s): tx_blob is not hex: %w", context, step.Op, err)
		}
		blob, _ := hex.DecodeString(step.TxBlob)
		if _, err := tx.ParseFromBinary(blob); err != nil {
			return fmt.Errorf("%s (%s): tx_blob cannot be parsed: %w", context, step.Op, err)
		}
		if len(step.TxJSON) == 0 {
			return fmt.Errorf("%s (%s): tx_json is required", context, step.Op)
		}
		var txJSON map[string]any
		if err := decodeStrictJSON(step.TxJSON, &txJSON); err != nil {
			return fmt.Errorf("%s (%s): tx_json is malformed: %w", context, step.Op, err)
		}
		if txJSON == nil {
			return fmt.Errorf("%s (%s): tx_json must be an object", context, step.Op)
		}
		if txType, ok := txJSON["TransactionType"].(string); !ok || strings.TrimSpace(txType) == "" {
			return fmt.Errorf("%s (%s): tx_json TransactionType is required", context, step.Op)
		}
		if strings.TrimSpace(step.ExpectTER) == "" {
			return fmt.Errorf("%s (%s): expect_ter is required", context, step.Op)
		}
		if !validTERCode(step.ExpectTER) {
			return fmt.Errorf("%s (%s): invalid expect_ter %q", context, step.Op, step.ExpectTER)
		}
		if err := validateResultExpectation(step.ExpectedResult, step.ExpectTER, context); err != nil {
			return err
		}
	case "env_reset":
		if step.Env == nil {
			return fmt.Errorf("%s (env_reset): env is required", context)
		}
		if err := validateEnvConfig(step.Env, fixturePath, fmt.Sprintf("step %d env", index)); err != nil {
			return err
		}
	case "enable_amendment":
		if step.Amendment == "" {
			return fmt.Errorf("%s (enable_amendment): amendment is required", context)
		}
		if amendment.FeatureByName(step.Amendment) == nil {
			return fmt.Errorf("%s (enable_amendment): unknown amendment %q", context, step.Amendment)
		}
	case "modify_state":
		if step.ModifyState == nil {
			return fmt.Errorf("%s (modify_state): modify_state is required", context)
		}
		if strings.TrimSpace(step.ModifyState.Account) == "" {
			return fmt.Errorf("%s (modify_state): account is required", context)
		}
		if bump := step.ModifyState.BumpLastPage; bump != nil {
			if bump.Directory != "owner" {
				return fmt.Errorf("%s (modify_state): unsupported directory %q", context, bump.Directory)
			}
			if bump.TargetPage == 0 || strings.TrimSpace(bump.AdjustField) == "" {
				return fmt.Errorf("%s (modify_state): bump_last_page target_page and adjust_field are required", context)
			}
		}
	default:
		return fmt.Errorf("%s: unknown op %q", context, step.Op)
	}
	return nil
}

func validateResultExpectation(expected *ResultExpectation, terName, context string) error {
	if expected == nil {
		return fmt.Errorf("%s: expected_result is required by fixture version %s", context, expectedFixtureVersion)
	}
	if expected.Boundary == nil || strings.TrimSpace(*expected.Boundary) == "" {
		return fmt.Errorf("%s: expected_result.boundary is required", context)
	}
	if *expected.Boundary != "engine" {
		return fmt.Errorf("%s: unsupported expected_result.boundary %q (only engine is supported)", context, *expected.Boundary)
	}
	if expected.TER == nil || strings.TrimSpace(*expected.TER) == "" {
		return fmt.Errorf("%s: expected_result.ter is required", context)
	}
	if *expected.TER != terName {
		return fmt.Errorf("%s: expected_result.ter=%q does not match expect_ter=%q", context, *expected.TER, terName)
	}
	if expected.TERCode == nil || expected.Applied == nil || expected.Queued == nil || expected.Fee == nil || expected.MetadataSHA512Half == nil || expected.StateSHA512Half == nil {
		return fmt.Errorf("%s: expected_result must include ter_code, applied, queued, fee, metadata_sha512_half, and state_sha512_half", context)
	}
	code, err := definitions.Get().TransactionResultCode(terName)
	if err != nil {
		return fmt.Errorf("%s: unknown TER %q: %w", context, terName, err)
	}
	if int32(*expected.TERCode) != code {
		return fmt.Errorf("%s: expected_result.ter_code=%d does not match %s=%d", context, *expected.TERCode, terName, code)
	}
	if hash := strings.TrimSpace(*expected.MetadataSHA512Half); hash != "" {
		if len(hash) != 64 {
			return fmt.Errorf("%s: metadata_sha512_half must be empty or 64 hex characters", context)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return fmt.Errorf("%s: metadata_sha512_half is not hex: %w", context, err)
		}
	}
	if hash := strings.TrimSpace(*expected.StateSHA512Half); len(hash) != 64 {
		return fmt.Errorf("%s: state_sha512_half must be 64 hex characters", context)
	} else if _, err := hex.DecodeString(hash); err != nil {
		return fmt.Errorf("%s: state_sha512_half is not hex: %w", context, err)
	}
	return nil
}

func validTERCode(code string) bool {
	if code == "tesSUCCESS" {
		return true
	}
	for _, prefix := range []string{"tec", "tef", "tel", "tem", "ter"} {
		if strings.HasPrefix(code, prefix) && len(code) > len(prefix) {
			return true
		}
	}
	return false
}

func outOfScopeSuitesStrict() (map[string]bool, error) {
	set := map[string]bool{}
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		return nil, errors.New("resolve conformance package path")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "../../../scripts/conformance-out-of-scope.txt"))
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open out-of-scope policy %q: %w", path, err)
	}
	defer f.Close()
	if err := loadOutOfScopeLines(f, set); err != nil {
		return nil, fmt.Errorf("read out-of-scope policy %q: %w", path, err)
	}
	return set, nil
}

func loadOutOfScopeLines(reader io.Reader, set map[string]bool) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		set[strings.ReplaceAll(line, " ", "")] = true
	}
	return nil
}
