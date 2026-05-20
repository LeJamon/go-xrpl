package handlers

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/LeJamon/goXRPLd/internal/observability"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/version"
)

// loadBase is the fee-tracker reference level. Mirrors rippled's
// LoadFeeTrack default at LoadFeeTrack.h — every load_factor_* field
// is expressed as a multiple of this base.
const loadBase uint64 = 256

// validatedLedgerAgeThreshold matches rippled's
// NetworkOPsImp::getServerInfo (NetworkOPs.cpp:2952): once the
// validated ledger is older than this the age is clamped to 0 in the
// JSON so monitoring dashboards don't render misleading values from a
// stalled node. 600s is rippled's published threshold.
const validatedLedgerAgeThreshold = 600 * time.Second

// stateAccountingModes is the fixed set of operating-mode keys
// rippled emits. Emitting them all (zero-filled when needed) keeps the
// shape stable for downstream consumers.
var stateAccountingModes = []string{
	"connected",
	"disconnected",
	"full",
	"syncing",
	"tracking",
}

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
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	info := buildServerInfo(ctx, true)

	response := map[string]interface{}{
		"info": info,
	}

	return response, nil
}

// buildServerInfo constructs the info/state object.
// When human is true it produces the server_info format (XRP decimals, converge_time_s, hostid).
// When human is false it produces the server_state format (drops integers, converge_time, load_base, etc.).
func buildServerInfo(ctx *types.RpcContext, human bool) map[string]interface{} {
	services := ctx.Services
	serverInfo := services.Ledger.GetServerInfo()
	baseFee, reserveBase, reserveIncrement := services.Ledger.GetCurrentFees()

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

	overflow, peerDisc, peerDiscRes := resolveDisconnectCounters(services)

	info := map[string]interface{}{
		"build_version":     BuildVersion,
		"complete_ledgers":  completeLedgers,
		"io_latency_ms":     observability.SchedLatencyMs(),
		"pubkey_node":       services.NodePublicKey,
		"server_state":      serverState,
		"uptime":            uptime,
		"validation_quorum": resolveValidationQuorum(services),
		"peers":             getPeerCount(ctx),

		// Overflow/disconnect counters (string in rippled).
		"jq_trans_overflow":          fmt.Sprintf("%d", overflow),
		"peer_disconnects":           fmt.Sprintf("%d", peerDisc),
		"peer_disconnects_resources": fmt.Sprintf("%d", peerDiscRes),

		"server_state_duration_us": fmt.Sprintf("%d", uptimeUs),
		"state_accounting":         resolveStateAccounting(services, serverState, uptimeUs),
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
	proposers := 0
	convergeTimeMs := 0
	if services.LastCloseInfo != nil {
		proposers, convergeTimeMs = services.LastCloseInfo()
	}
	if human {
		info["last_close"] = map[string]interface{}{
			"converge_time_s": float64(convergeTimeMs) / 1000.0,
			"proposers":       proposers,
		}
	} else {
		info["last_close"] = map[string]interface{}{
			"converge_time": convergeTimeMs,
			"proposers":     proposers,
		}
	}

	// load_factor: human mode is float (loadFactor/loadBase), machine mode has integers.
	// Until a full LoadFeeTrack lands, the server-wide load factor mirrors the
	// fee-escalation level — the only load signal we currently track. Matches
	// rippled's NetworkOPs.cpp:2863-2875 fallback when no other load source
	// (cluster, admin) is active.
	feeEscalation, feeQueue, feeReference := resolveLoadFactorFees(services)
	loadFactor := feeEscalation
	if loadFactor < loadBase {
		loadFactor = loadBase
	}
	if human {
		info["load_factor"] = float64(loadFactor) / float64(loadBase)
	} else {
		info["load_base"] = loadBase
		info["load_factor"] = loadFactor
		info["load_factor_server"] = loadBase
		info["load_factor_fee_escalation"] = feeEscalation
		info["load_factor_fee_queue"] = feeQueue
		info["load_factor_fee_reference"] = feeReference
	}

	now := time.Now()
	validatedAge := ledgerAge(serverInfo.ValidatedLedgerCloseTime, now)
	closedAge := ledgerAge(serverInfo.ClosedLedgerCloseTime, now)

	if human {
		baseFeeXRP := float64(baseFee) / 1_000_000.0
		reserveBaseXRP := float64(reserveBase) / 1_000_000.0
		reserveIncXRP := float64(reserveIncrement) / 1_000_000.0

		info["validated_ledger"] = map[string]interface{}{
			"age":              validatedAge,
			"base_fee_xrp":     baseFeeXRP,
			"hash":             validatedLedgerHash,
			"reserve_base_xrp": reserveBaseXRP,
			"reserve_inc_xrp":  reserveIncXRP,
			"seq":              serverInfo.ValidatedLedgerSeq,
		}

		// closed_ledger in human mode
		info["closed_ledger"] = map[string]interface{}{
			"age":              closedAge,
			"base_fee_xrp":     baseFeeXRP,
			"hash":             closedLedgerHash,
			"reserve_base_xrp": reserveBaseXRP,
			"reserve_inc_xrp":  reserveIncXRP,
			"seq":              serverInfo.ClosedLedgerSeq,
		}
	} else {
		info["validated_ledger"] = map[string]interface{}{
			"base_fee":     baseFee,
			"close_time":   serverInfo.ValidatedLedgerCloseTime,
			"hash":         validatedLedgerHash,
			"reserve_base": reserveBase,
			"reserve_inc":  reserveIncrement,
			"seq":          serverInfo.ValidatedLedgerSeq,
		}

		// closed_ledger in machine mode
		info["closed_ledger"] = map[string]interface{}{
			"base_fee":     baseFee,
			"close_time":   serverInfo.ClosedLedgerCloseTime,
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
	if services.Ledger.IsAmendmentBlocked() {
		info["amendment_blocked"] = true
	}

	return info
}

func getPeerCount(ctx *types.RpcContext) int {
	if ctx == nil || ctx.PeerSource == nil {
		return 0
	}
	return ctx.PeerSource.PeerCount()
}

// resolveValidationQuorum returns the live consensus quorum from the
// adaptor via the services container, falling back to 1 when the
// consensus subsystem hasn't been wired (standalone or pre-startup).
// Rippled exposes the runtime quorum here; previously goxrpl hardcoded
// 1, which made network-mode soaks misleading (#451).
func resolveValidationQuorum(services *types.ServiceContainer) int {
	if services != nil && services.ValidationQuorum != nil {
		if q := services.ValidationQuorum(); q > 0 {
			return q
		}
	}
	return 1
}

// resolveDisconnectCounters reads the overlay/TxQ disconnect &
// overflow counters via service hooks. Returns zeros when hooks aren't
// wired so server_info still produces a complete shape.
func resolveDisconnectCounters(services *types.ServiceContainer) (overflow, peerDisc, peerDiscRes uint64) {
	if services == nil {
		return 0, 0, 0
	}
	if services.TxQMetrics != nil {
		overflow = services.TxQMetrics().JqTransOverflow
	}
	if services.PeerDisconnects != nil {
		peerDisc, peerDiscRes = services.PeerDisconnects()
	}
	return overflow, peerDisc, peerDiscRes
}

// resolveLoadFactorFees returns (escalation, queue, reference) levels
// for the server_info load_factor_fee_* fields. Falls back to the
// reference level (loadBase) when the TxQ isn't wired.
func resolveLoadFactorFees(services *types.ServiceContainer) (escalation, queue, reference uint64) {
	escalation, queue, reference = loadBase, loadBase, loadBase
	if services == nil || services.TxQMetrics == nil {
		return
	}
	m := services.TxQMetrics()
	if m.ReferenceFeeLevel > 0 {
		reference = m.ReferenceFeeLevel
	}
	if m.OpenLedgerFeeLevel > 0 {
		escalation = m.OpenLedgerFeeLevel
	}
	if m.MinProcessingFeeLevel > 0 {
		queue = m.MinProcessingFeeLevel
	}
	return
}

// resolveStateAccounting builds the state_accounting JSON value.
// Prefers the adaptor's per-mode tracker when wired, otherwise falls
// back to attributing the whole uptime to the current server state
// (matching the pre-#480 stub shape).
func resolveStateAccounting(services *types.ServiceContainer, serverState string, uptimeUs int64) map[string]interface{} {
	out := make(map[string]interface{}, len(stateAccountingModes))
	for _, m := range stateAccountingModes {
		out[m] = map[string]interface{}{
			"duration_us": "0",
			"transitions": "0",
		}
	}

	if services != nil && services.StateAccounting != nil {
		for mode, entry := range services.StateAccounting() {
			out[mode] = map[string]interface{}{
				"duration_us": fmt.Sprintf("%d", entry.DurationUs),
				"transitions": fmt.Sprintf("%d", entry.Transitions),
			}
		}
		return out
	}

	if _, ok := out[serverState]; ok {
		out[serverState] = map[string]interface{}{
			"duration_us": fmt.Sprintf("%d", uptimeUs),
			"transitions": "1",
		}
	}
	return out
}

// ledgerAge returns the age of a ledger in seconds. Clamps to 0 when
// the close time is unknown, in the future (clock skew), or past
// rippled's high-age threshold — see NetworkOPs.cpp:2952.
func ledgerAge(closeTimeRippleEpoch int64, now time.Time) int64 {
	if closeTimeRippleEpoch <= 0 {
		return 0
	}
	closeUnix := closeTimeRippleEpoch + protocol.RippleEpochUnix
	age := now.Unix() - closeUnix
	if age <= 0 {
		return 0
	}
	if time.Duration(age)*time.Second >= validatedLedgerAgeThreshold {
		return 0
	}
	return age
}
