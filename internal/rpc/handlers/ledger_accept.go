package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// LedgerAcceptMethod handles the ledger_accept RPC method
// This is a standalone-mode only command that manually closes and validates
// the current open ledger, allowing progression without consensus.
type LedgerAcceptMethod struct{ adminHandler }

func (m *LedgerAcceptMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	if !ctx.Services.Ledger().IsStandalone() {
		return nil, types.RpcErrorNotStandalone("ledger_accept is only available in standalone mode")
	}

	closedSeq, err := ctx.Services.Ledger().AcceptLedger(ctx.Context)
	if err != nil {
		return nil, rpcInternalError("ledger_accept: accepting ledger failed", err)
	}

	response := map[string]any{
		"ledger_current_index": closedSeq + 1, // Return the new open ledger index
	}

	return response, nil
}

func (m *LedgerAcceptMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
