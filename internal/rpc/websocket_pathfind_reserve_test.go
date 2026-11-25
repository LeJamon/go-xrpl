package rpc

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
	"github.com/stretchr/testify/require"
)

type pathFindReserveView struct {
	*pathFindSessionLedger
	fees drops.Fees
}

func (v *pathFindReserveView) Fees() drops.Fees { return v.fees }

type pathFindWebSocketLedger struct {
	*mockLedgerService
	view types.LedgerStateView
}

func (s *pathFindWebSocketLedger) GetClosedLedgerView() (types.LedgerStateView, error) {
	return s.view, nil
}

func newPathFindReserveFixture(t *testing.T) (*pathFindReserveView, [20]byte, [20]byte) {
	t.Helper()
	ledger := newPathFindSessionLedger()
	source := pathFindSessionAccount(1)
	destination := pathFindSessionAccount(2)
	pathFindSessionAddAccount(t, ledger, source)
	return &pathFindReserveView{pathFindSessionLedger: ledger, fees: drops.DefaultFees()}, source, destination
}

func newPathFindReserveHarness(t *testing.T) (*WebSocketServer, *websocketConnection, *types.RpcContext, *pathFindReserveView, [20]byte, [20]byte) {
	t.Helper()
	ws, conn, ctx := newSpecialDispatchHarness(t)
	view, source, destination := newPathFindReserveFixture(t)
	ledger := &pathFindWebSocketLedger{
		mockLedgerService: newMockLedgerService(),
		view:              view,
	}
	services := types.NewTestServiceGraph(&types.ServiceContainer{
		Ledger:       ledger,
		Capabilities: types.RPCCapabilities{PathSearchMax: 3},
	})
	ws.services = services
	ctx.Services = services
	t.Cleanup(func() {
		require.NoError(t, ws.Close(context.Background()))
	})
	return ws, conn, ctx, view, source, destination
}

func pathFindReserveCreateParams(source, destination string, amount int64) json.RawMessage {
	return json.RawMessage(`{"subcommand":"create","source_account":"` + source +
		`","destination_account":"` + destination + `","destination_amount":"` +
		strconv.FormatInt(amount, 10) + `"}`)
}

func pathFindReserveConvertAllParams(source, destination string) json.RawMessage {
	return json.RawMessage(`{"subcommand":"create","source_account":"` + source +
		`","destination_account":"` + destination + `","destination_amount":"-1"}`)
}

func TestWebSocketPathFindReserveRejectsBelowReserveOnWire(t *testing.T) {
	ws, conn, ctx, _, source, destination := newPathFindReserveHarness(t)
	old := &PathFindSession{id: "old"}
	conn.installPathFindSession(old)
	before := conn.pathFindGeneration
	cmd := specialDispatchCommand("path_find")
	cmd.Params = pathFindReserveCreateParams(
		pathFindSessionAccountAddress(source),
		pathFindSessionAccountAddress(destination),
		int64(drops.DefaultReserveBase)-1,
	)

	ws.handleSpecialCommand(conn, ctx, cmd, ws.executePathFind)
	body := <-conn.Outbound()
	require.JSONEq(t,
		`{"error":"dstAmtMalformed","error_code":51,"error_message":"Destination amount/currency/issuer is malformed.","type":"response","status":"error","id":7,"request":{"command":"path_find","id":7}}`,
		string(body),
	)
	var response map[string]any
	require.NoError(t, json.Unmarshal(body, &response))
	require.Equal(t, float64(51), response["error_code"])
	require.Equal(t, "dstAmtMalformed", response["error"])
	_, active := conn.snapshotPathFindUpdate()
	require.False(t, active)
	require.Equal(t, before+1, conn.pathFindGeneration)
}

func TestWebSocketPathFindReserveBoundaryAndConvertAllCreateSession(t *testing.T) {
	for _, test := range []struct {
		name   string
		params func(source, destination string) json.RawMessage
	}{{
		name: "reserve boundary",
		params: func(source, destination string) json.RawMessage {
			return pathFindReserveCreateParams(source, destination, int64(drops.DefaultReserveBase))
		},
	}, {
		name:   "convert all",
		params: pathFindReserveConvertAllParams,
	}} {
		t.Run(test.name, func(t *testing.T) {
			ws, conn, ctx, _, source, destination := newPathFindReserveHarness(t)
			result, rpcErr := ws.executePathFindCreate(conn, ctx, types.WebSocketCommand{
				ID:     "request-id",
				Params: test.params(pathFindSessionAccountAddress(source), pathFindSessionAccountAddress(destination)),
			})
			require.Nil(t, rpcErr)
			require.NotNil(t, result)
			_, active := conn.snapshotPathFindUpdate()
			require.True(t, active)
		})
	}
}

