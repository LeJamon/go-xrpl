package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestPreparedReplayMapCapacityFollowsContainerLifetime(t *testing.T) {
	pool := membudget.New(512 << 20)
	parent, err := pool.NewAccount("acquisition", 512<<20, 512<<20)
	require.NoError(t, err)
	budget, err := shamap.NewAcquisitionBudgetWithParent(512<<20, parent)
	require.NoError(t, err)
	c := &catchupReplayCoordinator{acquisitionMemory: budget}

	require.True(t, budget.TryReserveReplayHead(128<<20))
	c.acquisitionMu.Lock()
	admitted := c.prepareStandardReplayEntriesLocked()
	c.acquisitionMu.Unlock()
	require.False(t, admitted)
	require.Nil(t, c.standardReplay.entries, "deny before allocating the prepared map")
	budget.ReleaseReplayHead(128 << 20)

	c.acquisitionMu.Lock()
	admitted = c.prepareStandardReplayEntriesLocked()
	c.acquisitionMu.Unlock()
	require.True(t, admitted)
	require.NotNil(t, c.standardReplay.entries)
	require.Equal(t, uint64(standardReplayMapMemoryBytes), budget.Snapshot().Used)

	c.acquisitionMu.Lock()
	for seq := uint32(1); seq <= standardReplayPreparedLimit; seq++ {
		c.standardReplay.entries[seq] = nil
	}
	clear(c.standardReplay.entries)
	c.acquisitionMu.Unlock()
	require.Empty(t, c.standardReplay.entries)
	require.Equal(t, uint64(standardReplayMapMemoryBytes), budget.Snapshot().Used,
		"deleting entries does not release retained map capacity")

	c.acquisitionMu.Lock()
	c.dropStandardReplayEntriesLocked()
	c.dropStandardReplayEntriesLocked()
	c.acquisitionMu.Unlock()
	require.Zero(t, budget.Snapshot().Used)
	require.True(t, budget.Close())
	require.Zero(t, pool.Snapshot().Used)
	require.Zero(t, pool.Snapshot().InvalidReleases)
}
