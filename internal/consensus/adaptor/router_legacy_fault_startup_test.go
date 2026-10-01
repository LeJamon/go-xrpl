package adaptor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

func TestLegacyReplayFaultStartupMissingStateAndAgedAuthentication(t *testing.T) {
	ctx := t.Context()
	kv := memorydb.New()
	db, err := nodestore.NewKVDatabase(kv, nodestore.DatabaseConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(ctx, t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rm.Close()) })
	cfg := service.Config{
		Standalone: true, GenesisConfig: genesis.DefaultConfig(),
		NodeStore: db, SHAMapFamily: backend.New(db), RelationalDB: rm,
	}
	writer, err := service.New(cfg)
	require.NoError(t, err)
	require.NoError(t, writer.Start())
	t.Cleanup(writer.Stop)
	_, err = writer.AcceptLedger(ctx)
	require.NoError(t, err)
	writer.FlushPersists()
	parent := writer.GetValidatedLedger()
	_, target, _, _ := buildSuccessorAgainstParent(t, parent)
	stateKey := keylet.LedgerHashes()
	stateData, err := parent.Read(stateKey)
	require.NoError(t, err)
	require.NotEmpty(t, stateData)
	missingHash := sha512half.Sum(protocol.HashPrefixLeafNode().Bytes(), stateData, stateKey.Key[:])
	stored, err := db.Fetch(ctx, nodestore.Hash256(missingHash))
	require.NoError(t, err)
	require.NotNil(t, stored)
	writer.Stop()
	batch, err := kv.NewBatch()
	require.NoError(t, err)
	require.NoError(t, batch.Delete(missingHash[:]))
	require.NoError(t, batch.Write())
	require.NoError(t, batch.Close())

	evidence, err := json.Marshal(map[string]any{
		"parent": parent.Header(), "target": target.Header(),
		"network_id": cfg.NetworkID, "authenticated": false, "parent_snapshot": false,
		"detail": map[string]any{"error": "parent verification: context deadline exceeded"},
	})
	require.NoError(t, err)
	fault := replayfault.Fault{
		ID: "legacy-2033", Class: replayfault.Unclassified,
		ParentHash: parent.Hash(), TargetHash: target.Hash(), Sequence: target.Sequence(),
		Message:  "legacy replay divergence; parent verification: context deadline exceeded",
		Evidence: evidence,
	}
	faultPath := filepath.Join(t.TempDir(), "replay-fault.json")
	raw, err := json.Marshal(fault)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(faultPath, raw, 0o600))
	cfg.Standalone = false
	cfg.FastLoad = true
	cfg.ReplayFaultPath = faultPath
	cfg.SHAMapFamily = backend.New(db)
	svc, err := service.New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.Nil(t, svc.GetValidatedLedger())
	require.Equal(t, uint32(2), svc.GetClosedLedgerIndex())
	status := svc.ReplayFaultStatus()
	require.True(t, status.Blocked)
	require.Zero(t, status.Fault.Attempts)
	require.False(t, status.Recovery.InFlight)
	require.Equal(t, "startup_verification_missing_state", status.Recovery.BlockedReason)
	require.Contains(t, status.Recovery.LastError, fmt.Sprintf("%x", missingHash))
	require.Contains(t, status.Recovery.OperatorAction, "replay_recover")
	journal, err := replayfault.Open(faultPath)
	require.NoError(t, err)
	saved := journal.Snapshot()
	require.Equal(t, fault.ID, saved.ID)
	require.Equal(t, fault.Message, saved.Message)
	require.Equal(t, fault.Class, saved.Class)
	var original, enriched map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(evidence, &original))
	require.NoError(t, json.Unmarshal(saved.Evidence, &enriched))
	for key, value := range original {
		require.JSONEq(t, string(value), string(enriched[key]), key)
	}
	require.Contains(t, enriched, "startup_verification")

	a, sender := newRecordingAdaptor(t, svc)
	r := newTestRouter(nil, a, nil)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)
	tracker := rcl.NewValidationTracker(1)
	tracker.SetTrustedAndQuorum([]consensus.NodeID{trusted}, 1)
	tracker.SetFullyValidatedCallback(func(id consensus.LedgerID, seq uint32) {
		a.OnLedgerFullyValidated(id, seq)
	})
	a.SetValidationHistorian(tracker)
	pivotSeq, pivotHash := target.Sequence()+1000, [32]byte{0xa1}
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	now := time.Now()
	require.True(t, tracker.Add(&consensus.Validation{
		NodeID: trusted, LedgerSeq: pivotSeq, LedgerID: consensus.LedgerID(pivotHash),
		SignTime: now, SeenTime: now, Full: true,
	}))
	for range 10 {
		r.handleMessage(statusChangeWithParent(t, 7, pivotSeq, pivotHash, [32]byte{0xa0}))
		a.OnLedgerFullyValidated(consensus.LedgerID(pivotHash), pivotSeq)
	}
	require.Zero(t, r.catchupReplay.standardReplay.generation)
	require.Empty(t, sender.legacyCalls())
	require.False(t, r.catchupReplay.replayTargetAuthenticated(target.Header()))
	require.ErrorContains(t, svc.RevalidateReplayFault(ctx, fault.ID), "lacks trusted-validation authentication")
	status = svc.ReplayFaultStatus()
	require.Equal(t, 1, status.Fault.Attempts)
	require.Zero(t, status.Recovery.AcquisitionAttempts)
	require.Contains(t, status.Recovery.OperatorAction, "separate observer")
	require.True(t, status.Blocked)
	require.Nil(t, svc.GetValidatedLedger())
	require.False(t, a.IsValidator())
	require.ErrorIs(t, a.SignValidation(&consensus.Validation{}), replayfault.ErrBlocked)
	require.ErrorIs(t, a.SignProposal(&consensus.Proposal{}), replayfault.ErrBlocked)
}
