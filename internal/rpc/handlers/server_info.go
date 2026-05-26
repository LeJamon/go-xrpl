package handlers

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/observability"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/version"
)

// loadBase is the fee-tracker reference level. Mirrors rippled's
// LoadFeeTrack::lftNormalFee at LoadFeeTrack.h:141-142 — every
// load_factor_* field is expressed as a multiple of this base.
const loadBase uint64 = 256

// clipToUint32 mirrors rippled's trunc32 / FeeLevel::jsonClipped at
// NetworkOPs.cpp:2862-2876: load_factor* and load_base are emitted as
// JSON UInts, with values above uint32 max saturated rather than
// overflowed. Pathological for realistic load, but keeps the wire type
// matching rippled.
func clipToUint32(v uint64) uint32 {
	if v > uint64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(v)
}

// validatedLedgerAgeThreshold matches rippled's
// NetworkOPsImp::getServerInfo (NetworkOPs.cpp:2951): once the
// validated ledger is older than this the age is clamped to 0 in the
// JSON. Rippled uses 1,000,000 seconds (~11.57 days), so ordinary
// stall durations (minutes / hours / days) are still reported.
const validatedLedgerAgeThreshold = 1_000_000 * time.Second

// closeTimeOffsetThreshold mirrors rippled NetworkOPs.cpp:2946-2949:
// close_time_offset is only surfaced when |offset| reaches a full
// minute, suppressing transient sub-minute drift.
const closeTimeOffsetThreshold = 60 * time.Second

// stateAccountingModes is the fixed set of operating-mode keys
// rippled emits, in OperatingMode index order to mirror
// NetworkOPs.cpp:871-872 + 4837-4845. JSON object keys are unordered
// on the wire but matching the iteration order keeps review noise
// down. Emitting all keys (zero-filled when needed) keeps the shape
// stable for downstream consumers.
var stateAccountingModes = []string{
	"disconnected",
	"connected",
	"syncing",
	"tracking",
	"full",
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
	if warnings := buildAmendmentWarnings(ctx.Services); len(warnings) > 0 {
		response["warnings"] = warnings
	}

	return response, nil
}

