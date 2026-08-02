package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	rtdebug "runtime/debug"
	"strings"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/LeJamon/go-xrpl/config"
	xrplgrpc "github.com/LeJamon/go-xrpl/internal/grpc"
	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
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
	return bindGRPCServer(ctx, name, p, lookup, log, listen)
}

func bindGRPCServer(
	ctx context.Context,
	name string,
	p config.PortConfig,
	lookup xrplgrpc.LedgerLookup,
	log xrpllog.Logger,
	listen listenFunc,
) (*boundGRPCServer, error) {
	addr := p.BindAddress()
	lis, err := listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("grpc listen on %s: %w", addr, err)
	}
	boundAddr := lis.Addr().String()
	readyListener := newServeReadyListener(lis)

	srv := googlegrpc.NewServer(
		googlegrpc.ChainUnaryInterceptor(grpcRecoveryInterceptor(log)),
	)
	rpcv1.RegisterXRPLedgerAPIServiceServer(srv, xrplgrpc.NewServer(lookup))

	return &boundGRPCServer{
		name:      name,
		address:   boundAddr,
		listener:  readyListener,
		ready:     readyListener.ready,
		markReady: readyListener.markReady,
		server:    srv,
	}, nil
}

func validateGRPCPort(name string, p config.PortConfig) error {
	if _, err := p.ParseSecureGatewayNets(); err != nil {
		return fmt.Errorf("parse secure_gateway nets for grpc port %q: %w", name, err)
	}
	// rippled forbids an unspecified address in grpc secure_gateway
	// (GRPCServer.cpp:361-368) — match-all would defeat the rate-limit
	// bypass it scopes to known Clio hosts.
	for _, entry := range p.SecureGateway {
		if ip := net.ParseIP(strings.TrimSpace(entry)); ip != nil && ip.IsUnspecified() {
			return fmt.Errorf("grpc port %q: unspecified IP %q in secure_gateway", name, entry)
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
