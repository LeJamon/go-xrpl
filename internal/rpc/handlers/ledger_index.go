package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// LedgerIndexMethod handles the ledger_index RPC method
type LedgerIndexMethod struct{}

func (m *LedgerIndexMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{"ledger_index": 1000}, nil
}

func (m *LedgerIndexMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *LedgerIndexMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LedgerIndexMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
