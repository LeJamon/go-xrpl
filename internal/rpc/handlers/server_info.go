package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/bits"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/rfc1751"
	"github.com/LeJamon/go-xrpl/internal/observability"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/version"
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
	return "go-xrpl"
}

func serverHostID(services *types.ServiceGraph, admin bool) string {
	if admin {
		return cachedHostID
	}
	if services != nil {
		key, err := addresscodec.DecodeNodePublicKey(services.NodePublicKey())
		if err == nil && len(key) == addresscodec.NodePublicKeyLength {
			return rfc1751.WordFromBlob(key)
		}
	}
	return "go-xrpl"
}

func ServerSubscriptionState(services *types.ServiceGraph, admin bool) map[string]any {
	random := make([]byte, 32)
	_, _ = rand.Read(random)
	load := ComputeServerLoad(services)
	status := "full"
	standalone := false
	if services != nil && services.Ledger() != nil {
		info := services.Ledger().GetServerInfo()
		standalone = info.Standalone
		if info.ServerState != "" {
			status = info.ServerState
		}
		if standalone {
			status = "full"
		} else if !admin && (status == "proposing" || status == "validating") {
			status = "full"
		}
	}
	result := map[string]any{
		"random":        strings.ToUpper(hex.EncodeToString(random)),
		"server_status": status,
		"load_base":     clipToUint32(load.LoadBase),
		"load_factor":   clipToUint32(load.LoadFactorServer),
		"hostid":        serverHostID(services, admin),
		"pubkey_node":   "",
	}
	if services != nil {
		result["pubkey_node"] = services.NodePublicKey()
	}
	if standalone {
		result["stand_alone"] = true
	}
	return result
}

func serverSystemTime(services *types.ServiceGraph) time.Time {
	if services != nil && services.SystemTime() != nil {
		return services.SystemTime()()
	}
	return time.Now()
}

func formatServerTime(t time.Time) string {
	return t.UTC().Format("2006-Jan-02 15:04:05.000000 UTC")
}

// ServerInfoMethod handles the server_info RPC method.
// This is the "human-readable" variant (rippled human=true).
type ServerInfoMethod struct{ baseHandler }

func (m *ServerInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	info := buildServerInfo(ctx, true)
	if serverCountersRequested(params) {
		addServerDiagnostics(info, ctx.Services)
	}
	if warnings := buildServerWarnings(ctx.Services, ctx.Role.IsAdmin()); len(warnings) > 0 {
		info["warnings"] = warnings
	}
	return map[string]any{"info": info}, nil
}

func serverCountersRequested(params json.RawMessage) bool {
	if len(params) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(params, &object) != nil || object == nil {
		return false
	}
	return jsonCppBoolRaw(object["counters"])
}

