package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

type consensusAcceptanceTimingCapture struct {
	xrpllog.Logger

	mu      sync.Mutex
	records []map[string]any
}

func (c *consensusAcceptanceTimingCapture) Info(msg string, args ...any) {
	if msg != "Consensus acceptance timings" {
		return
	}
	fields := make(map[string]any, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		key, ok := args[i].(string)
		if ok {
			fields[key] = args[i+1]
		}
	}
	c.mu.Lock()
	c.records = append(c.records, fields)
	c.mu.Unlock()
}

func (c *consensusAcceptanceTimingCapture) With(...any) xrpllog.Logger { return c }

func (c *consensusAcceptanceTimingCapture) Named(string) xrpllog.Logger { return c }

func (c *consensusAcceptanceTimingCapture) latest() (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.records) == 0 {
		return nil, false
	}
	return c.records[len(c.records)-1], true
}

func (c *consensusAcceptanceTimingCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.records)
}

func TestConsensusAcceptanceTimingsIncludeReplayGateObservation(t *testing.T) {
	logger := &consensusAcceptanceTimingCapture{Logger: xrpllog.Discard()}
	cfg := DefaultConfig()
	cfg.Logger = logger
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	parent := svc.GetClosedLedger()
	_, err = svc.AcceptConsensusResult(
		context.Background(), parent, nil, nil, parent.CloseTime().Add(time.Second), true,
	)
	require.NoError(t, err)

	record, ok := logger.latest()
	require.True(t, ok, "acceptance did not emit timing record")
	require.Equal(t, 1, logger.count())
	replayGateWait, ok := record["replay_gate_wait"].(time.Duration)
	require.True(t, ok, "timing record omitted replay gate wait")
	total, ok := record["total"].(time.Duration)
	require.True(t, ok, "timing record omitted total")
	require.Greater(t, replayGateWait, time.Duration(0))
	require.GreaterOrEqual(t, total, replayGateWait)
	require.False(t, record["blocked"].(bool))
	require.Equal(t, parent.Sequence()+1, record["seq"])
}

func TestConsensusAcceptanceTimingsLogInitialReplayBlock(t *testing.T) {
	logger := &consensusAcceptanceTimingCapture{Logger: xrpllog.Discard()}
	cfg := DefaultConfig()
	cfg.Logger = logger
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	faultID, err := svc.replayFaults.BeginReplay(replayfault.Fault{Class: replayfault.ExecutionDisagreement})
	require.NoError(t, err)
	require.NoError(t, svc.replayFaults.FailReplay(faultID, replayfault.Fault{Class: replayfault.ExecutionDisagreement}))
	parent := svc.GetClosedLedger()
	_, err = svc.AcceptConsensusResult(
		context.Background(), parent, nil, nil, parent.CloseTime().Add(time.Second), true,
	)
	require.ErrorIs(t, err, replayfault.ErrBlocked)

	record, ok := logger.latest()
	require.True(t, ok, "blocked acceptance did not emit timing record")
	require.Equal(t, 1, logger.count())
	require.True(t, record["blocked"].(bool), "blocked acceptance timing record was not marked blocked")
	require.Equal(t, parent.Sequence()+1, record["seq"])
	replayGateWait, ok := record["replay_gate_wait"].(time.Duration)
	require.True(t, ok, "blocked timing record omitted replay gate wait")
	total, ok := record["total"].(time.Duration)
	require.True(t, ok, "blocked timing record omitted total")
	require.GreaterOrEqual(t, total, replayGateWait)
}

func TestConsensusAcceptanceTimingsIncludeCanceledAdmission(t *testing.T) {
	for _, phase := range []string{"persistence", "state"} {
		t.Run(phase, func(t *testing.T) {
			logger := &consensusAcceptanceTimingCapture{Logger: xrpllog.Discard()}
			svc := &Service{
				config: Config{PersistenceMemory: &relayTestMemory{limit: 1 << 20}},
				logger: logger,
			}
			svc.persistStarted = true
			state := &blockedDiagnosticAdmission{entered: make(chan struct{})}
			if phase == "persistence" {
				svc.persistAdmissions = persistenceAdmissionLimit
			} else {
				svc.nodeStore = state
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			parent := &ledger.Ledger{}
			go func() {
				_, err := svc.AcceptConsensusResult(ctx, parent, nil, nil, time.Time{}, true)
				done <- err
			}()
			if phase == "persistence" {
				require.Eventually(t, func() bool {
					svc.persistMu.Lock()
					defer svc.persistMu.Unlock()
					return svc.persistCapacityWake != nil
				}, time.Second, time.Millisecond)
			} else {
				select {
				case <-state.entered:
				case err := <-done:
					t.Fatalf("acceptance returned before state admission: %v", err)
				case <-time.After(time.Second):
					t.Fatal("acceptance did not reach state admission")
				}
			}
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("acceptance did not cancel admission")
			}
			record, ok := logger.latest()
			require.True(t, ok, "canceled admission did not emit timings")
			require.Equal(t, 1, logger.count())
			require.Equal(t, parent.Sequence()+1, record["seq"])
			lifecycle := record["admission_lifecycle_wait"].(time.Duration)
			persistence := record["persistence_admission_wait"].(time.Duration)
			stateWait := record["state_admission_wait"].(time.Duration)
			require.Positive(t, lifecycle)
			require.Positive(t, persistence)
			if phase == "persistence" {
				require.Zero(t, stateWait)
				require.Equal(t, persistenceAdmissionLimit, svc.persistAdmissions)
			} else {
				require.Positive(t, stateWait)
				require.Zero(t, svc.persistAdmissions)
			}
			require.GreaterOrEqual(t, record["total"].(time.Duration), lifecycle+persistence+stateWait)
			require.Zero(t, record["parse"].(time.Duration))
			svc.ledgerWorkWG.Wait()
		})
	}
}
