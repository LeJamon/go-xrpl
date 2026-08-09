package node

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

func TestSetupStorageAllowsUnconfiguredRelationalDatabase(t *testing.T) {
	db, repo, err := setupStorage(context.Background(), &config.Config{}, xrpllog.Discard())
	require.NoError(t, err)
	require.Nil(t, db)
	require.Nil(t, repo)
}

func TestSetupStorageRejectsUnavailablePostgreSQL(t *testing.T) {
	cfg := &config.Config{
		DatabasePath: "postgres://127.0.0.1:1/xrpl?connect_timeout=1",
	}

	started := time.Now()
	db, repo, err := setupStorage(context.Background(), cfg, xrpllog.Discard())
	require.ErrorContains(t, err, "initialize PostgreSQL database")
	require.Less(t, time.Since(started), 5*time.Second)
	require.Nil(t, db)
	require.Nil(t, repo)
}

func TestSetupStorageRejectsInvalidSQLitePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(path, []byte("occupied"), 0o600))

	db, repo, err := setupStorage(context.Background(), &config.Config{DatabasePath: path}, xrpllog.Discard())
	require.ErrorContains(t, err, "initialize SQLite database")
	require.Nil(t, db)
	require.Nil(t, repo)
}

func TestSetupStorageClosesNodeStoreWhenRelationalStorageFails(t *testing.T) {
	root := t.TempDir()
	nodeStorePath := filepath.Join(root, "node-store")
	databasePath := filepath.Join(root, "not-a-directory")
	require.NoError(t, os.WriteFile(databasePath, []byte("occupied"), 0o600))
	cfg := &config.Config{
		NodeDB:       config.NodeDBConfig{Path: nodeStorePath},
		DatabasePath: databasePath,
	}

	db, repo, err := setupStorage(context.Background(), cfg, xrpllog.Discard())
	require.ErrorContains(t, err, "initialize SQLite database")
	require.Nil(t, db)
	require.Nil(t, repo)

	cfg.DatabasePath = ""
	reopened, _, err := setupStorage(context.Background(), cfg, xrpllog.Discard())
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
}

func TestSetupStorageHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nodeStorePath := filepath.Join(t.TempDir(), "node-store")

	db, repo, err := setupStorage(ctx, &config.Config{
		NodeDB:       config.NodeDBConfig{Path: nodeStorePath},
		DatabasePath: "postgres://127.0.0.1:1/xrpl?connect_timeout=30",
	}, xrpllog.Discard())
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, db)
	require.Nil(t, repo)
	_, statErr := os.Stat(nodeStorePath)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestRunDoesNotBindRPCWhenRelationalStorageFails(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := reserved.Addr().String()
	require.NoError(t, reserved.Close())
	_, portText, err := net.SplitHostPort(address)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	databasePath := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(databasePath, []byte("occupied"), 0o600))
	cfg := &config.Config{
		DatabasePath: databasePath,
		Ports: map[string]config.PortConfig{
			"rpc": {IP: "127.0.0.1", Port: port, Protocol: "http"},
		},
	}

	err = Run(
		context.Background(),
		cfg,
		"",
		true,
		service.StartupConfig{},
		xrpllog.Discard(),
		xrpllog.Discard(),
	)
	require.ErrorContains(t, err, "initialize SQLite database")

	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
}
