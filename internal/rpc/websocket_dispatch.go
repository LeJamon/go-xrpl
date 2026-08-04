package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime/debug"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/loadtrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type wsSpecialHandler func(*websocketConnection, *types.RpcContext, types.WebSocketCommand) (any, *types.RpcError)

func (ws *WebSocketServer) handleMessage(wsConn *websocketConnection, message []byte) {
	// Per-message recover so one bad command can't tear down the read
	// loop and drop the connection's pending subscriptions.
	defer func() {
		if rec := recover(); rec != nil {
			wsLog().Error("ws message panic", "conn", wsConn.ID, "err", rec, "stack", string(debug.Stack()))
			ws.sendErrorResponse(wsConn, rpcInternalError(), nil, nil, buildWSRequestEcho(message))
		}
	}()

	var requestValue any
	if err := decodeJSONUseNumber(message, &requestValue); err != nil {
		ws.sendJSONInvalid(wsConn, nil, false)
		return
	}
	cmdMap, ok := requestValue.(map[string]any)
	if !ok || cmdMap == nil {
		ws.sendJSONInvalid(wsConn, requestValue, true)
		return
	}

	requestEcho := redactedRequestMap(cmdMap)
	var id any
	if idVal, exists := requestEcho["id"]; exists {
		id = idVal
		cmdMap["id"] = idVal
	}

	// Role is always derived from the socket-level peer, never from
	// header-supplied IPs. ClientIP is the peer too, unless the peer is in this
	// port's secure_gateway set — then we substitute the value captured at
	// upgrade time (matches rippled WSInfoSub::forwarded_for,
	// ServerHandler.cpp:497-501). Derived before command resolution so a
	// malformed request can be load-charged like rippled charges it.
	peerIP := getWebSocketClientIP(wsConn.conn)
	clientIP := resolveWSClientIP(peerIP, wsConn.forwardedFor, wsConn.portCtx)
	role := roleForRequest(peerIP, wsConn.user, cmdMap, wsConn.portCtx)
	loadCtx := newRpcContext(wsConn.Context(), role, types.DefaultApiVersion, clientIP, ws.loadPeerSource(), ws.services)
	if rpcErr := gateLoad(ws.loadTracker, loadCtx, "", wsLog()); rpcErr != nil {
		wsConn.closeWithPolicyViolation("threshold exceeded")
		return
	}

	apiVersion := types.DefaultApiVersion
	if version, present := apiVersionFromObject(message); present {
		apiVersion = version
	}
	versionCtx := newRpcContext(wsConn.Context(), role, apiVersion, clientIP, ws.loadPeerSource(), ws.services)
	if rpcErr := validateApiVersion(versionCtx); rpcErr != nil {
		chargeLoad(ws.loadTracker, versionCtx, "", loadtrack.LoadMalformed, wsLog())
		ws.sendErrorResponse(wsConn, rpcErr, id, nil, requestEcho)
		return
	}

	// rippled accepts `method` as an alias for `command`, rejecting only when
	// neither is present (or both are present strings that disagree) with a
	// bare missingCommand token that echoes the original request, and charges
	// feeMalformedRPC (ServerHandler.cpp:446-468).
	command, ok := resolveWSCommand(cmdMap)
	if !ok {
		chargeLoad(ws.loadTracker, versionCtx, "", loadtrack.LoadMalformed, wsLog())
		ws.sendMissingCommand(wsConn, cmdMap, id)
		return
	}

	cmd := types.WebSocketCommand{
		Command: command,
		ID:      id,
		Request: requestEcho,
	}

	delete(cmdMap, "command")
	delete(cmdMap, "method")
	delete(cmdMap, "api_version")

	if len(cmdMap) > 0 {
		paramsBytes, _ := json.Marshal(cmdMap)
		cmd.Params = paramsBytes
	}

	wsLog().Debug("ws request", "cmd", cmd.Command, "remoteAddr", wsConn.conn.RemoteAddr().String(), "clientIP", clientIP, "role", role, "isAdmin", role == types.RoleAdmin)
	dispatchCtx := wsConn.Context()
	var cancel context.CancelFunc
	if ws.timeout > 0 {
		dispatchCtx, cancel = context.WithTimeout(wsConn.Context(), ws.timeout)
		defer cancel()
	}
	rpcCtx := newRpcContext(dispatchCtx, role, apiVersion, clientIP, ws.loadPeerSource(), ws.services)

	switch cmd.Command {
	case "subscribe":
		ws.handleSpecialCommand(wsConn, rpcCtx, cmd, ws.executeSubscribe)
		return
	case "unsubscribe":
		ws.handleSpecialCommand(wsConn, rpcCtx, cmd, ws.executeUnsubscribe)
		return
	case "path_find":
		ws.handleSpecialCommand(wsConn, rpcCtx, cmd, ws.executePathFind)
		return
	}

	ws.handleRPCMethod(wsConn, rpcCtx, cmd)
}
func (ws *WebSocketServer) handleSpecialCommand(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand, handler wsSpecialHandler) {
	ctx.LoadCost = uint32(loadtrack.LoadReference)
	if rpcErr := handlers.RequireNotBusyClient(ctx); rpcErr != nil {
		finalizeLoad(ws.loadTracker, ctx, cmd.Command, loadtrack.LoadReference, wsLog())
		ws.sendCommandError(wsConn, rpcErr, cmd)
		return
	}

	resolution := resolveMethod(ws.methodRegistry, cmd.Command, ctx.ApiVersion)
	if !resolution.resolved {
		finalizeLoad(ws.loadTracker, ctx, cmd.Command, loadtrack.LoadReference, wsLog())
		ws.sendCommandError(wsConn, types.RpcErrorMethodNotFound(), cmd)
		return
	}
	if rpcErr := conditionMet(resolution.handler.RequiredCondition(), ctx); rpcErr != nil {
		finalizeLoad(ws.loadTracker, ctx, cmd.Command, loadtrack.LoadReference, wsLog())
		ws.sendCommandError(wsConn, rpcErr, cmd)
		return
	}

	result, rpcErr, recovered := func() (any, *types.RpcError, bool) {
		if ws.services != nil && ws.services.ClientLoad != nil {
			ws.services.ClientLoad.Begin()
			defer ws.services.ClientLoad.End()
		}
		finishDiagnostics := startRPCDiagnostics(ws.services, cmd.Command)
		result, rpcErr, recovered := invokeWSSpecial(handler, wsConn, ctx, cmd)
		finishDiagnostics(recovered)
		return result, rpcErr, recovered
	}()
	kind := loadtrack.LoadKind(ctx.LoadCost)
	if recovered && kind == loadtrack.LoadReference {
		kind = loadtrack.LoadException
	}
	finalizeLoad(ws.loadTracker, ctx, cmd.Command, kind, wsLog())
	if rpcErr != nil {
		// Error responses deliberately omit warnings produced by the final charge.
		ws.sendCommandError(wsConn, rpcErr, cmd)
		return
	}
	ws.sendCommandResponse(wsConn, result, cmd, wsLoadWarningOpts(ctx))
}
func invokeWSSpecial(handler wsSpecialHandler, wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (result any, rpcErr *types.RpcError, recovered bool) {
	defer func() {
		if rec := recover(); rec != nil {
			wsLog().Error("rpc handler panic", "err", rec, "stack", string(debug.Stack()), "method", cmd.Command, "client", ctx.ClientIP)
			result = nil
			rpcErr = rpcInternalError()
			recovered = true
		}
	}()
	result, rpcErr = handler(wsConn, ctx, cmd)
	return result, rpcErr, false
}
func (ws *WebSocketServer) handleRPCMethod(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	// Shared dispatch core (registry → admin gate → conditionMet →
	// api-version → busy/load gates → handle → finalize), identical to the
	// HTTP path. The WS-specific admin gate returns rpcFORBIDDEN instead of
	// rpcNO_PERMISSION (ServerHandler.cpp:482-486): when requestRole returns
	// Role::FORBID for an admin-required command, rippled writes
	// rpcError(rpcFORBIDDEN) before doCommand ever runs.
	resolution := resolveMethod(ws.methodRegistry, cmd.Command, ctx.ApiVersion)
	if rpcErr := admitMethod(ws.loadTracker, ctx, cmd.Command, resolution, types.RpcErrorForbidden, false, wsLog()); rpcErr != nil {
		warnLoad(ws.loadTracker, ctx, cmd.Command, wsLog())
		ws.sendErrorResponse(wsConn, rpcErr, cmd.ID, nil, cmd.Request)
		return
	}
	result, rpcErr := dispatchResolvedMethod(ws.loadTracker, ws.services, ctx, cmd.Command, cmd.Params, resolution, wsLog())
	if rpcErr != nil {
		ws.sendErrorResponse(wsConn, rpcErr, cmd.ID, nil, cmd.Request)
		return
	}
	ws.sendCommandResponse(wsConn, result, cmd, wsLoadWarningOpts(ctx))
}

// wsLoadWarningOpts surfaces rippled's warning:"load" on a WS reply when the
// dispatch crossed the resource warn threshold (recorded on ctx by
// finalizeLoad), and returns nil otherwise.
func wsLoadWarningOpts(ctx *types.RpcContext) *types.WebSocketResponseOptions {
	if ctx != nil && ctx.LoadWarning {
		return &types.WebSocketResponseOptions{Warning: "load"}
	}
	return nil
}
func (ws *WebSocketServer) sendCommandResponse(wsConn *websocketConnection, result any, cmd types.WebSocketCommand, opts *types.WebSocketResponseOptions) {
	payload := map[string]any{
		"type":   "response",
		"status": "success",
	}
	if result != nil {
		payload["result"] = result
	}
	copyWSMetadata(payload, cmd.Request, cmd.ID)
	if opts != nil {
		if opts.Warning != "" {
			payload["warning"] = opts.Warning
		}
		if len(opts.Warnings) > 0 {
			payload["warnings"] = opts.Warnings
		}
		if opts.Forwarded {
			payload["forwarded"] = true
		}
	}

	data, err := marshalWebSocketJSON(payload)
	if err != nil {
		wsLog().Error("Failed to marshal WebSocket response", "err", err)
		return
	}
	ws.deliver(wsConn, data)
}

// deliver queues an already-marshalled WS frame through the canonical
// connection so per-request response delivery and broadcast delivery use the
// same drop policy.
func (ws *WebSocketServer) deliver(wsConn *websocketConnection, data []byte) {
	if !wsConn.TrySend(data) {
		wsLog().Debug("WebSocket send dropped (slow consumer)", "connID", wsConn.ID)
	}
}

// resolveWSCommand resolves the WS command name from the incoming JSON,
// accepting `method` as an alias for `command` (ServerHandler.cpp:446-475).
// ok is false when either supplied field is not a string, neither is supplied,
// or both strings disagree. Empty strings are valid at this layer and resolve
// to unknownCmd during dispatch.
func resolveWSCommand(m map[string]any) (string, bool) {
	cmdValue, cmdPresent := m["command"]
	methodValue, methodPresent := m["method"]
	cmd, cmdOK := cmdValue.(string)
	method, methodOK := methodValue.(string)
	if (cmdPresent && !cmdOK) || (methodPresent && !methodOK) {
		return "", false
	}
	switch {
	case cmdPresent && methodPresent:
		if cmd != method {
			return "", false
		}
		return cmd, true
	case cmdPresent:
		return cmd, true
	case methodPresent:
		return method, true
	default:
		return "", false
	}
}

// sendMissingCommand emits rippled's bare missingCommand reply: a lone
// `error` token (no error_code/error_message) plus the echoed request and id
// (ServerHandler.cpp:452-468). Credentials in the echo are redacted — a
// deliberate goxrpl superset of rippled's raw echo.
func (ws *WebSocketServer) sendMissingCommand(wsConn *websocketConnection, request map[string]any, id any) {
	echo := redactedRequestMap(request)
	resp := map[string]any{
		"type":    "response",
		"status":  "error",
		"error":   "missingCommand",
		"request": echo,
	}
	copyWSMetadata(resp, echo, id)
	data, err := marshalWebSocketJSON(resp)
	if err != nil {
		wsLog().Error("Failed to marshal malformed WebSocket response", "err", err)
		return
	}
	ws.deliver(wsConn, data)
}
func (ws *WebSocketServer) sendJSONInvalid(wsConn *websocketConnection, value any, parsed bool) {
	rawValue := "<redacted>"
	if parsed {
		if data, err := marshalWebSocketJSON(redactJSONValue(value)); err == nil {
			rawValue = string(data)
		}
	}
	data, err := marshalWebSocketJSON(map[string]any{
		"type":  "error",
		"error": "jsonInvalid",
		"value": rawValue,
	})
	if err != nil {
		wsLog().Error("Failed to marshal invalid JSON response", "err", err)
		return
	}
	ws.deliver(wsConn, data)
}
func (ws *WebSocketServer) sendCommandError(wsConn *websocketConnection, rpcErr *types.RpcError, cmd types.WebSocketCommand) {
	ws.sendErrorResponse(wsConn, rpcErr, cmd.ID, nil, cmd.Request)
}
func (ws *WebSocketServer) sendErrorResponse(wsConn *websocketConnection, rpcErr *types.RpcError, id any, opts *types.WebSocketResponseOptions, request map[string]any) {
	response := map[string]any{
		"type":   "response",
		"status": "error",
		"error":  rpcErr.ErrorString,
	}
	if id != nil {
		response["id"] = id
	}
	if rpcErr.ErrorException != "" {
		response["error_exception"] = rpcErr.ErrorException
	} else if !rpcErr.IsBareToken() {
		response["error_code"] = rpcErr.Code
		response["error_message"] = rpcErr.Message
	}

	if opts != nil {
		if opts.Warning != "" {
			response["warning"] = opts.Warning
		}
		if len(opts.Warnings) > 0 {
			response["warnings"] = opts.Warnings
		}
		if opts.Forwarded {
			response["forwarded"] = true
		}
	}

	if request != nil {
		response["request"] = request
	}
	copyWSMetadata(response, request, id)

	data, err := marshalWebSocketJSON(response)
	if err != nil {
		wsLog().Error("Failed to marshal WebSocket error response", "err", err)
		return
	}
	ws.deliver(wsConn, data)
}
func buildWSRequestEcho(message []byte) map[string]any {
	var request map[string]any
	if err := decodeJSONUseNumber(message, &request); err != nil || request == nil {
		return nil
	}
	return redactedRequestMap(request)
}
func copyWSMetadata(response map[string]any, request map[string]any, fallbackID any) {
	if fallbackID != nil {
		response["id"] = fallbackID
	}
	for _, key := range []string{"id", "jsonrpc", "ripplerpc", "api_version"} {
		if value, ok := request[key]; ok {
			response[key] = value
		}
	}
}
func marshalWebSocketJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}
