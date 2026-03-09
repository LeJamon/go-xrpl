package handlers

import (
	"encoding/hex"
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// LedgerRangeMethod handles the ledger_range RPC method
type LedgerRangeMethod struct{}

func (m *LedgerRangeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Parse parameters
	var request struct {
		StartLedger uint32 `json:"start_ledger"`
		StopLedger  uint32 `json:"stop_ledger"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
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

	// Check if ledger service is available
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Get ledger range from the ledger service
	result, err := types.Services.Ledger.GetLedgerRange(request.StartLedger, request.StopLedger)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to get ledger range: " + err.Error())
	}

	// Build ledgers array
	ledgers := make([]map[string]interface{}, 0, len(result.Hashes))
	for seq, hash := range result.Hashes {
		ledgers = append(ledgers, map[string]interface{}{
			"ledger_index": seq,
			"ledger_hash":  hex.EncodeToString(hash[:]),
		})
	}

	response := map[string]interface{}{
		"ledger_first": result.LedgerFirst,
		"ledger_last":  result.LedgerLast,
		"ledgers":      ledgers,
	}

	return response, nil
}

func (m *LedgerRangeMethod) RequiredRole() types.Role {
	return types.RoleAdmin // This method requires admin privileges
}

func (m *LedgerRangeMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
