package rpc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func TestRPCDiagnosticsSnapshotsAndCompletion(t *testing.T) {
	now := time.Unix(100, 0)
	diagnostics := newRPCDiagnostics(func() time.Time { return now })

	finishInfo := diagnostics.Start("server_info")
	now = now.Add(1250 * time.Microsecond)
	active := diagnostics.Snapshot()
	if got := active.Methods["server_info"].Started; got != 1 {
		t.Fatalf("started = %d, want 1", got)
	}
	if len(active.Current) != 1 || active.Current[0].DurationUs != 1250 {
		t.Fatalf("current = %#v", active.Current)
	}

	finishInfo(false)
	finishInfo(false)
	now = now.Add(250 * time.Microsecond)
	finishState := diagnostics.Start("server_state")
	now = now.Add(750 * time.Microsecond)
	finishState(true)

	snapshot := diagnostics.Snapshot()
	if got := snapshot.Methods["server_info"]; got.Finished != 1 || got.Errored != 0 || got.DurationUs != 1250 {
		t.Fatalf("server_info = %#v", got)
	}
	if got := snapshot.Methods["server_state"]; got.Finished != 0 || got.Errored != 1 || got.DurationUs != 750 {
		t.Fatalf("server_state = %#v", got)
	}
	if len(snapshot.Current) != 0 {
		t.Fatalf("current after finish = %#v", snapshot.Current)
	}

	delete(snapshot.Methods, "server_info")
	if _, ok := diagnostics.Snapshot().Methods["server_info"]; !ok {
		t.Fatal("mutating a snapshot changed collector state")
	}
}

func TestRPCDiagnosticsConcurrentAccounting(t *testing.T) {
	diagnostics := NewRPCDiagnostics()
	const calls = 64
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			diagnostics.Start("ledger")(false)
		}()
	}
	wg.Wait()
	stats := diagnostics.Snapshot().Methods["ledger"]
	if stats.Started != calls || stats.Finished != calls || stats.Errored != 0 {
		t.Fatalf("stats = %#v", stats)
	}
}

type diagnosticTestHandler struct {
	panicValue any
	rpcErr     *rpcerrors.RpcError
}

func (h diagnosticTestHandler) Handle(*types.RpcContext, json.RawMessage) (any, *rpcerrors.RpcError) {
	if h.panicValue != nil {
		panic(h.panicValue)
	}
	return nil, h.rpcErr
}

func (diagnosticTestHandler) RequiredRole() types.Role { return types.RoleGuest }
func (diagnosticTestHandler) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2}
}
func (diagnosticTestHandler) RequiredCondition() types.Condition { return types.NoCondition }

func TestDispatchDiagnosticsTreatsRPCResultsAsFinished(t *testing.T) {
	diagnostics := NewRPCDiagnostics()
	services := &types.ServiceContainer{RPCDiagnostics: diagnostics}
	graph := types.NewTestServiceGraph(services)
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   graph,
	}

	resolution := methodResolution{handler: diagnosticTestHandler{rpcErr: rpcerrors.RpcErrorInvalidParams("bad request")}, resolved: true}
	_, _ = dispatchResolvedMethod(graph, ctx, "normal_error", nil, resolution, rpcLog())
	stats := diagnostics.Snapshot().Methods["normal_error"]
	if stats.Finished != 1 || stats.Errored != 0 {
		t.Fatalf("ordinary RPC error stats = %#v", stats)
	}

	resolution = methodResolution{handler: diagnosticTestHandler{panicValue: "boom"}, resolved: true}
	_, _ = dispatchResolvedMethod(graph, ctx, "panic", nil, resolution, rpcLog())
	stats = diagnostics.Snapshot().Methods["panic"]
	if stats.Finished != 0 || stats.Errored != 1 {
		t.Fatalf("panic stats = %#v", stats)
	}
}
