package conformance

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// diffCorpusRoot resolves the recorded-rippled fixture corpus directory, or ""
// if it cannot be found. GOXRPL_FIXTURES_DIR overrides the location (useful from
// a git worktree, where the default relative path does not reach the sibling
// fixtures tree); otherwise it falls back to the same default TestConformance
// uses.
func diffCorpusRoot() string {
	if dir := os.Getenv("GOXRPL_FIXTURES_DIR"); dir != "" {
		if abs, err := filepath.Abs(dir); err == nil && isDir(abs) {
			return abs
		}
		return ""
	}
	if abs, err := filepath.Abs(fixturesRoot); err == nil && isDir(abs) {
		return abs
	}
	return ""
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// outOfScopeSuites loads the suite paths conformance treats as out of scope
// (scripts/conformance-out-of-scope.txt), so the differential fuzzer does not
// flag intentional stubs / known gaps (e.g. Vault, XChain) as divergences.
func outOfScopeSuites() map[string]bool {
	set := map[string]bool{}
	path, err := filepath.Abs("../../../scripts/conformance-out-of-scope.txt")
	if err != nil {
		return set
	}
	f, err := os.Open(path)
	if err != nil {
		return set
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		set[strings.ReplaceAll(line, " ", "")] = true
	}
	return set
}

// suiteOf returns the "app/<Suite>" (or "ledger/<Suite>") prefix of a fixture's
// relative name, matching conformance-summary.sh's suite bucketing.
func suiteOf(relName string) string {
	parts := strings.Split(relName, "/")
	if len(parts) < 2 {
		return relName
	}
	return parts[0] + "/" + parts[1]
}

// inScopeFixtures returns the relative names and absolute paths of every fixture
// under root that is in scope (suite not out of scope, and not in skipTests),
// sorted by name for stable indexing.
func inScopeFixtures(root string) (names, paths []string) {
	oos := outOfScopeSuites()
	relToPath := map[string]string{}
	var rels []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		name := strings.TrimSuffix(rel, ".json")
		if oos[suiteOf(name)] || skipTests[name] != "" {
			return nil
		}
		rels = append(rels, name)
		relToPath[name] = path
		return nil
	})
	sort.Strings(rels)
	for _, r := range rels {
		names = append(names, r)
		paths = append(paths, relToPath[r])
	}
	return names, paths
}

// FuzzEngineDifferential is the differential-vs-rippled property (issue #682,
// scope 2). It replays recorded rippled fixtures through the goXRPL engine and
// fails when the per-step transaction result (TER) or recorded post-state does
// not match rippled's recorded values -- an in-scope divergence is a
// conformance/fork bug. The byte input selects which in-scope fixture to replay,
// so coverage feedback steers toward fixtures that exercise new engine paths;
// the recorded tx bytes are deliberately not mutated, which would change the
// outcome and lose the rippled oracle.
//
// Out-of-scope suites (scripts/conformance-out-of-scope.txt) are excluded so the
// fuzzer does not re-report intentional stubs. Requires the recorded-rippled
// fixture corpus (the same `just conformance` uses): set GOXRPL_FIXTURES_DIR to
// point at it, or run from the main checkout. Skips when the corpus is absent so
// plain `go test` / CI stay green.
func FuzzEngineDifferential(f *testing.F) {
	root := diffCorpusRoot()
	if root == "" {
		f.Skip("recorded-rippled fixture corpus not found; set GOXRPL_FIXTURES_DIR")
	}
	names, paths := inScopeFixtures(root)
	if len(names) == 0 {
		f.Skip("no in-scope fixtures found in corpus")
	}

	// Seed with one fixture from each of a few suites conformance passes in
	// full, so plain `go test` exercises the harness without tripping on a
	// pre-existing in-scope gap. Active fuzzing explores the whole in-scope set.
	for _, prefix := range []string{"app/Escrow/", "app/Oracle/", "app/Check/", "app/MultiSign/", "app/PayChan/"} {
		for i, name := range names {
			if strings.HasPrefix(name, prefix) {
				f.Add(uint32(i))
				break
			}
		}
	}

	f.Fuzz(func(t *testing.T, sel uint32) {
		idx := int(sel % uint32(len(paths)))
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("PANIC replaying %s: %v", names[idx], r)
			}
		}()
		RunFixture(t, paths[idx])
	})
}
