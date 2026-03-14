package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// FeeMethod handles the fee RPC method.
// See rippled: src/xrpld/rpc/handlers/Fee1.cpp -> TxQ::doRPC()
//
// Without a real TxQ implementation, fee escalation metrics are approximated
// using rippled's default idle-state values:
//   - reference_level = 256 (baseLevel in rippled TxQ.h)
//   - expected_ledger_size = 32 (minimumTxnInLedger default, non-standalone)
//   - max_queue_size = 20 * expected = 640 (ledgersInQueue=20 * txPerLedger)
//   - current_ledger_size = 0 (no open ledger tx count available yet)
//   - current_queue_size = 0 (no TxQ)
//   - drops: median_fee/minimum_fee/open_ledger_fee all equal base_fee (no escalation)
type FeeMethod struct{}

func (m *FeeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Get fee settings from ledger service
	baseFee, _, _ := types.Services.Ledger.GetCurrentFees()

	// Get current ledger index
	currentLedgerIndex := types.Services.Ledger.GetCurrentLedgerIndex()

	baseFeeStr := fmt.Sprintf("%d", baseFee)

	// Reference fee level is 256 (baseLevel in rippled TxQ.h).
	// Without a real TxQ implementation, all fee levels stay at the reference level.
	referenceFeeLevel := "256"

	// Rippled defaults: minimumTxnInLedger=32 (non-standalone), ledgersInQueue=20.
	// In standalone: minimumTxnInLedgerSA=1000.
	// max_queue_size = ledgersInQueue * txPerLedger.
	expectedLedgerSize := "32"
	maxQueueSize := "640"
	if types.Services.Ledger.IsStandalone() {
		expectedLedgerSize = "1000"
		maxQueueSize = "20000"
	}

	response := map[string]interface{}{
		"current_ledger_size": "0",
		"current_queue_size":  "0",
		"drops": map[string]interface{}{
			"base_fee":        baseFeeStr,
			"median_fee":      baseFeeStr,
			"minimum_fee":     baseFeeStr,
			"open_ledger_fee": baseFeeStr,
		},
		"expected_ledger_size": expectedLedgerSize,
		"ledger_current_index": currentLedgerIndex,
		"levels": map[string]interface{}{
			"median_level":      referenceFeeLevel,
			"minimum_level":     referenceFeeLevel,
			"open_ledger_level": referenceFeeLevel,
			"reference_level":   referenceFeeLevel,
		},
		"max_queue_size": maxQueueSize,
	}

	return response, nil
}

func (m *FeeMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *FeeMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *FeeMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
