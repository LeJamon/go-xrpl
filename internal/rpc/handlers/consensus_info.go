package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// ConsensusInfoMethod handles the consensus_info RPC method.
//
// Mirrors rippled's ConsensusInfo.cpp, which returns
// context.netOps.getConsensusInfo() (→ RCLConsensus::getJson(true)). In
// standalone / RPC-only mode there is no consensus engine wired, so the
// handler returns an empty info object — matching rippled's behavior on a
// node that is not participating in consensus.
type ConsensusInfoMethod struct{ AdminHandler }

func (m *ConsensusInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	info := map[string]interface{}{}
	if ctx.Services != nil && ctx.Services.ConsensusInfo != nil {
		if live := ctx.Services.ConsensusInfo(true); live != nil {
			info = live
		}
	}
	return map[string]interface{}{
		"info": info,
	}, nil
}
