package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// LedgerClosedMethod handles the ledger_closed RPC method
type LedgerClosedMethod struct{ baseHandler }

func (m *LedgerClosedMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	seq := ctx.Services.Ledger().GetClosedLedgerIndex()
	if seq == 0 {
		return nil, rpcerrors.RpcErrorLgrNotFound("No closed ledger")
	}

	ledger, err := ctx.Services.Ledger().GetLedgerBySequence(seq)
	if err != nil {
		return nil, rpcerrors.RpcErrorLgrNotFound("Closed ledger not found")
	}

	hash := ledger.Hash()
	response := map[string]any{
		"ledger_hash":  FormatLedgerHash(hash),
		"ledger_index": seq,
	}

	return response, nil
}

func (m *LedgerClosedMethod) RequiredCondition() types.Condition {
	return types.NeedsClosedLedger
}
