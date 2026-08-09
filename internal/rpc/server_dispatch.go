package rpc

import (
	"context"
	"encoding/json"
	"net/url"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

func (s *Server) withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if s.timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, s.timeout)
}

// newRpcContext assembles an RpcContext, deriving IsAdmin / Unlimited from the
// role so every HTTP/WS dispatch site computes them identically.
func newRpcContext(ctx context.Context, role types.Role, apiVersion int, clientIP string, peers types.PeerSource, services *types.ServiceContainer) *types.RpcContext {
	return &types.RpcContext{
		Context:    ctx,
		Role:       role,
		ApiVersion: apiVersion,
		IsAdmin:    role == types.RoleAdmin,
		Unlimited:  role.IsUnlimited(),
		ClientIP:   clientIP,
		ResourceIP: clientIP,
		PeerSource: peers,
		Services:   services,
	}
}
func redactedRequestMap(request map[string]any) map[string]any {
	if request == nil {
		return nil
	}
	return redactJSONValue(request).(map[string]any)
}
func redactJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(value))
		for key, item := range value {
			if isCredentialKey(key) {
				redacted[key] = maskedValue
				continue
			}
			if strings.EqualFold(key, "url") {
				if rawURL, ok := item.(string); ok && urlContainsUserinfo(rawURL) {
					redacted[key] = maskedValue
					continue
				}
			}
			redacted[key] = redactJSONValue(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(value))
		for i, item := range value {
			redacted[i] = redactJSONValue(item)
		}
		return redacted
	default:
		return value
	}
}
func urlContainsUserinfo(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.User != nil {
		return true
	}

	authority := rawURL
	if _, after, ok := strings.Cut(authority, "://"); ok {
		authority = after
	} else if strings.HasPrefix(authority, "//") {
		authority = authority[2:]
	} else {
		return false
	}
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	return strings.Contains(authority, "@")
}
func isCredentialKey(key string) bool {
	for _, credentialKey := range credentialKeys {
		if strings.EqualFold(key, credentialKey) {
			return true
		}
	}
	return false
}
func rpcInternalError() *types.RpcError {
	return types.RpcErrorInternal()
}
func (s *Server) executeMethod(method string, params json.RawMessage, ctx *types.RpcContext) (any, *types.RpcError) {
	clientIP := ""
	if ctx != nil {
		clientIP = ctx.ClientIP
	}
	rpcLog().Debug("rpc", "method", method, "client", clientIP)
	// Both transports signal a forbidden admin-only command via RpcErrorForbidden.
	// rippled resolves it at the role layer (Role::FORBID) ahead of the handler
	// and renders it per transport — HTTP single 403 "Forbidden", batch
	// make_json_error(forbidden), WS rpcError(rpcFORBIDDEN) (ServerHandler.cpp:482-486,
	// 750-762). The writers special-case IsForbidden; in-handler permission
	// denials keep returning rpcNO_PERMISSION on the normal result envelope.
	return dispatchMethod(s.registry, s.resourceManager, s.services, ctx, method, params, types.RpcErrorForbidden, rpcLog())
}
func resolveMethod(registry *types.MethodRegistry, method string, apiVersion int) methodResolution {
	handler, exists := registry.Get(method)
	return methodResolution{
		handler:  handler,
		resolved: exists && handlerSupportsVersion(handler, apiVersion),
	}
}
func dispatchMethod(
	registry *types.MethodRegistry,
	manager *resource.Manager,
	services *types.ServiceContainer,
	ctx *types.RpcContext,
	method string,
	params json.RawMessage,
	adminGate func(string) *types.RpcError,
	log xrpllog.Logger,
) (any, *types.RpcError) {
	if rpcErr := validateApiVersion(ctx); rpcErr != nil {
		return nil, rpcErr
	}
	resolution := resolveMethod(registry, method, ctx.ApiVersion)
	if rpcErr := admitMethod(manager, ctx, method, resolution, adminGate, true, log); rpcErr != nil {
		return nil, rpcErr
	}
	return dispatchResolvedMethod(manager, services, ctx, method, params, resolution, log)
}
func admitMethod(
	manager *resource.Manager,
	ctx *types.RpcContext,
	method string,
	resolution methodResolution,
	adminGate func(string) *types.RpcError,
	checkLoad bool,
	log xrpllog.Logger,
) *types.RpcError {
	if checkLoad {
		if rpcErr := gateLoad(manager, ctx, method, log); rpcErr != nil {
			return rpcErr
		}
	}
	if resolution.resolved && resolution.handler.RequiredRole() == types.RoleAdmin && ctx.Role != types.RoleAdmin {
		rpcErr := adminGate(method)
		chargeLoad(manager, ctx, method, resource.FeeMalformedRPC(), log)
		return rpcErr
	}
	return nil
}
func dispatchResolvedMethod(
	manager *resource.Manager,
	services *types.ServiceContainer,
	ctx *types.RpcContext,
	method string,
	params json.RawMessage,
	resolution methodResolution,
	log xrpllog.Logger,
) (any, *types.RpcError) {
	ctx.LoadCost = uint32(resource.FeeReferenceRPC().Cost())
	ctx.LoadWarning = false

	if rpcErr := handlers.RequireNotBusyClient(ctx); rpcErr != nil {
		finalizeLoad(manager, ctx, method, resource.FeeReferenceRPC(), log)
		return nil, rpcErr
	}

	if !resolution.resolved {
		rpcErr := types.RpcErrorMethodNotFound()
		finalizeLoad(manager, ctx, method, resource.FeeReferenceRPC(), log)
		return nil, rpcErr
	}

	if rpcErr := conditionMet(resolution.handler.RequiredCondition(), ctx); rpcErr != nil {
		finalizeLoad(manager, ctx, method, resource.FeeReferenceRPC(), log)
		return nil, rpcErr
	}

	if services != nil && services.ClientLoad != nil {
		release := services.ClientLoad.Begin()
		defer release()
	}
	finishDiagnostics := startRPCDiagnostics(services, method)
	result, rpcErr, recovered := invokeHandler(resolution.handler, ctx, params, method, log)
	finishDiagnostics(recovered)
	fee := rpcCharge(ctx.LoadCost)
	if recovered && fee == resource.FeeReferenceRPC() {
		fee = resource.FeeExceptionRPC()
	}
	finalizeLoad(manager, ctx, method, fee, log)
	return result, rpcErr
}
func startRPCDiagnostics(services *types.ServiceContainer, method string) func(bool) {
	if services == nil || services.RPCDiagnostics == nil {
		return func(bool) {}
	}
	return services.RPCDiagnostics.Start(method)
}
func dispatchNestedMethod(
	registry *types.MethodRegistry,
	ctx *types.RpcContext,
	method string,
	params json.RawMessage,
	log xrpllog.Logger,
) (any, *types.RpcError) {
	if rpcErr := validateApiVersion(ctx); rpcErr != nil {
		return nil, rpcErr
	}
	resolution := resolveMethod(registry, method, ctx.ApiVersion)
	if !resolution.resolved {
		return nil, types.RpcErrorMethodNotFound()
	}
	if resolution.handler.RequiredRole() == types.RoleAdmin && ctx.Role != types.RoleAdmin {
		ctx.LoadCost = uint32(resource.FeeMalformedRPC().Cost())
		return nil, types.RpcErrorForbidden(method)
	}
	if rpcErr := conditionMet(resolution.handler.RequiredCondition(), ctx); rpcErr != nil {
		return nil, rpcErr
	}
	result, rpcErr, recovered := invokeHandler(resolution.handler, ctx, params, method, log)
	if recovered && ctx.LoadCost == uint32(resource.FeeReferenceRPC().Cost()) {
		ctx.LoadCost = uint32(resource.FeeExceptionRPC().Cost())
	}
	return result, rpcErr
}
func invokeHandler(handler types.MethodHandler, ctx *types.RpcContext, params json.RawMessage, method string, log xrpllog.Logger) (result any, rpcErr *types.RpcError, recovered bool) {
	defer func() {
		if rec := recover(); rec != nil {
			clientIP := ""
			if ctx != nil {
				clientIP = ctx.ClientIP
			}
			log.Error("rpc handler panic", "err", rec, "stack", string(debug.Stack()), "method", method, "client", clientIP)
			result = nil
			rpcErr = rpcInternalError()
			recovered = true
		}
	}()
	result, rpcErr = handler.Handle(ctx, redactStructuredID(params))
	return result, rpcErr, false
}
func redactStructuredID(params json.RawMessage) json.RawMessage {
	if len(params) == 0 {
		return params
	}
	var request map[string]any
	if err := decodeJSONUseNumber(params, &request); err != nil || request == nil {
		return params
	}
	id, ok := request["id"]
	if !ok {
		return params
	}
	switch id.(type) {
	case map[string]any, []any:
		request["id"] = redactJSONValue(id)
	default:
		return params
	}
	redacted, err := json.Marshal(request)
	if err != nil {
		return params
	}
	return redacted
}