func addServerDiagnostics(info map[string]any, services *types.ServiceGraph) {
	rpcCounters := make(map[string]any)
	var snapshot types.RPCDiagnosticsSnapshot
	if services != nil && services.RPCDiagnostics() != nil {
		snapshot = services.RPCDiagnostics().Snapshot()
	}
	methods := make([]map[string]any, 0, len(snapshot.Current))

	var total types.RPCMethodDiagnostics
	for method, stats := range snapshot.Methods {
		if stats.Started == 0 {
			continue
		}
		rpcCounters[method] = rpcDiagnosticsJSON(stats)
		total.Started += stats.Started
		total.Finished += stats.Finished
		total.Errored += stats.Errored
		total.DurationUs += stats.DurationUs
	}
	if total.Started != 0 {
		rpcCounters["total"] = rpcDiagnosticsJSON(total)
	}
	for _, activity := range snapshot.Current {
		methods = append(methods, map[string]any{
			"method":      activity.Method,
			"duration_us": strconv.FormatUint(activity.DurationUs, 10),
		})
	}

	nodeStore := make(map[string]any)
	if services != nil && services.GetCounts() != nil {
		if counts := services.GetCounts()().NodeStore; counts != nil {
			nodeStore["node_writes"] = strconv.FormatUint(counts.Writes, 10)
			nodeStore["node_reads_total"] = strconv.FormatUint(counts.Reads, 10)
			nodeStore["node_reads_hit"] = strconv.FormatUint(counts.FetchHits, 10)
			nodeStore["node_written_bytes"] = strconv.FormatUint(counts.WriteBytes, 10)
			nodeStore["node_read_bytes"] = strconv.FormatUint(counts.ReadBytes, 10)
		}
	}

	counters := map[string]any{
		"rpc":       rpcCounters,
		"job_queue": map[string]any{},
		"nodestore": nodeStore,
	}
	if services != nil && services.SubscriptionMetrics() != nil {
		metrics := services.SubscriptionMetrics()()
		counters["subscriptions"] = map[string]any{
			"connections":                 strconv.FormatUint(metrics.Connections, 10),
			"items":                       strconv.FormatUint(metrics.Items, 10),
			"request_limit_rejections":    strconv.FormatUint(metrics.RequestLimitRejections, 10),
			"connection_limit_rejections": strconv.FormatUint(metrics.ConnectionLimitRejections, 10),
			"global_limit_rejections":     strconv.FormatUint(metrics.GlobalLimitRejections, 10),
			"deliveries_queued":           strconv.FormatUint(metrics.DeliveriesQueued, 10),
			"deliveries_dropped":          strconv.FormatUint(metrics.DeliveriesDropped, 10),
			"delivery_disconnects":        strconv.FormatUint(metrics.DeliveryDisconnects, 10),
		}
	}
	info["counters"] = counters
	info["current_activities"] = map[string]any{
		"jobs":    []map[string]any{},
		"methods": methods,
	}
}

func rpcDiagnosticsJSON(stats types.RPCMethodDiagnostics) map[string]any {
	return map[string]any{
		"started":     strconv.FormatUint(stats.Started, 10),
		"finished":    strconv.FormatUint(stats.Finished, 10),
		"errored":     strconv.FormatUint(stats.Errored, 10),
		"duration_us": strconv.FormatUint(stats.DurationUs, 10),
	}
}

func buildServerWarnings(services *types.ServiceGraph, isAdmin bool) []types.WarningObject {
	if services == nil || services.Ledger() == nil {
		return nil
	}

	var warnings []types.WarningObject
	blocked := services.Ledger().IsAmendmentBlocked()
	if blocked {
		warnings = append(warnings, types.WarningObject{
			ID:      types.WarningAmendmentBlocked,
			Message: "This server is amendment blocked, and must be updated to be able to stay in sync with the network.",
		})
	}
	if services.ValidatorList() != nil && services.ValidatorList().IsUNLBlocked() {
		warnings = append(warnings, types.WarningObject{
			ID:      types.WarningExpiredValidatorList,
			Message: "This server has an expired validator list. validators.txt may be incorrectly configured or some [validator_list_sites] may be unreachable.",
		})
	}

	if isAdmin && !blocked {
		if p, ok := services.Ledger().(interface {
			Table() *amendment.Table
		}); ok {
			if tbl := p.Table(); tbl != nil {
				if exp, has := tbl.FirstUnsupportedExpected(); has {
					warnings = append(warnings, types.WarningObject{
						ID:      types.WarningUnsupportedAmendmentsMajority,
						Message: "One or more unsupported amendments have reached majority. Upgrade to the latest version before they are activated to avoid being amendment blocked.",
						Details: map[string]any{
							"expected_date":     exp,
							"expected_date_UTC": time.Unix(int64(exp)+protocol.RippleEpochUnix, 0).UTC().Format("2006-Jan-02 15:04:05 UTC"),
						},
					})
				}
			}
		}
	}
	return warnings
}

