package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// LedgerCleanerMethod handles the ledger_cleaner admin RPC. It configures the
// background ledger-integrity verifier, mirroring rippled's ledger_cleaner.
//
// Parameters (all optional, mirroring rippled): ledger (single sequence,
// forces a deep check), min_ledger, max_ledger, full (bool, deep check),
// check_nodes (bool, walk every node), stop (bool, halt an in-progress run).
type LedgerCleanerMethod struct{ adminHandler }

// RequiredCondition mirrors rippled's handler-table entry
// {"ledger_cleaner", …, NEEDS_NETWORK_CONNECTION} (Handler.cpp:121-124): the
// command is unavailable until the node has network state. The dispatcher's
// conditionMet enforces the network/sync gate before Handle runs.
func (m *LedgerCleanerMethod) RequiredCondition() types.Condition {
	return types.NeedsNetworkConnection
}

func (m *LedgerCleanerMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	if ctx.Services == nil || ctx.Services.LedgerCleanerConfigure() == nil {
		return nil, rpcInternalInvariantError("ledger_cleaner: service unavailable")
	}

	var req struct {
		Ledger     jsonCppUInt32Field `json:"ledger"`
		MinLedger  jsonCppUInt32Field `json:"min_ledger"`
		MaxLedger  jsonCppUInt32Field `json:"max_ledger"`
		Full       jsonCppBoolField   `json:"full"`
		CheckNodes jsonCppBoolField   `json:"check_nodes"`
		FixTxns    jsonCppBoolField   `json:"fix_txns"`
		Stop       jsonCppBoolField   `json:"stop"`
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(params, &object); err != nil || object == nil {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, rpcInternalError("ledger_cleaner: parameter conversion failed", err)
	}

	optionalUInt := func(field jsonCppUInt32Field) *uint32 {
		if !field.present {
			return nil
		}
		return &field.value
	}
	optionalBool := func(field jsonCppBoolField) *bool {
		if !field.present {
			return nil
		}
		return &field.value
	}

	ctx.Services.LedgerCleanerConfigure()(types.LedgerCleanerParams{
		Ledger:     optionalUInt(req.Ledger),
		MinLedger:  optionalUInt(req.MinLedger),
		MaxLedger:  optionalUInt(req.MaxLedger),
		Full:       optionalBool(req.Full),
		CheckNodes: optionalBool(req.CheckNodes),
		FixTxns:    optionalBool(req.FixTxns),
		Stop:       req.Stop.value,
	})
	return map[string]any{"message": "Cleaner configured"}, nil
}
