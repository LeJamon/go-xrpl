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
	script, err := filepath.Abs("../../../scripts/acceptance/final-evidence.sh")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
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
	sha := runGit("rev-parse", "HEAD")
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
