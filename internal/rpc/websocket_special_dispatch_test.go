package rpc

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/loadtrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func newSpecialDispatchHarness(t *testing.T) (*WebSocketServer, *websocketConnection, *types.RpcContext) {
	t.Helper()
	services := &types.ServiceContainer{ClientLoad: types.NewClientLoadShedder()}
	ws := NewWebSocketServerWithLoadTracker(time.Second, services, loadtrack.New())
	ws.methodRegistry.Register("subscribe", &stubHandler{})
	ws.methodRegistry.Register("path_find", &stubHandler{})
	send := make(chan []byte, 1)
	conn := &websocketConnection{
		Connection: types.NewConnectionWithContext(context.Background(), "special-dispatch", send),
	}
	ctx := newRpcContext(context.Background(), types.RoleGuest, types.DefaultApiVersion, "192.0.2.1", nil, services)
	return ws, conn, ctx
}

func specialDispatchResponse(t *testing.T, conn *websocketConnection) map[string]any {
	t.Helper()
	select {
	case body := <-conn.SendChannel:
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
				ws.services.ClientLoad.Begin()
			}
			if !test.wantCalled {
				ws.loadTracker.Import("peer", loadtrack.Gossip{Items: []loadtrack.GossipItem{{
					Key:     ctx.ClientIP,
					Balance: loadtrack.WarningThreshold,
				}}})
			}
			called := false
			ws.handleSpecialCommand(conn, ctx, specialDispatchCommand("subscribe"), func(_ *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *types.RpcError) {
				called = true
				if got := ws.services.ClientLoad.InFlight(); got != int64(test.inFlight+1) {
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
			if got := ws.services.ClientLoad.InFlight(); got != int64(test.inFlight) {
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
			handler: func(_ *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *types.RpcError) {
				return map[string]any{}, nil
			},
			wantWarning: true,
		},
		{
			name: "error suppresses warning",
			handler: func(_ *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *types.RpcError) {
				return nil, types.RpcErrorInvalidParams("Invalid parameters.")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ws, conn, ctx := newSpecialDispatchHarness(t)
			ws.loadTracker.Import("peer", loadtrack.Gossip{Items: []loadtrack.GossipItem{{
				Key:     ctx.ClientIP,
				Balance: loadtrack.WarningThreshold,
			}}})

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
	ws.handleSpecialCommand(conn, ctx, specialDispatchCommand("subscribe"), func(_ *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *types.RpcError) {
		panic("private panic detail")
	})

	response := specialDispatchResponse(t, conn)
	wantResponse := map[string]any{
		"type":          "response",
		"status":        "error",
		"error":         "internal",
		"error_code":    float64(types.RpcINTERNAL),
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
	if got, want := ws.loadTracker.LocalBalance(ctx.ClientIP), float64(loadtrack.ChargeException/uint32(loadtrack.DecayWindow/time.Second)); got != want {
		t.Fatalf("panic charge = %v, want %v", got, want)
	}
	if got := ws.services.ClientLoad.InFlight(); got != 0 {
		t.Fatalf("in-flight leaked after panic: %d", got)
	}
}

func TestWebSocketPathFindDynamicLoadCost(t *testing.T) {
	ws, conn, ctx := newSpecialDispatchHarness(t)
	for _, test := range []struct {
		subcommand string
		want       loadtrack.LoadKind
	}{
		{subcommand: "create", want: loadtrack.LoadHeavy},
		{subcommand: "close", want: loadtrack.LoadReference},
		{subcommand: "status", want: loadtrack.LoadReference},
	} {
		t.Run(test.subcommand, func(t *testing.T) {
			ctx.LoadCost = uint32(loadtrack.LoadReference)
			cmd := specialDispatchCommand("path_find")
			cmd.Params = json.RawMessage(`{"subcommand":"` + test.subcommand + `"}`)
			_, _ = ws.executePathFind(conn, ctx, cmd)
			if got := loadtrack.LoadKind(ctx.LoadCost); got != test.want {
				t.Fatalf("load kind = %d, want %d", got, test.want)
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
		want      loadtrack.LoadKind
		wantError bool
	}{
		{name: "validated snapshot", params: validSnapshot, want: loadtrack.LoadMedium},
		{name: "validated state_now", params: validStateNow, want: loadtrack.LoadMedium},
		{name: "invalid snapshot", params: invalidSnapshot, want: loadtrack.LoadReference, wantError: true},
		{name: "later invalid book retains snapshot load", params: validThenInvalidSnapshot, want: loadtrack.LoadMedium, wantError: true},
		{name: "no snapshot", params: `{}`, want: loadtrack.LoadReference},
	} {
		t.Run(test.name, func(t *testing.T) {
			ws, conn, ctx := newSpecialDispatchHarness(t)
			ctx.LoadCost = uint32(loadtrack.LoadReference)
			cmd := specialDispatchCommand("subscribe")
			cmd.Params = json.RawMessage(test.params)
			_, rpcErr := ws.executeSubscribe(conn, ctx, cmd)
			if (rpcErr != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %t", rpcErr, test.wantError)
			}
			if got := loadtrack.LoadKind(ctx.LoadCost); got != test.want {
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
	books := conn.Subscriptions[types.SubBook].Books
	if len(books) != 2 {
		t.Fatalf("book subscriptions = %d, want 2", len(books))
	}
}
