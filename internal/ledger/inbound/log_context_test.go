package inbound

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/stretchr/testify/require"
)

func TestNewAddsAcquisitionIdentityToLogs(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	hash := [32]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}

	ledger := New(hash, 1234, 1, logger)
	ledger.logger.Info("progress")

	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	require.Equal(t, float64(1234), record["ledger_seq"])
	require.Equal(t, "0123456789abcdef", record["ledger_hash"])
}

func TestNoProgressLogIncludesAcquisitionDiagnostics(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	ledger := New([32]byte{0xA1}, 4321, 1, logger)
	base := time.Unix(1_700_000_000, 0)

	ledger.mu.Lock()
	ledger.lastTimer = base
	ledger.header = &header.LedgerHeader{}
	ledger.state = StateWantState
	ledger.peers = append(ledger.peers, 2)
	ledger.requestPeers[2] = struct{}{}
	ledger.neededState = [][32]byte{{0x01}, {0x02}}
	ledger.neededTx = [][32]byte{{0x03}}
	ledger.stateRecv = 14
	ledger.stateUseful = 3
	ledger.txRecv = 9
	ledger.txUseful = 0
	ledger.mu.Unlock()

	require.Equal(t, TimerEscalate, ledger.OnTimer(base.Add(acquireTimerInterval)))

	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	require.Equal(t, "state", record["phase"])
	require.Equal(t, float64(2), record["peers"])
	require.Equal(t, float64(1), record["request_peers"])
	require.Equal(t, float64(2), record["needed_state"])
	require.Equal(t, float64(1), record["needed_tx"])
	require.Equal(t, float64(14), record["state_received_total"])
	require.Equal(t, float64(3), record["state_useful_total"])
	require.Equal(t, float64(9), record["tx_received_total"])
	require.Equal(t, float64(0), record["tx_useful_total"])
}

func TestSnapshotPhase(t *testing.T) {
	tests := []struct {
		name string
		snap Snapshot
		want string
	}{
		{name: "base", snap: Snapshot{}, want: "base"},
		{name: "state", snap: Snapshot{HaveHeader: true}, want: "state"},
		{name: "transactions", snap: Snapshot{HaveHeader: true, HaveState: true}, want: "transactions"},
		{name: "complete", snap: Snapshot{Complete: true}, want: "complete"},
		{name: "failed", snap: Snapshot{Failed: true}, want: "failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.snap.Phase())
		})
	}
}