// betaEnabled reports whether the operator turned on the beta RPC API for
// this request. nil-safe: a request without a service container (routing-only
// tests) is treated as non-beta.
func betaEnabled(ctx *types.RpcContext) bool {
	return ctx.Services != nil && ctx.Services.BetaRPCAPI
}

// validateApiVersion enforces the accepted api_version range, mirroring
// rippled's dispatch-layer cap (getAPIVersionNumber rejecting anything above
// apiBetaVersion when beta is off, ServerHandler.cpp:685-695). It is handler-
// independent: a version outside the range yields invalid_API_version ahead of
// command resolution, exactly as rippled rejects it before reaching a handler.
// The narrower per-handler support set is enforced separately as part of
// command resolution — see handlerSupportsVersion.
func validateApiVersion(ctx *types.RpcContext) *types.RpcError {
	maxVersion := types.MaxSupportedApiVersion
	if betaEnabled(ctx) {
		maxVersion = types.BetaApiVersion
	}
	if ctx.ApiVersion < types.ApiVersion1 || ctx.ApiVersion > maxVersion {
		return types.RpcErrorInvalidApiVersion(strconv.Itoa(ctx.ApiVersion))
	}
	return nil
}

// handlerSupportsVersion reports whether the handler serves the requested
// api_version; a handler listing no versions serves every in-range version.
// rippled folds this per-handler range match into getHandler (Handler.cpp:265-
// 272): a name match whose version falls outside the handler's range returns
// null, which the caller treats as an unknown command — never as an invalid
// api_version (that error is reserved for the out-of-range dispatch-layer cap).
func handlerSupportsVersion(handler types.MethodHandler, version int) bool {
	supportedVersions := handler.SupportedApiVersions()
	return len(supportedVersions) == 0 || slices.Contains(supportedVersions, version)
}

