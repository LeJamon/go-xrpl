package cli

import (
	"context"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/drops"
	xrplgrpc "github.com/LeJamon/go-xrpl/internal/grpc"
	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
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
	return ledger.FromGenesis(hdr, stateMap, txMap, drops.Fees{})
}

// TestGRPCServer_RoundTrip boots the gRPC listener on an ephemeral port
// and round-trips GetLedger and GetLedgerData over a real grpc.NewClient
// connection, exercising the listener wiring against the live
// XRPLedgerAPIService.
func TestGRPCServer_RoundTrip(t *testing.T) {
	lookup := &stubLookup{validated: newStubLedger(t)}
	p := config.PortConfig{Port: 0, IP: "127.0.0.1", Protocol: "grpc"}
	errCh := make(chan error, 1)

	srv, addr, err := startGRPCServer("port_grpc", p, lookup, xrpllog.Discard(), errCh)
	if err != nil {
		t.Fatalf("startGRPCServer: %v", err)
	}
	defer srv.GracefulStop()

	conn, err := googlegrpc.NewClient(addr, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
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
	p := config.PortConfig{
		Port:          0,
		IP:            "127.0.0.1",
		Protocol:      "grpc",
		SecureGateway: []string{"0.0.0.0"},
	}
	_, _, err := startGRPCServer("port_grpc", p, lookup, xrpllog.Discard(), make(chan error, 1))
	if err == nil {
		t.Fatal("expected startGRPCServer to reject unspecified secure_gateway IP")
	}
}

// TestGRPCServer_DisabledByDefault confirms the boot path starts no gRPC
// listener when the config has no [port_grpc] section.
func TestGRPCServer_DisabledByDefault(t *testing.T) {
	cfg := &config.Config{Ports: map[string]config.PortConfig{
		"port_rpc": {Port: 5005, IP: "127.0.0.1", Protocol: "http"},
		"port_ws":  {Port: 6006, IP: "127.0.0.1", Protocol: "ws"},
	}}
	if _, _, ok := cfg.GetGRPCPort(); ok {
		t.Fatal("gRPC must be disabled when no [port_grpc] section is configured")
	}
}
