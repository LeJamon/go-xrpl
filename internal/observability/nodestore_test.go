package observability

import (
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestNodeStoreCollectorReportsClassesAndLifetime(t *testing.T) {
	db, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DefaultDatabaseConfig())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	hash := nodestore.Hash256{1}
	_, err = db.FetchWithPolicy(t.Context(), hash, nodestore.ReadPeer)
	require.NoError(t, err)
	_, err = db.Fetch(t.Context(), hash)
	require.NoError(t, err)
	cleanup := SetNodeStore(db)
	defer cleanup()
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(NewNodeStoreCollector()))
	families, err := registry.Gather()
	require.NoError(t, err)
	require.Len(t, families, 1)
	require.Len(t, families[0].Metric, 14)
	values := map[string]float64{}
	for _, metric := range families[0].Metric {
		key := ""
		for _, label := range metric.Label {
			key += "/" + label.GetValue()
		}
		values[key] = metric.Counter.GetValue()
	}
	require.Equal(t, float64(1), values["/peer/lookup"])
	require.Equal(t, float64(1), values["/foreground/lookup"])
	require.Zero(t, values["/peer/admission"])
	cleanup()
	families, err = registry.Gather()
	require.NoError(t, err)
	require.Empty(t, families)
}
