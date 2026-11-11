package engine

import (
	"os"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/all"
)

// TestMain registers every transaction type: per-type attributes such as
// invariant privileges come from the registered descriptors.
func TestMain(m *testing.M) {
	all.RegisterAll()
	os.Exit(m.Run())
}
