package conformance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
)

func TestCorpusResolverAcceptsPinnedCorpus(t *testing.T) {
	root := writeContractCorpus(t, "app/AccountSet", 1)
	t.Setenv("GOXRPL_FIXTURES_DIR", root)

	corpus, err := resolveCorpus(true)
	if err != nil {
		t.Fatalf("resolveCorpus: %v", err)
	}
	if len(corpus.Fixtures) != 1 || len(corpus.InScope) != 1 {
		t.Fatalf("fixture counts = %d total/%d in scope, want 1/1", len(corpus.Fixtures), len(corpus.InScope))
	}
	if corpus.InScope[0].Name != "app/AccountSet/case" {
		t.Fatalf("fixture name = %q", corpus.InScope[0].Name)
	}
}

func TestCorpusResolverFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		modify func(t *testing.T, root string)
		want   error
	}{
		{
			name: "missing path",
			modify: func(t *testing.T, root string) {
				t.Setenv("GOXRPL_FIXTURES_DIR", filepath.Join(root, "missing"))
			},
			want: os.ErrNotExist,
		},
		{
			name: "not a directory",
			modify: func(t *testing.T, root string) {
				path := filepath.Join(root, "file")
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOXRPL_FIXTURES_DIR", path)
			},
			want: nil,
		},
		{
			name: "empty",
			modify: func(t *testing.T, root string) {
				corpus := filepath.Join(root, "empty")
				if err := os.Mkdir(corpus, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(corpus, corpusManifestName), []byte(contractManifest(0, 0, 0, "{}")), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOXRPL_FIXTURES_DIR", corpus)
			},
			want: errCorpusEmpty,
		},
		{
			name: "wrong oracle commit",
			modify: func(t *testing.T, root string) {
				corpus := writeContractCorpus(t, "app/AccountSet", 1)
				manifest := strings.Replace(contractManifest(1, 1, 0, "{}"), expectedRippledCommit, strings.Repeat("0", 40), 1)
				if err := os.WriteFile(filepath.Join(corpus, corpusManifestName), []byte(manifest), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOXRPL_FIXTURES_DIR", corpus)
			},
			want: nil,
		},
		{
			name: "unknown manifest field",
			modify: func(t *testing.T, root string) {
				corpus := writeContractCorpus(t, "app/AccountSet", 1)
				manifest := strings.TrimSuffix(contractManifest(1, 1, 0, "{}"), "}") + `,"unexpected":true}`
				if err := os.WriteFile(filepath.Join(corpus, corpusManifestName), []byte(manifest), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOXRPL_FIXTURES_DIR", corpus)
			},
			want: nil,
		},
		{
			name: "unknown operation",
			modify: func(t *testing.T, root string) {
				corpus := writeContractCorpus(t, "app/AccountSet", 1)
				fixture := `{"rippled_version":"3.4.0","suite":"app/AccountSet","testcase":"case","steps":[{"op":"not_an_operation"}]}`
				if err := os.WriteFile(filepath.Join(corpus, "app", "AccountSet", "case.json"), []byte(fixture), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOXRPL_FIXTURES_DIR", corpus)
			},
			want: nil,
		},
		{
			name: "unknown amendment",
			modify: func(t *testing.T, root string) {
				corpus := writeContractCorpus(t, "app/AccountSet", 1)
				fixture := `{"rippled_version":"3.4.0","suite":"app/AccountSet","testcase":"case","env":{"amendments_enabled":["notAnAmendment"]},"steps":[{"op":"close"}]}`
				if err := os.WriteFile(filepath.Join(corpus, "app", "AccountSet", "case.json"), []byte(fixture), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOXRPL_FIXTURES_DIR", corpus)
			},
			want: nil,
		},
		{
			name: "no in-scope fixtures",
			modify: func(t *testing.T, root string) {
				corpus := writeContractCorpus(t, "app/Vault", 1)
				t.Setenv("GOXRPL_FIXTURES_DIR", corpus)
			},
			want: errCorpusNoInScope,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.modify(t, root)
			_, err := resolveCorpus(true)
			if err == nil {
				t.Fatal("resolveCorpus unexpectedly succeeded")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCorpusManifestRequiresAuditableProvenanceAndMatrix(t *testing.T) {
	valid := contractManifest(1, 1, 0, "{}")
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{
			name: "legacy fixture version",
			mutate: func(manifest string) string {
				return strings.Replace(manifest, `"fixture_version":"v3"`, `"fixture_version":"v2"`, 1)
			},
		},
		{
			name: "wrong oracle repository",
			mutate: func(manifest string) string {
				return strings.Replace(manifest, "XRPLF/rippled", "other/rippled", 1)
			},
		},
		{
			name: "missing recorder commit",
			mutate: func(manifest string) string {
				return strings.Replace(manifest, "1111111111111111111111111111111111111111", "", 1)
			},
		},
		{
			name: "incomplete amendment matrix",
			mutate: func(manifest string) string {
				return strings.Replace(manifest,
					`[{"fixCleanup3_4_0":false,"LendingProtocolV1_1":false},{"fixCleanup3_4_0":false,"LendingProtocolV1_1":true},{"fixCleanup3_4_0":true,"LendingProtocolV1_1":false},{"fixCleanup3_4_0":true,"LendingProtocolV1_1":true}]`,
					`[{"fixCleanup3_4_0":false,"LendingProtocolV1_1":false}]`, 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, err := decodeCorpusManifest([]byte(test.mutate(valid)))
			if err != nil {
				t.Fatalf("decodeCorpusManifest: %v", err)
			}
			if err := validateCorpusManifest(manifest); err == nil {
				t.Fatal("validateCorpusManifest unexpectedly succeeded")
			}
		})
	}
}

func TestFixtureValidationRejectsMalformedTransactions(t *testing.T) {
	base := func(step Step) Fixture {
		return Fixture{
			RippledVersion: expectedRippledTag,
			Suite:          "app/AccountSet",
			Testcase:       "case",
			Steps:          []Step{step},
		}
	}
	for _, test := range []struct {
		name string
		step Step
	}{
		{name: "empty blob", step: Step{Op: "tx", TxJSON: []byte(`{"TransactionType":"AccountSet"}`), ExpectTER: "temMALFORMED"}},
		{name: "unparseable hex", step: Step{Op: "tx", TxBlob: "00", TxJSON: []byte(`{"TransactionType":"AccountSet"}`), ExpectTER: "temMALFORMED"}},
		{name: "empty retry", step: Step{Op: "retry", TxJSON: []byte(`{"TransactionType":"AccountSet"}`), ExpectTER: "terPRE_SEQ"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := base(test.step)
			if err := validateFixture(&fixture, "contract.json"); err == nil {
				t.Fatal("validateFixture unexpectedly accepted malformed transaction")
			}
		})
	}
}

func TestModifyStateValidationAllowsInferredAccountForDirectoryBump(t *testing.T) {
	step := Step{
		Op: "modify_state",
		ModifyState: &ModifyState{
			BumpLastPage: &BumpLastPage{
				Directory:   "owner",
				TargetPage:  1,
				AdjustField: "IssuerNode",
			},
		},
	}
	if err := validateStep(&step, "contract.json", 0); err != nil {
		t.Fatalf("validateStep rejected an inferred-account directory bump: %v", err)
	}

	step.ModifyState.BumpLastPage = nil
	if err := validateStep(&step, "contract.json", 0); err == nil {
		t.Fatal("validateStep accepted an empty state modification")
	}
}

func TestResultExpectationRejectsUnsupportedBoundaryAndMissingState(t *testing.T) {
	ter := "tesSUCCESS"
	boundary := "rpc"
	code := 0
	applied := true
	queued := false
	fee := uint64(0)
	metadata := ""
	state := strings.Repeat("0", 64)
	expected := &ResultExpectation{
		Boundary:           &boundary,
		TER:                &ter,
		TERCode:            &code,
		Applied:            &applied,
		Queued:             &queued,
		Fee:                &fee,
		MetadataSHA512Half: &metadata,
		StateSHA512Half:    &state,
	}
	if err := validateResultExpectation(expected, ter, "test"); err == nil {
		t.Fatal("unsupported result boundary unexpectedly accepted")
	}
	expected.Boundary = nil
	if err := validateResultExpectation(expected, ter, "test"); err == nil {
		t.Fatal("missing result boundary unexpectedly accepted")
	}
}

func TestReplayResultExpectationRejectsTERMismatch(t *testing.T) {
	boundary := "engine"
	ter := "tesSUCCESS"
	code := 0
	applied := true
	queued := false
	fee := uint64(0)
	metadata := ""
	state := strings.Repeat("0", 64)
	step := Step{
		ExpectTER: "tesSUCCESS",
		ExpectedResult: &ResultExpectation{
			Boundary:           &boundary,
			TER:                &ter,
			TERCode:            &code,
			Applied:            &applied,
			Queued:             &queued,
			Fee:                &fee,
			MetadataSHA512Half: &metadata,
			StateSHA512Half:    &state,
		},
	}
	if err := resultExpectationError(step, jtx.TxResult{Code: "tefFAILURE"}, state); err == nil {
		t.Fatal("replay TER mismatch unexpectedly accepted")
	}
}

func TestDependencyChainFailuresAreFatalErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app", "AccountSet"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "app", "AccountSet", "target.json")
	writeFixture := func(path, depends string) {
		fixture := fmt.Sprintf(`{"rippled_version":"3.4.0","suite":"app/AccountSet","testcase":%q,"depends_on":%q,"steps":[{"op":"close"}]}`, filepath.Base(path), depends)
		if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeFixture(target, "missing")
	if _, err := loadDependsOnChain(t, target, "missing"); err == nil {
		t.Fatal("missing dependency unexpectedly succeeded")
	}

	writeFixture(filepath.Join(root, "app", "AccountSet", "a.json"), "b")
	writeFixture(filepath.Join(root, "app", "AccountSet", "b.json"), "a")
	if _, err := loadDependsOnChain(t, filepath.Join(root, "app", "AccountSet", "a.json"), "b"); err == nil {
		t.Fatal("cyclic dependency unexpectedly succeeded")
	}

	if err := os.WriteFile(filepath.Join(root, "app", "AccountSet", "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDependsOnChain(t, target, "bad"); err == nil {
		t.Fatal("malformed dependency unexpectedly succeeded")
	}
}

func writeContractCorpus(t *testing.T, suite string, count int) string {
	t.Helper()
	root := t.TempDir()
	fixtureDir := filepath.Join(root, filepath.FromSlash(suite))
	if err := os.MkdirAll(fixtureDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := fmt.Sprintf(`{"rippled_version":"3.4.0","suite":%q,"testcase":"case","steps":[{"op":"close"}]}`, suite)
	if err := os.WriteFile(filepath.Join(fixtureDir, "case.json"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	inScope, skipped, reasons := count, 0, "{}"
	if suite == "app/Vault" {
		inScope, skipped, reasons = 0, count, `{"app/Vault/case":"out-of-scope suite"}`
	}
	manifest := contractManifest(count, inScope, skipped, reasons)
	if err := os.WriteFile(filepath.Join(root, corpusManifestName), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func contractManifest(total, inScope, skipped int, skipReasons string) string {
	return fmt.Sprintf(`{"schema":2,"fixture_version":"v3","oracle_repository":"XRPLF/rippled","rippled_tag":"3.4.0","rippled_commit":"4a4fded2eba11427c48ce3f24d9c1aea5e7a9d17","recorder_commit":"1111111111111111111111111111111111111111","build_identity":"rippled-3.4.0-final","config_identity":"cleanup+lending matrix","amendment_matrix":[{"fixCleanup3_4_0":false,"LendingProtocolV1_1":false},{"fixCleanup3_4_0":false,"LendingProtocolV1_1":true},{"fixCleanup3_4_0":true,"LendingProtocolV1_1":false},{"fixCleanup3_4_0":true,"LendingProtocolV1_1":true}],"fixture_count":%d,"in_scope_fixture_count":%d,"skipped_fixture_count":%d,"skip_reasons":%s}`, total, inScope, skipped, skipReasons)
}
