package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// LedgerCleanerMethod handles the ledger_cleaner admin RPC. It configures the
// background ledger-integrity verifier and returns its status, mirroring
// rippled's ledger_cleaner (LedgerCleaner.cpp). A request with no parameters is
// treated as a non-destructive status query.
//
// Parameters (all optional, mirroring rippled): ledger (single sequence,
// forces a deep check), min_ledger, max_ledger, full (bool, deep check),
// check_nodes (bool, walk every node), stop (bool, halt an in-progress run).
type LedgerCleanerMethod struct{ AdminHandler }

// RequiredCondition mirrors rippled's handler-table entry
// {"ledger_cleaner", …, NEEDS_NETWORK_CONNECTION} (Handler.cpp:121-124): the
// command is unavailable until the node has network state. The dispatcher's
// conditionMet enforces the network/sync gate before Handle runs.
func (m *LedgerCleanerMethod) RequiredCondition() types.Condition {
	return types.NeedsNetworkConnection
}

func (m *LedgerCleanerMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if ctx.Services == nil || ctx.Services.LedgerCleanerConfigure == nil {
		return nil, types.RpcErrorInternal("Ledger cleaner service not available")
	}

	var req struct {
		Ledger     *uint32 `json:"ledger,omitempty"`
		MinLedger  *uint32 `json:"min_ledger,omitempty"`
		MaxLedger  *uint32 `json:"max_ledger,omitempty"`
		Full       *bool   `json:"full,omitempty"`
		CheckNodes *bool   `json:"check_nodes,omitempty"`
		Stop       *bool   `json:"stop,omitempty"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, types.RpcErrorInvalidParams("ledger_cleaner: malformed params")
		}
	}

	// No parameters at all → non-destructive status query.
	hasParams := req.Ledger != nil || req.MinLedger != nil || req.MaxLedger != nil ||
		req.Full != nil || req.CheckNodes != nil || req.Stop != nil
	if !hasParams {
		if ctx.Services.LedgerCleanerStatusFn != nil {
			return statusResponse(ctx.Services.LedgerCleanerStatusFn(), false), nil
		}
		return statusResponse(types.LedgerCleanerStatus{State: "idle"}, false), nil
	}

	st := ctx.Services.LedgerCleanerConfigure(types.LedgerCleanerParams{
		Ledger:     req.Ledger,
		MinLedger:  req.MinLedger,
		MaxLedger:  req.MaxLedger,
		Full:       req.Full != nil && *req.Full,
		CheckNodes: req.CheckNodes != nil && *req.CheckNodes,
		Stop:       req.Stop != nil && *req.Stop,
	})
	return statusResponse(st, true), nil
}

// statusResponse renders a cleaner status as the RPC result. configured marks a
// request that changed the cleaner's state (vs a pure status query). The
// status / min_ledger / max_ledger / check_nodes / fail_counts fields mirror
// rippled's PropertyStream output (LedgerCleaner.cpp:110-127); the *_checked /
// missing_nodes progress counters are the go-xrpl addition the issue asks for.
func statusResponse(st types.LedgerCleanerStatus, configured bool) map[string]any {
	resp := map[string]any{
		"status":          st.State,
		"check_nodes":     st.CheckNodes,
		"ledgers_checked": st.LedgersChecked,
		"nodes_checked":   st.NodesChecked,
		"missing_nodes":   st.MissingNodes,
	}
	if configured {
		resp["message"] = "Ledger cleaner configured"
	}
	if st.State == "running" {
		resp["min_ledger"] = st.MinLedger
		resp["max_ledger"] = st.MaxLedger
	}
	if st.Failures > 0 {
		resp["fail_counts"] = st.Failures
	}
	if st.LastError != "" {
		resp["last_error"] = st.LastError
	}
	return resp
}
