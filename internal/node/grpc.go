package node

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	rtdebug "runtime/debug"
	"strings"
	"sync"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
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
	requestMu sync.Mutex
	requestWG sync.WaitGroup
	stopping  bool
}

func prepareGRPCServer(
	ctx context.Context,
	name string,
	p config.PortConfig,
	lookup xrplgrpc.LedgerLookup,
	log xrpllog.Logger,
	listen listenFunc,
) (*boundGRPCServer, error) {
	if err := validateGRPCPort(name, p); err != nil {
		return nil, err
	}
	return bindGRPCServer(ctx, name, p, lookup, log, listen, newConnectionLimiter(-1), nil)
}

func bindGRPCServer(
	ctx context.Context,
	name string,
	p config.PortConfig,
	lookup xrplgrpc.LedgerLookup,
	log xrpllog.Logger,
	listen listenFunc,
	limiter *connectionLimiter,
	resourceManager *resource.Manager,
) (*boundGRPCServer, error) {
	secureGatewayIPs, err := grpcSecureGatewayIPs(name, p)
	if err != nil {
		return nil, err
	}
	transportCredentials, err := grpcTransportCredentials(p)
	if err != nil {
		return nil, fmt.Errorf("configure grpc transport: %w", err)
	}

	addr := p.BindAddress()
	lis, err := listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("grpc listen on %s: %w", addr, err)
	}
	boundAddr := lis.Addr().String()
	if limiter != nil {
		lis = &limitedListener{Listener: lis, limiter: limiter, portName: name, portLimit: p.Limit}
	}
	readyListener := newServeReadyListener(lis)

	bound := &boundGRPCServer{
		name:      name,
		address:   boundAddr,
		listener:  readyListener,
		ready:     readyListener.ready,
		markReady: readyListener.markReady,
	}
	serverOptions := []googlegrpc.ServerOption{
		googlegrpc.ChainUnaryInterceptor(bound.trackUnary, grpcRecoveryInterceptor(log)),
		googlegrpc.ChainStreamInterceptor(bound.trackStream),
	}
	if transportCredentials != nil {
		serverOptions = append(serverOptions, googlegrpc.Creds(transportCredentials))
	}
	srv := googlegrpc.NewServer(serverOptions...)
	rpcv1.RegisterXRPLedgerAPIServiceServer(srv, xrplgrpc.NewServer(lookup, xrplgrpc.ServerConfig{
		Logger:           log,
		ResourceManager:  resourceManager,
		SecureGatewayIPs: secureGatewayIPs,
	}))
	bound.server = srv
	return bound, nil
}

func (s *boundGRPCServer) admitRequest() bool {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if s.stopping {
		return false
	}
	s.requestWG.Add(1)
	return true
}

func (s *boundGRPCServer) trackUnary(
	ctx context.Context,
	req any,
	_ *googlegrpc.UnaryServerInfo,
	handler googlegrpc.UnaryHandler,
) (any, error) {
	if !s.admitRequest() {
		return nil, status.Error(codes.Unavailable, "server shutting down")
	}
	defer s.requestWG.Done()
	return handler(ctx, req)
}

func (s *boundGRPCServer) trackStream(
	srv any,
	stream googlegrpc.ServerStream,
	_ *googlegrpc.StreamServerInfo,
	handler googlegrpc.StreamHandler,
) error {
	if !s.admitRequest() {
		return status.Error(codes.Unavailable, "server shutting down")
	}
	defer s.requestWG.Done()
	return handler(srv, stream)
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
	if _, err := grpcSecureGatewayIPs(name, p); err != nil {
		return err
	}
	if (p.SSLCert == "") != (p.SSLKey == "") {
		return fmt.Errorf("grpc port %q: ssl_cert and ssl_key must be configured together", name)
	}
	if p.SSLChain != "" && p.SSLCert == "" {
		return fmt.Errorf("grpc port %q: ssl_chain requires ssl_cert and ssl_key", name)
	}
	if p.SSLCiphers != "" {
		return fmt.Errorf("grpc port %q: ssl_ciphers is not supported", name)
	}
	if p.SSLClientCA != "" {
		return fmt.Errorf("grpc port %q: ssl_client_ca is not supported", name)
	}
	return nil
}

func grpcSecureGatewayIPs(name string, p config.PortConfig) ([]net.IP, error) {
	secureGatewayIPs := make([]net.IP, 0, len(p.SecureGateway))
	for _, entry := range p.SecureGateway {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("grpc port %q: secure_gateway entry must not be empty", name)
		}
		ip := net.ParseIP(entry)
		if ip == nil {
			return nil, fmt.Errorf("grpc port %q: secure_gateway entry %q must be an IP address", name, entry)
		}
		if ip.IsUnspecified() {
			return nil, fmt.Errorf("grpc port %q: unspecified IP %q in secure_gateway", name, entry)
		}
		secureGatewayIPs = append(secureGatewayIPs, ip)
	}
	return secureGatewayIPs, nil
}

func grpcTransportCredentials(p config.PortConfig) (credentials.TransportCredentials, error) {
	if p.SSLCert == "" && p.SSLKey == "" {
		return nil, nil
	}
	certPEM, err := os.ReadFile(p.SSLCert)
	if err != nil {
		return nil, fmt.Errorf("read ssl_cert: %w", err)
	}
	if p.SSLChain != "" {
		chainPEM, chainErr := os.ReadFile(p.SSLChain)
		if chainErr != nil {
			return nil, fmt.Errorf("read ssl_chain: %w", chainErr)
		}
		certPEM = append(append(certPEM, '\n'), chainPEM...)
	}
	keyPEM, err := os.ReadFile(p.SSLKey)
	if err != nil {
		return nil, fmt.Errorf("read ssl_key: %w", err)
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load ssl certificate: %w", err)
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	}), nil
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
