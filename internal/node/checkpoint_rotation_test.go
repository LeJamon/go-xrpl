package node

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

func TestCheckpointRecertificationAcrossOnlineDeleteAndRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		NetworkID:    config.NetworkID{Set: true, ID: 0},
		NodeSize:     "small",
		DatabasePath: filepath.Join(dir, "relational"),
		NodeDB: config.NodeDBConfig{
			Path: filepath.Join(dir, "nodes"), OnlineDelete: 4, FastLoad: true,
		},
	}
	start := func(mode service.StartupMode, checkpoint bool) (*nodeRuntime, func()) {
		logs, logger := checkpointShutdownTestLogger()
		ctx, cancel := context.WithCancelCause(t.Context())
		r := &nodeRuntime{
			ctx: ctx, cancel: cancel, appConfig: cfg, standalone: true,
			startup: service.StartupConfig{Mode: mode}, rootLogger: logger, serverLog: logger,
		}
		var once sync.Once
		stop := func() {
			once.Do(func() {
				if r.stopSampler != nil {
					r.stopSampler()
				}
				cancel(nil)
				require.NoError(t, r.shutdownWithin(10*time.Second))
			})
		}
		t.Cleanup(stop)
		require.NoError(t, r.configureStorage())
		require.NoError(t, r.configureLedger())
		if checkpoint {
			require.Contains(t, logs.String(), "Fast-load checkpoint accepted")
		} else {
			require.NotContains(t, logs.String(), "Fast-load checkpoint accepted")
		}
		return r, stop
	}

	writer, stopWriter := start(service.StartupFresh, false)
	_, err := writer.ledger.AcceptLedger(t.Context())
	require.NoError(t, err)
	stopWriter()

	runtime, stopRuntime := start(service.StartupNormal, true)
	require.NoError(t, runtime.configureMaintenance())
	durable := runtime.nodeStore.(nodestore.DurableSnapshotDatabase)
	before, err := durable.DurableFingerprint(t.Context())
	require.NoError(t, err)
	initialSeq := runtime.ledger.GetValidatedLedgerIndex()
	runtime.rotator.Notify(initialSeq)
	require.Eventually(t, func() bool {
		return runtime.services.AdvisoryDeleteState.GetLastRotated() == initialSeq
	}, 10*time.Second, 10*time.Millisecond)
	for range 8 {
		seq, err := runtime.ledger.AcceptLedger(t.Context())
		require.NoError(t, err)
		runtime.ledger.FlushPersists()
		runtime.rotator.Notify(seq)
		if (seq-initialSeq)%4 == 0 {
			require.Eventually(t, func() bool {
				return runtime.services.AdvisoryDeleteState.GetLastRotated() == seq
			}, 10*time.Second, 10*time.Millisecond)
		}
	}
	after, err := durable.DurableFingerprint(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, before, after)
	require.Positive(t, runtime.rotator.MinimumOnline())
	require.Eventually(t, func() bool {
		root, release, available, err := runtime.ledger.AcquireValidatedStateBase(t.Context())
		if release != nil {
			release()
		}
		return err == nil && available && root == runtime.ledger.GetValidatedLedger().Header().AccountHash
	}, 10*time.Second, 10*time.Millisecond)
	want := runtime.ledger.GetValidatedLedger().Hash()
	stopRuntime()

	restarted, stopRestarted := start(service.StartupNormal, true)
	require.Equal(t, want, restarted.ledger.GetValidatedLedger().Hash())
	restarted.prepareFastLoadCheckpoint = func(context.Context) (bool, error) { return false, nil }
	stopRestarted()
	consumed, stopConsumed := start(service.StartupNormal, false)
	require.Equal(t, want, consumed.ledger.GetValidatedLedger().Hash())
	stopConsumed()
}
