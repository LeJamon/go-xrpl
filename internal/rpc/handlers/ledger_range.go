package handlers

import (
	"encoding/json"
	"slices"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// LedgerRangeMethod handles the ledger_range RPC method
type LedgerRangeMethod struct{ AdminHandler }

func (m *LedgerRangeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	// Parse parameters
	var request struct {
		StartLedger uint32 `json:"start_ledger"`
		StopLedger  uint32 `json:"stop_ledger"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// Validate range
	if request.StartLedger == 0 || request.StopLedger == 0 {
		return nil, types.RpcErrorInvalidParams("start_ledger and stop_ledger are required")
	}

	if request.StartLedger > request.StopLedger {
		return nil, types.RpcErrorInvalidParams("start_ledger cannot be greater than stop_ledger")
	}

	// Limit range size to prevent abuse
	if request.StopLedger-request.StartLedger > 1000 {
		return nil, types.RpcErrorInvalidParams("Ledger range too large (max 1000 ledgers)")
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	result, err := ctx.Services.Ledger.GetLedgerRange(ctx.Context, request.StartLedger, request.StopLedger)
	if err != nil {
		return nil, rpcInternalError("ledger_range: ledger query failed", err)
	}

	sequences := make([]uint32, 0, len(result.Hashes))
	for seq := range result.Hashes {
		sequences = append(sequences, seq)
	}
	slices.Sort(sequences)

	ledgers := make([]map[string]any, 0, len(sequences))
	for _, seq := range sequences {
		ledgers = append(ledgers, map[string]any{
			"ledger_index": seq,
			"ledger_hash":  FormatLedgerHash(result.Hashes[seq]),
		})
	}

	response := map[string]any{
		"ledger_first": result.LedgerFirst,
		"ledger_last":  result.LedgerLast,
		"ledgers":      ledgers,
	}

	return response, nil
}
