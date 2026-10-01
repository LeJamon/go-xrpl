package conformance

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcceptanceEvidenceRequiresEveryProducer(t *testing.T) {
	script, repo, sha := acceptanceEvidenceRepo(t)
	jobs := []string{
		"lint", "lint-advisory", "generate", "build", "build-386", "postgres", "test",
		"test-mpt-crypto", "peer-interop", "peer-interop-final", "consensus-smoke",
		"consensus-smoke-final", "test-repeated", "conformance-final",
	}
	producers := []string{
		"lint", "lint-advisory", "generate", "build", "build-386", "postgres", "test-integration-offer",
		"test-integration", "test-tx", "test-core", "test-libs",
		"test-mpt-crypto-ubuntu-latest", "test-mpt-crypto-macos-latest",
		"peer-interop", "peer-interop-final", "consensus-smoke-3.3.0", "consensus-smoke-3.2.0",
		"consensus-smoke-final", "test-repeated", "conformance-final",
	}
	type evidenceCase struct {
		name, missing, replace, with, diagnostic string
	}
	cases := make([]evidenceCase, 0, 4+len(producers))
	cases = append(cases, []evidenceCase{
		{name: "complete"},
		{name: "failed_producer", replace: "status=success", with: "status=failure", diagnostic: "producer_failed="},
		{name: "dirty_producer", replace: "go_dirty=false", with: "go_dirty=true", diagnostic: "producer_dirty="},
		{name: "stale_producer", replace: "tested_sha=" + sha, with: "tested_sha=" + strings.Repeat("0", 40), diagnostic: "producer_sha_mismatch="},
	}...)
	for _, producer := range producers {
		cases = append(cases, evidenceCase{name: "missing_" + producer, missing: producer, diagnostic: "producer_evidence_missing=" + producer + ".txt"})
	}
	for _, tc := range cases {
		name, missing := tc.name, tc.missing
		t.Run(name, func(t *testing.T) {
			evidenceDir := t.TempDir()
			if err := os.Mkdir(filepath.Join(evidenceDir, "producers"), 0o755); err != nil {
				t.Fatal(err)
			}
			write := func(name, content string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(evidenceDir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("needs-results.txt", strings.Join(jobs, "=success\n")+"=success\n")
			write("workflow-commands.txt", "fixture commands\n")
			write(filepath.Join("producers", "producer-test-libs.git-status.txt"), "?? diagnostic-only.txt\n")
			for _, producer := range producers {
				if producer != missing {
					content := fmt.Sprintf("tested_sha=%s\ngo_dirty=false\nstatus=success\n", sha)
					if isFinalOracleProducer(producer) {
						content += "oracle_repository=XRPLF/xrpld-private\noracle_tag=3.4.1\noracle_commit=d147fccf54a500fce586522f28d6044c37fd8d29\n"
					}
					if producer == "test-core" && tc.replace != "" {
						content = strings.ReplaceAll(content, tc.replace, tc.with)
					}
					write(filepath.Join("producers", "producer-"+producer+".txt"), content)
				}
			}
			cmd := exec.CommandContext(t.Context(), "bash", script, "aggregate")
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "EVIDENCE_DIR="+evidenceDir, "EXPECTED_SHA="+sha)
			output, runErr := cmd.CombinedOutput()
			if name == "complete" {
				if runErr != nil {
					t.Fatalf("complete evidence rejected: %v\n%s", runErr, output)
				}
				return
			}
			if runErr == nil {
				t.Fatalf("accepted invalid evidence: %s", name)
			}
			manifest, err := os.ReadFile(filepath.Join(evidenceDir, "final-acceptance.txt"))
			if err != nil {
				t.Fatal(err)
			}
			want := tc.diagnostic
			if !strings.Contains(string(manifest), want) {
				t.Fatalf("invalid evidence was not diagnosed: want %q in\n%s\ncommand: %v\n%s", want, manifest, runErr, output)
			}
		})
	}
}

func TestAcceptanceEvidenceRejectsMixedOracle(t *testing.T) {
	script, repo, sha := acceptanceEvidenceRepo(t)
	evidenceDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(evidenceDir, "producers"), 0o755); err != nil {
		t.Fatal(err)
	}
	jobs := []string{
		"lint", "lint-advisory", "generate", "build", "build-386", "postgres", "test",
		"test-mpt-crypto", "peer-interop", "peer-interop-final", "consensus-smoke",
		"consensus-smoke-final", "test-repeated", "conformance-final",
	}
	producers := []string{
		"lint", "lint-advisory", "generate", "build", "build-386", "postgres", "test-integration-offer",
		"test-integration", "test-tx", "test-core", "test-libs",
		"test-mpt-crypto-ubuntu-latest", "test-mpt-crypto-macos-latest",
		"peer-interop", "peer-interop-final", "consensus-smoke-3.3.0", "consensus-smoke-3.2.0",
		"consensus-smoke-final", "test-repeated", "conformance-final",
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(evidenceDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("needs-results.txt", strings.Join(jobs, "=success\n")+"=success\n")
	write("workflow-commands.txt", "fixture commands\n")
	for _, producer := range producers {
		content := fmt.Sprintf("tested_sha=%s\ngo_dirty=false\nstatus=success\n", sha)
		switch producer {
		case "peer-interop-final":
			content += "oracle_repository=XRPLF/rippled\noracle_tag=3.4.0\noracle_commit=4a4fded2eba11427c48ce3f24d9c1aea5e7a9d17\n"
		case "consensus-smoke-final":
			content += "oracle_repository=XRPLF/xrpld-private\noracle_tag=3.4.0\noracle_commit=d147fccf54a500fce586522f28d6044c37fd8d29\n"
		case "conformance-final":
			content += "oracle_repository=XRPLF/xrpld-private\noracle_tag=3.4.1\noracle_commit=4a4fded2eba11427c48ce3f24d9c1aea5e7a9d17\n"
		}
		write(filepath.Join("producers", "producer-"+producer+".txt"), content)
	}
	cmd := exec.CommandContext(t.Context(), "bash", script, "aggregate")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "EVIDENCE_DIR="+evidenceDir, "EXPECTED_SHA="+sha)
	output, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("accepted mixed oracle evidence")
	}
	manifest, err := os.ReadFile(filepath.Join(evidenceDir, "final-acceptance.txt"))
	if err != nil {
		t.Fatal(err)
	}
	manifestText := string(manifest)
	if strings.Count(manifestText, "producer_oracle_mismatch=") < 3 {
		t.Fatalf("mixed oracle identity was not diagnosed:\n%s\ncommand: %v\n%s", manifest, runErr, output)
	}
	for _, producer := range []string{"peer-interop-final", "consensus-smoke-final", "conformance-final"} {
		if !strings.Contains(manifestText, "producer-"+producer+".txt") {
			t.Fatalf("mixed oracle identity was not diagnosed for %s:\n%s", producer, manifest)
		}
	}
}

