package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
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

func (m *ConsensusInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{
		"info": map[string]interface{}{},
	}, nil
}

func (m *ConsensusInfoMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *ConsensusInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
