package rpc

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

func newSpecialDispatchHarness(t *testing.T) (*WebSocketServer, *websocketConnection, *types.RpcContext) {
	t.Helper()
	services := types.NewTestServiceGraph(&types.ServiceContainer{
		ClientLoad:     types.NewClientLoadShedder(),
		RPCDiagnostics: NewRPCDiagnostics(),
		Capabilities:   types.RPCCapabilities{PathSearchMax: 3},
	})
	manager := resource.NewManager(nil, nil)
	ws := NewWebSocketServer(WebSocketServerOptions{
		Timeout:         time.Second,
		Services:        services,
		ResourceManager: manager,
		Registry: mustTestMethodRegistry(t, map[string]types.MethodHandler{
			"subscribe": &stubHandler{},
			"path_find": &stubHandler{},
		}),
	})
	send := make(chan []byte, 1)
	conn := &websocketConnection{
		Connection: subscription.NewConnectionWithContext(context.Background(), "special-dispatch", send),
	}
	registration, attached := ws.subscriptionManager.Attach(conn.Connection)
	require.True(t, attached)
	conn.registration = registration
	t.Cleanup(func() { ws.subscriptionManager.Detach(registration) })
	ctx := newRpcContext(context.Background(), types.RoleGuest, types.DefaultApiVersion, "192.0.2.1", nil, services, nil, nil)
	consumer := manager.NewInboundEndpoint(ctx.ClientIP)
	require.NotNil(t, consumer)
	t.Cleanup(consumer.Release)
	conn.resourceConsumer = consumer
	ctx.ResourceConsumer = consumer
	ctx.ResourceAdmission, _ = consumer.Admit(resource.FeeReferenceRPC())
	require.NotNil(t, ctx.ResourceAdmission)
	return ws, conn, ctx
}

func setSpecialServices(ctx *types.RpcContext, mutate func(*types.ServiceContainer)) {
	previous := ctx.Services
	services := &types.ServiceContainer{}
	if previous != nil {
		services.ClientLoad = previous.ClientLoad()
		services.RPCDiagnostics = previous.RPCDiagnostics()
		services.Capabilities = previous.Capabilities()
		services.IsLoadedLocal = previous.IsLoadedLocal()
	}
	mutate(services)
	ctx.Services = types.NewTestServiceGraph(services)
}

func specialDispatchResponse(t *testing.T, conn *websocketConnection) map[string]any {
	t.Helper()
	select {
	case body := <-conn.Outbound():
		var response map[string]any
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatalf("decode response %s: %v", body, err)
		}
		return response
	default:
		t.Fatal("special dispatch produced no response")
		return nil
	}
}

func specialDispatchCommand(command string) types.WebSocketCommand {
	return types.WebSocketCommand{
		Command: command,
		ID:      int32(7),
		Request: map[string]any{"command": command, "id": int32(7)},
	}
}

func TestWebSocketSpecialDispatchBusyBoundary(t *testing.T) {
	for _, test := range []struct {
		name       string
		inFlight   int
		wantCalled bool
		wantStatus string
	}{
		{name: "499 admitted", inFlight: 499, wantCalled: true, wantStatus: "success"},
		{name: "500 rejected", inFlight: 500, wantCalled: false, wantStatus: "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ws, conn, ctx := newSpecialDispatchHarness(t)
			for range test.inFlight {
				ws.services.ClientLoad().Begin()
			}
			if !test.wantCalled {
				require.NoError(t, ws.resourceManager.ImportConsumers("peer", resource.Gossip{Items: []resource.GossipItem{{
					Address: ctx.ClientIP,
					Balance: resource.WarningThreshold,
				}}}))
			}
			called := false
			ws.handleSpecialCommand(conn, ctx, specialDispatchCommand("subscribe"), func(_ *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *rpcerrors.RpcError) {
				called = true
				if got := ws.services.ClientLoad().InFlight(); got != int64(test.inFlight+1) {
					t.Fatalf("in-flight inside handler = %d, want %d", got, test.inFlight+1)
				}
				return map[string]any{}, nil
			})

			response := specialDispatchResponse(t, conn)
			if called != test.wantCalled {
				t.Fatalf("handler called = %t, want %t", called, test.wantCalled)
			}
			if got := response["status"]; got != test.wantStatus {
				t.Fatalf("status = %v, want %s", got, test.wantStatus)
			}
			if !test.wantCalled && (!ctx.LoadWarning || response["warning"] != nil) {
				t.Fatalf("busy admission must consume but hide its load warning: %v", response)
			}
			if got := ws.services.ClientLoad().InFlight(); got != int64(test.inFlight) {
				t.Fatalf("in-flight after dispatch = %d, want %d", got, test.inFlight)
			}
		})
	}
}

