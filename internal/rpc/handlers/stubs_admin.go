package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	xrpllog "github.com/LeJamon/goXRPLd/log"
)

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
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"ledger_cleaner is not yet implemented — requires LedgerCleaner service")
}

// PrintMethod handles the print RPC method.
// Mirrors rippled Print.cpp, which returns the root of a property-stream tree
// of internal subsystem state. goXRPL has no property-stream registry, so this
// aggregates the real state already exposed to the RPC layer — ledger
// positions, overlay peers, lifecycle counters, last-close info and the
// operating-mode state machine. Sections are included only when their backing
// service is wired. A string subtree selector (rippled Print.cpp:33-37) narrows
// the output to a single named section.
//
// Cumulative counters (peer_disconnects, jq_trans_overflow, state-accounting
// transitions/durations) are rendered as decimal strings to match rippled's
// std::to_string convention (NetworkOPs.cpp:2986-2991, 4843-4846) and goXRPL's
// own server_info; sequence numbers and proposer/converge counts stay numeric.
type PrintMethod struct{ AdminHandler }

func (m *PrintMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	out := map[string]interface{}{}

	info := ctx.Services.Ledger.GetServerInfo()
	ledger := map[string]interface{}{
		"standalone":        info.Standalone,
		"server_state":      info.ServerState,
		"open_ledger_seq":   info.OpenLedgerSeq,
		"closed_ledger_seq": info.ClosedLedgerSeq,
		"complete_ledgers":  info.CompleteLedgers,
		"network_id":        info.NetworkID,
	}
	if info.HaveValidated {
		ledger["validated_ledger_seq"] = info.ValidatedLedgerSeq
	}
	out["ledger"] = ledger

	if ctx.PeerSource != nil {
		overlay := map[string]interface{}{"count": ctx.PeerSource.PeerCount()}
		if peers := ctx.PeerSource.PeersJSON(); peers != nil {
			overlay["peers"] = peers
		}
		if cluster := ctx.PeerSource.ClusterJSON(); cluster != nil {
			overlay["cluster"] = cluster
		}
		out["overlay"] = overlay
	}

	counters := map[string]interface{}{}
	if ctx.Services.PeerDisconnects != nil {
		total, resources := ctx.Services.PeerDisconnects()
		counters["peer_disconnects"] = fmt.Sprintf("%d", total)
		counters["peer_disconnects_resources"] = fmt.Sprintf("%d", resources)
	}
	if ctx.Services.JqTransOverflow != nil {
		counters["jq_trans_overflow"] = fmt.Sprintf("%d", ctx.Services.JqTransOverflow())
	}
	if len(counters) > 0 {
		out["counters"] = counters
	}

	if ctx.Services.LastCloseInfo != nil {
		proposers, convergeMs := ctx.Services.LastCloseInfo()
		out["last_close"] = map[string]interface{}{
			"proposers":        proposers,
			"converge_time_ms": convergeMs,
		}
	}

	if ctx.Services.StateAccounting != nil {
		if snap := ctx.Services.StateAccounting(); len(snap.Modes) > 0 {
			states := make(map[string]interface{}, len(snap.Modes))
			for mode, e := range snap.Modes {
				states[mode] = map[string]interface{}{
					"transitions": fmt.Sprintf("%d", e.Transitions),
					"duration_us": fmt.Sprintf("%d", e.DurationUs),
				}
			}
			out["state_accounting"] = map[string]interface{}{
				"states":              states,
				"current_duration_us": fmt.Sprintf("%d", snap.CurrentDurationUs),
			}
		}
	}

	if section := printSection(params); section != "" {
		if v, ok := out[section]; ok {
			return map[string]interface{}{section: v}, nil
		}
		return map[string]interface{}{}, nil
	}

	return out, nil
}

// printSection returns the optional subtree selector, mirroring rippled's
// doPrint reading params[jss::params][0] (Print.cpp:33-37). An empty string
// means no selector, so the full aggregate is returned.
func printSection(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var req struct {
		Params []string `json:"params"`
	}
	if json.Unmarshal(params, &req) != nil || len(req.Params) == 0 {
		return ""
	}
	return req.Params[0]
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
	return nil, types.RpcErrorNotEnabled("Advisory delete is not enabled — requires SHAMapStore configuration")
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
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	serverInfo := ctx.Services.Ledger.GetServerInfo()
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

	// GET: return current levels snapshot
	if request.Severity == "" {
		global, partitions := xrpllog.GetCurrentLevels()
		levels := map[string]string{
			"base": xrpllog.LevelName(global),
		}
		for name, lvl := range partitions {
			levels[name] = xrpllog.LevelName(lvl)
		}
		return map[string]interface{}{"levels": levels}, nil
	}

	// SET: parse and apply the new level
	lvl, ok := xrpllog.ParseLevel(request.Severity)
	if !ok {
		return nil, types.RpcErrorInvalidParams("Invalid severity level: " + request.Severity)
	}

	if request.Partition != "" {
		xrpllog.SetPartitionLevel(request.Partition, lvl)
	} else {
		xrpllog.SetLevel(lvl)
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
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return map[string]interface{}{
		"message": "Log rotation requested",
	}, nil
}
