package node

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

func TestRunWithOptionsStandaloneReadyThenStopping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var events []string

	err := RunWithOptions(
		ctx,
		&config.Config{
			NetworkID: config.NetworkID{Set: true, ID: 0},
			NodeDB:    config.NodeDBConfig{Path: filepath.Join(t.TempDir(), "node-store")},
			Ports: map[string]config.PortConfig{
				"http": {IP: "127.0.0.1", Port: 0, Protocol: "http"},
			},
		},
		"",
		true,
		service.StartupConfig{},
		xrpllog.Discard(),
		xrpllog.Discard(),
		RunOptions{
			Ready: func() {
				events = append(events, "ready")
				cancel()
			},
			Stopping: func() {
				events = append(events, "stopping")
			},
		},
	)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{"ready", "stopping"}, events)
}

func TestRunWithOptionsBindFailureSuppressesReadyAndClosesStorage(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = reserved.Close() })
	_, portText, err := net.SplitHostPort(reserved.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	cfg := &config.Config{
		NetworkID: config.NetworkID{Set: true, ID: 0},
		NodeDB:    config.NodeDBConfig{Path: filepath.Join(t.TempDir(), "node-store")},
		Ports: map[string]config.PortConfig{
			"http": {IP: "127.0.0.1", Port: port, Protocol: "http"},
		},
	}
	var ready, stopping bool
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = RunWithOptions(
		ctx,
		cfg,
		"",
		true,
		service.StartupConfig{},
		xrpllog.Discard(),
		xrpllog.Discard(),
		RunOptions{
			Ready:    func() { ready = true },
			Stopping: func() { stopping = true },
		},
	)

	require.ErrorContains(t, err, "listen on")
	var listenErr *net.OpError
	require.ErrorAs(t, err, &listenErr)
	require.False(t, ready)
	require.True(t, stopping)
	require.NoError(t, reserved.Close())

	reopened, repo, reopenErr := setupStorage(context.Background(), cfg, xrpllog.Discard())
	require.NoError(t, reopenErr)
	require.Nil(t, repo)
	require.NotNil(t, reopened)
	require.NoError(t, reopened.Close())
}
