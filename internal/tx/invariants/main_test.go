package invariants_test

import (
	"os"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/all"
)

// TestMain registers every transaction type: the checks consult descriptor
// privileges, and the ledger-driven tests apply real transactions. It lives in
// the external test package because the batch package depends on this one.
func TestMain(m *testing.M) {
	all.RegisterAll()
	os.Exit(m.Run())
}
