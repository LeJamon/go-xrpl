package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// LedgerCurrentMethod handles the ledger_current RPC method
type LedgerCurrentMethod struct{ BaseHandler }

func (m *LedgerCurrentMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	seq := ctx.Services.Ledger.GetCurrentLedgerIndex()
	if seq == 0 {
		return nil, types.RPCErrorLgrNotFound("No current ledger")
	}

	response := map[string]any{
		"ledger_current_index": seq,
	}

	return response, nil
}

func (m *LedgerCurrentMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
