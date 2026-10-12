package replaytool

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	ledgertestutil "github.com/LeJamon/go-xrpl/internal/ledger/testutil"
)

func newTestExecutionStore(t testing.TB) *ledger.ExecutionStore {
	t.Helper()
	return ledgertestutil.NewExecutionStore(t, nil)
}
