package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplayFaultStatusJSONOmitsEvidence(t *testing.T) {
	created := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	status := replayfault.Status{
		Blocked: true,
		Fault: &replayfault.Fault{
			ID:         "fault-1",
			Class:      replayfault.ExecutionDisagreement,
			ParentHash: [32]byte{0x01},
			TargetHash: [32]byte{0x02},
			Sequence:   99,
			Message:    "execution mismatch",
			Revision:   "rev",
			CreatedAt:  created,
			Evidence:   json.RawMessage(`{"transactions":["private"]}`),
			Attempts:   2,
		},
	}

	got := replayFaultStatusJSON(status, true)
	assert.Equal(t, false, got["follower_mode"])
	assert.Equal(t, "pending", got["transition_verification"])
	fault := got["fault"].(map[string]any)
	assert.Equal(t, "fault-1", fault["id"])
	assert.Equal(t, "execution_disagreement", fault["class"])
	assert.NotContains(t, fault, "evidence")

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private")
}

func TestReplayFaultStatusJSONMarksRecoveryRunning(t *testing.T) {
	status := replayfault.Status{
		Blocked: true,
		Recovery: replayfault.RecoveryProgress{
			InFlight:  true,
			ID:        "fault-1",
			LastError: "",
		},
	}
	got := replayFaultStatusJSON(status, true)
	assert.Equal(t, "running", got["transition_verification"])
}

func TestReplayRecoverRequestRequiresFaultID(t *testing.T) {
	for _, params := range []json.RawMessage{nil, []byte(`{}`), []byte(`{"fault_id":"   "}`), []byte(`[]`)} {
		_, err := decodeReplayRecoverRequest(params)
		assert.Error(t, err)
	}

	id, err := decodeReplayRecoverRequest(json.RawMessage(`{"fault_id":" fault-1 "}`))
	require.NoError(t, err)
	assert.Equal(t, "fault-1", id)
}

func TestReplayRecoverMethodIsAdminOnly(t *testing.T) {
	method := &ReplayRecoverMethod{}
	assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	assert.Equal(t, types.NoCondition, method.RequiredCondition())
}
