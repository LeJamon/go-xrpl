package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// LedgerCurrentMethod handles the ledger_current RPC method
type LedgerCurrentMethod struct{}

func (m *LedgerCurrentMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Get the current (open) ledger index
	seq := types.Services.Ledger.GetCurrentLedgerIndex()
	if seq == 0 {
		return nil, &types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "No current ledger"}
	}

	response := map[string]interface{}{
		"ledger_current_index": seq,
	}

	return response, nil
}

func (m *LedgerCurrentMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *LedgerCurrentMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LedgerCurrentMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