func TestWebSocketSpecialDispatchWarningVisibility(t *testing.T) {
	for _, test := range []struct {
		name        string
		handler     wsSpecialHandler
		wantWarning bool
	}{
		{
			name: "success exposes warning",
			handler: func(_ *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *rpcerrors.RpcError) {
				return map[string]any{}, nil
			},
			wantWarning: true,
		},
		{
			name: "error suppresses warning",
			handler: func(_ *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *rpcerrors.RpcError) {
				return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ws, conn, ctx := newSpecialDispatchHarness(t)
			require.NoError(t, ws.resourceManager.ImportConsumers("peer", resource.Gossip{Items: []resource.GossipItem{{
				Address: ctx.ClientIP,
				Balance: resource.WarningThreshold,
			}}}))

			ws.handleSpecialCommand(conn, ctx, specialDispatchCommand("subscribe"), test.handler)
			response := specialDispatchResponse(t, conn)
			if got := response["warning"] == "load"; got != test.wantWarning {
				t.Fatalf("warning present = %t, want %t (response %v)", got, test.wantWarning, response)
			}
			if !ctx.LoadWarning {
				t.Fatal("post-dispatch Warn was not consumed")
			}
		})
	}
}

func TestWebSocketSpecialDispatchRecoversPanic(t *testing.T) {
	ws, conn, ctx := newSpecialDispatchHarness(t)
	ws.handleSpecialCommand(conn, ctx, specialDispatchCommand("subscribe"), func(_ *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *rpcerrors.RpcError) {
		panic("private panic detail")
	})

	response := specialDispatchResponse(t, conn)
	wantResponse := map[string]any{
		"type":          "response",
		"status":        "error",
		"error":         "internal",
		"error_code":    float64(rpcerrors.RpcINTERNAL),
		"error_message": "Internal error.",
		"id":            float64(7),
		"request":       map[string]any{"command": "subscribe", "id": float64(7)},
	}
	if !reflect.DeepEqual(response, wantResponse) {
		t.Fatalf("panic response = %v, want %v", response, wantResponse)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal panic response: %v", err)
	}
	if strings.Contains(string(encoded), "private panic detail") {
		t.Fatalf("panic response leaked private detail: %s", encoded)
	}
	if got, want := resourceLocalBalance(t, ws.resourceManager, ctx.ClientIP), uint32(resource.FeeExceptionRPC().Cost()/resource.DecayWindowSeconds); got != want {
		t.Fatalf("panic charge = %v, want %v", got, want)
	}
	if got := ws.services.ClientLoad().InFlight(); got != 0 {
		t.Fatalf("in-flight leaked after panic: %d", got)
	}
	if stats := ws.services.RPCDiagnostics().Snapshot().Methods["subscribe"]; stats.Started != 1 || stats.Finished != 0 || stats.Errored != 1 {
		t.Fatalf("diagnostics = %#v", stats)
	}
}

func TestWebSocketPathFindDynamicLoadCost(t *testing.T) {
	ws, conn, ctx := newSpecialDispatchHarness(t)
	for _, test := range []struct {
		subcommand string
		want       int
	}{
		{subcommand: "create", want: resource.FeeHeavyBurdenRPC().Cost()},
		{subcommand: "close", want: resource.FeeReferenceRPC().Cost()},
		{subcommand: "status", want: resource.FeeReferenceRPC().Cost()},
	} {
		t.Run(test.subcommand, func(t *testing.T) {
			ctx.LoadCost = uint32(resource.FeeReferenceRPC().Cost())
			cmd := specialDispatchCommand("path_find")
			cmd.Params = json.RawMessage(`{"subcommand":"` + test.subcommand + `"}`)
			_, _ = ws.executePathFind(conn, ctx, cmd)
			if got := int(ctx.LoadCost); got != test.want {
				t.Fatalf("load kind = %d, want %d", got, test.want)
			}
		})
	}
}