func TestPathFindRefreshReserveErrorWireAndRecovery(t *testing.T) {
	ws, connections := newPathFindRefreshTestServer(t, 1)
	manager := ws.ensurePathFindRefreshManager()
	defer func() { require.NoError(t, manager.wait(context.Background())) }()

	view, source, destination := newPathFindReserveFixture(t)
	params := pathFindReserveCreateParams(
		pathFindSessionAccountAddress(source),
		pathFindSessionAccountAddress(destination),
		int64(drops.DefaultReserveBase),
	)
	session, rpcErr := ParseAndCreateSession(params, "request-id")
	require.Nil(t, rpcErr)
	var computes int
	computed := make(chan struct{}, 2)
	session.computeFn = func(tx.LedgerView) *pathfinder.PathRequestResult {
		computes++
		computed <- struct{}{}
		return &pathfinder.PathRequestResult{}
	}
	conn := connections[0]
	require.NotNil(t, conn.clearPathFindSession())
	conn.installPathFindSession(session)

	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return view, nil })
	select {
	case <-computed:
	case <-time.After(time.Second):
		t.Fatal("initial refresh did not compute")
	}
	initial := <-conn.Outbound()
	var initialEvent map[string]any
	require.NoError(t, json.Unmarshal(initial, &initialEvent))
	require.Equal(t, "path_find", initialEvent["type"])
	require.NotContains(t, initialEvent, "error")
	stored := session.Status()
	require.Empty(t, stored.Type)
	require.Equal(t, "success", stored.Status)

	invalidView := &pathFindReserveView{pathFindSessionLedger: view.pathFindSessionLedger, fees: drops.Fees{
		Base:      view.fees.Base,
		Reserve:   drops.DefaultReserveBase + 1,
		Increment: view.fees.Increment,
	}}
	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return invalidView, nil })
	invalid := <-conn.Outbound()
	require.JSONEq(t,
		`{"type":"path_find","error":"dstAmtMalformed","error_code":51,"error_message":"Destination amount/currency/issuer is malformed."}`,
		string(invalid),
	)
	require.Equal(t, 1, computes)
	stored = session.Status()
	require.Empty(t, stored.Type)
	require.Equal(t, "dstAmtMalformed", stored.Error)
	require.Equal(t, "success", stored.Status)

	ws.UpdatePathFindSessions(func() (types.LedgerStateView, error) { return view, nil })
	select {
	case <-computed:
	case <-time.After(time.Second):
		t.Fatal("refresh did not recover after reserve validation error")
	}
	recovered := <-conn.Outbound()
	var recoveredEvent map[string]any
	require.NoError(t, json.Unmarshal(recovered, &recoveredEvent))
	require.Equal(t, "path_find", recoveredEvent["type"])
	require.NotContains(t, recoveredEvent, "error")
	require.Equal(t, 2, computes)
}

func TestPathFindCommandsAfterRefreshError(t *testing.T) {
	ws, conn, ctx, view, source, destination := newPathFindReserveHarness(t)
	params := pathFindReserveCreateParams(pathFindSessionAccountAddress(source),
		pathFindSessionAccountAddress(destination), int64(view.fees.Reserve))
	session, rpcErr := ParseAndCreateSession(params, "original")
	require.Nil(t, rpcErr)
	ws.bindPathFindRefreshManager(conn)
	conn.installPathFindSession(session)
	view.fees.Reserve++
	ws.queuePathFindSessions(func() (types.LedgerStateView, error) { return view, nil }, conn)
	select {
	case pushed := <-conn.Outbound():
		require.JSONEq(t, `{"type":"path_find","error":"dstAmtMalformed","error_code":51,"error_message":"Destination amount/currency/issuer is malformed."}`, string(pushed))
	case <-time.After(time.Second):
		t.Fatal("missing path_find refresh error")
	}

	for _, subcommand := range []string{"status", "close"} {
		cmd := specialDispatchCommand("path_find")
		cmd.Request["subcommand"] = subcommand
		params, err := json.Marshal(cmd.Request)
		require.NoError(t, err)
		cmd.Params = params
		ws.handleSpecialCommand(conn, ctx, cmd, ws.executePathFind)

		want := map[string]any{
			"type": "response", "status": "error", "id": cmd.ID, "request": cmd.Request,
			"error": "dstAmtMalformed", "error_code": 51,
			"error_message": "Destination amount/currency/issuer is malformed.",
		}
		if subcommand == "close" {
			want["closed"] = true
		}
		body, err := json.Marshal(want)
		require.NoError(t, err)
		require.JSONEq(t, string(body), string(<-conn.Outbound()))
	}
	_, active := conn.snapshotPathFindUpdate()
	require.False(t, active)
}
