package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	rtdebug "runtime/debug"
	"strings"
	"sync"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/LeJamon/go-xrpl/config"
	xrplgrpc "github.com/LeJamon/go-xrpl/internal/grpc"
	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// grpcRecoveryInterceptor is the panic boundary for gRPC handlers. grpc-go does
// not recover handler panics by default, so a latent panic in the binary
// ledger surface (a slice-bounds error on a truncated blob, a SHAMap walk on a
// corrupt request-selected ledger) would take down the node. It mirrors the
// HTTP and WebSocket recover middleware: recover, log the detail with a stack
// trace server-side, and return a fixed codes.Internal status so no internal
// detail reaches the client.
func grpcRecoveryInterceptor(log xrpllog.Logger) googlegrpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *googlegrpc.UnaryServerInfo,
		handler googlegrpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("gRPC handler panic",
					"method", info.FullMethod, "panic", rec, "stack", string(rtdebug.Stack()))
				err = status.Error(codes.Internal, "Internal error.")
			}
		}()
		return handler(ctx, req)
	}
}

type boundGRPCServer struct {
	name      string
	address   string
	listener  net.Listener
	ready     <-chan struct{}
	markReady func()
	server    *googlegrpc.Server

	resourceManager   *resource.Manager
	secureGatewayNets []net.IPNet
	requestLimit      int
	requestMu         sync.Mutex
	requestWG         sync.WaitGroup
	requests          int
	stopping          bool
}

func prepareGRPCServer(
	ctx context.Context,
	name string,
	p config.PortConfig,
	lookup xrplgrpc.LedgerLookup,
	resourceManager *resource.Manager,
	log xrpllog.Logger,
	listen listenFunc,
) (*boundGRPCServer, error) {
	if err := validateGRPCPort(name, p); err != nil {
		return nil, err
	}
	return bindGRPCServer(ctx, name, p, lookup, resourceManager, log, listen)
}

func bindGRPCServer(
	ctx context.Context,
	name string,
	p config.PortConfig,
	lookup xrplgrpc.LedgerLookup,
	resourceManager *resource.Manager,
	log xrpllog.Logger,
	listen listenFunc,
) (*boundGRPCServer, error) {
	secureGatewayNets, err := p.ParseSecureGatewayNets()
	if err != nil {
		return nil, fmt.Errorf("parse secure_gateway nets for grpc port %q: %w", name, err)
	}
	addr := p.BindAddress()
	lis, err := listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("grpc listen on %s: %w", addr, err)
	}
	boundAddr := lis.Addr().String()
	readyListener := newServeReadyListener(lis)

	bound := &boundGRPCServer{
		name:              name,
		address:           boundAddr,
		listener:          readyListener,
		ready:             readyListener.ready,
		markReady:         readyListener.markReady,
		resourceManager:   resourceManager,
		secureGatewayNets: secureGatewayNets,
		requestLimit:      p.Limit,
	}
	srv := googlegrpc.NewServer(
		googlegrpc.ChainUnaryInterceptor(bound.trackUnary, grpcRecoveryInterceptor(log)),
		googlegrpc.ChainStreamInterceptor(bound.trackStream),
	)
	rpcv1.RegisterXRPLedgerAPIServiceServer(srv, xrplgrpc.NewServer(lookup))
	bound.server = srv
	return bound, nil
}

func (s *boundGRPCServer) admitRequest() error {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if s.stopping {
		return status.Error(codes.Unavailable, "server shutting down")
	}
	if s.requestLimit > 0 && s.requests >= s.requestLimit {
		return status.Error(codes.ResourceExhausted, "request limit exceeded")
	}
	s.requestWG.Add(1)
	s.requests++
	return nil
}

func (s *boundGRPCServer) finishRequest() {
	s.requestMu.Lock()
	s.requests--
	s.requestMu.Unlock()
	s.requestWG.Done()
}

func (s *boundGRPCServer) trackUnary(
	ctx context.Context,
	req any,
	info *googlegrpc.UnaryServerInfo,
	handler googlegrpc.UnaryHandler,
) (any, error) {
	if err := s.admitRequest(); err != nil {
		return nil, err
	}
	defer s.finishRequest()

	admission, unlimited, err := s.admitResource(ctx, req)
	if err != nil {
		return nil, err
	}
	if admission != nil {
		method := ""
		if info != nil {
			method = info.FullMethod
		}
		defer admission.Finish(resource.FeeMediumBurdenRPC(), method)
	}

	resp, err := handler(ctx, req)
	if unlimited && resp != nil {
		setGRPCUnlimited(resp)
	}
	return resp, err
}

