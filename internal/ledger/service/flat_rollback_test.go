package service

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFlatRecoveryBeforeLegacyRollback(t *testing.T) {
	paths := newBoundedTestPaths(t)
	first := newBoundedTestRuntime(t, paths, newBoundedTestRepository(t, paths), boundedFlatOptions())
	t.Cleanup(func() { first.close(t) })
	require.NoError(t, first.service.Start())
	_, err := first.service.AcceptLedger(t.Context())
	require.NoError(t, err)
	want := first.service.GetClosedLedger().Hash()
	first.close(t)

	restarted := newBoundedTestRuntimeWithStartup(t, paths, newBoundedTestRepository(t, paths), boundedFlatOptions(), StartupConfig{Mode: StartupLoad, Ledger: hex.EncodeToString(want[:])})
	t.Cleanup(func() { restarted.close(t) })
	restarted.service.config.FlatRecoveryOnly = true
	recovered := false
	restarted.service.config.OnFlatRecovery = func() error {
		recovered = true
		pending, err := restarted.flat.PendingOutbox(t.Context(), 0)
		require.NoError(t, err)
		require.Empty(t, pending)
		return nil
	}
	require.NoError(t, restarted.service.Start())
	require.True(t, recovered)
	require.Equal(t, want, restarted.service.GetClosedLedger().Hash())
	require.False(t, restarted.service.GetClosedLedger().UsesBoundedExecution())
	_, err = restarted.service.AcceptLedger(t.Context())
	require.NoError(t, err)
}
