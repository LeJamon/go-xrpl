package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// FeeMethod handles the fee RPC method
type FeeMethod struct{}

func (m *FeeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Check if ledger service is available
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Get fee settings from ledger service
	baseFee, _, _ := types.Services.Ledger.GetCurrentFees()

	// Get current ledger index
	currentLedgerIndex := types.Services.Ledger.GetCurrentLedgerIndex()

	// Format base fee as string
	baseFeeStr := fmt.Sprintf("%d", baseFee)

	response := map[string]interface{}{
		"current_ledger_size": "0",
		"current_queue_size":  "0",
		"drops": map[string]interface{}{
			"base_fee":        baseFeeStr,
			"median_fee":      baseFeeStr, // In standalone, no median calculation
			"minimum_fee":     baseFeeStr,
			"open_ledger_fee": baseFeeStr,
		},
		"expected_ledger_size": "0",
		"ledger_current_index": currentLedgerIndex,
		"levels": map[string]interface{}{
			"median_level":      "256",
			"minimum_level":     "256",
			"open_ledger_level": "256",
			"reference_level":   "256",
		},
		"max_queue_size": "2000",
	}

	return response, nil
}

func (m *FeeMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *FeeMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
