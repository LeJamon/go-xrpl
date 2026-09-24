package handlers

import (
	"context"
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
// mandatory: the service verifies the exact persisted transition identified by
// that ID before it can clear the durable validator-duty gate.
type ReplayRecoverMethod struct{ adminHandler }

func (m *ReplayRecoverMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	faultID, err := decodeReplayRecoverRequest(params)
	if err != nil {
		return nil, rpcerrors.RpcErrorInvalidParams(err.Error())
	}
	if ctx == nil || ctx.Services == nil || ctx.Services.Ledger() == nil {
		return nil, rpcInternalInvariantError("replay_recover: ledger service unavailable")
	}
	operator, ok := ctx.Services.Ledger().(replayFaultRecovery)
	if !ok {
		return nil, rpcerrors.RpcErrorNotEnabled("replay recovery is unavailable")
	}
	requestContext := ctx.Context
	if requestContext == nil {
		requestContext = context.Background()
	}
	if err := operator.RevalidateReplayFault(requestContext, faultID); err != nil {
		return nil, rpcInternalError("replay_recover: transition revalidation failed", err)
	}

	response := map[string]any{
		"fault_id":  faultID,
		"recovered": true,
	}
	if status, available := replayFaultStatus(ctx.Services); available {
		response["replay_fault"] = status
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