// conditionMet mirrors rippled's RPC::conditionMet (Handler.h:78-139). A method
// whose RequiredCondition is NoCondition is always allowed. Otherwise the node
// must be usable: not amendment-blocked, at least SYNCING, not lagging the
// validated ledger (the age / current-vs-valid checks are skipped in
// standalone), and holding a closed ledger.
//
// On failure it returns the apiVersion-1 code (rpcNO_NETWORK / rpcNO_CURRENT /
// rpcNO_CLOSED) and rpcNOT_SYNCED for later versions, matching rippled. The
// rpcEXPIRED_VALIDATOR_LIST branch fires when the UNL is blocked, driven by the
// optional ServiceContainer.UNLBlocked signal (nil ⇒ never blocked).
func conditionMet(cond types.Condition, ctx *types.RpcContext) *types.RpcError {
	if cond == types.NoCondition {
		return nil
	}
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil
	}
	svc := ctx.Services.Ledger

	if svc.IsAmendmentBlocked() {
		return types.NewRpcError(types.RpcAMENDMENT_BLOCKED,
			"amendmentBlocked", "amendmentBlocked", "Amendment blocked, need upgrade.")
	}

	if ctx.Services.UNLBlocked != nil && ctx.Services.UNLBlocked() {
		return types.NewRpcError(types.RpcEXPIRED_VALIDATOR_LIST,
			"unlBlocked", "unlBlocked", "Validator list expired.")
	}

	info := svc.GetServerInfo()

	if !atLeastSyncing(info.ServerState) {
		return notSyncedError(ctx.ApiVersion, syncFailureNoNetwork)
	}

	if !info.Standalone {
		if types.ValidatedLedgerStale(info) || info.OpenLedgerSeq+10 < info.ValidatedLedgerSeq {
			return notSyncedError(ctx.ApiVersion, syncFailureNoCurrent)
		}
	}

	if info.ClosedLedgerSeq == 0 {
		return notSyncedError(ctx.ApiVersion, syncFailureNoClosed)
	}

	return nil
}

