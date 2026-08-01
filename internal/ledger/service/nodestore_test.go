package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

func newTestNodeStore(t testing.TB, cacheSize int) *nodestore.KVDatabase {
	t.Helper()
	config := nodestore.DatabaseConfig{}
	if cacheSize > 0 {
		config.PositiveCache = nodestore.CacheConfig{
			Enabled:    true,
			MaxEntries: cacheSize,
			TTL:        time.Hour,
		}
	}
	database, err := nodestore.NewKVDatabase(memorydb.New(), config)
	require.NoError(t, err)
	return database
}

func newTestRepositories(t *testing.T, ctx context.Context) *sqlitedb.RepositoryManager {
	t.Helper()
	repositories, err := sqlitedb.NewRepositoryManager(ctx, t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repositories.Close()) })
	return repositories
}

func newTestRotatingNodeStore(
	t *testing.T,
	store kvstore.RotatingStore,
	cacheSize int,
) *nodestore.RotatingKVDatabase {
	t.Helper()
	config := nodestore.DatabaseConfig{}
	if cacheSize > 0 {
		config.PositiveCache = nodestore.CacheConfig{
			Enabled:    true,
			MaxEntries: cacheSize,
			TTL:        time.Hour,
		}
	}
	database, err := nodestore.NewRotatingKVDatabase(store, config)
	require.NoError(t, err)
	return database
}
