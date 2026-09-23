package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHasCompleteLedgerHashRequiresMatchingHashAndCompleteRange(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	hash := [32]byte{1}
	require.False(t, svc.HasCompleteLedgerHash(10, hash))
	svc.completeMu.Lock()
	svc.ensureCompleteLedgerStateLocked()
	svc.completeLedgerHashes[10] = hash
	svc.completeMu.Unlock()
	require.False(t, svc.HasCompleteLedgerHash(10, hash))
	seedCompleteLedgers(t, svc, 10, 11)
	require.True(t, svc.HasCompleteLedgerHash(10, hash))
	require.False(t, svc.HasCompleteLedgerHash(10, [32]byte{2}))
	require.False(t, svc.HasCompleteLedgerHash(11, hash))
	require.False(t, svc.HasCompleteLedgerHash(11, [32]byte{}))
}
