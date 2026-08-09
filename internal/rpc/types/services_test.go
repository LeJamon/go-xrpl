package types

import (
	"context"
	"reflect"
	"sync"
	"testing"
)

type serviceGraphLedger struct{ LedgerService }

type serviceGraphDiagnostics struct{}

type serviceGraphOptionalCapabilities struct {
	ManifestLookup
	ValidatorListReader
	AdvisoryDeleteStore
	AccountHistorySubscriptionService
}

type readOnlyLedgerCapability struct {
	LedgerSelectionReader
	LedgerDataReader
	TransactionQuerier
	AccountQuerier
	BookReader
	GatewayReader
	NFTReader
}

type ledgerMutationCapability struct {
	LedgerAcceptor
	TransactionSubmission
}

type readOnlyLedgerState struct{ LedgerStateReader }

var _ LedgerReadService = (*readOnlyLedgerCapability)(nil)
var _ LedgerMutationService = (*ledgerMutationCapability)(nil)
var _ LedgerStateReader = (*readOnlyLedgerState)(nil)

func (serviceGraphDiagnostics) Start(string) func(bool) { return func(bool) {} }
func (serviceGraphDiagnostics) Snapshot() RPCDiagnosticsSnapshot {
	return RPCDiagnosticsSnapshot{}
}

func completeServiceGraphBuilder() *ServiceGraphBuilder {
	return &ServiceGraphBuilder{ServiceContainer: ServiceContainer{
		Ledger:              &serviceGraphLedger{},
		Shutdown:            ShutdownFunc(func() {}),
		ClientLoad:          NewClientLoadShedder(),
		RPCDiagnostics:      serviceGraphDiagnostics{},
		ServerInfoConfig:    ServerInfoConfigSnapshot{Ports: []ServerInfoPortSnapshot{}},
		SubscriptionMetrics: func() SubscriptionMetrics { return SubscriptionMetrics{} },
	}}
}

func TestServiceGraphBuildValidatesProductionDependencies(t *testing.T) {
	if _, err := completeServiceGraphBuilder().Build(); err != nil {
		t.Fatalf("complete graph build: %v", err)
	}

	tests := []struct {
		name   string
		remove func(*ServiceGraphBuilder)
	}{
		{name: "ledger", remove: func(b *ServiceGraphBuilder) { b.Ledger = nil }},
		{name: "typed nil ledger", remove: func(b *ServiceGraphBuilder) { b.Ledger = (*serviceGraphLedger)(nil) }},
		{name: "shutdown", remove: func(b *ServiceGraphBuilder) { b.Shutdown = nil }},
		{name: "typed nil shutdown", remove: func(b *ServiceGraphBuilder) { b.Shutdown = ShutdownFunc(nil) }},
		{name: "client load", remove: func(b *ServiceGraphBuilder) { b.ClientLoad = nil }},
		{name: "diagnostics", remove: func(b *ServiceGraphBuilder) { b.RPCDiagnostics = nil }},
		{name: "typed nil diagnostics", remove: func(b *ServiceGraphBuilder) { b.RPCDiagnostics = (*serviceGraphDiagnostics)(nil) }},
		{name: "configuration", remove: func(b *ServiceGraphBuilder) { b.ServerInfoConfig.Ports = nil }},
		{name: "subscription metrics", remove: func(b *ServiceGraphBuilder) { b.SubscriptionMetrics = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			builder := completeServiceGraphBuilder()
			test.remove(builder)
			if _, err := builder.Build(); err == nil {
				t.Fatal("incomplete production graph unexpectedly built")
			}
		})
	}
}

func TestServiceGraphBuildCopiesTopologyAndSnapshots(t *testing.T) {
	firstLedger := &serviceGraphLedger{}
	secondLedger := &serviceGraphLedger{}
	builder := completeServiceGraphBuilder()
	builder.Ledger = firstLedger
	builder.ValidatorPublicKey = []byte{1, 2, 3}
	builder.ServerInfoConfig.Ports = []ServerInfoPortSnapshot{{Port: 5005, Protocol: "http"}}

	graph, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	builder.Ledger = secondLedger
	builder.ValidatorPublicKey[0] = 9
	builder.ServerInfoConfig.Ports[0].Port = 6006

	if graph.Ledger() != firstLedger {
		t.Fatal("builder mutation changed the published ledger capability")
	}
	key := graph.ValidatorPublicKey()
	config := graph.ServerInfoConfig()
	if !reflect.DeepEqual(key, []byte{1, 2, 3}) || config.Ports[0].Port != 5005 {
		t.Fatalf("published snapshots changed: key=%v config=%+v", key, config)
	}
	key[0] = 8
	config.Ports[0].Port = 7007
	if graph.ValidatorPublicKey()[0] != 1 || graph.ServerInfoConfig().Ports[0].Port != 5005 {
		t.Fatal("accessors exposed mutable snapshot storage")
	}
}

