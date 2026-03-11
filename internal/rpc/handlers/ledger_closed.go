package handlers

import (
	"encoding/hex"
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// LedgerClosedMethod handles the ledger_closed RPC method
type LedgerClosedMethod struct{}

func (m *LedgerClosedMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Check if ledger service is available
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Get the closed ledger index
	seq := types.Services.Ledger.GetClosedLedgerIndex()
	if seq == 0 {
		return nil, &types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "No closed ledger"}
	}

	// Get the ledger to retrieve its hash
	ledger, err := types.Services.Ledger.GetLedgerBySequence(seq)
	if err != nil {
		return nil, &types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "Closed ledger not found"}
	}

	hash := ledger.Hash()
	response := map[string]interface{}{
		"ledger_hash":  hex.EncodeToString(hash[:]),
		"ledger_index": seq,
	}

	return response, nil
}

func (m *LedgerClosedMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *LedgerClosedMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LedgerClosedMethod) RequiredCondition() types.Condition {
	return types.NeedsClosedLedger
}
