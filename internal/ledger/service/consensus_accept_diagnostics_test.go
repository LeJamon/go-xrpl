package service

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

type acceptanceProfileCapture struct {
	xrpllog.Logger
	record  chan map[string]any
	release <-chan struct{}
	count   atomic.Int32
}

func (c *acceptanceProfileCapture) Warn(msg string, args ...any) {
	if msg != "Slow consensus acceptance profile" {
		return
	}
	c.count.Add(1)
	fields := make(map[string]any, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		fields[args[i].(string)] = args[i+1]
	}
	c.record <- fields
	if c.release != nil {
		<-c.release
	}
}

func TestConsensusAcceptanceProfileBoundsReporting(t *testing.T) {
	var diagnostics consensusAcceptanceDiagnostics
	release := make(chan struct{})
	logger := &acceptanceProfileCapture{
		Logger: xrpllog.Discard(), record: make(chan map[string]any, 2), release: release,
	}
	observation := &consensusAcceptanceObservation{seq: 42, started: time.Now()}
	done := make(chan struct{})
	go func() {
		diagnostics.report(logger, observation)
		close(done)
	}()
	t.Cleanup(func() { close(release); <-done })
	select {
	case record := <-logger.record:
		require.Equal(t, uint32(42), record["seq"])
		require.Contains(t, record["goroutine_profile"], "goroutine profile: total")
		require.LessOrEqual(t, len(record["goroutine_profile"].(string)), maxAcceptanceProfileBytes)
		require.Nil(t, record["profile_error"])
	case <-time.After(5 * time.Second):
		t.Fatal("slow acceptance did not capture a grouped profile")
	}
	// Reporting must remain nonblocking even if the previous log sink is stuck.
	diagnostics.report(logger, observation)
	require.Equal(t, int32(1), logger.count.Load())
}

func TestConsensusAcceptanceProfileCooldownAndCompletion(t *testing.T) {
	var diagnostics consensusAcceptanceDiagnostics
	logger := &acceptanceProfileCapture{Logger: xrpllog.Discard(), record: make(chan map[string]any, 3)}
	observation := &consensusAcceptanceObservation{seq: 42, started: time.Now()}
	diagnostics.report(logger, observation)
	diagnostics.report(logger, observation)
	require.Equal(t, int32(1), logger.count.Load())
	diagnostics.next = time.Time{}
	observation.complete.Store(true)
	diagnostics.report(logger, observation)
	require.Equal(t, int32(1), logger.count.Load())
}

func TestConsensusAcceptanceProfileTimer(t *testing.T) {
	var diagnostics consensusAcceptanceDiagnostics
	logger := &acceptanceProfileCapture{Logger: xrpllog.Discard(), record: make(chan map[string]any, 2)}
	stop := diagnostics.watch(logger, 42, time.Now(), time.Millisecond)
	defer stop()
	select {
	case record := <-logger.record:
		require.Equal(t, uint32(42), record["seq"])
	case <-time.After(5 * time.Second):
		t.Fatal("slow acceptance timer did not trigger a profile")
	}
}

func TestConsensusAcceptanceProfileBufferBound(t *testing.T) {
	var profile acceptanceProfileBuffer
	input := bytes.Repeat([]byte("x"), maxAcceptanceProfileBytes+1)
	n, err := profile.Write(input)
	require.NoError(t, err)
	require.Equal(t, len(input), n)
	require.True(t, profile.truncated)
	require.Equal(t, maxAcceptanceProfileBytes, profile.buffer.Len())
	n, err = profile.Write(input)
	require.NoError(t, err)
	require.Equal(t, len(input), n)
	require.Equal(t, maxAcceptanceProfileBytes, profile.buffer.Len())
}
