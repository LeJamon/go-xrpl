package node

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/stretchr/testify/assert"
)

func TestRPCFastSyncMetricsMapsPivotDiscovery(t *testing.T) {
	snapshot := adaptor.FastSyncMetrics{
		ReplayPipelinePivotNodesExamined:    11,
		ReplayPipelinePivotEqualSubtrees:    22,
		ReplayPipelinePivotMissingNodes:     33,
		ReplayPipelinePivotStateDownloaded:  44,
		ReplayPipelinePivotStateNodesPerSec: 55,
	}

	metrics := rpcFastSyncMetrics(snapshot)
	assert.Equal(t, snapshot.ReplayPipelinePivotNodesExamined, metrics.ReplayPipelinePivotNodesExamined)
	assert.Equal(t, snapshot.ReplayPipelinePivotEqualSubtrees, metrics.ReplayPipelinePivotEqualSubtrees)
	assert.Equal(t, snapshot.ReplayPipelinePivotMissingNodes, metrics.ReplayPipelinePivotMissingNodes)
	assert.Equal(t, snapshot.ReplayPipelinePivotStateDownloaded, metrics.ReplayPipelinePivotStateDownloaded)
	assert.Equal(t, snapshot.ReplayPipelinePivotStateNodesPerSec, metrics.ReplayPipelinePivotStateNodesPerSec)
}
