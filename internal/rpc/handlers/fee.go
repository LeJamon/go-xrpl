package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// FeeMethod handles the fee RPC method
type FeeMethod struct{}

func (m *FeeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Get fee settings from ledger service
	baseFee, reserveBase, reserveInc := types.Services.Ledger.GetCurrentFees()

	// Get current ledger index
	currentLedgerIndex := types.Services.Ledger.GetCurrentLedgerIndex()

	baseFeeStr := fmt.Sprintf("%d", baseFee)

	// Reference fee level is 256 (1 unit in fee level = 256)
	// In standalone mode, all fee levels are at reference
	referenceFeeLevel := "256"

	response := map[string]interface{}{
		"current_ledger_size": "0",
		"current_queue_size":  "0",
		"drops": map[string]interface{}{
			"base_fee":        baseFeeStr,
			"median_fee":      baseFeeStr,
			"minimum_fee":     baseFeeStr,
			"open_ledger_fee": baseFeeStr,
		},
		"expected_ledger_size": "24",
		"ledger_current_index": currentLedgerIndex,
		"levels": map[string]interface{}{
			"median_level":      referenceFeeLevel,
			"minimum_level":     referenceFeeLevel,
			"open_ledger_level": referenceFeeLevel,
			"reference_level":   referenceFeeLevel,
		},
		"max_queue_size": "480",
	}

	// Support counters parameter
	var request struct {
		Counters bool `json:"counters,omitempty"`
	}
	if params != nil {
		json.Unmarshal(params, &request)
	}

	if request.Counters {
		response["reserve_base_drops"] = fmt.Sprintf("%d", reserveBase)
		response["reserve_inc_drops"] = fmt.Sprintf("%d", reserveInc)
	}

	return response, nil
}

func (m *FeeMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *FeeMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
