package handlers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type replayRecoverRequest struct {
	FaultID  string `json:"fault_id"`
	Strategy string `json:"strategy"`
}

// ReplayRecoverMethod admits admin recovery bound to the active fault ID.
type ReplayRecoverMethod struct{ adminHandler }

func (m *ReplayRecoverMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	request, err := decodeReplayRecoverRequest(params)
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
	var admissionErr error
	if request.Strategy == "exact_transition" {
		admissionErr = operator.StartReplayFaultRecovery(request.FaultID)
	} else {
		pivotOperator, ok := ctx.Services.Ledger().(interface {
			StartReplayFaultRecoveryStrategy(string, string) error
		})
		if !ok {
			return nil, rpcerrors.RpcErrorNotEnabled("verified pivot recovery is unavailable")
		}
		admissionErr = pivotOperator.StartReplayFaultRecoveryStrategy(request.FaultID, request.Strategy)
	}
	if err := admissionErr; err != nil {
		return nil, rpcInternalError("replay_recover: recovery admission failed", err)
	}

	response := map[string]any{
		"accepted": true,
		"fault_id": request.FaultID,
		"strategy": request.Strategy,
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

func decodeReplayRecoverRequest(params json.RawMessage) (replayRecoverRequest, error) {
	var request replayRecoverRequest
	if len(params) == 0 || string(params) == "null" {
		return request, fmt.Errorf("fault_id is required")
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return request, fmt.Errorf("invalid parameters: %w", err)
	}
	request.FaultID = strings.TrimSpace(request.FaultID)
	if request.FaultID == "" {
		return request, fmt.Errorf("fault_id is required")
	}
	if request.Strategy == "" {
		request.Strategy = "exact_transition"
	}
	if request.Strategy != "exact_transition" && request.Strategy != "verified_pivot" {
		return request, fmt.Errorf("strategy must be exact_transition or verified_pivot")
	}
	return request, nil
}
