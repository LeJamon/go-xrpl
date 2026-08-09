package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type ledgerCurrentMethod struct{ baseHandler }

func (m *ledgerCurrentMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	seq := ctx.Services.Ledger().GetCurrentLedgerIndex()
	if seq == 0 {
		return nil, rpcerrors.RpcErrorLgrNotFound("No current ledger")
	}

	response := map[string]any{
		"ledger_current_index": seq,
	}

	return response, nil
}

func (m *ledgerCurrentMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
