package conformance

import (
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
		"lint", "generate", "build", "build-386", "postgres", "test", "test-purego",
		"test-mpt-crypto", "peer-interop", "peer-interop-final", "consensus-smoke",
		"consensus-smoke-final", "test-repeated", "conformance-final",
	}
	producers := []string{
		"lint", "generate", "build", "build-386", "postgres", "test-integration-offer",
		"test-integration", "test-tx", "test-core", "test-libs", "test-purego",
		"test-mpt-crypto-ubuntu-latest", "test-mpt-crypto-macos-latest",
		"peer-interop", "peer-interop-final", "consensus-smoke-3.3.0",
		"consensus-smoke-3.2.0", "consensus-smoke-final", "test-repeated", "conformance-final",
	}
	for _, missing := range append([]string{""}, producers...) {
		name := "complete"
		if missing != "" {
			name = "missing_" + missing
		}
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
					write(filepath.Join("producers", "producer-"+producer+".txt"),
						fmt.Sprintf("tested_sha=%s\ngo_dirty=false\nstatus=success\n", sha))
				}
			}
			cmd := exec.CommandContext(t.Context(), "bash", script, "aggregate")
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "EVIDENCE_DIR="+evidenceDir, "EXPECTED_SHA="+sha)
			output, runErr := cmd.CombinedOutput()
			if missing == "" {
				if runErr != nil {
					t.Fatalf("complete evidence rejected: %v\n%s", runErr, output)
				}
				return
			}
			if runErr == nil {
				t.Fatalf("accepted evidence without producer %s", missing)
			}
			manifest, err := os.ReadFile(filepath.Join(evidenceDir, "final-acceptance.txt"))
			if err != nil {
				t.Fatal(err)
			}
			want := "producer_evidence_missing=" + missing + ".txt\n"
			if !strings.Contains(string(manifest), want) {
				t.Fatalf("missing producer was not diagnosed: want %q in\n%s\ncommand: %v\n%s", want, manifest, runErr, output)
			}
		})
	}
}

func TestProducerEvidenceExcludesReferenceCheckouts(t *testing.T) {
	script, repo, sha := acceptanceEvidenceRepo(t)
	for _, path := range []string{
		"rippled-worktrees/v3.2.0-oracle", "rippled-worktrees/v3.3.0-oracle",
		"rippled-worktrees/v3.4.0-oracle", "fixtures/rippled-3.4.0-v3",
	} {
		dir := filepath.Join(repo, path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "reference.txt"), []byte("reference"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dirty := range []bool{false, true} {
		t.Run(fmt.Sprintf("dirty_%t", dirty), func(t *testing.T) {
			if dirty {
				if err := os.WriteFile(filepath.Join(repo, "unexpected.txt"), []byte("changed"), 0o600); err != nil {
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
			if want := fmt.Sprintf("go_dirty=%t\n", dirty); !strings.Contains(string(metadata), want) {
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
