package node

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type serverStatusRecorder struct {
	mu     sync.Mutex
	events []*rpc.ServerStatusEvent
	ready  chan *rpc.ServerStatusEvent
}

func newServerStatusRecorder() *serverStatusRecorder {
	return &serverStatusRecorder{ready: make(chan *rpc.ServerStatusEvent, 8)}
}

func (r *serverStatusRecorder) PublishServerStatus(event *rpc.ServerStatusEvent) bool {
	copy := *event
	r.mu.Lock()
	r.events = append(r.events, &copy)
	r.mu.Unlock()
	r.ready <- &copy
	return true
}

func (r *serverStatusRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func serverStatusTestServices(svc *service.Service) *types.ServiceContainer {
	services := types.NewServiceContainer(rpcadapter.NewLedgerServiceAdapter(svc))
	services.TxQMetrics = func() types.TxQServerMetrics {
		metrics := svc.TxQMetrics()
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     metrics.ReferenceFeeLevel,
			MinProcessingFeeLevel: metrics.MinProcessingFeeLevel,
			OpenLedgerFeeLevel:    metrics.OpenLedgerFeeLevel,
		}
	}
	services.LoadFactorFees = func() types.LoadFactorFees {
		fees := svc.FeeTrack()
		return types.LoadFactorFees{
			Local:   fees.LocalFee(),
			Net:     fees.RemoteFee(),
			Cluster: fees.ClusterFee(),
		}
	}
	return services
}

func waitForServerStatus(t *testing.T, recorder *serverStatusRecorder) *rpc.ServerStatusEvent {
	t.Helper()
	select {
	case event := <-recorder.ready:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server status")
		return nil
	}
}

func TestServerStatusPublishesModeAndLoadWithoutLedgerAcceptance(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true})
	if err != nil {
		t.Fatalf("New service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start service: %v", err)
	}
	t.Cleanup(svc.Stop)

	consensusAdaptor := adaptor.New(adaptor.Config{LedgerService: svc})
	svc.SetServerStateFunc(func() string { return consensusAdaptor.GetOperatingMode().String() })
	recorder := newServerStatusRecorder()
	status := newServerStatusPublisher(serverStatusTestServices(svc), recorder)
	svc.SetServerStatusCallback(status.publish)
	consensusAdaptor.SetOnOperatingModeChange(func(mode consensus.OperatingMode) {
		svc.SignalServerStatusPublication(status.modePublication(mode.String()))
	})
	svc.FeeTrack().SetOnChange(func() {
		svc.SignalServerStatusPublication(status.statusPublication(nil))
	})

	status.publish(nil)
	initial := waitForServerStatus(t, recorder)
	if initial.ServerStatus != consensus.OpModeDisconnected.String() {
		t.Fatalf("initial server status = %q", initial.ServerStatus)
	}

	consensusAdaptor.SetOperatingMode(consensus.OpModeConnected)
	modeEvent := waitForServerStatus(t, recorder)
	if modeEvent.ServerStatus != consensus.OpModeConnected.String() {
		t.Fatalf("mode-change server status = %q", modeEvent.ServerStatus)
	}
	svc.FeeTrack().SetRemoteFee(512)
	consensusAdaptor.SetOperatingMode(consensus.OpModeSyncing)
	consensusAdaptor.SetOperatingMode(consensus.OpModeTracking)
	loadEvent := waitForServerStatus(t, recorder)
	if loadEvent.ServerStatus != consensus.OpModeConnected.String() ||
		loadEvent.LoadFactorServer != 512 || loadEvent.LoadFactor != 512 {
		t.Fatalf("load event = status %q, server %d, overall %d",
			loadEvent.ServerStatus, loadEvent.LoadFactorServer, loadEvent.LoadFactor)
	}
	if event := waitForServerStatus(t, recorder); event.ServerStatus != consensus.OpModeSyncing.String() {
		t.Fatalf("first rapid mode status = %q", event.ServerStatus)
	}
	if event := waitForServerStatus(t, recorder); event.ServerStatus != consensus.OpModeTracking.String() {
		t.Fatalf("second rapid mode status = %q", event.ServerStatus)
	}
}

func TestServerStatusPublisherSuppressesUnchangedSnapshot(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true})
	if err != nil {
		t.Fatalf("New service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start service: %v", err)
	}
	t.Cleanup(svc.Stop)

	recorder := newServerStatusRecorder()
	status := newServerStatusPublisher(serverStatusTestServices(svc), recorder)
	status.publish(nil)
	waitForServerStatus(t, recorder)
	status.publish(nil)
	if got := recorder.count(); got != 1 {
		t.Fatalf("unchanged snapshot published %d events, want 1", got)
	}
}

func TestServerStatusPublisherDoesNotCacheWithoutSubscribers(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true})
	if err != nil {
		t.Fatalf("New service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start service: %v", err)
	}
	t.Cleanup(svc.Stop)

	manager := subscription.NewManager()
	status := newServerStatusPublisher(serverStatusTestServices(svc), rpc.NewPublisher(manager))
	status.publish(nil)

	conn := subscription.NewConnection("server-subscriber", make(chan []byte, 1))
	registration, attached := manager.Attach(conn)
	if !attached {
		t.Fatal("attach server subscriber")
	}
	t.Cleanup(func() { manager.Detach(registration) })
	if rpcErr := manager.HandleSubscribe(registration, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubServer}}, true); rpcErr != nil {
		t.Fatalf("subscribe server stream: %v", rpcErr)
	}
	status.publish(nil)
	select {
	case <-conn.Outbound():
	case <-time.After(time.Second):
		t.Fatal("first status after subscribing was suppressed")
	}
}

func TestServerStatusPublicationUsesTransitionSnapshot(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true})
	if err != nil {
		t.Fatalf("New service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start service: %v", err)
	}
	t.Cleanup(svc.Stop)

	recorder := newServerStatusRecorder()
	status := newServerStatusPublisher(serverStatusTestServices(svc), recorder)
	publication := status.modePublication(consensus.OpModeTracking.String())
	svc.FeeTrack().SetRemoteFee(512)
	publication()

	event := waitForServerStatus(t, recorder)
	if event.ServerStatus != consensus.OpModeTracking.String() || event.LoadFactorServer != 256 {
		t.Fatalf("mode transition snapshot = status %q, load %d", event.ServerStatus, event.LoadFactorServer)
	}
}

func TestServerStatusPublisherClipsSaturatedTxQLoad(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true})
	if err != nil {
		t.Fatalf("New service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start service: %v", err)
	}
	t.Cleanup(svc.Stop)
	services := serverStatusTestServices(svc)
	services.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: math.MaxUint64,
			OpenLedgerFeeLevel:    math.MaxUint64,
		}
	}
	recorder := newServerStatusRecorder()
	status := newServerStatusPublisher(services, recorder)
	status.publish(nil)

	event := waitForServerStatus(t, recorder)
	if event.LoadFactor != math.MaxUint32 ||
		event.LoadFactorFeeEscalation != math.MaxUint32 ||
		event.LoadFactorFeeQueue != math.MaxUint32 {
		t.Fatalf("saturated load event = %+v", event)
	}
}
