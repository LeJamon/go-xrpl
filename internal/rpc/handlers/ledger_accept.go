package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// LedgerAcceptMethod handles the ledger_accept RPC method
// This is a standalone-mode only command that manually closes and validates
// the current open ledger, allowing progression without consensus.
type LedgerAcceptMethod struct{}

func (m *LedgerAcceptMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	if !ctx.Services.Ledger.IsStandalone() {
		return nil, types.NewRpcError(types.RpcNOT_STANDALONE, "notStandalone", "notStandalone",
			"ledger_accept is only available in standalone mode")
	}

	closedSeq, err := ctx.Services.Ledger.AcceptLedger()
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to accept ledger: %v", err))
	}

	response := map[string]interface{}{
		"ledger_current_index": closedSeq + 1, // Return the new open ledger index
	}

	return response, nil
}

func (m *LedgerAcceptMethod) RequiredRole() types.Role {
	return types.RoleAdmin // ledger_accept requires admin privileges
}

func (m *LedgerAcceptMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LedgerAcceptMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
