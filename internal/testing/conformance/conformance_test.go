package conformance

import (
	"fmt"
	"testing"
)

func TestConformance(t *testing.T) {
	corpus, err := resolveCorpus(conformanceCorpusRequired())
	if err != nil {
		if !conformanceCorpusRequired() && err == errCorpusNotConfigured {
			t.Skip("recorded-rippled fixture corpus not configured; set GOXRPL_FIXTURES_DIR")
		}
		t.Fatalf("conformance corpus rejected: %v", err)
	}
	for _, fixture := range corpus.InScope {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("PANIC: %v", fmt.Sprintf("%v", recovered))
				}
			}()
			RunFixture(t, fixture.Path)
		})
	}
}
