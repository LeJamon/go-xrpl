package handlers

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/version"
)

// serverStartTime tracks when the server started for uptime calculation
var serverStartTime = time.Now()

// BuildVersion is the reported build version for server_info/server_state.
var BuildVersion = version.Version

// cachedHostID is resolved once at startup to avoid repeated syscalls.
var cachedHostID = resolveHostID()

func resolveHostID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "goXRPLd"
}

// ServerInfoMethod handles the server_info RPC method.
// This is the "human-readable" variant (rippled human=true).
type ServerInfoMethod struct{ BaseHandler }

func (m *ServerInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	info := buildServerInfo(true)

	response := map[string]interface{}{
		"info": info,
	}

	return response, nil
}

// buildServerInfo constructs the info/state object.
// When human is true it produces the server_info format (XRP decimals, converge_time_s, hostid).
// When human is false it produces the server_state format (drops integers, converge_time, load_base, etc.).
func buildServerInfo(human bool) map[string]interface{} {
	serverInfo := types.Services.Ledger.GetServerInfo()
	baseFee, reserveBase, reserveIncrement := types.Services.Ledger.GetCurrentFees()

	// Uptime in seconds
	uptimeDuration := time.Since(serverStartTime)
	uptime := int64(uptimeDuration.Seconds())

	// Complete ledgers string
	completeLedgers := serverInfo.CompleteLedgers
	if completeLedgers == "" {
		completeLedgers = "empty"
	}

	// Ledger hashes (uppercase hex, matching rippled)
	validatedLedgerHash := strings.ToUpper(fmt.Sprintf("%064x", serverInfo.ValidatedLedgerHash))
	closedLedgerHash := strings.ToUpper(fmt.Sprintf("%064x", serverInfo.ClosedLedgerHash))

	// Server state — use actual operating mode from service
	serverState := serverInfo.ServerState
	if serverState == "" {
		serverState = "full"
	}
	if serverInfo.Standalone {
		serverState = "standalone"
	}

	// Duration in microseconds for state accounting
	uptimeUs := uptimeDuration.Microseconds()

	info := map[string]interface{}{
		"build_version":     BuildVersion,
		"complete_ledgers":  completeLedgers,
		"io_latency_ms":     getIOLatencyMs(),
		"pubkey_node":       types.Services.NodePublicKey,
		"server_state":      serverState,
		"uptime":            uptime,
		"validation_quorum": 1, // TODO: get from consensus/validators
		"peers":             getPeerCount(),

		// Overflow/disconnect counters (string in rippled)
		"jq_trans_overflow":          "0", // TODO: track real overflow count
		"peer_disconnects":           "0", // TODO: track real disconnect count
		"peer_disconnects_resources": "0", // TODO: track real disconnect-resources count

		// State accounting
		"server_state_duration_us": fmt.Sprintf("%d", uptimeUs),
		"state_accounting": map[string]interface{}{
			"connected": map[string]interface{}{
				"duration_us": "0",
				"transitions": "0",
			},
			"disconnected": map[string]interface{}{
				"duration_us": "0",
				"transitions": "0",
			},
			"full": map[string]interface{}{
				"duration_us": fmt.Sprintf("%d", uptimeUs),
				"transitions": "1",
			},
			"syncing": map[string]interface{}{
				"duration_us": "0",
				"transitions": "0",
			},
			"tracking": map[string]interface{}{
				"duration_us": "0",
				"transitions": "0",
			},
		},
	}

	// hostid: only in human mode (server_info), matching rippled
	if human {
		info["hostid"] = cachedHostID
	}

	// time: rippled uses different formats for human vs machine
	if human {
		// rippled human format: "2024-Jan-15 12:34:56.789012 UTC"
		info["time"] = time.Now().UTC().Format("2006-Jan-02 15:04:05.000000 UTC")
	} else {
		// rippled machine format: ISO 8601
		info["time"] = time.Now().UTC().Format(time.RFC3339)
	}

	// last_close: converge_time_s (float seconds) for human, converge_time (int ms) for machine
	if human {
		info["last_close"] = map[string]interface{}{
			"converge_time_s": 0.0, // TODO: get from consensus
			"proposers":       0,   // TODO: get from consensus
		}
	} else {
		info["last_close"] = map[string]interface{}{
			"converge_time": 0, // TODO: get from consensus (milliseconds)
			"proposers":     0, // TODO: get from consensus
		}
	}

	// load_factor: human mode is float (loadFactor/loadBase), machine mode has integers
	if human {
		info["load_factor"] = 1.0 // TODO: compute from fee tracker
	} else {
		info["load_base"] = 256                  // TODO: get from fee tracker (rippled default is 256)
		info["load_factor"] = 256                // TODO: get from fee tracker
		info["load_factor_server"] = 256         // TODO: get from fee tracker
		info["load_factor_fee_escalation"] = 256 // TODO: get from TxQ metrics
		info["load_factor_fee_queue"] = 256      // TODO: get from TxQ metrics
		info["load_factor_fee_reference"] = 256  // TODO: get from TxQ metrics
	}

	// Validated ledger info
	if human {
		baseFeeXRP := float64(baseFee) / 1_000_000.0
		reserveBaseXRP := float64(reserveBase) / 1_000_000.0
		reserveIncXRP := float64(reserveIncrement) / 1_000_000.0

		info["validated_ledger"] = map[string]interface{}{
			"age":              0, // TODO: compute from ledger close time vs now
			"base_fee_xrp":     baseFeeXRP,
			"hash":             validatedLedgerHash,
			"reserve_base_xrp": reserveBaseXRP,
			"reserve_inc_xrp":  reserveIncXRP,
			"seq":              serverInfo.ValidatedLedgerSeq,
		}

		// closed_ledger in human mode
		info["closed_ledger"] = map[string]interface{}{
			"age":              0,
			"base_fee_xrp":     baseFeeXRP,
			"hash":             closedLedgerHash,
			"reserve_base_xrp": reserveBaseXRP,
			"reserve_inc_xrp":  reserveIncXRP,
			"seq":              serverInfo.ClosedLedgerSeq,
		}
	} else {
		info["validated_ledger"] = map[string]interface{}{
			"base_fee":     baseFee,
			"close_time":   0, // TODO: get from ledger close time (ripple epoch seconds)
			"hash":         validatedLedgerHash,
			"reserve_base": reserveBase,
			"reserve_inc":  reserveIncrement,
			"seq":          serverInfo.ValidatedLedgerSeq,
		}

		// closed_ledger in machine mode
		info["closed_ledger"] = map[string]interface{}{
			"base_fee":     baseFee,
			"close_time":   0,
			"hash":         closedLedgerHash,
			"reserve_base": reserveBase,
			"reserve_inc":  reserveIncrement,
			"seq":          serverInfo.ClosedLedgerSeq,
		}
	}

	// published_ledger: rippled includes "none" if no published ledger,
	// or the sequence if it differs from closed.
	// For now, report the validated sequence as published.
	if serverInfo.ValidatedLedgerSeq > 0 {
		info["published_ledger"] = serverInfo.ValidatedLedgerSeq
	} else {
		info["published_ledger"] = "none"
	}

	// network_id: only include if configured (non-zero), matching rippled
	if serverInfo.NetworkID > 0 {
		info["network_id"] = serverInfo.NetworkID
	}

	// amendment_blocked: rippled only includes this when true
	if types.Services.Ledger.IsAmendmentBlocked() {
		info["amendment_blocked"] = true
	}

	return info
}

func getPeerCount() int {
	if types.Services.PeerCount != nil {
		return types.Services.PeerCount()
	}
	return 0
}

func getIOLatencyMs() int {
	if types.Services.IOLatencyMs != nil {
		return types.Services.IOLatencyMs()
	}
	return 0
}
