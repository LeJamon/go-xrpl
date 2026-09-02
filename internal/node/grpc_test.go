package node

import (
	"context"
	"net"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/drops"
	xrplgrpc "github.com/LeJamon/go-xrpl/internal/grpc"
	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
)

// stubLookup is a minimal xrplgrpc.LedgerLookup serving one validated
// ledger, used to exercise the gRPC listener wiring end-to-end.
type stubLookup struct {
	validated *ledger.Ledger
}

func (s *stubLookup) GetLedgerByHash([32]byte) (*ledger.Ledger, error)   { return s.validated, nil }
func (s *stubLookup) GetLedgerBySequence(uint32) (*ledger.Ledger, error) { return s.validated, nil }
func (s *stubLookup) GetClosedLedger() *ledger.Ledger                    { return s.validated }
func (s *stubLookup) GetValidatedLedger() *ledger.Ledger                 { return s.validated }
func (s *stubLookup) GetOpenLedger() *ledger.Ledger                      { return s.validated }
func (s *stubLookup) GetValidatedLedgerAge() time.Duration               { return 0 }
func (s *stubLookup) IsStandalone() bool                                 { return true }
func (s *stubLookup) GetLedgerEntry(context.Context, [32]byte, string) (*service.LedgerEntryResult, error) {
	return nil, svcerr.ErrLedgerEntryNotFound
}

var _ xrplgrpc.LedgerLookup = (*stubLookup)(nil)

func newStubLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	stateMap := shamap.New(shamap.TypeState)
	key := [32]byte{0x01}
	if err := stateMap.Put(key, []byte("ledger-object-payload")); err != nil {
		t.Fatalf("state Put: %v", err)
	}
	txMap := shamap.New(shamap.TypeTransaction)
	hdr := header.LedgerHeader{
		LedgerIndex:         123,
		Drops:               100_000_000_000_000,
		CloseTime:           time.Unix(1_700_000_000, 0).UTC(),
		ParentCloseTime:     time.Unix(1_699_999_990, 0).UTC(),
		CloseTimeResolution: 10,
		Validated:           true,
		Accepted:            true,
	}
	hdr.Hash = [32]byte{0x7B, 0xAB}
	l, err := ledger.FromGenesis(hdr, stateMap, txMap, drops.Fees{})
	if err != nil {
		t.Fatalf("ledger.FromGenesis: %v", err)
	}
	return l
}

