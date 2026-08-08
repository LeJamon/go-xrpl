package node

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/require"
)

func TestBindRPCWiresExplicitSharedServices(t *testing.T) {
	runtime := &nodeRuntime{
		appConfig:  &config.Config{},
		services:   types.NewServiceContainer(nil),
		serverLog:  xrpllog.Discard(),
		shutdownCh: make(chan struct{}, 1),
	}
	runtime.services.ClientLoad = types.NewClientLoadShedder()
	if err := runtime.bindRPC(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.wsServer.Close(context.Background()) }()

	if runtime.services.Dispatcher != runtime.httpServer {
		t.Fatal("bindRPC did not install the HTTP dispatcher")
	}
	if runtime.services.URLSubscriptions != runtime.wsServer.URLSubscriptionService() {
		t.Fatal("bindRPC did not install the WebSocket URL subscription service")
	}
	if runtime.services.ClientLoad == nil {
		t.Fatal("bindRPC lost the configured client-load shedder")
	}
	if runtime.resourceManager == nil || !runtime.ownsResourceManager {
		t.Fatal("standalone RPC binding did not own a resource manager")
	}
	consumer := runtime.resourceManager.NewInboundEndpoint("192.0.2.25")
	consumer.Charge(resource.NewCharge(resource.WarningThreshold*resource.DecayWindowSeconds, "test"), "")
	defer consumer.Release()
	if got := runtime.services.ResourceBlacklist(nil); got["192.0.2.25"] == nil {
		t.Fatalf("standalone resource blacklist = %+v", got)
	}
}

func TestRunReturnsCanceledContextBeforeStartup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("startup canceled")
	cancel(cause)

	err := Run(ctx, nil, "", false, service.StartupConfig{}, xrpllog.Discard(), xrpllog.Discard())
	if !errors.Is(err, cause) {
		t.Fatalf("Run() error = %v, want %v", err, cause)
	}
}

func TestWaitForShutdownReturnsContextCause(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("auxiliary listener failed")
	result := make(chan error, 1)
	go func() {
		result <- waitForShutdown(
			ctx,
			xrpllog.Discard(),
			make(chan os.Signal),
			make(chan struct{}),
			make(chan error),
			nil,
			nil,
			nil,
			"",
		)
	}()

	cancel(cause)
	select {
	case err := <-result:
		if !errors.Is(err, cause) {
			t.Fatalf("waitForShutdown() error = %v, want %v", err, cause)
		}
	case <-time.After(time.Second):
		t.Fatal("waitForShutdown() did not return after context cancellation")
	}
}

func TestWaitForShutdownReturnsComponentError(t *testing.T) {
	t.Parallel()

	componentErr := errors.New("consensus router stopped")
	componentErrCh := make(chan error, 1)
	componentErrCh <- componentErr

	err := waitForShutdown(
		context.Background(),
		xrpllog.Discard(),
		make(chan os.Signal),
		make(chan struct{}),
		make(chan error),
		componentErrCh,
		nil,
		nil,
		"",
	)
	if !errors.Is(err, componentErr) {
		t.Fatalf("waitForShutdown() error = %v, want %v", err, componentErr)
	}
}

func TestWaitForShutdownReturnsLedgerServiceError(t *testing.T) {
	t.Parallel()

	serviceErr := errors.New("publication queue exceeded capacity")
	serviceErrCh := make(chan error, 1)
	serviceErrCh <- serviceErr

	err := waitForShutdown(
		context.Background(),
		xrpllog.Discard(),
		make(chan os.Signal),
		make(chan struct{}),
		make(chan error),
		nil,
		serviceErrCh,
		nil,
		"",
	)
	if !errors.Is(err, serviceErr) {
		t.Fatalf("waitForShutdown() error = %v, want %v", err, serviceErr)
	}
}

func TestValidatorReloadLoopCancellationDoesNotWaitForLoader(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan os.Signal, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	sink := &recordingSink{}
	go func() {
		defer close(done)
		runValidatorReloadLoop(
			ctx,
			xrpllog.Discard(),
			sink,
			"blocked.toml",
			requests,
			func(config.Paths) (*config.Config, error) {
				close(started)
				<-release
				return nil, errors.New("released")
			},
		)
	}()
	requests <- os.Interrupt
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reload loop did not stop after cancellation")
	}
	close(release)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.calls) != 0 {
		t.Fatal("canceled reload mutated the trusted validator set")
	}
}

