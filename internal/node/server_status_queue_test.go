package node

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

func blockStatusPublication(t *testing.T, svc *service.Service) func() {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	require.True(t, svc.SignalServerStatusPublication(func() {
		close(entered)
		<-release
	}))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("publication worker did not reach barrier")
	}
	return unblock
}

type statusFeeReadProbe struct {
	types.LedgerService
	reads atomic.Int32
}

func (p *statusFeeReadProbe) GetCurrentFees() (uint64, uint64, uint64) {
	p.reads.Add(1)
	return p.LedgerService.GetCurrentFees()
}

func TestRuntimeFeeChangeDefersLedgerReads(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	probe := &statusFeeReadProbe{LedgerService: rpcadapter.NewLedgerServiceAdapter(svc)}
	runtime := &nodeRuntime{
		ledger:       svc,
		serviceGraph: types.NewTestServiceGraph(types.NewServiceContainer(probe)),
		publisher:    rpc.NewPublisher(subscription.NewManager()),
	}
	require.NoError(t, runtime.bindStreams())
	unblock := blockStatusPublication(t, svc)

	svc.FeeTrack().RaiseLocalFee()
	require.True(t, svc.FeeTrack().RaiseLocalFee())
	require.True(t, svc.FeeTrack().LowerLocalFee())
	require.Zero(t, probe.reads.Load(), "fee callbacks must not read ledger state synchronously")

	completed := make(chan struct{})
	require.True(t, svc.SignalServerStatusPublication(func() { close(completed) }))
	unblock()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("fee publication did not drain")
	}
	require.Positive(t, probe.reads.Load(), "the worker must sample ledger fees")
}

func TestQueuedFeeStatusPrecedesPendingModes(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	consensusAdaptor := adaptor.New(adaptor.Config{LedgerService: svc})
	svc.SetServerStateFunc(func() string { return consensusAdaptor.GetOperatingMode().String() })
	recorder := newServerStatusRecorder()
	status := newServerStatusPublisher(serverStatusTestServices(svc), recorder)
	svc.SetServerStatusCallback(status.publish)
	svc.FeeTrack().SetOnChange(func() { svc.SignalServerStatus() })
	consensusAdaptor.SetOnOperatingModeChange(func(mode consensus.OperatingMode) {
		svc.SignalServerStatusPublication(status.modePublication(mode.String()))
	})
	unblock := blockStatusPublication(t, svc)

	svc.FeeTrack().SetRemoteFee(512)
	consensusAdaptor.SetOperatingMode(consensus.OpModeSyncing)
	consensusAdaptor.SetOperatingMode(consensus.OpModeTracking)
	unblock()

	for _, mode := range []consensus.OperatingMode{
		consensus.OpModeDisconnected, consensus.OpModeSyncing, consensus.OpModeTracking,
	} {
		event := waitForServerStatus(t, recorder)
		require.Equal(t, mode.String(), event.ServerStatus)
		require.EqualValues(t, 512, event.LoadFactorServer)
	}
}

type serverStatusPublisherFunc func(*rpc.ServerStatusEvent) bool

func (f serverStatusPublisherFunc) PublishServerStatus(event *rpc.ServerStatusEvent) bool {
	return f(event)
}

func TestServerStatusRemembersDeliveredModeWithoutSubscribers(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	status := newServerStatusPublisher(serverStatusTestServices(svc), serverStatusPublisherFunc(func(*rpc.ServerStatusEvent) bool {
		return false
	}))
	status.modePublication(consensus.OpModeTracking.String())()

	recorder := newServerStatusRecorder()
	status.publisher = recorder
	status.publish(nil)
	require.Equal(t, consensus.OpModeTracking.String(), waitForServerStatus(t, recorder).ServerStatus)
}
