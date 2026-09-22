package conformance

import (
	"errors"
	"strings"
	"testing"
)

// suiteOf returns the "app/<Suite>" (or "ledger/<Suite>") prefix of a fixture's
// relative name, matching conformance-summary.sh's suite bucketing.
func suiteOf(relName string) string {
	parts := strings.Split(relName, "/")
	if len(parts) < 2 {
		return relName
	}
	return parts[0] + "/" + parts[1]
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
	corpus, err := resolveCorpus(false)
	if err != nil {
		if errors.Is(err, errCorpusNotConfigured) || errors.Is(err, errCorpusNoInScope) {
			f.Skip("recorded-rippled fixture corpus is not configured or has no in-scope fixtures")
		}
		f.Fatalf("conformance corpus rejected: %v", err)
	}
	names := make([]string, 0, len(corpus.InScope))
	paths := make([]string, 0, len(corpus.InScope))
	for _, fixture := range corpus.InScope {
		names = append(names, fixture.Name)
		paths = append(paths, fixture.Path)
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