func TestWebSocketPathFindCapabilityPrecedesSubcommandValidation(t *testing.T) {
	ws, conn, ctx := newSpecialDispatchHarness(t)
	setSpecialServices(ctx, func(services *types.ServiceContainer) {
		services.Capabilities.PathSearchMax = 0
	})
	_, rpcErr := ws.executePathFind(conn, ctx, types.WebSocketCommand{Params: json.RawMessage(`{not json`)})
	if rpcErr == nil || rpcErr.ErrorString != "notSupported" {
		t.Fatalf("disabled path_find error = %v, want notSupported", rpcErr)
	}
}

func TestWebSocketPathFindCreateDoesNotUseLegacyBusyGate(t *testing.T) {
	ws, conn, ctx := newSpecialDispatchHarness(t)
	setSpecialServices(ctx, func(services *types.ServiceContainer) {
		services.IsLoadedLocal = func() bool { return true }
	})
	_, rpcErr := ws.executePathFind(conn, ctx, types.WebSocketCommand{
		Params: json.RawMessage(`{"subcommand":"create"}`),
	})
	if rpcErr == nil || rpcErr.ErrorString != "invalidParams" || rpcErr.Message != "Missing field 'source_account'." {
		t.Fatalf("path_find create error = %v, want field validation after admission", rpcErr)
	}
}

func TestWebSocketPathFindSubcommandValidationUsesCanonicalParams(t *testing.T) {
	for _, params := range []string{`{}`, `{"subcommand":7}`, `{"subcommand":"future"}`, `{not json`} {
		t.Run(params, func(t *testing.T) {
			ws, conn, ctx := newSpecialDispatchHarness(t)
			_, rpcErr := ws.executePathFind(conn, ctx, types.WebSocketCommand{Params: json.RawMessage(params)})
			if rpcErr == nil || rpcErr.ErrorString != "invalidParams" || rpcErr.Message != "Invalid parameters." {
				t.Fatalf("path_find error = %v, want canonical invalidParams", rpcErr)
			}
		})
	}
}

func TestWebSocketSubscribeSnapshotLoadCostAfterValidation(t *testing.T) {
	const validSnapshot = `{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"USD","issuer":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},"snapshot":true}]}`
	const validStateNow = `{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"USD","issuer":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},"state_now":true}]}`
	const invalidSnapshot = `{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"XRP"},"snapshot":true}]}`
	const validThenInvalidSnapshot = `{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"USD","issuer":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},"snapshot":true},{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"XRP"}}]}`
	for _, test := range []struct {
		name      string
		params    string
		want      int
		wantError bool
	}{
		{name: "validated snapshot", params: validSnapshot, want: resource.FeeMediumBurdenRPC().Cost()},
		{name: "validated state_now", params: validStateNow, want: resource.FeeMediumBurdenRPC().Cost()},
		{name: "invalid snapshot", params: invalidSnapshot, want: resource.FeeReferenceRPC().Cost(), wantError: true},
		{name: "later invalid book retains snapshot load", params: validThenInvalidSnapshot, want: resource.FeeMediumBurdenRPC().Cost(), wantError: true},
		{name: "no snapshot", params: `{}`, want: resource.FeeReferenceRPC().Cost()},
	} {
		t.Run(test.name, func(t *testing.T) {
			ws, conn, ctx := newSpecialDispatchHarness(t)
			ctx.LoadCost = uint32(resource.FeeReferenceRPC().Cost())
			cmd := specialDispatchCommand("subscribe")
			cmd.Params = json.RawMessage(test.params)
			_, rpcErr := ws.executeSubscribe(conn, ctx, cmd)
			if (rpcErr != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %t", rpcErr, test.wantError)
			}
			if got := int(ctx.LoadCost); got != test.want {
				t.Fatalf("load kind = %d, want %d", got, test.want)
			}
		})
	}
}

func TestWebSocketSubscribeBothSidesAlias(t *testing.T) {
	ws, conn, ctx := newSpecialDispatchHarness(t)
	cmd := specialDispatchCommand("subscribe")
	cmd.Params = json.RawMessage(`{"books":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"USD","issuer":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},"both_sides":true}]}`)
	_, rpcErr := ws.executeSubscribe(conn, ctx, cmd)
	if rpcErr != nil {
		t.Fatalf("subscribe error = %v", rpcErr)
	}
	books := conn.registration.Snapshot().BookCount()
	if books != 2 {
		t.Fatalf("book subscriptions = %d, want 2", books)
	}
}
