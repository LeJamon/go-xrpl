package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// ConsensusInfoMethod handles the consensus_info RPC method.
// STUB: Returns empty info. Network-only — no consensus in standalone mode.
//
// TODO [network]: Implement when adding consensus protocol.
//   - Reference: rippled ConsensusInfo.cpp → context.app.getOPs().getConsensusInfo()
//   - Returns: phase, proposing, validating, proposers, converge_percent,
//     close_resolution, have_time_consensus, previous_proposers, etc.
//   - In standalone mode, returning empty info is correct behavior
type ConsensusInfoMethod struct{}

func (m *ConsensusInfoMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return map[string]interface{}{
		"info": map[string]interface{}{},
	}, nil
}

func (m *ConsensusInfoMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *ConsensusInfoMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
