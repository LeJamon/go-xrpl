package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

type cancelReplayLogger struct {
	xrpllog.Logger
	cancel  context.CancelFunc
	applied int
}

func (l *cancelReplayLogger) Named(string) xrpllog.Logger { return l }

func (l *cancelReplayLogger) Debug(msg string, _ ...any) {
	if msg == "apply result" {
		l.applied++
		l.cancel()
	}
}

func TestReplayCancellationPreservesFrontierAndAllowsRetry(t *testing.T) {
	for _, mode := range []string{"direct", "standalone", "consensus"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fault.json")
			svc := replayFaultService(t, path)
			parent := svc.GetClosedLedger()
			open := svc.GetOpenLedger()
			view := svc.openLedgerView.Current()
			var pending []openledger.PendingTx
			for sequence := uint32(1); sequence <= 2; sequence++ {
				blob, _ := startupPaymentBlob(t, "cancel-replay-destination", sequence)
				ptx, err := openledger.ParsePendingTx(blob)
				require.NoError(t, err)
				pending = append(pending, ptx)
			}
			salt, err := openledger.ComputeSalt(pending)
			require.NoError(t, err)
			closeTime := parent.CloseTime().Add(10 * time.Second)
			target, retries, err := svc.buildClosedLedger(t.Context(), parent, pending, salt, closeTime, false, nil)
			require.NoError(t, err)
			require.Empty(t, retries)
			require.EqualValues(t, 2, target.TxCount())
			require.NoError(t, target.Close(closeTime, 0))
			replay, err := inbound.NewStoredLedgerReplay(parent, target, nil)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			logger := &cancelReplayLogger{Logger: xrpllog.Discard(), cancel: cancel}
			svc.config.Logger = logger
			if mode != "direct" {
				svc.startupReplay = replay
			}
			invoke := func(ctx context.Context) (*ledger.Ledger, error) {
				switch mode {
				case "direct":
					return svc.ApplyReplay(ctx, replay, svc.EngineConfigForReplay(parent), true)
				case "standalone":
					_, err := svc.AcceptLedger(ctx)
					return svc.GetClosedLedger(), err
				default:
					_, err := svc.AcceptConsensusResult(ctx, parent, nil, nil, closeTime, true)
					return svc.GetClosedLedger(), err
				}
			}

			_, err = invoke(ctx)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 1, logger.applied, "cancellation must stop before the next transaction")
			require.Equal(t, inbound.StateReplayReady, replay.State())
			require.Same(t, parent, svc.GetClosedLedger())
			require.Same(t, open, svc.GetOpenLedger())
			require.Same(t, view, svc.openLedgerView.Current())
			require.False(t, svc.ReplayBlocked())
			restarted, err := replayfault.Open(path)
			require.NoError(t, err)
			require.False(t, restarted.Blocked(), "ordinary cancellation must not leave a durable replay fault")
			if mode != "direct" {
				require.Same(t, replay, svc.startupReplay)
			}

			svc.config.Logger = xrpllog.Discard()
			derived, err := invoke(t.Context())
			require.NoError(t, err)
			require.Equal(t, target.Hash(), derived.Hash())
			require.Equal(t, inbound.StateComplete, replay.State())
			require.False(t, svc.ReplayBlocked())
		})
	}
}

func TestReplayCanceledBeforeExecutionDoesNotStartIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	svc := replayFaultService(t, path)
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, false)
	replay, err := inbound.NewStoredLedgerReplay(parent, target, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = svc.ApplyReplay(ctx, replay, svc.EngineConfigForReplay(parent), true)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, inbound.StateReplayReady, replay.State())
	require.NoFileExists(t, path)
	require.False(t, svc.ReplayBlocked())
}
