package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// LedgerIndexMethod handles the ledger_index RPC method
type LedgerIndexMethod struct{ BaseHandler }

func (m *LedgerIndexMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{"ledger_index": 1000}, nil
}

