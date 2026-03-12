package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// =============================================================================
// LEDGER STUB HANDLERS
// =============================================================================
//
// These handlers require additional ledger query capabilities.
// =============================================================================

// OwnerInfoMethod handles the owner_info RPC method.
// STUB: Returns notImplemented. Requires NetworkOPs integration.
//
// TODO [ledger]: Implement owner_info.
//   - Reference: rippled OwnerInfo.cpp → context.netOps.getOwnerInfo()
//   - Returns: owner-specific info about offers and account objects
//   - Params: account (required)
//   - This is a rarely-used legacy method; low priority
type OwnerInfoMethod struct{}

func (m *OwnerInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Account string `json:"account,omitempty"`
		Ident   string `json:"ident,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	account := request.Account
	if account == "" {
		account = request.Ident
	}
	if account == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: account")
	}

	return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"owner_info is not yet implemented — requires NetworkOPs.GetOwnerInfo")
}

func (m *OwnerInfoMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *OwnerInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *OwnerInfoMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

// LedgerDiffMethod handles the ledger_diff RPC method.
// STUB: Returns error. Only available via gRPC in rippled.
//
// NOTE: This is gRPC-only in rippled and is NOT available via JSON-RPC.
//
//	It computes the state diff between two ledger versions.
//	This stub exists for completeness but may never need implementation.
type LedgerDiffMethod struct{ AdminHandler }

func (m *LedgerDiffMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"ledger_diff is only available via gRPC in rippled — JSON-RPC not supported")
}
