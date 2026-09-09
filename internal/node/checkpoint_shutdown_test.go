package node

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

func TestSlowProducerDrainStillPreparesCheckpoint(t *testing.T) {
	prepared := false
	runtime := &nodeRuntime{
		consensus: &adaptor.Components{Engine: &lifecycleShutdownEngine{onStop: func() error {
			time.Sleep(producerShutdownGrace + 100*time.Millisecond)
			return nil
		}}},
		prepareFastLoadCheckpoint: func(context.Context) (bool, error) {
			prepared = true
			return true, nil
		},
		serverLog: xrpllog.Discard(),
	}
	require.NoError(t, runtime.shutdownWithin(15*time.Second))
	require.True(t, prepared, "a completed slow drain must not discard checkpoint eligibility")
}

func TestNodeRuntimeUsesConfiguredCheckpointShutdownGrace(t *testing.T) {
	const grace = 30 * time.Minute
	started := make(chan time.Duration, 1)
	release := make(chan struct{})
	logs, logger := checkpointShutdownTestLogger()
	runtime := &nodeRuntime{
		appConfig: &config.Config{Server: config.ServerConfig{
			CheckpointShutdownGrace: durationPointer(grace),
		}},
		prepareFastLoadCheckpoint: func(ctx context.Context) (bool, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				return false, errors.New("checkpoint context has no deadline")
			}
			started <- time.Until(deadline)
			<-release
			return true, nil
		},
		serverLog: logger,
	}

	result := make(chan error, 1)
	go func() { result <- runtime.shutdown() }()
	remaining := <-started
	require.Greater(t, remaining, 29*time.Minute)
	require.LessOrEqual(t, remaining, grace)
	time.Sleep(25 * time.Millisecond)
	close(release)
	require.NoError(t, <-result)
	require.Contains(t, logs.String(), "Fast-load checkpoint preparation started")
	require.Contains(t, logs.String(), "grace=30m0s")
	require.Contains(t, logs.String(), "deadline=")
	require.Contains(t, logs.String(), "prepared=true")
}

func TestNodeRuntimeCheckpointShutdownGraceExpires(t *testing.T) {
	const grace = 25 * time.Millisecond
	callbackDone := make(chan struct{})
	logs, logger := checkpointShutdownTestLogger()
	runtime := &nodeRuntime{
		appConfig: &config.Config{Server: config.ServerConfig{
			CheckpointShutdownGrace: durationPointer(grace),
		}},
		prepareFastLoadCheckpoint: func(ctx context.Context) (bool, error) {
			<-ctx.Done()
			close(callbackDone)
			return false, context.Cause(ctx)
		},
		serverLog: logger,
	}

	err := runtime.shutdownWithin(time.Second)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("checkpoint callback did not stop after its configured deadline")
	}
	require.Contains(t, logs.String(), "Fast-load checkpoint preparation started")
	require.Contains(t, logs.String(), "grace=25ms")
	require.True(t,
		strings.Contains(logs.String(), "Fast-load checkpoint preparation failed") ||
			strings.Contains(logs.String(), "Fast-load checkpoint preparation expired"),
		logs.String(),
	)
}

func TestNodeShutdownTimeoutIncludesConfiguredCheckpointGrace(t *testing.T) {
	const maxDuration = time.Duration(1<<63 - 1)
	require.Equal(t, 3*time.Minute, nodeShutdownTimeoutFor(2*time.Minute))
	require.Equal(t, 31*time.Minute, nodeShutdownTimeoutFor(30*time.Minute))
	require.Equal(t, maxDuration, nodeShutdownTimeoutFor(maxDuration))
}

func checkpointShutdownTestLogger() (*bytes.Buffer, xrpllog.Logger) {
	logs := &bytes.Buffer{}
	cfg := &xrpllog.Config{Level: xrpllog.LevelInfo, Format: "text", Output: logs}
	return logs, xrpllog.New(xrpllog.NewHandler(cfg), cfg)
}

func durationPointer(duration time.Duration) *time.Duration {
	return &duration
}
