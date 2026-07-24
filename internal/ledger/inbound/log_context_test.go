package inbound

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

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
