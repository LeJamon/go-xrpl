package rippled

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedSourceRejectsWrongIdentityAndContent(t *testing.T) {
	const relative = "include/xrpl/protocol/LedgerFormats.h"
	data, err := os.ReadFile(File(t, relative))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		args = append([]string{"-C", root}, args...)
		out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	commitFixture := func() string {
		t.Helper()
		git("add", relative)
		git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
		return git("rev-parse", "HEAD")
	}
	sha := commitFixture()
	if got, err := verifiedFile(t.Context(), root, relative, sha); err != nil || got != path {
		t.Fatalf("verified inventory source rejected: path=%s, error=%v", got, err)
	}
	for _, test := range []struct {
		name, relative, sha, diagnostic string
	}{
		{"wrong_commit", relative, commit, "rev-parse"},
		{"unrecorded_file", "include/xrpl/protocol/detail/features.macro", sha, "unrecorded"},
		{"path_traversal", "../LedgerFormats.h", sha, "unrecorded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := verifiedFile(t.Context(), root, test.relative, test.sha); err == nil || !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("want rejection containing %q, got %v", test.diagnostic, err)
			}
		})
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedFile(t.Context(), root, relative, sha); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("want dirty source rejection, got %v", err)
	}
	sha = commitFixture()
	if _, err := verifiedFile(t.Context(), root, relative, sha); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("want changed content rejection, got %v", err)
	}
}