// TestGRPCServer_RoundTrip boots the gRPC listener on an ephemeral port
// and round-trips GetLedger and GetLedgerData over a real grpc.NewClient
// connection, exercising the listener wiring against the live
// XRPLedgerAPIService.
func TestGRPCServer_RoundTrip(t *testing.T) {
	lookup := &stubLookup{validated: newStubLedger(t)}
	manager := resource.NewManager(nil, nil)
	p := config.PortConfig{Port: 0, IP: "127.0.0.1", Protocol: "grpc", Limit: 1}
	errCh := make(chan error, 1)

	bound, err := prepareGRPCServer(
		context.Background(), "port_grpc", p, lookup, manager, xrpllog.Discard(), systemListen,
	)
	if err != nil {
		t.Fatalf("prepareGRPCServer: %v", err)
	}
	bound.serve(xrpllog.Discard(), errCh, nil)
	defer bound.server.GracefulStop()

	conn, err := googlegrpc.NewClient(bound.address, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := rpcv1.NewXRPLedgerAPIServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ledgerResp, err := client.GetLedger(ctx, &rpcv1.GetLedgerRequest{
		Ledger: &rpcv1.LedgerSpecifier{
			Ledger: &rpcv1.LedgerSpecifier_Shortcut_{Shortcut: rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED},
		},
	})
	if err != nil {
		t.Fatalf("GetLedger RPC: %v", err)
	}
	if !ledgerResp.Validated {
		t.Errorf("expected validated ledger")
	}
	if len(ledgerResp.LedgerHeader) != header.SizeWithHash {
		t.Errorf("ledger_header size=%d, want %d", len(ledgerResp.LedgerHeader), header.SizeWithHash)
	}

	dataResp, err := client.GetLedgerData(ctx, &rpcv1.GetLedgerDataRequest{
		Ledger: &rpcv1.LedgerSpecifier{
			Ledger: &rpcv1.LedgerSpecifier_Shortcut_{Shortcut: rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED},
		},
	})
	if err != nil {
		t.Fatalf("GetLedgerData RPC: %v", err)
	}
	if dataResp.LedgerIndex != 123 {
		t.Errorf("ledger_index=%d, want 123", dataResp.LedgerIndex)
	}
	if got := len(dataResp.LedgerObjects.Objects); got != 1 {
		t.Errorf("expected 1 ledger object, got %d", got)
	}
	if bound.requestLimit != 1 || bound.resourceManager != manager {
		t.Fatal("gRPC admission configuration was not wired into the bound server")
	}
	consumer := manager.NewInboundEndpoint("127.0.0.1")
	if consumer == nil {
		t.Fatal("round trip did not create a resource consumer")
	}
	if balance := consumer.Balance(); balance == 0 {
		t.Fatal("round trip did not charge the resource consumer")
	}
	consumer.Release()

	select {
	case e := <-errCh:
		t.Fatalf("unexpected listener error: %v", e)
	default:
	}
}

// TestGRPCServer_RejectsUnspecifiedSecureGateway mirrors rippled
// GRPCServer.cpp:361-368: a 0.0.0.0 secure_gateway entry is a startup
// error, not a match-all wildcard.
func TestGRPCServer_RejectsUnspecifiedSecureGateway(t *testing.T) {
	lookup := &stubLookup{validated: newStubLedger(t)}
	for _, gateway := range []string{"0.0.0.0", "::", "0.0.0.0/0", "::/0"} {
		t.Run(gateway, func(t *testing.T) {
			p := config.PortConfig{
				Port:          0,
				IP:            "127.0.0.1",
				Protocol:      "grpc",
				SecureGateway: []string{gateway},
			}
			_, err := prepareGRPCServer(
				context.Background(), "port_grpc", p, lookup, nil, xrpllog.Discard(), systemListen,
			)
			if err == nil {
				t.Fatalf("expected prepareGRPCServer to reject unspecified secure_gateway %q", gateway)
			}
		})
	}
}

func TestGRPCServer_RequestLimit(t *testing.T) {
	server := &boundGRPCServer{requestLimit: 2}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	handler := func(context.Context, any) (any, error) {
		entered <- struct{}{}
		<-release
		return nil, nil
	}
	for range 2 {
		go func() {
			_, err := server.trackUnary(context.Background(), nil, nil, handler)
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not enter the handler")
		}
	}

	_, err := server.trackUnary(context.Background(), nil, nil, func(context.Context, any) (any, error) {
		return nil, nil
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("over-limit request code = %v, want %v", status.Code(err), codes.ResourceExhausted)
	}

	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("admitted request failed: %v", err)
		}
	}
	if _, err := server.trackUnary(context.Background(), nil, nil, func(context.Context, any) (any, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("request after completion failed: %v", err)
	}
}

func TestGRPCServer_CancellationReleasesAdmission(t *testing.T) {
	manager := resource.NewManager(nil, nil)
	server := &boundGRPCServer{requestLimit: 1, resourceManager: manager}
	ctx, cancel := context.WithCancel(grpcPeerContext("192.0.2.1"))
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := server.trackUnary(ctx, &rpcv1.GetLedgerRequest{}, nil, func(ctx context.Context, _ any) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not enter the handler")
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("canceled request error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request did not exit")
	}

	stats := manager.Stats()
	if stats.Inflight != 0 || stats.Active != 0 {
		t.Fatalf("resource state after cancellation = inflight %d, active %d; want zero", stats.Inflight, stats.Active)
	}
	if _, err := server.trackUnary(grpcPeerContext("192.0.2.1"), &rpcv1.GetLedgerRequest{}, nil, func(context.Context, any) (any, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("request after cancellation failed: %v", err)
	}
}

func TestGRPCServer_ResourceAdmissionAndSecureGateway(t *testing.T) {
	_, gatewayNet, err := net.ParseCIDR("192.0.2.10/32")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("ordinary request is charged", func(t *testing.T) {
		manager := resource.NewManager(nil, nil)
		server := &boundGRPCServer{resourceManager: manager, secureGatewayNets: []net.IPNet{*gatewayNet}}
		if _, err := server.trackUnary(grpcPeerContext("192.0.2.11"), &rpcv1.GetLedgerRequest{}, nil, func(context.Context, any) (any, error) {
			return &rpcv1.GetLedgerResponse{}, nil
		}); err != nil {
			t.Fatalf("ordinary request failed: %v", err)
		}
		consumer := manager.NewInboundEndpoint("192.0.2.11")
		if consumer == nil {
			t.Fatal("ordinary request did not create a resource consumer")
		}
		defer consumer.Release()
		want := int64(resource.FeeMediumBurdenRPC().Cost() / resource.DecayWindowSeconds)
		if balance := consumer.Balance(); balance != want {
			t.Fatalf("ordinary request balance = %d, want %d", balance, want)
		}
	})

	t.Run("only a matching peer with a user is unlimited", func(t *testing.T) {
		manager := resource.NewManager(nil, nil)
		server := &boundGRPCServer{resourceManager: manager, secureGatewayNets: []net.IPNet{*gatewayNet}}
		seedGRPCResourceDrop(t, manager, "192.0.2.10")
		seedGRPCResourceDrop(t, manager, "192.0.2.11")

		for _, test := range []struct {
			name string
			ip   string
			user string
		}{
			{name: "matching peer without user", ip: "192.0.2.10"},
			{name: "non-matching peer with user", ip: "192.0.2.11", user: "clio"},
		} {
			t.Run(test.name, func(t *testing.T) {
				_, err := server.trackUnary(grpcPeerContext(test.ip), &rpcv1.GetLedgerRequest{User: test.user}, nil, func(context.Context, any) (any, error) {
					return &rpcv1.GetLedgerResponse{}, nil
				})
				if status.Code(err) != codes.ResourceExhausted {
					t.Fatalf("request code = %v, want %v", status.Code(err), codes.ResourceExhausted)
				}
			})
		}

		response, err := server.trackUnary(grpcPeerContext("192.0.2.10"), &rpcv1.GetLedgerRequest{User: "clio"}, nil, func(context.Context, any) (any, error) {
			return &rpcv1.GetLedgerResponse{}, nil
		})
		if err != nil {
			t.Fatalf("identified gateway request failed: %v", err)
		}
		if !response.(*rpcv1.GetLedgerResponse).GetIsUnlimited() {
			t.Fatal("identified gateway response did not set is_unlimited")
		}
	})
}

func grpcPeerContext(ip string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 50051},
	})
}

