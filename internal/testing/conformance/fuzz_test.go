package conformance

import "testing"

// FuzzEngineDifferential selects immutable signed oracle inputs. Mutating a
// transaction would invalidate the corresponding recorded expectations.
func FuzzEngineDifferential(f *testing.F) {
	corpus, err := resolvePinnedCorpus(conformanceCorpusRequired())
	if err != nil {
		f.Fatalf("conformance corpus rejected: %v", err)
	}
	var fixtures []snapshotFixture
	for _, c := range corpus.Cases {
		if c.Pin.ExcludeReason == "" {
			f.Add(uint32(len(fixtures)))
			fixtures = append(fixtures, c.Fixture)
		}
	}
	if len(fixtures) == 0 {
		f.Fatal("conformance corpus has no executable fixtures")
	}
	f.Fuzz(func(t *testing.T, selection uint32) {
		fixture := fixtures[selection%uint32(len(fixtures))]
		if err := runSnapshotFixture(fixture); err != nil {
			t.Fatalf("%s/%s: %v", fixture.Suite, fixture.Testcase, err)
		}
	})
}