func (s *boundGRPCServer) trackStream(
	srv any,
	stream googlegrpc.ServerStream,
	_ *googlegrpc.StreamServerInfo,
	handler googlegrpc.StreamHandler,
) error {
	if err := s.admitRequest(); err != nil {
		return err
	}
	defer s.finishRequest()
	return handler(srv, stream)
}

func (s *boundGRPCServer) admitResource(ctx context.Context, req any) (*resource.Admission, bool, error) {
	if s.resourceManager == nil {
		return nil, false, nil
	}
	clientIP, ok := grpcClientIP(ctx)
	if !ok {
		return nil, false, status.Error(codes.Internal, "Failed to get client endpoint")
	}
	unlimited := grpcUser(req) != "" && config.IPInNets(clientIP, s.secureGatewayNets)
	if unlimited {
		admission, disposition := s.resourceManager.AdmitUnlimited(clientIP.String())
		if admission == nil || disposition == resource.Drop {
			return nil, false, status.Error(codes.ResourceExhausted, "usage balance exceeds threshold")
		}
		return admission, true, nil
	}
	admission, disposition := s.resourceManager.AdmitInbound(clientIP.String(), resource.FeeMediumBurdenRPC())
	if admission == nil || disposition == resource.Drop {
		return nil, false, status.Error(codes.ResourceExhausted, "usage balance exceeds threshold")
	}
	return admission, false, nil
}

func grpcClientIP(ctx context.Context) (net.IP, bool) {
	client, ok := peer.FromContext(ctx)
	if !ok || client.Addr == nil {
		return nil, false
	}
	if tcpAddr, ok := client.Addr.(*net.TCPAddr); ok && tcpAddr.IP != nil {
		return tcpAddr.IP, true
	}
	host, _, err := net.SplitHostPort(client.Addr.String())
	if err != nil {
		return nil, false
	}
	ip := net.ParseIP(host)
	return ip, ip != nil
}

func grpcUser(req any) string {
	if request, ok := req.(interface{ GetUser() string }); ok {
		return request.GetUser()
	}
	return ""
}

func setGRPCUnlimited(resp any) {
	switch response := resp.(type) {
	case *rpcv1.GetLedgerResponse:
		response.IsUnlimited = true
	case *rpcv1.GetLedgerDataResponse:
		response.IsUnlimited = true
	}
}

func (s *boundGRPCServer) stopRequests() {
	if s == nil {
		return
	}
	s.requestMu.Lock()
	s.stopping = true
	s.requestMu.Unlock()
}

func (s *boundGRPCServer) waitRequests() {
	if s != nil {
		s.requestWG.Wait()
	}
}

func validateGRPCPort(name string, p config.PortConfig) error {
	if _, err := p.ParseSecureGatewayNets(); err != nil {
		return fmt.Errorf("parse secure_gateway nets for grpc port %q: %w", name, err)
	}
	// rippled forbids an unspecified address in grpc secure_gateway
	// (GRPCServer.cpp:361-368) — match-all would defeat the rate-limit
	// bypass it scopes to known Clio hosts.
	for _, entry := range p.SecureGateway {
		entry = strings.TrimSpace(entry)
		if ip := net.ParseIP(entry); ip != nil && ip.IsUnspecified() {
			return fmt.Errorf("grpc port %q: unspecified IP %q in secure_gateway", name, entry)
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			prefix, _ := network.Mask.Size()
			if prefix == 0 {
				return fmt.Errorf("grpc port %q: unspecified network %q in secure_gateway", name, entry)
			}
		}
	}
	return nil
}

func (s *boundGRPCServer) serve(log xrpllog.Logger, errCh chan<- error, done func()) {
	go func() {
		if done != nil {
			defer done()
		}
		defer s.markReady()
		log.Info("Listening", "protocol", "grpc", "name", s.name, "addr", s.address)
		if err := s.server.Serve(s.listener); err != nil && !errors.Is(err, googlegrpc.ErrServerStopped) {
			log.Error("gRPC server failed", "name", s.name, "addr", s.address, "err", err)
			select {
			case errCh <- fmt.Errorf("grpc %s (%s): %w", s.name, s.address, err):
			default:
			}
		}
	}()
}
