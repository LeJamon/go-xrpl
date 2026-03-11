package handlers

import (
	"encoding/json"
	"fmt"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// =============================================================================
// STUB RPC HANDLERS
// =============================================================================
//
// This file contains RPC methods that are stubs — either returning placeholder
// data or notImplemented errors. Each handler has a TODO comment explaining
// what's needed to implement it and what category it falls into:
//
//   [network]   — Requires P2P networking layer (not needed for standalone)
//   [admin]     — Admin/operational tool, low priority
//   [ledger]    — Requires additional ledger query capabilities
//   [validator] — Requires validator infrastructure
//   [engine]    — Requires transaction engine changes
//
// =============================================================================

// FetchInfoMethod handles the fetch_info RPC method.
// STUB: Returns empty info. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Reference: rippled FetchInfo.cpp → context.app.getFetchPack()
//   - Returns info about current fetch operations for missing ledger data
//   - Params: clear (bool) — resets fetch counters
type FetchInfoMethod struct{}

func (m *FetchInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Clear bool `json:"clear,omitempty"`
	}
	if params != nil {
		_ = json.Unmarshal(params, &request)
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	response := make(map[string]interface{})
	if request.Clear {
		response["clear"] = true
	}
	response["info"] = map[string]interface{}{}

	return response, nil
}

func (m *FetchInfoMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *FetchInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *FetchInfoMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// OwnerInfoMethod handles the owner_info RPC method.
// STUB: Returns notImplemented. Requires NetworkOPs integration.
//
// TODO [ledger]: Implement owner_info.
//   - Reference: rippled OwnerInfo.cpp → context.netOps.getOwnerInfo()
//   - Returns: owner-specific info about offers and account objects
//   - Params: account (required)
//   - This is a rarely-used legacy method; low priority
type OwnerInfoMethod struct{}

func (m *OwnerInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Account string `json:"account,omitempty"`
		Ident   string `json:"ident,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	account := request.Account
	if account == "" {
		account = request.Ident
	}
	if account == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: account")
	}

	return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"owner_info is not yet implemented — requires NetworkOPs.GetOwnerInfo")
}

func (m *OwnerInfoMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *OwnerInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *OwnerInfoMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

// LedgerHeaderMethod handles the ledger_header RPC method.
// PARTIAL: Works for ledger_index lookups. Missing ledger_hash support.
//
// TODO [ledger]: Support lookup by ledger_hash.
//   - Requires: hex.DecodeString(hash) → GetLedgerByHash(hash)
//   - Same pattern as ledger.go (which already handles hash lookup)
//   - Low priority since ledger_header is deprecated in favor of 'ledger'
type LedgerHeaderMethod struct{}

func (m *LedgerHeaderMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.LedgerSpecifier
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	var ledger types.LedgerReader
	var err error

	if request.LedgerHash != "" {
		// TODO [ledger]: support hash lookup (see type-level TODO)
		return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
			"ledger_header by hash is not yet implemented")
	} else if request.LedgerIndex != "" {
		ledgerIndexStr := request.LedgerIndex.String()
		switch ledgerIndexStr {
		case "validated":
			seq := types.Services.Ledger.GetValidatedLedgerIndex()
			ledger, err = types.Services.Ledger.GetLedgerBySequence(seq)
		case "closed":
			seq := types.Services.Ledger.GetClosedLedgerIndex()
			ledger, err = types.Services.Ledger.GetLedgerBySequence(seq)
		case "current":
			seq := types.Services.Ledger.GetCurrentLedgerIndex()
			ledger, err = types.Services.Ledger.GetLedgerBySequence(seq)
		default:
			var seq uint32
			if _, scanErr := fmt.Sscanf(ledgerIndexStr, "%d", &seq); scanErr == nil {
				ledger, err = types.Services.Ledger.GetLedgerBySequence(seq)
			} else {
				return nil, types.RpcErrorInvalidParams("Invalid ledger_index: " + ledgerIndexStr)
			}
		}
	} else {
		seq := types.Services.Ledger.GetValidatedLedgerIndex()
		ledger, err = types.Services.Ledger.GetLedgerBySequence(seq)
	}

	if err != nil {
		return nil, types.RpcErrorLgrNotFound("Ledger not found: " + err.Error())
	}

	response := map[string]interface{}{
		"ledger_index": ledger.Sequence(),
		"closed":       ledger.IsClosed(),
	}

	hash := ledger.Hash()
	if hash != [32]byte{} {
		response["ledger_hash"] = fmt.Sprintf("%X", hash)
	}
	parentHash := ledger.ParentHash()
	if parentHash != [32]byte{} {
		response["parent_hash"] = fmt.Sprintf("%X", parentHash)
	}

	return response, nil
}

func (m *LedgerHeaderMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *LedgerHeaderMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LedgerHeaderMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// LedgerRequestMethod handles the ledger_request RPC method.
// STUB: Returns error. Network-only — requests missing ledgers from peers.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Reference: rippled LedgerRequest.cpp
//   - Triggers a fetch of a specific ledger from the network
//   - In standalone mode, correctly returns notSynced
type LedgerRequestMethod struct{}

func (m *LedgerRequestMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	if types.Services.Ledger.IsStandalone() {
		return nil, types.NewRpcError(types.RpcNOT_SYNCED, "notSynced", "notSynced",
			"Not synced to the network")
	}

	return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"ledger_request is not yet implemented — requires network ledger fetching")
}

func (m *LedgerRequestMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *LedgerRequestMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LedgerRequestMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// LedgerCleanerMethod handles the ledger_cleaner RPC method.
// STUB: Returns error. Admin-only maintenance tool.
//
// TODO [admin]: Implement when adding ledger integrity checking.
//   - Reference: rippled LedgerCleaner.cpp
//   - Schedules verification and repair of stored ledger data
//   - Params: ledger (sequence), max_ledger, min_ledger, full (bool)
//   - Requires: LedgerCleaner background service
type LedgerCleanerMethod struct{}

func (m *LedgerCleanerMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"ledger_cleaner is not yet implemented — requires LedgerCleaner service")
}

func (m *LedgerCleanerMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *LedgerCleanerMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LedgerCleanerMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// LedgerDiffMethod handles the ledger_diff RPC method.
// STUB: Returns error. Only available via gRPC in rippled.
//
// NOTE: This is gRPC-only in rippled and is NOT available via JSON-RPC.
//   It computes the state diff between two ledger versions.
//   This stub exists for completeness but may never need implementation.
type LedgerDiffMethod struct{}

func (m *LedgerDiffMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"ledger_diff is only available via gRPC in rippled — JSON-RPC not supported")
}

func (m *LedgerDiffMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *LedgerDiffMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LedgerDiffMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// TxReduceRelayMethod handles the tx_reduce_relay RPC method.
// STUB: Returns zero counters. Network-only relay optimization.
//
// TODO [network]: Implement when adding P2P transaction relay.
//   - Reference: rippled TxReduceRelay.cpp
//   - Returns statistics about reduced transaction relay (squelching)
//   - Requires: Transaction relay subsystem with squelch tracking
type TxReduceRelayMethod struct{}

func (m *TxReduceRelayMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return map[string]interface{}{
		"transactions": map[string]interface{}{
			"total_relayed":   0,
			"total_squelched": 0,
		},
	}, nil
}

func (m *TxReduceRelayMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *TxReduceRelayMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *TxReduceRelayMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// SimulateMethod handles the simulate RPC method.
// Runs a transaction against a snapshot of the open ledger without committing.
// Reference: rippled Simulate.cpp
type SimulateMethod struct{}

func (m *SimulateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		TxBlob string                 `json:"tx_blob,omitempty"`
		TxJSON map[string]interface{} `json:"tx_json,omitempty"`
		Binary bool                   `json:"binary,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	hasTxBlob := request.TxBlob != ""
	hasTxJSON := request.TxJSON != nil && len(request.TxJSON) > 0

	if hasTxBlob && hasTxJSON {
		return nil, types.RpcErrorInvalidParams("Can only include one of `tx_blob` and `tx_json`")
	}
	if !hasTxBlob && !hasTxJSON {
		return nil, types.RpcErrorInvalidParams("Neither `tx_blob` nor `tx_json` included")
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	var txJSON []byte
	var txJsonMap map[string]interface{}

	if hasTxBlob {
		// Decode tx_blob to get tx_json
		decoded, err := binarycodec.Decode(request.TxBlob)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid tx_blob: " + err.Error())
		}
		txJsonMap = decoded
		txJSON, err = json.Marshal(decoded)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to marshal decoded tx_blob")
		}
	} else {
		txJsonMap = request.TxJSON
		var err error
		txJSON, err = json.Marshal(request.TxJSON)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to marshal tx_json")
		}
	}

	// Run the transaction in simulation mode (snapshot, no commit)
	result, err := types.Services.Ledger.SimulateTransaction(txJSON)
	if err != nil {
		return nil, types.RpcErrorInternal("Simulation failed: " + err.Error())
	}

	response := map[string]interface{}{
		"engine_result":         result.EngineResult,
		"engine_result_code":    result.EngineResultCode,
		"engine_result_message": result.EngineResultMessage,
		"applied":               result.Applied,
		"ledger_index":          result.CurrentLedger,
	}

	if request.Binary {
		if encoded, err := binarycodec.Encode(txJsonMap); err == nil {
			response["tx_blob"] = encoded
		}
	} else {
		response["tx_json"] = txJsonMap
	}

	return response, nil
}