func TestValidatorReloadLoaderDoesNotAccumulateAfterTimeout(t *testing.T) {
	t.Parallel()

	loadGate := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	load := func(config.Paths) (*config.Config, error) {
		calls.Add(1)
		<-release
		return nil, errors.New("released")
	}
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		applyValidatorReloadContextWithGate(
			ctx,
			xrpllog.Discard(),
			&recordingSink{},
			"blocked.toml",
			load,
			loadGate,
		)
		cancel()
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("blocked loader goroutines = %d, want 1", got)
	}
	close(release)
}

func TestValidatorReloadLoopSerializesAndCoalescesRequests(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan os.Signal, 1)
	started := make(chan int, 2)
	releaseFirst := make(chan struct{})
	done := make(chan struct{})
	var calls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	go func() {
		defer close(done)
		runValidatorReloadLoop(
			ctx,
			xrpllog.Discard(),
			&recordingSink{},
			"reload.toml",
			requests,
			func(config.Paths) (*config.Config, error) {
				call := int(calls.Add(1))
				current := active.Add(1)
				for {
					previous := maxActive.Load()
					if current <= previous || maxActive.CompareAndSwap(previous, current) {
						break
					}
				}
				started <- call
				if call == 1 {
					<-releaseFirst
				}
				active.Add(-1)
				return nil, errors.New("test reload")
			},
		)
	}()

	requests <- os.Interrupt
	if call := <-started; call != 1 {
		t.Fatalf("first reload call = %d", call)
	}
	for range 100 {
		select {
		case requests <- os.Interrupt:
		default:
		}
	}
	close(releaseFirst)
	select {
	case call := <-started:
		if call != 2 {
			t.Fatalf("pending reload call = %d, want 2", call)
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced pending reload was not processed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reload loop did not stop")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("reload calls = %d, want 2", got)
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("concurrent reloads = %d, want 1", got)
	}
}

type shutdownProbeDB struct {
	nodestore.Database
	close func() error
}

func (d *shutdownProbeDB) Close() error { return d.close() }

type shutdownProbeRepository struct {
	relationaldb.RepositoryManager
	close func() error
}

func (r *shutdownProbeRepository) Close() error { return r.close() }

func TestNodeRuntimeShutdownReportsCloseErrorsInDependencyOrder(t *testing.T) {
	t.Parallel()

	dbErr := errors.New("node store close failed")
	repoErr := errors.New("repository close failed")
	var mu sync.Mutex
	var order []string
	db := &shutdownProbeDB{close: func() error {
		mu.Lock()
		order = append(order, "node-store")
		mu.Unlock()
		return dbErr
	}}
	repo := &shutdownProbeRepository{close: func() error {
		mu.Lock()
		order = append(order, "repository")
		mu.Unlock()
		return repoErr
	}}

	runtime := &nodeRuntime{nodeStore: db, repo: repo, serverLog: xrpllog.Discard()}
	err := runtime.shutdownWithin(time.Second)
	if !errors.Is(err, dbErr) || !errors.Is(err, repoErr) {
		t.Fatalf("shutdown error = %v, want both close errors", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "node-store" || order[1] != "repository" {
		t.Fatalf("close order = %v, want node-store then repository", order)
	}
}

func TestNodeRuntimeShutdownBoundsBlockingStoreClose(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	repo := &shutdownProbeRepository{close: func() error {
		close(started)
		<-release
		return nil
	}}
	result := make(chan error, 1)
	go func() {
		runtime := &nodeRuntime{repo: repo, serverLog: xrpllog.Discard()}
		result <- runtime.shutdownWithin(50 * time.Millisecond)
	}()
	<-started
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown exceeded its total budget")
	}
	close(release)
}

func TestNodeRuntimeShutdownLeavesStoresOpenWhileTransportHandlerIsRunning(t *testing.T) {
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	defer cancelRuntime()
	started := make(chan struct{})
	release := make(chan struct{})
	bound, err := bindRPCTransports(
		runtimeCtx,
		xrpllog.Discard(),
		&config.Config{Ports: map[string]config.PortConfig{
			"http": {IP: "127.0.0.1", Port: 0, Protocol: "http"},
		}},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(started)
			<-release
		}),
		rpc.NewWebSocketServer(rpc.WebSocketServerOptions{Timeout: time.Second}),
		nil,
		systemListen,
	)
	if err != nil {
		t.Fatalf("bind transports: %v", err)
	}
	if err := bound.serve(xrpllog.Discard()); err != nil {
		t.Fatalf("serve transports: %v", err)
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, _ := http.Get("http://" + bound.http[0].address + "/")
		if response != nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not start")
	}

	var storeClosed atomic.Bool
	db := &shutdownProbeDB{close: func() error {
		storeClosed.Store(true)
		return nil
	}}
	startedAt := time.Now()
	runtime := &nodeRuntime{transports: bound, nodeStore: db, serverLog: xrpllog.Discard()}
	err = runtime.shutdownWithin(50 * time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want deadline exceeded", err)
	}
	if time.Since(startedAt) >= time.Second {
		t.Fatal("shutdown exceeded its total budget")
	}
	if storeClosed.Load() {
		t.Fatal("node store closed while a transport handler was still running")
	}

	close(release)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("transport handler did not exit after release")
	}
}