// buildAmendmentWarnings surfaces the rippled-conformant amendment warnings:
// warnRPC_AMENDMENT_BLOCKED (1002) when the node is blocked, and
// warnRPC_UNSUPPORTED_MAJORITY (1001) — with the projected activation date —
// when an unsupported amendment is holding majority.
func buildAmendmentWarnings(services *types.ServiceContainer) []types.WarningObject {
	if services == nil || services.Ledger == nil {
		return nil
	}

	var warnings []types.WarningObject
	if services.Ledger.IsAmendmentBlocked() {
		warnings = append(warnings, types.WarningObject{
			ID:      types.WarningAmendmentBlocked,
			Message: "This server is amendment blocked, and must be updated to be able to stay in sync with the network.",
		})
	}

	if p, ok := services.Ledger.(interface {
		AmendmentTable() *amendment.AmendmentTable
	}); ok {
		if tbl := p.AmendmentTable(); tbl != nil {
			if exp, has := tbl.FirstUnsupportedExpected(); has {
				warnings = append(warnings, types.WarningObject{
					ID:      types.WarningUnsupportedAmendmentsMajority,
					Message: "One or more unsupported amendments have reached majority. Upgrade before they are activated to avoid becoming amendment blocked.",
					Details: map[string]interface{}{
						"expected_date":     exp,
						"expected_date_UTC": time.Unix(int64(exp)+protocol.RippleEpochUnix, 0).UTC().Format(time.RFC3339),
					},
				})
			}
		}
	}
	return warnings
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

	// Fallback used only when consensus hasn't wired a state-accounting tracker.
	uptimeUs := uptimeDuration.Microseconds()

	overflow, peerDisc, peerDiscRes := resolveDisconnectCounters(services)
	accounting := resolveStateAccounting(services, serverState, uptimeUs)

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

		// Time spent in the current operating mode (NOT total uptime),
		// matching rippled NetworkOPs.cpp:4846 which emits
		// `current.count()` = now - last-transition-time.
		"server_state_duration_us": fmt.Sprintf("%d", accounting.currentDurationUs),
		"state_accounting":         accounting.modes,
	}

	// Rippled emits initial_sync_duration_us only when the node has
	// completed its first sync to Full (NetworkOPs.cpp:4847-4848).
	if accounting.initialSyncUs > 0 {
		info["initial_sync_duration_us"] = fmt.Sprintf("%d", accounting.initialSyncUs)
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

	// load_factor mixes two load sources, matching NetworkOPs.cpp:2845-2858:
	//   loadFactorServer = max(local, remote, cluster) from LoadFeeTrack
	//   loadFactorFeeEscalation = openLedgerFeeLevel * loadBase / referenceFeeLevel
	//   load_factor = max(loadFactorServer, loadFactorFeeEscalation), floored at loadBase
	feeEscalation, feeQueue, feeReference := resolveLoadFactorFees(services)
	loadFactorFeeEscalation := feeEscalation
	if feeReference != 0 {
		loadFactorFeeEscalation = feeEscalation * loadBase / feeReference
	}
	var loadFactorFees types.LoadFactorFees
	if services != nil && services.LoadFactorFees != nil {
		loadFactorFees = services.LoadFactorFees()
	} else {
		// Tracker unwired (older test fixtures): treat as no load so
		// loadFactorServer collapses to loadBase, matching a fresh
		// LoadFeeTrack.
		base32 := uint32(loadBase)
		loadFactorFees = types.LoadFactorFees{Local: base32, Net: base32, Cluster: base32}
	}
	loadFactorServer := uint64(loadFactorFees.Local)
	if uint64(loadFactorFees.Net) > loadFactorServer {
		loadFactorServer = uint64(loadFactorFees.Net)
	}
	if uint64(loadFactorFees.Cluster) > loadFactorServer {
		loadFactorServer = uint64(loadFactorFees.Cluster)
	}
	loadFactor := loadFactorFeeEscalation
	if loadFactorServer > loadFactor {
		loadFactor = loadFactorServer
	}
	if human {
		info["load_factor"] = float64(loadFactor) / float64(loadBase)
		// Mirror rippled NetworkOPs.cpp:2883-2885: emit load_factor_server
		// when it diverges from the overall load_factor.
		if loadFactorServer != loadFactor {
			info["load_factor_server"] = float64(loadFactorServer) / float64(loadBase)
		}
		// Mirror rippled NetworkOPs.cpp:2887-2901: admin-only emission
		// of load_factor_{local,net,cluster}, each gated on the fee
		// differing from loadBase.
		if ctx.IsAdmin {
			if uint64(loadFactorFees.Local) != loadBase {
				info["load_factor_local"] = float64(loadFactorFees.Local) / float64(loadBase)
			}
			if uint64(loadFactorFees.Net) != loadBase {
				info["load_factor_net"] = float64(loadFactorFees.Net) / float64(loadBase)
			}
			if uint64(loadFactorFees.Cluster) != loadBase {
				info["load_factor_cluster"] = float64(loadFactorFees.Cluster) / float64(loadBase)
			}
		}
		// Mirror rippled NetworkOPs.cpp:2902-2912: in human mode the
		// escalation field is gated on
		//   openLedgerFeeLevel != referenceFeeLevel
		//     && (admin || loadFactorFeeEscalation != loadFactor)
		// and the queue field on
		//   minProcessingFeeLevel != referenceFeeLevel.
		if feeEscalation != feeReference && (ctx.IsAdmin || loadFactorFeeEscalation != loadFactor) {
			info["load_factor_fee_escalation"] = float64(feeEscalation) / float64(feeReference)
		}
		if feeQueue != feeReference {
			info["load_factor_fee_queue"] = float64(feeQueue) / float64(feeReference)
		}
	} else {
		// Machine mode mirrors rippled NetworkOPs.cpp:2862-2876: load_base
		// and load_factor* are emitted as JSON UInts; rippled clamps via
		// trunc32() / jsonClipped() so the field type stays uint32.
		info["load_base"] = uint32(loadBase)
		info["load_factor"] = clipToUint32(loadFactor)
		info["load_factor_server"] = clipToUint32(loadFactorServer)
		info["load_factor_fee_escalation"] = clipToUint32(feeEscalation)
		info["load_factor_fee_queue"] = clipToUint32(feeQueue)
		info["load_factor_fee_reference"] = clipToUint32(feeReference)
	}

	// Mirror rippled NetworkOPs.cpp:2915-2975: emit exactly one of
	// validated_ledger / closed_ledger, sourced from the validated
	// ledger when haveValidated(), otherwise from the closed ledger.
	// Suppress both when neither is available.
	var (
		ledgerSeq       uint32
		ledgerHash      string
		ledgerCloseTime int64
		ledgerKey       string
		haveLedger      bool
	)
	switch {
	case serverInfo.HaveValidated:
		ledgerSeq = serverInfo.ValidatedLedgerSeq
		ledgerHash = validatedLedgerHash
		ledgerCloseTime = serverInfo.ValidatedLedgerCloseTime
		ledgerKey = "validated_ledger"
		haveLedger = true
	case serverInfo.ClosedLedgerSeq > 0:
		ledgerSeq = serverInfo.ClosedLedgerSeq
		ledgerHash = closedLedgerHash
		ledgerCloseTime = serverInfo.ClosedLedgerCloseTime
		ledgerKey = "closed_ledger"
		haveLedger = true
	}

	if haveLedger {
		now := time.Now()
		age, ageOK := ledgerAge(ledgerCloseTime, now)
		if human {
			baseFeeXRP := float64(baseFee) / 1_000_000.0
			reserveBaseXRP := float64(reserveBase) / 1_000_000.0
			reserveIncXRP := float64(reserveIncrement) / 1_000_000.0

			ledger := map[string]interface{}{
				"base_fee_xrp":     baseFeeXRP,
				"hash":             ledgerHash,
				"reserve_base_xrp": reserveBaseXRP,
				"reserve_inc_xrp":  reserveIncXRP,
				"seq":              ledgerSeq,
			}
			// rippled NetworkOPs.cpp:2946-2949: close_time_offset is
			// emitted on the ledger object when |offset| >= 60s. Rippled
			// casts the signed seconds count through static_cast<uint32_t>,
			// preserving the two's-complement bit pattern — so a negative
			// offset surfaces as a large positive number. Match that wire
			// shape rather than emit a signed value.
			if services != nil && services.CloseTimeOffset != nil {
				offset := services.CloseTimeOffset()
				abs := offset
				if abs < 0 {
					abs = -abs
				}
				if abs >= closeTimeOffsetThreshold {
					ledger["close_time_offset"] = uint32(int32(offset / time.Second))
				}
			}
			// Age handling differs by branch (NetworkOPs.cpp:2952-2969):
			// validated → always emit (0 when unknown / too old);
			// closed-only → omit when close-time is in the future.
			if serverInfo.HaveValidated {
				ledger["age"] = age
			} else if ageOK {
				ledger["age"] = age
			}
			info[ledgerKey] = ledger
		} else {
			info[ledgerKey] = map[string]interface{}{
				"base_fee":     baseFee,
				"close_time":   ledgerCloseTime,
				"hash":         ledgerHash,
				"reserve_base": reserveBase,
				"reserve_inc":  reserveIncrement,
				"seq":          ledgerSeq,
			}
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

// resolveDisconnectCounters reads the overlay overflow & disconnect
// counters via service hooks. Returns zeros when hooks aren't wired
// so server_info still produces a complete shape. overflow sources
// from the overlay's TMTransaction-refusal counter (the rippled-shape
// jq_trans_overflow signal at PeerImp.cpp:1353).
func resolveDisconnectCounters(services *types.ServiceContainer) (overflow, peerDisc, peerDiscRes uint64) {
	if services == nil {
		return 0, 0, 0
	}
	if services.JqTransOverflow != nil {
		overflow = services.JqTransOverflow()
	}
	if services.PeerDisconnects != nil {
		peerDisc, peerDiscRes = services.PeerDisconnects()
	}
	return overflow, peerDisc, peerDiscRes
}

// resolveLoadFactorFees returns (escalation, queue, reference) levels
// for the server_info load_factor_fee_* fields. Falls back to (loadBase,
// loadBase, loadBase) when the TxQ isn't wired so the load_factor_fee_*
// gates collapse to "absent". Once the hook fires, values pass through
// unfiltered — a zero from TxQ would be a TxQ bug, not something to
// paper over here.
func resolveLoadFactorFees(services *types.ServiceContainer) (escalation, queue, reference uint64) {
	if services == nil || services.TxQMetrics == nil {
		return loadBase, loadBase, loadBase
	}
	m := services.TxQMetrics()
	return m.OpenLedgerFeeLevel, m.MinProcessingFeeLevel, m.ReferenceFeeLevel
}

// ServerLoadSnapshot bundles the load factors the `server_info` RPC
// and the `server` WebSocket stream both emit. Keeps the
// NetworkOPs.cpp:2850-2912 algebra in one place.
type ServerLoadSnapshot struct {
	LoadBase                uint64
	LoadFactor              uint64
	LoadFactorServer        uint64
	LoadFactorFeeEscalation uint64
	LoadFactorFeeQueue      uint64
	LoadFactorFeeReference  uint64
	LoadFactorLocal         uint64
	LoadFactorNet           uint64
	LoadFactorCluster       uint64
}

// ComputeServerLoad samples the load-fee track via the ServiceContainer
// and returns the rendered triple every server_info / server-stream
// emit needs. Mirrors the NetworkOPs::getServerStatus computation at
// rippled NetworkOPs.cpp:2850-2912.
func ComputeServerLoad(services *types.ServiceContainer) ServerLoadSnapshot {
	feeEscalation, feeQueue, feeReference := resolveLoadFactorFees(services)
	snap := ServerLoadSnapshot{
		LoadBase:                loadBase,
		LoadFactor:              loadBase,
		LoadFactorServer:        loadBase,
		LoadFactorFeeEscalation: feeEscalation,
		LoadFactorFeeQueue:      feeQueue,
		LoadFactorFeeReference:  feeReference,
		LoadFactorLocal:         loadBase,
		LoadFactorNet:           loadBase,
		LoadFactorCluster:       loadBase,
	}
	if feeReference != 0 {
		snap.LoadFactorFeeEscalation = feeEscalation * loadBase / feeReference
	}
	if snap.LoadFactorFeeEscalation > snap.LoadFactor {
		snap.LoadFactor = snap.LoadFactorFeeEscalation
	}
	if services != nil && services.LoadFactorFees != nil {
		fees := services.LoadFactorFees()
		if fees.Local > 0 {
			snap.LoadFactorLocal = uint64(fees.Local)
		}
		if fees.Net > 0 {
			snap.LoadFactorNet = uint64(fees.Net)
		}
		if fees.Cluster > 0 {
			snap.LoadFactorCluster = uint64(fees.Cluster)
		}
	}
	return snap
}

// stateAccountingResolved is the rendered shape consumed by
// buildServerInfo — the state_accounting map plus the two top-level
// companion fields (server_state_duration_us, initial_sync_duration_us).
type stateAccountingResolved struct {
	modes             map[string]interface{}
	currentDurationUs uint64
	initialSyncUs     uint64
}

// resolveStateAccounting builds the state_accounting JSON value and the
// top-level companion durations. Prefers the adaptor's tracker when
// wired; otherwise attributes total uptime to the current server state
// as a synthetic single-transition row.
//
// The synthetic fallback is intentionally non-rippled-conformant: rippled
// always has a StateAccounting instance, so this branch only fires in
// goxrpl-only deployments (standalone / RPC-only tests) where wiring the
// real tracker isn't applicable. Production network nodes always take
// the wired path above.
func resolveStateAccounting(services *types.ServiceContainer, serverState string, uptimeUs int64) stateAccountingResolved {
	out := make(map[string]interface{}, len(stateAccountingModes))
	for _, m := range stateAccountingModes {
		out[m] = map[string]interface{}{
			"duration_us": "0",
			"transitions": "0",
		}
	}

	if services != nil && services.StateAccounting != nil {
		snap := services.StateAccounting()
		for mode, entry := range snap.Modes {
			out[mode] = map[string]interface{}{
				"duration_us": fmt.Sprintf("%d", entry.DurationUs),
				"transitions": fmt.Sprintf("%d", entry.Transitions),
			}
		}
		return stateAccountingResolved{
			modes:             out,
			currentDurationUs: snap.CurrentDurationUs,
			initialSyncUs:     snap.InitialSyncUs,
		}
	}

	// No tracker wired — attribute total uptime to the current state.
	if _, ok := out[serverState]; ok {
		out[serverState] = map[string]interface{}{
			"duration_us": fmt.Sprintf("%d", uptimeUs),
			"transitions": "1",
		}
	}
	currentDur := uint64(0)
	if uptimeUs > 0 {
		currentDur = uint64(uptimeUs)
	}
	return stateAccountingResolved{
		modes:             out,
		currentDurationUs: currentDur,
	}
}

// ledgerAge returns the age of a ledger in seconds, along with an
// `ok` flag indicating whether the field should be emitted at all.
// Clamps to 0 when the close time is past rippled's high-age
// threshold (NetworkOPs.cpp:2956). Returns ok=false when the close
// time is unknown or in the future — rippled omits the `age` field
// in that case (NetworkOPs.cpp:2962-2969); callers may still emit a
// 0 when their branch is the "validated_ledger" path, which rippled
// always emits.
func ledgerAge(closeTimeRippleEpoch int64, now time.Time) (int64, bool) {
	if closeTimeRippleEpoch <= 0 {
		return 0, false
	}
	closeUnix := closeTimeRippleEpoch + protocol.RippleEpochUnix
	age := now.Unix() - closeUnix
	if age < 0 {
		return 0, false
	}
	if time.Duration(age)*time.Second >= validatedLedgerAgeThreshold {
		return 0, true
	}
	return age, true
}
