package cli

import (
	"context"
	"fmt"
	"net"
	"strings"

	googlegrpc "google.golang.org/grpc"

	"github.com/LeJamon/go-xrpl/config"
	xrplgrpc "github.com/LeJamon/go-xrpl/internal/grpc"
	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

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

	addr := p.GetBindAddress()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("grpc listen on %s: %w", addr, err)
	}
	boundAddr := lis.Addr().String()

	srv := googlegrpc.NewServer()
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
