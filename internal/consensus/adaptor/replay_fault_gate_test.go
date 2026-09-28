package adaptor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newReplayFaultBlockedAdaptor(t *testing.T) *Adaptor {
	t.Helper()
	path := filepath.Join(t.TempDir(), "replay-fault.json")
	fault, err := json.Marshal(replayfault.Fault{
		ID:       "fault-1",
		Class:    replayfault.ExecutionDisagreement,
		Sequence: 99,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, fault, 0o600))

	svc, err := service.New(service.Config{
		ReplayFaultPath: path,
		Standalone:      true,
		GenesisConfig:   genesis.DefaultConfig(),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	return New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
}

func TestAdaptorReplayFaultBlocksAllValidatorDuties(t *testing.T) {
	a := newReplayFaultBlockedAdaptor(t)

	assert.False(t, a.IsValidator())
	assert.ErrorIs(t, a.SignProposal(&consensus.Proposal{}), replayfault.ErrBlocked)
	assert.ErrorIs(t, a.SignValidation(&consensus.Validation{}), replayfault.ErrBlocked)
	assert.ErrorIs(t, a.BroadcastProposal(&consensus.Proposal{}), replayfault.ErrBlocked)
	assert.ErrorIs(t, a.BroadcastValidation(&consensus.Validation{LedgerSeq: 99}), replayfault.ErrBlocked)
	assert.Zero(t, a.lastIssuedValidationSeq.Load(), "blocked broadcast must not advance the sequence floor")
}
