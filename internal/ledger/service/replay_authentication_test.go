package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/stretchr/testify/require"
)

func TestOrdinaryReplayPreservesAuthenticationAcrossFaultAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	svc := replayFaultService(t, path)
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, true)
	proof := json.RawMessage(`{"signed_quorum_fixture":true}`)
	svc.SetReplayEvidenceAuthenticator(func(h header.LedgerHeader) (json.RawMessage, error) {
		require.Equal(t, target.Hash(), h.Hash)
		require.False(t, svc.ReplayBlocked())
		return proof, nil
	}, func(header.LedgerHeader, json.RawMessage) error { return nil })
	replay, err := inbound.NewStoredLedgerReplay(parent, target, nil)
	require.NoError(t, err)
	_, err = svc.ApplyReplay(context.Background(), replay, svc.EngineConfigForReplay(parent), false)
	require.Error(t, err)
	fault := svc.replayFaults.Snapshot()
	require.NotNil(t, fault)
	var evidence replayEvidence
	require.NoError(t, json.Unmarshal(fault.Evidence, &evidence))
	require.JSONEq(t, string(proof), string(evidence.Authentication))
	require.True(t, evidence.Authenticated)
	svc.Stop()
	restarted := replayFaultService(t, path)
	require.True(t, restarted.ReplayBlocked())
	var restored replayEvidence
	require.NoError(t, json.Unmarshal(restarted.replayFaults.Snapshot().Evidence, &restored))
	require.JSONEq(t, string(proof), string(restored.Authentication))
	require.False(t, restarted.authenticateReplayEvidence(restored), "persisted proof requires a verifier")
	restarted.SetReplayEvidenceAuthenticator(nil, func(header.LedgerHeader, json.RawMessage) error { return errors.New("changed trust") })
	require.False(t, restarted.authenticateReplayEvidence(restored))
	restarted.SetReplayEvidenceAuthenticator(nil, func(h header.LedgerHeader, p json.RawMessage) error {
		require.Equal(t, target.Hash(), h.Hash)
		require.JSONEq(t, string(proof), string(p))
		return nil
	})
	require.True(t, restarted.authenticateReplayEvidence(restored))
}