// atLeastSyncing reports whether the server_state is SYNCING or higher,
// mirroring rippled's OperatingMode >= SYNCING floor. The comparison is
// ordinal rather than a positive allow-list so it cannot silently reject the
// modes above FULL: a validator presents server_state "proposing" or
// "validating", which are FULL-mode aliases and so sit above the floor.
func atLeastSyncing(serverState string) bool {
	return serverStateRank(serverState) >= serverStateRank("syncing")
}

// serverStateRank maps a server_state presentation string to its operating
// mode rank. "proposing" and "validating" are the aliases a FULL-mode
// validator reports, so they rank with "full". An unrecognised string ranks
// below every operating mode and fails any floor comparison.
func serverStateRank(serverState string) int {
	switch serverState {
	case "disconnected":
		return 0
	case "connected":
		return 1
	case "syncing":
		return 2
	case "tracking":
		return 3
	case "full", "proposing", "validating":
		return 4
	default:
		return -1
	}
}
func notSyncedError(apiVersion int, failure syncFailure) *types.RpcError {
	if apiVersion == types.ApiVersion1 {
		switch failure {
		case syncFailureNoCurrent:
			return types.CurrentLedgerUnavailable(apiVersion)
		case syncFailureNoClosed:
			return types.NewRpcError(types.RpcNO_CLOSED, "noClosed", "noClosed", "Closed ledger is unavailable.")
		default:
			return types.NewRpcError(types.RpcNO_NETWORK, "noNetwork", "noNetwork", "Not synced to the network.")
		}
	}
	return types.NewRpcError(types.RpcNOT_SYNCED, "notSynced", "notSynced",
		"Not synced to the network.")
}
func gateLoad(manager *resource.Manager, ctx *types.RpcContext, method string, log xrpllog.Logger) *types.RpcError {
	if manager == nil || ctx == nil || ctx.ClientIP == "" {
		return nil
	}
	var admission *resource.Admission
	var result resource.Disposition
	if ctx.ResourceConsumer != nil {
		admission, result = ctx.ResourceConsumer.Admit(resource.FeeReferenceRPC())
	} else if ctx.Unlimited {
		admission, result = manager.AdmitUnlimited(ctx.ResourceIP)
	} else {
		admission, result = manager.AdmitInbound(ctx.ClientIP, resource.FeeReferenceRPC())
	}
	if admission == nil || result == resource.Drop {
		log.Warn("rpc dropped: client over load threshold",
			"client", ctx.ClientIP, "method", method)
		return types.RpcErrorOverloaded()
	}
	ctx.ResourceAdmission = admission
	return nil
}
func chargeLoad(_ *resource.Manager, ctx *types.RpcContext, method string, fee resource.Charge, log xrpllog.Logger) {
	if ctx == nil || ctx.ResourceAdmission == nil {
		return
	}
	completion := ctx.ResourceAdmission.Finish(fee, method)
	ctx.LoadWarning = completion.Warning
	switch completion.Disposition {
	case resource.Drop:
		log.Warn("rpc client crossed drop threshold (post-charge)",
			"client", ctx.ClientIP, "method", method, "balance", completion.Balance)
	case resource.Warn:
		log.Info("rpc client over warn threshold",
			"client", ctx.ClientIP, "method", method, "balance", completion.Balance)
	}
}
func finalizeLoad(manager *resource.Manager, ctx *types.RpcContext, method string, fee resource.Charge, log xrpllog.Logger) {
	chargeLoad(manager, ctx, method, fee, log)
}

func rpcCharge(cost uint32) resource.Charge {
	switch int(cost) {
	case resource.FeeReferenceRPC().Cost():
		return resource.FeeReferenceRPC()
	case resource.FeeMediumBurdenRPC().Cost():
		return resource.FeeMediumBurdenRPC()
	case resource.FeeHeavyBurdenRPC().Cost():
		return resource.FeeHeavyBurdenRPC()
	case resource.FeeMalformedRPC().Cost():
		return resource.FeeMalformedRPC()
	default:
		return resource.NewCharge(int(cost), "RPC")
	}
}