func isFinalOracleProducer(name string) bool {
	switch name {
	case "peer-interop-final", "consensus-smoke-final", "conformance-final":
		return true
	default:
		return false
	}
}

func TestAcceptanceEvidenceRequiresCleanCandidate(t *testing.T) {
	for _, state := range []string{"missing_sha", "wrong_sha", "dirty"} {
		t.Run(state, func(t *testing.T) {
			script, repo, sha := acceptanceEvidenceRepo(t)
			want := "must identify the candidate"
			switch state {
			case "missing_sha":
				sha = ""
			case "wrong_sha":
				sha = strings.Repeat("0", 40)
				want = "does not match the candidate"
			case "dirty":
				if err := os.WriteFile(filepath.Join(repo, "untracked.go"), []byte("package fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "checkout must be clean"
			}
			cmd := exec.CommandContext(t.Context(), "bash", script, "aggregate")
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "EVIDENCE_DIR="+t.TempDir(), "EXPECTED_SHA="+sha, "GITHUB_SHA=")
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), want) {
				t.Fatalf("want rejection %q: %v\n%s", want, err, output)
			}
		})
	}
}

func TestAcceptanceEvidenceRejectsDirtyCorpus(t *testing.T) {
	for _, state := range []string{"clean", "modified", "untracked", "ignored"} {
		t.Run(state, func(t *testing.T) {
			script, repo, _ := acceptanceEvidenceRepo(t)
			fixture := filepath.Join(repo, "fixture.json")
			if err := os.WriteFile(fixture, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{
				{"add", "fixture.json"},
				{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "corpus"},
			} {
				cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, output)
				}
			}
			cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", "HEAD")
			sha, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if state != "clean" {
				path := fixture
				if state == "untracked" || state == "ignored" {
					path = filepath.Join(repo, "extra.json")
				}
				if state == "ignored" {
					if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("extra.json\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "jq"), []byte("#!/bin/sh\necho manifest-validation-reached >&2\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd = exec.CommandContext(t.Context(), "bash", script, "conformance")
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"EVIDENCE_DIR="+t.TempDir(), "FINAL_CONFORMANCE_CORPUS="+repo,
				"FINAL_CONFORMANCE_REPOSITORY=fixture/corpus", "FINAL_CONFORMANCE_COMMIT="+strings.TrimSpace(string(sha)),
				"FINAL_CONFORMANCE_MANIFEST="+fixture, "ORACLE_REPOSITORY=XRPLF/xrpld-private", "ORACLE_TAG=3.4.1",
				"ORACLE_COMMIT=d147fccf54a500fce586522f28d6044c37fd8d29")
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("incomplete corpus unexpectedly accepted")
			}
			want := "final conformance corpus must be clean"
			if state == "clean" {
				want = "manifest-validation-reached"
			}
			if !strings.Contains(string(output), want) {
				t.Fatalf("want %q in output: %v\n%s", want, err, output)
			}
		})
	}
}