func (m *SimulateMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *SimulateMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *SimulateMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

// ConnectMethod handles the connect RPC method.
// STUB: Returns message without actually connecting. Network-only.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Reference: rippled Connect.cpp → context.app.overlay().connect()
//   - Params: ip (required), port (optional, default 51235)
//   - Should initiate an outbound peer connection
type ConnectMethod struct{}

func (m *ConnectMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		IP   string `json:"ip"`
		Port int    `json:"port,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if request.IP == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: ip")
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	if types.Services.Ledger.IsStandalone() {
		return nil, types.NewRpcError(types.RpcNOT_SYNCED, "notSynced", "notSynced",
			"Cannot connect to peers in standalone mode")
	}

	port := request.Port
	if port == 0 {
		port = 51235
	}

	return map[string]interface{}{
		"message": fmt.Sprintf("attempting connection to IP:%s port:%d", request.IP, port),
	}, nil
}

func (m *ConnectMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *ConnectMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *ConnectMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// PrintMethod handles the print RPC method.
// STUB: Returns acknowledgment. Admin debug tool.
//
// TODO [admin]: Implement internal state printing for debugging.
//   - Reference: rippled Print.cpp → context.app.journal()
//   - Returns internal debug information about server state
//   - Low priority admin debugging tool
type PrintMethod struct{}

func (m *PrintMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return map[string]interface{}{}, nil
}

func (m *PrintMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *PrintMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *PrintMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// ValidatorInfoMethod handles the validator_info RPC method.
// STUB: Returns notValidator. Requires validator configuration.
//
// TODO [validator]: Implement when adding validator support.
//   - Reference: rippled ValidatorInfo.cpp
//   - Returns: master_key, ephemeral_key, seq, domain, signing_key, token
//   - Requires: Server to be configured as a validator with keys
//   - In standalone mode, correctly returns notValidator
type ValidatorInfoMethod struct{}

func (m *ValidatorInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.NewRpcError(types.RpcNOT_VALIDATOR, "notValidator", "notValidator",
		"This server is not configured as a validator")
}

func (m *ValidatorInfoMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *ValidatorInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *ValidatorInfoMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// CanDeleteMethod handles the can_delete RPC method.
// STUB: Returns notEnabled. Requires SHAMapStore advisory delete.
//
// TODO [admin]: Implement when adding online delete support.
//   - Reference: rippled CanDelete.cpp → context.app.getSHAMapStore()
//   - Used to manage advisory deletion of old ledgers
//   - Requires: SHAMapStore with online_delete configuration
type CanDeleteMethod struct{}

func (m *CanDeleteMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.NewRpcError(types.RpcNOT_ENABLED, "notEnabled", "notEnabled",
		"Advisory delete is not enabled — requires SHAMapStore configuration")
}

func (m *CanDeleteMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *CanDeleteMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *CanDeleteMethod) RequiredCondition() types.Condition {
	return types.NoCondition
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
type GetCountsMethod struct{}

func (m *GetCountsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	serverInfo := types.Services.Ledger.GetServerInfo()
	return map[string]interface{}{
		"standalone": serverInfo.Standalone,
	}, nil
}

func (m *GetCountsMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *GetCountsMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *GetCountsMethod) RequiredCondition() types.Condition {
	return types.NoCondition
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
type LogLevelMethod struct{}

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

func (m *LogLevelMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *LogLevelMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LogLevelMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// LogRotateMethod handles the log_rotate RPC method (logrotate).
// STUB: Returns acknowledgment without actually rotating.
//
// TODO [admin]: Wire to actual log file rotation.
//   - Reference: rippled LogRotate.cpp
//   - Closes and reopens log files for external log rotation tools
//   - Requires: File-based logging with rotation support
type LogRotateMethod struct{}

func (m *LogRotateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return map[string]interface{}{
		"message": "Log rotation requested",
	}, nil
}

func (m *LogRotateMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *LogRotateMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LogRotateMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// UnlListMethod handles the unl_list RPC method.
// STUB: Returns empty list. Network-only — tracks negative UNL.
//
// TODO [network]: Implement when adding UNL/consensus support.
//   - Reference: rippled UNLList.cpp
//   - Returns the current Unique Node List (trusted validators)
//   - In standalone mode, there is no UNL
type UnlListMethod struct{}

func (m *UnlListMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return map[string]interface{}{
		"unl": []interface{}{},
	}, nil
}

func (m *UnlListMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *UnlListMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *UnlListMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// BlackListMethod handles the black_list (blacklist) RPC method.
// STUB: Returns empty list. Network-only — manages IP blacklisting.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Reference: rippled BlackList.cpp
//   - Returns/manages the peer IP blacklist
//   - Params: threshold (int) — auto-blacklist peers above this score
type BlackListMethod struct{}

func (m *BlackListMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return map[string]interface{}{
		"blacklist": []interface{}{},
	}, nil
}

func (m *BlackListMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *BlackListMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *BlackListMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