func TestNodeRuntimeStopOrder(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	var order []string
	runtime := &nodeRuntime{
		ctx:    ctx,
		cancel: cancel,
		stopWatchdog: func() {
			order = append(order, "watchdog")
		},
		stopSampler: func() {
			order = append(order, "sampler")
		},
		options: RunOptions{Stopping: func() {
			if context.Cause(ctx) != context.Canceled {
				t.Fatalf("runtime context cause = %v, want canceled", context.Cause(ctx))
			}
			order = append(order, "stopping")
		}},
	}

	runtime.stopRuntime()
	if want := []string{"watchdog", "sampler", "stopping"}; !slices.Equal(order, want) {
		t.Fatalf("stop order = %v, want %v", order, want)
	}
}

func TestNodeRuntimeStopsPeerConnectSchedulerBeforeConsensus(t *testing.T) {
	var mu sync.Mutex
	var order []string
	started := make(chan struct{})
	appendOrder := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}
	scheduler := newPeerConnectScheduler(context.Background(), func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		appendOrder("peer-connect")
		return ctx.Err()
	}, 1, 1, nil)
	if err := scheduler.Enqueue("blocked"); err != nil {
		t.Fatalf("enqueue blocked peer = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("peer connect worker did not start")
	}
	runtime := &nodeRuntime{
		peerConnectScheduler: scheduler,
		consensus: &adaptor.Components{Engine: &lifecycleShutdownEngine{onStop: func() error {
			appendOrder("consensus")
			return nil
		}}},
		serverLog: xrpllog.Discard(),
	}
	if err := runtime.shutdownWithin(time.Second); err != nil {
		t.Fatalf("shutdown error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "peer-connect" || order[1] != "consensus" {
		t.Fatalf("shutdown order = %v, want peer-connect then consensus", order)
	}
}

func TestNodeRuntimeDoesNotStopConsensusWhilePeerConnectIsRunning(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler := newPeerConnectScheduler(context.Background(), func(context.Context, string) error {
		close(started)
		<-release
		return nil
	}, 1, 1, nil)
	require.NoError(t, scheduler.Enqueue("blocked"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("peer connect worker did not start")
	}
	var consensusStopped atomic.Bool
	runtime := &nodeRuntime{
		peerConnectScheduler: scheduler,
		consensus: &adaptor.Components{Engine: &lifecycleShutdownEngine{onStop: func() error {
			consensusStopped.Store(true)
			return nil
		}}},
		serverLog: xrpllog.Discard(),
	}
	err := runtime.shutdownWithin(25 * time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want deadline exceeded", err)
	}
	if consensusStopped.Load() {
		t.Fatal("consensus stopped while peer connect worker was still running")
	}
	close(release)
	scheduler.Close()
	require.ErrorIs(t, scheduler.Enqueue("after-close"), types.ErrPeerConnectClosed)
}

type lifecycleShutdownEngine struct {
	consensus.Engine
	onStop func() error
}

func (e *lifecycleShutdownEngine) Stop() error { return e.onStop() }
