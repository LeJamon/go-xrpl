package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

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

// CanDeleteMethod handles the can_delete RPC method, mirroring rippled
// CanDelete.cpp. It manages the advisory deletion boundary tracked by the
// SHAMapStore advisory-delete state (internal/ledger/shamapstore): without a
// can_delete param it returns the current boundary; with one it sets it.
//
// Accepted can_delete values match rippled exactly: a ledger sequence (JSON
// number or numeric string), a 64-char ledger hash (resolved to its seq),
// "never" (0), "always" (max uint32), or "now" (the last rotated ledger,
// notReady if none). The method returns notEnabled unless advisory_delete is
// configured, matching rippled's getSHAMapStore().advisoryDelete() gate.
type CanDeleteMethod struct{ AdminHandler }

func (m *CanDeleteMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if ctx.Services == nil {
		return nil, types.RpcErrorNotEnabled("")
	}
	store := ctx.Services.AdvisoryDeleteState
	if store == nil || !store.AdvisoryDelete() {
		return nil, types.RpcErrorNotEnabled("")
	}

	var request struct {
		CanDelete json.RawMessage `json:"can_delete,omitempty"`
	}
	if params != nil {
		_ = json.Unmarshal(params, &request)
	}

	if len(request.CanDelete) == 0 {
		return map[string]interface{}{"can_delete": store.GetCanDelete()}, nil
	}

	seq, rpcErr := resolveCanDeleteSeq(ctx, store, request.CanDelete)
	if rpcErr != nil {
		return nil, rpcErr
	}
	stored, err := store.SetCanDelete(seq)
	if err != nil {
		return nil, types.RpcErrorInternal("failed to persist can_delete: " + err.Error())
	}
	return map[string]interface{}{"can_delete": stored}, nil
}

// resolveCanDeleteSeq interprets the can_delete param into a ledger sequence,
// mirroring the branch logic in rippled CanDelete.cpp:42-88.
func resolveCanDeleteSeq(ctx *types.RpcContext, store types.AdvisoryDeleteStore, raw json.RawMessage) (uint32, *types.RpcError) {
	// JSON number (rippled canDelete.isUInt()).
	var num uint32
	if err := json.Unmarshal(raw, &num); err == nil {
		return num, nil
	}

	var str string
	if err := json.Unmarshal(raw, &str); err != nil {
		return 0, types.RpcErrorInvalidParams("")
	}
	// rippled applies only boost::to_lower (CanDelete.cpp:53-54) — it does
	// not trim, so whitespace-padded input falls through to invalidParams.
	str = strings.ToLower(str)

	switch {
	case isAllDigits(str):
		n, err := strconv.ParseUint(str, 10, 32)
		if err != nil {
			return 0, types.RpcErrorInvalidParams("")
		}
		return uint32(n), nil
	case str == "never":
		return 0, nil
	case str == "always":
		return ^uint32(0), nil
	case str == "now":
		seq := store.GetLastRotated()
		if seq == 0 {
			return 0, types.RpcErrorNotReady("")
		}
		return seq, nil
	}

	// Ledger hash (64 hex chars) → resolve to its sequence.
	if len(str) == 64 {
		if hb, err := hex.DecodeString(str); err == nil {
			var h [32]byte
			copy(h[:], hb)
			lr, lerr := ctx.Services.Ledger.GetLedgerByHash(h)
			if lerr != nil || lr == nil {
				return 0, types.RpcErrorLgrNotFound("ledgerNotFound")
			}
			return lr.Sequence(), nil
		}
	}
	return 0, types.RpcErrorInvalidParams("")
}

// isAllDigits reports whether s is non-empty and consists solely of ASCII
// digits, mirroring rippled's find_first_not_of("0123456789") == npos check.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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
// Mirrors rippled LogRotate.cpp: closes and reopens the log file so external
// rotation tools can rename it and have writes continue against a fresh file.
// When logging is not file-backed (stdout/stderr) there is nothing to rotate.
type LogRotateMethod struct{ AdminHandler }

func (m *LogRotateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if err := xrpllog.Rotate(); err != nil {
		if errors.Is(err, xrpllog.ErrLogNotRotatable) {
			return map[string]interface{}{
				"message": "logging is not file-backed; nothing to rotate",
			}, nil
		}
		// Mirror rippled's Logs::rotate(): a failed reopen yields a success
		// result carrying the failure message, never an RPC error.
		return map[string]interface{}{
			"message": "The log file could not be closed and reopened.",
		}, nil
	}

	return map[string]interface{}{
		"message": "The log file was closed and reopened.",
	}, nil
}
