package node

import (
	"context"
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

// startGRPCServer binds a listener for the [port_grpc] section and serves
// the XRPLedgerAPIService (the binary ledger surface consumed by Clio).
// It returns the running server and its bound address; Serve runs in a
// goroutine and reports a non-graceful exit on errCh.
//
// Mirrors rippled's GRPCServer: the server only exists when a [port_grpc]
// section supplies both ip and port. secure_gateway is parsed (and an
// unspecified address rejected, as rippled does) but does not yet alter
// per-request handling — go-xrpl's gRPC surface has no resource-limit
// accounting to bypass.
func startGRPCServer(
	name string,
	p config.PortConfig,
	lookup xrplgrpc.LedgerLookup,
	log xrpllog.Logger,
	errCh chan<- error,
) (*googlegrpc.Server, string, error) {
	if _, err := p.ParseSecureGatewayNets(); err != nil {
		return nil, "", fmt.Errorf("parse secure_gateway nets for grpc port %q: %w", name, err)
	}
	// rippled forbids an unspecified address in grpc secure_gateway
	// (GRPCServer.cpp:361-368) — match-all would defeat the rate-limit
	// bypass it scopes to known Clio hosts.
	for _, entry := range p.SecureGateway {
		if ip := net.ParseIP(strings.TrimSpace(entry)); ip != nil && ip.IsUnspecified() {
			return nil, "", fmt.Errorf("grpc port %q: unspecified IP %q in secure_gateway", name, entry)
		}
	}

	addr := p.BindAddress()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("grpc listen on %s: %w", addr, err)
	}
	boundAddr := lis.Addr().String()

	srv := googlegrpc.NewServer(
		googlegrpc.ChainUnaryInterceptor(grpcRecoveryInterceptor(log)),
	)
	rpcv1.RegisterXRPLedgerAPIServiceServer(srv, xrplgrpc.NewServer(lookup))

	go func() {
		log.Info("Listening", "protocol", "grpc", "name", name, "addr", boundAddr)
		if err := srv.Serve(lis); err != nil {
			log.Error("gRPC server failed", "name", name, "addr", boundAddr, "err", err)
			select {
			case errCh <- fmt.Errorf("grpc %s (%s): %w", name, boundAddr, err):
			default:
			}
		}
	}()

	return srv, boundAddr, nil
}
