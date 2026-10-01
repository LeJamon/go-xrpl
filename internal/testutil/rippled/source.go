// Package rippled locates pinned protocol source files for inventory tests.
package rippled

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	commit      = "d147fccf54a500fce586522f28d6044c37fd8d29"
	priorCommit = "4a4fded2eba11427c48ce3f24d9c1aea5e7a9d17"
)

// These files are byte-identical at private 3.4.1 and public 3.4.0. The
// equivalence is limited to these inventory inputs, not release behavior.
var sourceHashes = map[string]string{
	"include/xrpl/protocol/SField.h":                    "0566abd201da2c6525b095de1d92a27ad9e4f9d180885583657e9ea9995514e4",
	"include/xrpl/protocol/detail/sfields.macro":        "777a602cb7b2359c6cf7d5f9fa38b0d7782814f9ef684d10a849c166889e76df",
	"include/xrpl/protocol/detail/transactions.macro":   "33be64de6196c9af8a127b8787d3b5641ec5ab6ddc59e45ee9c5ca7932a9a9a7",
	"include/xrpl/protocol/detail/ledger_entries.macro": "1d88299fa88e7b8e276ad0b301c3c8220d2478dfb38f3c14478a189e80a92c76",
	"include/xrpl/protocol/TER.h":                       "ed20bbdf5e6a8a474861612d0aa928fcb4e1b574db5ba35edd4b15b6736cef8b",
	"include/xrpl/protocol/LedgerFormats.h":             "1d39f4352f32fcf71bfa0c03552893f194561da00b4fbfc19989426e8d687c7d",
	"src/libxrpl/protocol/LedgerFormats.cpp":            "6775a368d73f659e81ddef30b742489d7f7bcac96089296e747aaab34b983b28",
}

// File returns an inventory input verified against private rippled 3.4.1.
// GOXRPL_ORACLE_DIR selects an explicit checkout of that release. Public CI
// may use the pinned 3.4.0 checkout only for the byte-identical files above.
func File(t testing.TB, relative string) string {
	t.Helper()
	if _, ok := sourceHashes[relative]; !ok {
		t.Fatalf("no private rippled 3.4.1 source hash for %q", relative)
	}
	root := os.Getenv("GOXRPL_ORACLE_DIR")
	wantCommit := commit
	if root == "" {
		_, caller, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("resolve oracle helper source path")
		}
		root = findCheckout(filepath.Dir(caller), "v3.4.1-oracle")
		if root == "" {
			root = findCheckout(filepath.Dir(caller), "v3.4.0-oracle")
			wantCommit = priorCommit
		}
	}
	if root == "" {
		t.Fatal("private rippled 3.4.1 oracle not found; set GOXRPL_ORACLE_DIR")
	}
	path, err := verifiedFile(t.Context(), root, relative, wantCommit)
	if err != nil {
		t.Fatal(err)
	}
	if wantCommit == priorCommit {
		t.Logf("%s: public 3.4.0 bytes match pinned private 3.4.1 SHA-256 %s", relative, sourceHashes[relative])
	}
	return path
}

func findCheckout(start, name string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "rippled-worktrees", name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		if parent := filepath.Dir(dir); parent == dir {
			return ""
		}
	}
}

func verifiedFile(ctx context.Context, root, relative, wantCommit string) (string, error) {
	wantHash, ok := sourceHashes[relative]
	if !ok {
		return "", fmt.Errorf("unrecorded oracle source file %q", relative)
	}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "HEAD"}, wantCommit},
		{[]string{"status", "--porcelain=v1", "--untracked-files=all"}, ""},
	} {
		args := append([]string{"-C", root}, check.args...)
		// #nosec G702 -- git is fixed; the trusted checkout path is one argument, never shell code.
		out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != check.want {
			return "", fmt.Errorf("oracle %s: git %v: got %q, want %q (error: %v)", root, check.args, strings.TrimSpace(string(out)), check.want, err)
		}
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	dir, err := os.OpenRoot(root)
	if err != nil {
		return "", fmt.Errorf("open oracle checkout: %w", err)
	}
	defer dir.Close()
	file, err := dir.Open(filepath.FromSlash(relative))
	if err != nil {
		return "", fmt.Errorf("open oracle source: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("read oracle source: %w", err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != wantHash {
		return "", fmt.Errorf("oracle %s SHA-256 = %s, want private 3.4.1 %s", relative, got, wantHash)
	}
	return path, nil
}
