package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// =============================================================================
// ADMIN STUB HANDLERS
// =============================================================================
//
// These handlers are admin/operational tools that require additional
// infrastructure (LedgerCleaner, logging framework, validator config, etc.).
// TODO [admin]: Implement when the corresponding infrastructure is in place.
// =============================================================================

// LedgerCleanerMethod handles the ledger_cleaner RPC method.
// STUB: Returns error. Admin-only maintenance tool.
//
// TODO [admin]: Implement when adding ledger integrity checking.
//   - Reference: rippled LedgerCleaner.cpp
//   - Schedules verification and repair of stored ledger data
//   - Params: ledger (sequence), max_ledger, min_ledger, full (bool)
//   - Requires: LedgerCleaner background service
type LedgerCleanerMethod struct{ AdminHandler }

func (m *LedgerCleanerMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"ledger_cleaner is not yet implemented — requires LedgerCleaner service")
}

// PrintMethod handles the print RPC method.
// STUB: Returns acknowledgment. Admin debug tool.
//
// TODO [admin]: Implement internal state printing for debugging.
//   - Reference: rippled Print.cpp → context.app.journal()
//   - Returns internal debug information about server state
//   - Low priority admin debugging tool
type PrintMethod struct{ AdminHandler }

func (m *PrintMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return map[string]interface{}{}, nil
}

// ValidatorInfoMethod handles the validator_info RPC method.
// STUB: Returns notValidator. Requires validator configuration.
//
// TODO [validator]: Implement when adding validator support.
//   - Reference: rippled ValidatorInfo.cpp
//   - Returns: master_key, ephemeral_key, seq, domain, signing_key, token
//   - Requires: Server to be configured as a validator with keys
//   - In standalone mode, correctly returns notValidator
type ValidatorInfoMethod struct{ AdminHandler }

func (m *ValidatorInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.NewRpcError(types.RpcNOT_VALIDATOR, "notValidator", "notValidator",
		"This server is not configured as a validator")
}

// CanDeleteMethod handles the can_delete RPC method.
// STUB: Returns notEnabled. Requires SHAMapStore advisory delete.
//
// TODO [admin]: Implement when adding online delete support.
//   - Reference: rippled CanDelete.cpp → context.app.getSHAMapStore()
//   - Used to manage advisory deletion of old ledgers
//   - Requires: SHAMapStore with online_delete configuration
type CanDeleteMethod struct{ AdminHandler }

func (m *CanDeleteMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.NewRpcError(types.RpcNOT_ENABLED, "notEnabled", "notEnabled",
		"Advisory delete is not enabled — requires SHAMapStore configuration")
}

// GetCountsMethod handles the get_counts RPC method.
// STUB: Returns minimal info. Admin diagnostic tool.
//
// TODO [admin]: Implement internal object count reporting.
//   - Reference: rippled GetCounts.cpp
//   - Returns: counts of internal objects (SHAMap nodes, SLE cache entries,
//     transaction counts, memory usage, etc.)
//   - Params: min_count (int) — only show objects above threshold
//   - Useful for debugging memory/performance issues
type GetCountsMethod struct{ AdminHandler }

func (m *GetCountsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	serverInfo := types.Services.Ledger.GetServerInfo()
	return map[string]interface{}{
		"standalone": serverInfo.Standalone,
	}, nil
}

// LogLevelMethod handles the log_level RPC method.
// STUB: Accepts level changes but doesn't actually modify logging.
//
// TODO [admin]: Wire to actual logging framework.
//   - Reference: rippled LogLevel.cpp
//   - When severity is empty: return current log levels for all partitions
//   - When severity is set: change the log level (optionally for a specific partition)
//   - Valid levels: trace, debug, info, warning, error, fatal
//   - Requires: Logging infrastructure with configurable levels
type LogLevelMethod struct{ AdminHandler }

func (m *LogLevelMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Severity  string `json:"severity,omitempty"`
		Partition string `json:"partition,omitempty"`
	}

	if params != nil {
		_ = json.Unmarshal(params, &request)
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	if request.Severity == "" {
		return map[string]interface{}{
			"levels": map[string]interface{}{
				"base": "info",
			},
		}, nil
	}

	validLevels := map[string]bool{
		"trace": true, "debug": true, "info": true,
		"warning": true, "error": true, "fatal": true,
	}
	if !validLevels[request.Severity] {
		return nil, types.RpcErrorInvalidParams("Invalid severity level: " + request.Severity)
	}

	return map[string]interface{}{}, nil
}

// LogRotateMethod handles the log_rotate RPC method (logrotate).
// STUB: Returns acknowledgment without actually rotating.
//
// TODO [admin]: Wire to actual log file rotation.
//   - Reference: rippled LogRotate.cpp
//   - Closes and reopens log files for external log rotation tools
//   - Requires: File-based logging with rotation support
type LogRotateMethod struct{ AdminHandler }

func (m *LogRotateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return map[string]interface{}{
		"message": "Log rotation requested",
	}, nil
}
