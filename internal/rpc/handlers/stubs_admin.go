package handlers

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

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
// STUB: Returns acknowledgment. Admin debug tool.
//
// TODO [admin]: Implement internal state printing for debugging.
//   - Reference: rippled Print.cpp → context.app.journal()
//   - Returns internal debug information about server state
//   - Low priority admin debugging tool
type PrintMethod struct{ AdminHandler }

func (m *PrintMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return map[string]interface{}{}, nil
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
// Mirrors the subset of rippled GetCounts.cpp that goXRPL has real data for:
// the node-store I/O counters, server uptime, and locally-held transactions.
// rippled's object-type counts, SLE / accepted-ledger cache rates, relational
// DB sizes and read-thread-pool gauges have no goXRPL equivalent and are
// omitted rather than fabricated. The node_* counters are emitted as decimal
// strings to match rippled's NodeStore::Database::getCountsJson
// (Database.cpp:283-288), which stringifies them via std::to_string.
type GetCountsMethod struct{ AdminHandler }

// uptimeText renders a duration the way rippled's GetCounts.cpp textTime does:
// the largest non-zero units in descending order, comma-separated and
// pluralized (e.g. "1 day, 3 hours, 20 seconds"). Zero-valued units are
// skipped; a sub-second uptime yields the empty string, as in rippled.
func uptimeText(d time.Duration) string {
	units := []struct {
		name string
		size time.Duration
	}{
		{"year", 365 * 24 * time.Hour},
		{"day", 24 * time.Hour},
		{"hour", time.Hour},
		{"minute", time.Minute},
		{"second", time.Second},
	}

	var parts []string
	for _, u := range units {
		n := int64(d / u.size)
		if n == 0 {
			continue
		}
		d -= time.Duration(n) * u.size
		label := u.name
		if n > 1 {
			label += "s"
		}
		parts = append(parts, strconv.FormatInt(n, 10)+" "+label)
	}
	return strings.Join(parts, ", ")
}

func (m *GetCountsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	result := map[string]interface{}{
		"standalone": ctx.Services.Ledger.GetServerInfo().Standalone,
	}

	if ctx.Services.GetCounts == nil {
		return result, nil
	}

	c := ctx.Services.GetCounts()
	result["standalone"] = c.Standalone
	result["uptime"] = uptimeText(time.Since(serverStartTime))

	// rippled emits local_txs only when there are locally-held transactions
	// (GetCounts.cpp:96-100); mirror that gate rather than always emitting 0.
	if c.LocalTxs > 0 {
		result["local_txs"] = c.LocalTxs
	}

	if ns := c.NodeStore; ns != nil {
		result["node_writes"] = strconv.FormatUint(ns.Writes, 10)
		result["node_reads_total"] = strconv.FormatUint(ns.Reads, 10)
		result["node_reads_hit"] = strconv.FormatUint(ns.FetchHits, 10)
		result["node_written_bytes"] = strconv.FormatUint(ns.WriteBytes, 10)
		result["node_read_bytes"] = strconv.FormatUint(ns.ReadBytes, 10)
	}

	return result, nil
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
