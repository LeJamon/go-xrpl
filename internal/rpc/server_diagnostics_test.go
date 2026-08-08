package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type staticRPCDiagnostics struct {
	snapshot types.RPCDiagnosticsSnapshot
}

func (d staticRPCDiagnostics) Start(string) func(bool) { return func(bool) {} }
func (d staticRPCDiagnostics) Snapshot() types.RPCDiagnosticsSnapshot {
	return d.snapshot
}

func TestServerDiagnosticsWireShape(t *testing.T) {
	services := servicesForServerInfo(newMockLedgerServiceServerInfo())
	services.RPCDiagnostics = staticRPCDiagnostics{snapshot: types.RPCDiagnosticsSnapshot{
		Methods: map[string]types.RPCMethodDiagnostics{
			"account_info": {Started: 3, Finished: 2, Errored: 1, DurationUs: 4500},
		},
		Current: []types.RPCActivity{{Method: "server_info", DurationUs: 25}},
	}}
	services.GetCounts = func() types.CountsResult {
		return types.CountsResult{NodeStore: &types.NodeStoreCounts{
			Reads: 11, FetchHits: 7, Writes: 5, ReadBytes: 101, WriteBytes: 202,
		}}
	}
	services.SubscriptionMetrics = func() types.SubscriptionMetrics {
		return types.SubscriptionMetrics{
			Connections: 2, Items: 7, RequestLimitRejections: 3,
			ConnectionLimitRejections: 4, GlobalLimitRejections: 5,
			DeliveriesQueued: 11, DeliveriesDropped: 6, DeliveryDisconnects: 1,
		}
	}

	for _, test := range []struct {
		name   string
		method types.MethodHandler
		root   string
	}{
		{name: "server_info", method: &handlers.ServerInfoMethod{}, root: "info"},
		{name: "server_state", method: &handlers.ServerStateMethod{}, root: "state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleAdmin, IsAdmin: true, ApiVersion: 1, Services: services}
			result, rpcErr := test.method.Handle(ctx, json.RawMessage(`{"counters":"yes"}`))
			if rpcErr != nil {
				t.Fatalf("Handle: %v", rpcErr)
			}
			body := result.(map[string]any)[test.root].(map[string]any)
			counters := body["counters"].(map[string]any)
			rpcCounters := counters["rpc"].(map[string]any)
			method := rpcCounters["account_info"].(map[string]any)
			if method["started"] != "3" || method["finished"] != "2" || method["errored"] != "1" || method["duration_us"] != "4500" {
				t.Fatalf("method counters = %#v", method)
			}
			if total := rpcCounters["total"].(map[string]any); total["started"] != "3" {
				t.Fatalf("total = %#v", total)
			}
			if len(counters["job_queue"].(map[string]any)) != 0 {
				t.Fatalf("job_queue = %#v", counters["job_queue"])
			}
			nodeStore := counters["nodestore"].(map[string]any)
			if nodeStore["node_reads_total"] != "11" || nodeStore["node_reads_hit"] != "7" || nodeStore["node_writes"] != "5" || nodeStore["node_read_bytes"] != "101" || nodeStore["node_written_bytes"] != "202" {
				t.Fatalf("nodestore = %#v", nodeStore)
			}
			subscriptions := counters["subscriptions"].(map[string]any)
			if subscriptions["connections"] != "2" || subscriptions["items"] != "7" ||
				subscriptions["request_limit_rejections"] != "3" || subscriptions["connection_limit_rejections"] != "4" ||
				subscriptions["global_limit_rejections"] != "5" || subscriptions["deliveries_queued"] != "11" ||
				subscriptions["deliveries_dropped"] != "6" || subscriptions["delivery_disconnects"] != "1" {
				t.Fatalf("subscriptions = %#v", subscriptions)
			}
			activities := body["current_activities"].(map[string]any)
			if len(activities["jobs"].([]map[string]any)) != 0 {
				t.Fatalf("jobs = %#v", activities["jobs"])
			}
			methods := activities["methods"].([]map[string]any)
			if len(methods) != 1 || methods[0]["method"] != "server_info" || methods[0]["duration_us"] != "25" {
				t.Fatalf("methods = %#v", methods)
			}
			if _, ok := body["load"]; ok {
				t.Fatal("fabricated admin JobQueue load")
			}
		})
	}
}

func TestServerDiagnosticsJsonCppCountersTruthiness(t *testing.T) {
	services := servicesForServerInfo(newMockLedgerServiceServerInfo())
	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleGuest, ApiVersion: 1, Services: services}
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: `false`, want: false},
		{value: `0`, want: false},
		{value: `""`, want: false},
		{value: `[]`, want: false},
		{value: `{}`, want: false},
		{value: `true`, want: true},
		{value: `-1`, want: true},
		{value: `"counters"`, want: true},
		{value: `[0]`, want: true},
		{value: `{"enabled":false}`, want: true},
	} {
		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"counters":`+test.value+`}`))
		if rpcErr != nil {
			t.Fatalf("value %s: %v", test.value, rpcErr)
		}
		_, got := result.(map[string]any)["info"].(map[string]any)["counters"]
		if got != test.want {
			t.Fatalf("value %s: counters present=%v, want %v", test.value, got, test.want)
		}
	}
}

func TestHTTPServerDiagnosticsRejectsRawPositionalParameter(t *testing.T) {
	for _, test := range []struct {
		method string
	}{
		{method: "server_info"},
		{method: "server_state"},
	} {
		t.Run(test.method, func(t *testing.T) {
			services := servicesForServerInfo(newMockLedgerServiceServerInfo())
			server := NewServer(ServerOptions{Timeout: time.Second, Services: services})
			body := `{"method":"` + test.method + `","params":["counters"]}`
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			if got := recorder.Body.String(); got != "params unparseable\r\n" {
				t.Fatalf("body = %q", got)
			}
		})
	}
}