func TestProducerEvidenceTracksCorpusAndExcludesReferenceCheckouts(t *testing.T) {
	script, repo, sha := acceptanceEvidenceRepo(t)
	for _, path := range []string{
		"rippled-worktrees/v3.2.0-oracle", "rippled-worktrees/v3.3.0-oracle",
		"rippled-worktrees/v3.4.0-oracle", "rippled-worktrees/v3.4.1-oracle",
		"internal/testing/conformance/testdata/rippled-3.4.1-v4",
	} {
		dir := filepath.Join(repo, path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "reference.txt"), []byte("reference"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	corpusFile := filepath.Join(repo, "internal/testing/conformance/testdata/rippled-3.4.1-v4/reference.txt")
	for _, args := range [][]string{
		{"add", "--", "internal/testing/conformance/testdata/rippled-3.4.1-v4/reference.txt"},
		{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "corpus"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	for _, dirty := range []string{"clean", "untracked", "corpus"} {
		t.Run("dirty_"+dirty, func(t *testing.T) {
			t.Cleanup(func() {
				if err := os.Remove(filepath.Join(repo, "unexpected.txt")); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Errorf("remove untracked fixture: %v", err)
				}
				if err := os.WriteFile(corpusFile, []byte("reference"), 0o600); err != nil {
					t.Errorf("restore corpus fixture: %v", err)
				}
			})
			switch dirty {
			case "untracked":
				if err := os.WriteFile(filepath.Join(repo, "unexpected.txt"), []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "corpus":
				if err := os.WriteFile(corpusFile, []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			evidenceDir := t.TempDir()
			cmd := exec.CommandContext(t.Context(), "bash", script, "producer")
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "EVIDENCE_DIR="+evidenceDir, "EXPECTED_SHA="+sha,
				"EVIDENCE_LABEL=test-libs", "EVIDENCE_STATUS=success", "EVIDENCE_COMMAND=fixture")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("producer: %v\n%s", err, output)
			}
			metadata, err := os.ReadFile(filepath.Join(evidenceDir, "producer-test-libs.txt"))
			if err != nil {
				t.Fatal(err)
			}
			wantDirty := dirty != "clean"
			if want := fmt.Sprintf("go_dirty=%t\n", wantDirty); !strings.Contains(string(metadata), want) {
				t.Fatalf("want %q in producer metadata:\n%s", want, metadata)
			}
		})
	}
}

func acceptanceEvidenceRepo(t *testing.T) (script, repo, sha string) {
	t.Helper()
	script, err := filepath.Abs("../../../scripts/acceptance/final-evidence.sh")
	if err != nil {
		t.Fatal(err)
	}
	repo = t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = repo
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	runGit("init", "--quiet")
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "fixture")
	return script, repo, runGit("rev-parse", "HEAD")
}
