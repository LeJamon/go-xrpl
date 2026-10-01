package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type replayRecoverLedgerFixture struct {
	types.LedgerService
	start  func(string) error
	status replayfault.Status
}

func (f *replayRecoverLedgerFixture) StartReplayFaultRecovery(id string) error {
	return f.start(id)
}

func (f *replayRecoverLedgerFixture) ReplayFaultStatus() replayfault.Status {
	return f.status
}

func (f *replayRecoverLedgerFixture) ReplayBlocked() bool {
	return f.status.Blocked
}

func TestReplayFaultStatusJSONOmitsEvidence(t *testing.T) {
	created := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	status := replayfault.Status{
		Blocked: true,
		Recovery: replayfault.RecoveryProgress{
			BlockedReason:  "historical_target_authentication_required",
			OperatorAction: "call replay_recover with fault_id fault-1",
		},
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
	recovery := got["recovery"].(map[string]any)
	assert.Equal(t, status.Recovery.BlockedReason, recovery["blocked_reason"])
	assert.Equal(t, status.Recovery.OperatorAction, recovery["operator_action"])
	fault := got["fault"].(map[string]any)
	assert.Equal(t, "fault-1", fault["id"])
	assert.Equal(t, "execution_disagreement", fault["class"])
	assert.NotContains(t, fault, "evidence")

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private")
}

func TestReplayFaultStatusJSONOmitsAbsentBlocker(t *testing.T) {
	got := replayFaultStatusJSON(replayfault.Status{}, false)
	recovery := got["recovery"].(map[string]any)
	assert.NotContains(t, recovery, "blocked_reason")
	assert.NotContains(t, recovery, "operator_action")
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

func TestReplayRecoverMethodAdmitsCanceledRequestAsInFlight(t *testing.T) {
	called := ""
	requestContext, cancel := context.WithCancel(context.Background())
	ledger := &replayRecoverLedgerFixture{
		start: func(id string) error {
			called = id
			cancel()
			return nil
		},
		status: replayfault.Status{
			Blocked: true,
			Fault:   &replayfault.Fault{ID: "fault-1"},
			Recovery: replayfault.RecoveryProgress{
				InFlight: true,
				ID:       "fault-1",
			},
		},
	}
	services := types.NewTestServiceGraph(&types.ServiceContainer{Ledger: ledger})

	result, rpcErr := (&ReplayRecoverMethod{}).Handle(&types.RpcContext{
		Context:  requestContext,
		Services: services,
	}, json.RawMessage(`{"fault_id":"fault-1"}`))
	require.Nil(t, rpcErr)
	require.Equal(t, "fault-1", called)
	response, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, response["accepted"])
	require.Equal(t, true, response["in_flight"])
	require.NotContains(t, response, "recovered")
}
