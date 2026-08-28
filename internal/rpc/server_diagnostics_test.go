package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	services := types.NewServiceContainer(newMockLedgerServiceServerInfo())
	services.NodePublicKey = testNodePublicKey()
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
	services.FastSyncMetrics = func() types.FastSyncMetrics {
		return types.FastSyncMetrics{
			CompletionRecheckAccepted:            8,
			CompletionRecheckRejectedNoEvidence:  3,
			CompletionRecheckRejectedBelowQuorum: 2,
			CompletionRecheckRejectedUnavailable: 1,
			TargetSuperseded:                     13,
			ObsoleteAcquisitionCompleted:         5,
			ReplayPipelineRequested:              21,
			ReplayPipelineReady:                  22,
			ReplayPipelineApplied:                23,
			ReplayPipelineDiscarded:              24,
			ReplayPipelineRetried:                25,
			ReplayPipelineFallbacks:              26,
			ReplayPipelineAcquireUs:              27,
			ReplayPipelineReadyWaitUs:            28,
			ReplayPipelineApplyUs:                29,
			ReplayPipelinePersistUs:              30,
			ReplayPipelineWindow:                 31,
			ReplayPipelineDepth:                  32,
			ReplayPipelineReadyDepth:             33,
			ReplayPipelineHeadSeq:                34,
			ReplayPipelineTargetSeq:              35,
			ReplayPipelineHeadBlockedUs:          36,
			ReplayPipelineCapacityRetargets:      37,
			ReplayPipelineRetargetFailures:       38,
			ReplayPipelinePreparedLimit:          39,
			ReplayPipelinePivotSeq:               40,
			ReplayPipelinePreparedTailSeq:        41,
			ReplayPipelineTrustedHeadSeq:         42,
			ReplayPipelineGeneration:             43,
			ReplayPipelinePivotStateNodesPerSec:  44,
			ReplayPipelineBackpressureEvents:     45,
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
			ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleAdmin, ApiVersion: 1, Services: types.NewTestServiceGraph(services)}
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
			fastSync := counters["fast_sync"].(map[string]any)
			wantFastSync := map[string]any{
				"completion_recheck_accepted":                    "8",
				"completion_recheck_rejected_no_evidence":        "3",
				"completion_recheck_rejected_below_quorum":       "2",
				"completion_recheck_rejected_quorum_unavailable": "1",
				"target_superseded":                              "13",
				"obsolete_acquisition_completed":                 "5",
				"replay_pipeline_requested":                      "21",
				"replay_pipeline_ready":                          "22",
				"replay_pipeline_applied":                        "23",
				"replay_pipeline_discarded":                      "24",
				"replay_pipeline_retried":                        "25",
				"replay_pipeline_fallbacks":                      "26",
				"replay_pipeline_acquire_us":                     "27",
				"replay_pipeline_ready_wait_us":                  "28",
				"replay_pipeline_apply_us":                       "29",
				"replay_pipeline_persist_us":                     "30",
				"replay_pipeline_window":                         "31",
				"replay_pipeline_depth":                          "32",
				"replay_pipeline_ready_depth":                    "33",
				"replay_pipeline_head_seq":                       "34",
				"replay_pipeline_target_seq":                     "35",
				"replay_pipeline_head_blocked_us":                "36",
				"replay_pipeline_capacity_retargets":             "37",
				"replay_pipeline_retarget_failures":              "38",
				"replay_pipeline_prepared_limit":                 "39",
				"replay_pipeline_pivot_seq":                      "40",
				"replay_pipeline_prepared_tail_seq":              "41",
				"replay_pipeline_trusted_head_seq":               "42",
				"replay_pipeline_generation":                     "43",
				"replay_pipeline_pivot_state_nodes_per_sec":      "44",
				"replay_pipeline_backpressure_events":            "45",
			}
			if !reflect.DeepEqual(fastSync, wantFastSync) {
				t.Fatalf("fast_sync = %#v, want %#v", fastSync, wantFastSync)
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
