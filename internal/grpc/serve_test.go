package grpc

import (
	"context"
	"net"
	"testing"

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
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	conn, err := googlegrpc.NewClient(lis.Addr().String(), googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := rpcv1.NewXRPLedgerAPIServiceClient(conn)
	resp, err := client.GetLedger(context.Background(), &rpcv1.GetLedgerRequest{
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