func TestServiceGraphBuildNormalizesTypedNilOptionalCapabilities(t *testing.T) {
	builder := completeServiceGraphBuilder()
	var optional *serviceGraphOptionalCapabilities
	builder.Manifests = optional
	builder.ValidatorList = optional
	builder.AdvisoryDeleteState = optional
	builder.AccountHistorySubscriptions = optional

	graph, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if graph.Manifests() != nil || graph.ValidatorList() != nil || graph.AdvisoryDeleteState() != nil || graph.AccountHistorySubscriptions() != nil {
		t.Fatal("typed-nil optional capability escaped the graph build")
	}
}

func TestServiceGraphPublishesSeparateLedgerCapabilities(t *testing.T) {
	graphType := reflect.TypeOf((*ServiceGraph)(nil))
	readMethod, ok := graphType.MethodByName("Ledger")
	if !ok {
		t.Fatal("ServiceGraph.Ledger is missing")
	}
	if readMethod.Type.Out(0) != reflect.TypeOf((*LedgerReadService)(nil)).Elem() {
		t.Fatalf("Ledger return type = %v", readMethod.Type.Out(0))
	}
	mutationMethod, ok := graphType.MethodByName("LedgerMutation")
	if !ok {
		t.Fatal("ServiceGraph.LedgerMutation is missing")
	}
	if mutationMethod.Type.Out(0) != reflect.TypeOf((*LedgerMutationService)(nil)).Elem() {
		t.Fatalf("LedgerMutation return type = %v", mutationMethod.Type.Out(0))
	}
}

func TestClientLoadShedderReleaseOwnership(t *testing.T) {
	var shedder ClientLoadShedder
	end := shedder.Begin()
	var dispatchReleases sync.WaitGroup
	for range 8 {
		dispatchReleases.Add(1)
		go func() {
			defer dispatchReleases.Done()
			end()
		}()
	}
	dispatchReleases.Wait()
	if got := shedder.InFlight(); got != 0 {
		t.Fatalf("in-flight after repeated release = %d, want 0", got)
	}

	first, ok := shedder.AcquirePathfind()
	if !ok {
		t.Fatal("first bounded acquire failed")
	}
	second, ok := shedder.AcquirePathfind()
	if !ok {
		t.Fatal("second bounded acquire failed")
	}
	if _, ok := shedder.AcquirePathfind(); ok {
		t.Fatal("third bounded acquire exceeded the cap")
	}

	var releases sync.WaitGroup
	for range 8 {
		releases.Add(1)
		go func() {
			defer releases.Done()
			first()
		}()
	}
	releases.Wait()
	if got := shedder.PathfindActive(); got != 1 {
		t.Fatalf("active after repeated release = %d, want 1", got)
	}

	third, ok := shedder.AcquirePathfind()
	if !ok {
		t.Fatal("release should make a bounded slot available")
	}
	second()
	third()
	third()
	if got := shedder.PathfindActive(); got != 0 {
		t.Fatalf("active after all releases = %d, want 0", got)
	}

	unlimited := shedder.AcquirePathfindUnlimited()
	bounded, ok := shedder.AcquirePathfind()
	if !ok {
		t.Fatal("unlimited acquisition should not poison bounded ownership")
	}
	bounded()
	unlimited()
	if got := shedder.PathfindActive(); got != 0 {
		t.Fatalf("active after mixed release = %d, want 0", got)
	}
}

func TestClientLoadShedderWaitCancellationAndZeroValues(t *testing.T) {
	var shedder ClientLoadShedder
	first := shedder.AcquirePathfindUnlimited()
	second := shedder.AcquirePathfindUnlimited()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, ok := shedder.WaitPathfind(ctx); ok || release != nil {
		t.Fatalf("canceled wait = (%T, %v), want (nil, false)", release, ok)
	}
	first()
	second()

	if release, ok := shedder.WaitPathfind(nil); !ok || release == nil {
		t.Fatalf("nil-context wait = (%T, %v), want successful owned release", release, ok)
	} else {
		release()
	}
	if got := shedder.PathfindActive(); got != 0 {
		t.Fatalf("zero-value shedder leaked pathfind slot: %d", got)
	}

	var nilShedder *ClientLoadShedder
	nilShedder.Begin()()
	if release, ok := nilShedder.AcquirePathfind(); !ok || release == nil {
		t.Fatalf("nil bounded acquire = (%T, %v), want no-op success", release, ok)
	} else {
		release()
	}
	if release := nilShedder.AcquirePathfindUnlimited(); release == nil {
		t.Fatal("nil unlimited acquire returned nil release")
	} else {
		release()
	}
}
