package handlers

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

// LedgerAcceptMethod handles the ledger_accept RPC method
// This is a standalone-mode only command that manually closes and validates
// the current open ledger, allowing progression without consensus.
type LedgerAcceptMethod struct{}

func (m *LedgerAcceptMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	if !ctx.Services.Ledger.IsStandalone() {
		return nil, types.RpcErrorNotStandalone("ledger_accept is only available in standalone mode")
	}

	// Optional close_time (XRPL ripple-epoch seconds). Not a rippled
	// RPC field — goxrpl-specific extension for differential testing
	// against rippled standalone, where unsynchronized time.Now()
	// clocks would otherwise produce divergent ledger headers.
	// Malformed input is an error rather than a silent fallback to
	// time.Now() so callers asking for determinism don't get a
	// non-deterministic close.
	var req struct {
		CloseTime *uint32 `json:"close_time,omitempty"`
	}
	closeTime := time.Time{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, types.NewRpcError(types.RpcINVALID_PARAMS, "invalidParams", "invalidParams",
				fmt.Sprintf("ledger_accept: malformed params: %v", err))
		}
		if req.CloseTime != nil {
			closeTime = time.Unix(int64(*req.CloseTime)+protocol.RippleEpochUnix, 0).UTC()
		}
	}

	closedSeq, err := ctx.Services.Ledger.AcceptLedgerAt(ctx.Context, closeTime)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to accept ledger: %v", err))
	}

	response := map[string]any{
		"ledger_current_index": closedSeq + 1, // Return the new open ledger index
	}

	return response, nil
}

func (m *LedgerAcceptMethod) RequiredRole() types.Role {
	return types.RoleAdmin // ledger_accept requires admin privileges
}

func (m *LedgerAcceptMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LedgerAcceptMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
