package handlers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type replayRecoverRequest struct {
	FaultID string `json:"fault_id"`
}

// ReplayRecoverMethod handles the admin-only replay_recover RPC. A fault ID is
// mandatory: the service admits asynchronous verification for the exact
// persisted transition identified by that ID before returning.
type ReplayRecoverMethod struct{ adminHandler }

func (m *ReplayRecoverMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	faultID, err := decodeReplayRecoverRequest(params)
	if err != nil {
		return nil, rpcerrors.RpcErrorInvalidParams(err.Error())
	}
	if ctx == nil || ctx.Services == nil || ctx.Services.Ledger() == nil {
		return nil, rpcInternalInvariantError("replay_recover: ledger service unavailable")
	}
	if ctx.Context != nil {
		if err := ctx.Context.Err(); err != nil {
			return nil, rpcInternalError("replay_recover: request canceled before admission", err)
		}
	}
	operator, ok := ctx.Services.Ledger().(replayFaultRecovery)
	if !ok {
		return nil, rpcerrors.RpcErrorNotEnabled("replay recovery is unavailable")
	}
	if err := operator.StartReplayFaultRecovery(faultID); err != nil {
		return nil, rpcInternalError("replay_recover: recovery admission failed", err)
	}

	response := map[string]any{
		"accepted": true,
		"fault_id": faultID,
	}
	if status, available := replayFaultStatus(ctx.Services); available {
		response["replay_fault"] = status
		recovery, _ := status["recovery"].(map[string]any)
		inFlight, _ := recovery["in_flight"].(bool)
		response["in_flight"] = inFlight
	} else {
		response["in_flight"] = true
	}
	return response, nil
}

func decodeReplayRecoverRequest(params json.RawMessage) (string, error) {
	if len(params) == 0 || string(params) == "null" {
		return "", fmt.Errorf("fault_id is required")
	}
	var request replayRecoverRequest
	if err := json.Unmarshal(params, &request); err != nil {
		return "", fmt.Errorf("invalid parameters: %w", err)
	}
	request.FaultID = strings.TrimSpace(request.FaultID)
	if request.FaultID == "" {
		return "", fmt.Errorf("fault_id is required")
	}
	return request.FaultID, nil
}