// buildServerInfo constructs the info/state object.
// When human is true it produces the server_info format (XRP decimals, converge_time_s, hostid).
// When human is false it produces the server_state format (drops integers, converge_time, load_base, etc.).
func buildServerInfo(ctx *types.RpcContext, human bool) map[string]any {
	services := ctx.Services
	now := serverSystemTime(services)
	serverInfo := services.Ledger().GetServerInfo()
	configSnapshot := services.ServerInfoConfig()
	baseFee, reserveBase, reserveIncrement := services.Ledger().GetCurrentFees()

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
	} else if !ctx.Role.IsAdmin() && (serverState == "proposing" || serverState == "validating") {
		serverState = "full"
	}

	// Fallback used only when consensus hasn't wired a state-accounting tracker.
	uptimeUs := uptimeDuration.Microseconds()

	overflow, peerDisc, peerDiscRes := resolveDisconnectCounters(services)
	accounting := resolveStateAccounting(services, serverState, uptimeUs)

	info := map[string]any{
		"build_version":     BuildVersion,
		"complete_ledgers":  completeLedgers,
		"io_latency_ms":     observability.SchedLatencyMs(),
		"pubkey_node":       services.NodePublicKey(),
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

	info["ports"] = buildServerInfoPorts(configSnapshot.Ports, ctx.Role.IsAdmin())
	if configSnapshot.ServerDomain != "" {
		info["server_domain"] = configSnapshot.ServerDomain
	}
	if ctx.Role.IsAdmin() {
		nodeSize := configSnapshot.NodeSize
		if nodeSize == "" {
			nodeSize = "medium"
		}
		info["node_size"] = nodeSize
		if configSnapshot.GitHash != "" {
			info["git"] = map[string]any{"hash": configSnapshot.GitHash}
		}
	}
	if services.FetchPackCacheSize() != nil {
		if size := services.FetchPackCacheSize()(); size != 0 {
			info["fetch_pack"] = size
		}
	}

	// Rippled emits initial_sync_duration_us only when the node has
	// completed its first sync to Full (NetworkOPs.cpp:4847-4848).
	if accounting.initialSyncUs > 0 {
		info["initial_sync_duration_us"] = fmt.Sprintf("%d", accounting.initialSyncUs)
	}
	if serverInfo.NeedsNetworkLedger {
		info["network_ledger"] = "waiting"
	}

	// pubkey_validator: admin-only, mirrors rippled NetworkOPs.cpp:2779-2791.
	// Emits the configured validator's MASTER public key (base58 NodePublic),
	// or "none" when the node is not a validator. Present in both server_info
	// (human) and server_state (machine) like rippled's shared getServerInfo.
	if ctx.Role.IsAdmin() {
		info["pubkey_validator"] = resolveValidatorPubKey(services)
		validatorList := resolveValidatorListSnapshot(services, now)
		if human {
			info["validator_list"] = validatorList.summary
		} else {
			info["validator_list_expires"] = validatorList.expires
		}
	}

	// hostid: only in human mode (server_info), matching rippled
	if human {
		info["hostid"] = serverHostID(services, ctx.Role.IsAdmin())
	}

	info["time"] = formatServerTime(now)

	// last_close: converge_time_s (float seconds) for human, converge_time (int ms) for machine
	proposers := 0
	convergeTimeMs := 0
	if services.LastCloseInfo() != nil {
		proposers, convergeTimeMs = services.LastCloseInfo()()
	}
	if human {
		info["last_close"] = map[string]any{
			"converge_time_s": float64(convergeTimeMs) / 1000.0,
			"proposers":       proposers,
		}
	} else {
		info["last_close"] = map[string]any{
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
		loadFactorFeeEscalation = mulDivSaturating(feeEscalation, loadBase, feeReference)
	}
	var loadFactorFees types.LoadFactorFees
	if services != nil && services.LoadFactorFees() != nil {
		loadFactorFees = services.LoadFactorFees()()
	} else {
		// Tracker unwired (older test fixtures): treat as no load so
		// loadFactorServer collapses to loadBase, matching a fresh
		// LoadFeeTrack.
		base32 := uint32(loadBase)
		loadFactorFees = types.LoadFactorFees{Local: base32, Net: base32, Cluster: base32}
	}
	loadFactorServer := max(uint64(loadFactorFees.Cluster), max(uint64(loadFactorFees.Net), uint64(loadFactorFees.Local)))
	loadFactor := max(loadFactorServer, loadFactorFeeEscalation)
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
		if ctx.Role.IsAdmin() {
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
		if feeEscalation != feeReference && (ctx.Role.IsAdmin() || loadFactorFeeEscalation != loadFactor) {
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
		age, ageOK := ledgerAge(ledgerCloseTime, now)
		if human {
			baseFeeXRP := float64(baseFee) / 1_000_000.0
			reserveBaseXRP := float64(reserveBase) / 1_000_000.0
			reserveIncXRP := float64(reserveIncrement) / 1_000_000.0

			ledger := map[string]any{
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
			if services != nil && services.CloseTimeOffset() != nil {
				offset := services.CloseTimeOffset()()
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
			info[ledgerKey] = map[string]any{
				"base_fee":     baseFee,
				"close_time":   ledgerCloseTime,
				"hash":         ledgerHash,
				"reserve_base": reserveBase,
				"reserve_inc":  reserveIncrement,
				"seq":          ledgerSeq,
			}
		}
	}

	if haveLedger {
		switch {
		case !serverInfo.HavePublished:
			info["published_ledger"] = "none"
		case serverInfo.PublishedLedgerSeq != ledgerSeq:
			info["published_ledger"] = serverInfo.PublishedLedgerSeq
		}
	}

	// network_id: only include if configured (non-zero), matching rippled
	if serverInfo.NetworkID > 0 {
		info["network_id"] = serverInfo.NetworkID
	}

	// amendment_blocked: rippled only includes this when true
	if services.Ledger().IsAmendmentBlocked() {
		info["amendment_blocked"] = true
	}

	return info
}

func buildServerInfoPorts(ports []types.ServerInfoPortSnapshot, isAdmin bool) []map[string]any {
	result := make([]map[string]any, 0, len(ports))
	for _, port := range ports {
		protocols, grpc := serverInfoProtocols(port.Protocol)
		if len(protocols) != 0 && (isAdmin || !port.Admin) {
			result = append(result, map[string]any{
				"port":     strconv.Itoa(port.Port),
				"protocol": protocols,
			})
		}
		if grpc {
			result = append(result, map[string]any{
				"port":     strconv.Itoa(port.Port),
				"protocol": []string{"grpc"},
			})
		}
	}
	return result
}

func serverInfoProtocols(configured string) ([]string, bool) {
	set := make(map[string]struct{})
	for _, protocol := range strings.FieldsFunc(configured, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	}) {
		set[protocol] = struct{}{}
	}

	supported := [...]string{"http", "https", "peer", "ws", "ws2", "wss", "wss2"}
	protocols := make([]string, 0, len(set))
	for _, protocol := range supported {
		if _, ok := set[protocol]; ok {
			protocols = append(protocols, protocol)
		}
	}
	_, grpc := set["grpc"]
	return protocols, grpc
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
func resolveValidationQuorum(services *types.ServiceGraph) uint32 {
	if services != nil && services.ValidationQuorum() != nil {
		if q := services.ValidationQuorum()(); q > 0 {
			return validationQuorumForRPC(q)
		}
	}
	return 1
}

// resolveValidatorPubKey returns the base58 NodePublic encoding of the
// configured validator's MASTER public key, or "none" when the node is
// not a validator. ValidatorPublicKey carries the 33-byte signing key;
// the master is resolved through the manifest cache exactly as
// validator_info does (rippled localPublicKey() = getMasterKey(signingKey);
// in seed-only mode master == signing). Matches rippled NetworkOPs.cpp:2781-2790.
//
// rippled gates the emit on two independently-nullable identities
// (localPublicKey() && getValidationPublicKey(), NetworkOPs.cpp:2781). goXRPL
// models the validator identity as a single object, so a populated 33-byte
// ValidatorPublicKey (set iff Adaptor.GetValidatorSigningKey succeeds, i.e.
// identity != nil) is the faithful single-term equivalent for every state the
// node can reach; the two-term form only matters if a separable
// manifest-revocation path is added later.
func resolveValidatorPubKey(services *types.ServiceGraph) string {
	if services == nil || len(services.ValidatorPublicKey()) != 33 {
		return "none"
	}
	var signing [33]byte
	copy(signing[:], services.ValidatorPublicKey())
	master := signing
	if services.Manifests() != nil {
		master = services.Manifests().GetMasterKey(signing)
	}
	enc, err := addresscodec.EncodeNodePublicKey(master[:])
	if err != nil {
		return "none"
	}
	return enc
}

// resolveDisconnectCounters reads the overlay overflow & disconnect
// counters via service hooks. Returns zeros when hooks aren't wired
// so server_info still produces a complete shape. overflow sources
// from the overlay's TMTransaction-refusal counter (the rippled-shape
// jq_trans_overflow signal at PeerImp.cpp:1353).
func resolveDisconnectCounters(services *types.ServiceGraph) (overflow, peerDisc, peerDiscRes uint64) {
	if services == nil {
		return 0, 0, 0
	}
	if services.JqTransOverflow() != nil {
		overflow = services.JqTransOverflow()()
	}
	if services.PeerDisconnects() != nil {
		peerDisc, peerDiscRes = services.PeerDisconnects()()
	}
	return overflow, peerDisc, peerDiscRes
}

// resolveLoadFactorFees returns (escalation, queue, reference) levels
// for the server_info load_factor_fee_* fields. Falls back to (loadBase,
// loadBase, loadBase) when the TxQ isn't wired so the load_factor_fee_*
// gates collapse to "absent". Once the hook fires, values pass through
// unfiltered — a zero from TxQ would be a TxQ bug, not something to
// paper over here.
func resolveLoadFactorFees(services *types.ServiceGraph) (escalation, queue, reference uint64) {
	if services == nil || services.TxQMetrics() == nil {
		return loadBase, loadBase, loadBase
	}
	m := services.TxQMetrics()()
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
func ComputeServerLoad(services *types.ServiceGraph) ServerLoadSnapshot {
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
	scaledFeeEscalation := feeEscalation
	if feeReference != 0 {
		scaledFeeEscalation = mulDivSaturating(feeEscalation, loadBase, feeReference)
	}
	if services != nil && services.LoadFactorFees() != nil {
		fees := services.LoadFactorFees()()
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
	snap.LoadFactorServer = max(snap.LoadFactorLocal, snap.LoadFactorNet, snap.LoadFactorCluster)
	snap.LoadFactor = max(snap.LoadFactorServer, scaledFeeEscalation)
	return snap
}

func mulDivSaturating(a, b, divisor uint64) uint64 {
	if divisor == 0 {
		return ^uint64(0)
	}
	hi, lo := bits.Mul64(a, b)
	if hi >= divisor {
		return ^uint64(0)
	}
	quotient, _ := bits.Div64(hi, lo, divisor)
	return quotient
}

// stateAccountingResolved is the rendered shape consumed by
// buildServerInfo — the state_accounting map plus the two top-level
// companion fields (server_state_duration_us, initial_sync_duration_us).
type stateAccountingResolved struct {
	modes             map[string]any
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
func resolveStateAccounting(services *types.ServiceGraph, serverState string, uptimeUs int64) stateAccountingResolved {
	out := make(map[string]any, len(stateAccountingModes))
	for _, m := range stateAccountingModes {
		out[m] = map[string]any{
			"duration_us": "0",
			"transitions": "0",
		}
	}

	if services != nil && services.StateAccounting() != nil {
		snap := services.StateAccounting()()
		for mode, entry := range snap.Modes {
			out[mode] = map[string]any{
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
		out[serverState] = map[string]any{
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
