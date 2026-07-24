package node

import (
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

func TestSetupStorageKeepsGenerationManifestWhenOnlineDeleteIsDisabled(t *testing.T) {
	cfg := &config.Config{
		NodeSize: "small",
		NodeDB: config.NodeDBConfig{
			Path:         filepath.Join(t.TempDir(), "nodes"),
			OnlineDelete: 256,
		},
	}
	db, _, err := setupStorage(cfg, xrpllog.Discard())
	require.NoError(t, err)
	_, ok := db.(nodestore.GenerationDatabase)
	require.True(t, ok)
	require.NoError(t, db.Close())

	cfg.NodeDB.OnlineDelete = 0
	reopened, _, err := setupStorage(cfg, xrpllog.Discard())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	_, ok = reopened.(nodestore.GenerationDatabase)
	require.True(t, ok, "existing generation state must remain authoritative")
}