func seedGRPCResourceDrop(t *testing.T, manager *resource.Manager, ip string) {
	t.Helper()
	consumer := manager.NewInboundEndpoint(ip)
	if consumer == nil {
		t.Fatalf("create resource consumer for %s", ip)
	}
	consumer.Charge(resource.NewCharge(resource.DropThreshold*resource.DecayWindowSeconds, "test"), "")
	consumer.Release()
}

func TestGRPCServer_StopBeforeServeIsNotFatal(t *testing.T) {
	lookup := &stubLookup{validated: newStubLedger(t)}
	p := config.PortConfig{Port: 0, IP: "127.0.0.1", Protocol: "grpc"}
	bound, err := prepareGRPCServer(
		context.Background(),
		"port_grpc",
		p,
		lookup,
		nil,
		xrpllog.Discard(),
		systemListen,
	)
	if err != nil {
		t.Fatalf("prepareGRPCServer: %v", err)
	}
	defer bound.listener.Close()

	bound.server.Stop()
	errCh := make(chan error, 1)
	bound.serve(xrpllog.Discard(), errCh, nil)
	select {
	case err := <-errCh:
		t.Fatalf("stopped gRPC server reported a fatal error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestGRPCRecoveryInterceptor_RecoversPanic proves a panicking gRPC handler is
// turned into a fixed codes.Internal status rather than crashing the process,
// and that no internal detail leaks into the returned message.
func TestGRPCRecoveryInterceptor_RecoversPanic(t *testing.T) {
	interceptor := grpcRecoveryInterceptor(xrpllog.Discard())
	info := &googlegrpc.UnaryServerInfo{FullMethod: "/xrpl.rpc.v1.XRPLedgerAPIService/GetLedger"}

	var resp any
	var err error
	assertNotPanics(t, func() {
		resp, err = interceptor(context.Background(), nil, info, func(context.Context, any) (any, error) {
			panic("simulated handler panic on a truncated blob")
		})
	})

	if resp != nil {
		t.Errorf("expected nil response on recovered panic, got %v", resp)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected a gRPC status error, got %v", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code=%v, want %v", st.Code(), codes.Internal)
	}
	if st.Message() != "Internal error." {
		t.Errorf("message=%q, want the fixed %q (no internal detail)", st.Message(), "Internal error.")
	}
}

// TestGRPCRecoveryInterceptor_PassesThrough confirms the interceptor is
// transparent on the non-panicking path.
func TestGRPCRecoveryInterceptor_PassesThrough(t *testing.T) {
	interceptor := grpcRecoveryInterceptor(xrpllog.Discard())
	info := &googlegrpc.UnaryServerInfo{FullMethod: "/test/Method"}
	want := "ok"

	resp, err := interceptor(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return want, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != want {
		t.Errorf("resp=%v, want %v", resp, want)
	}
}

func assertNotPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("interceptor let a panic escape: %v", r)
		}
	}()
	fn()
}

// TestGRPCServer_DisabledByDefault confirms the boot path starts no gRPC
// listener when the config has no [port_grpc] section.
func TestGRPCServer_DisabledByDefault(t *testing.T) {
	cfg := &config.Config{Ports: map[string]config.PortConfig{
		"port_rpc": {Port: 5005, IP: "127.0.0.1", Protocol: "http"},
		"port_ws":  {Port: 6006, IP: "127.0.0.1", Protocol: "ws"},
	}}
	if _, _, ok := cfg.GRPCPort(); ok {
		t.Fatal("gRPC must be disabled when no [port_grpc] section is configured")
	}
}
