package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixturesRoot is the path to the xrpl-fixtures directory relative to this test file.
// Adjust this if the fixtures are located elsewhere.
const fixturesRoot = "../../../../../../../fixtures/rippled-2.6.2-v2"

func TestConformance(t *testing.T) {
	root, err := filepath.Abs(fixturesRoot)
	if err != nil {
		t.Fatalf("Failed to resolve fixtures root: %v", err)
	}

	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skipf("Fixtures directory not found at %s — skipping conformance tests", root)
	}

	// Walk the fixtures directory and create a subtest per fixture file
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		// Build test name from relative path: "app/Escrow/Lockup"
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		testName := strings.TrimSuffix(rel, ".json")

		fixturePath := path
		t.Run(testName, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("PANIC: %v", fmt.Sprintf("%v", r))
				}
			}()
			RunFixture(t, fixturePath)
		})

		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk fixtures directory: %v", err)
	}
}
