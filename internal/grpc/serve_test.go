package grpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
)

// TestGRPC_ServeAndDial proves the registered server is reachable over the
// wire — the property the node's startup wiring provides via
// RegisterXRPLedgerAPIServiceServer + Serve. It serves on a real listener,
// dials it with the generated client, and round-trips a GetLedger call.
func TestGRPC_ServeAndDial(t *testing.T) {
	l := newTestLedger(t, 100, nil, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := googlegrpc.NewServer()
	rpcv1.RegisterXRPLedgerAPIServiceServer(srv, NewServer(&fakeLookup{validated: l}))
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, googlegrpc.ErrServerStopped) {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("Serve did not stop within one second")
		}
	})

	conn, err := googlegrpc.NewClient(lis.Addr().String(), googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := rpcv1.NewXRPLedgerAPIServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.GetLedger(ctx, &rpcv1.GetLedgerRequest{
		Ledger: &rpcv1.LedgerSpecifier{
			Ledger: &rpcv1.LedgerSpecifier_Shortcut_{Shortcut: rpcv1.LedgerSpecifier_SHORTCUT_VALIDATED},
		},
	})
	if err != nil {
		t.Fatalf("GetLedger over wire: %v", err)
	}
	if !resp.Validated {
		t.Error("expected validated=true from wire round-trip")
	}
}
