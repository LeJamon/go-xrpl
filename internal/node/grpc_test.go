package node

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/drops"
	xrplgrpc "github.com/LeJamon/go-xrpl/internal/grpc"
	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
)

// stubLookup is a minimal xrplgrpc.LedgerLookup serving one validated
// ledger, used to exercise the gRPC listener wiring end-to-end.
type stubLookup struct {
	validated *ledger.Ledger
}

func (s *stubLookup) GetLedgerByHashContext(context.Context, [32]byte) (*ledger.Ledger, error) {
	return s.validated, nil
}
func (s *stubLookup) GetLedgerBySequenceContext(context.Context, uint32) (*ledger.Ledger, error) {
	return s.validated, nil
}
func (s *stubLookup) GetClosedLedger() *ledger.Ledger      { return s.validated }
func (s *stubLookup) GetValidatedLedger() *ledger.Ledger   { return s.validated }
func (s *stubLookup) GetOpenLedger() *ledger.Ledger        { return s.validated }
func (s *stubLookup) GetValidatedLedgerAge() time.Duration { return 0 }
func (s *stubLookup) IsStandalone() bool                   { return true }

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
	p := config.PortConfig{Port: 0, IP: "127.0.0.1", Protocol: "grpc"}
	errCh := make(chan error, 1)

	bound, err := prepareGRPCServer(
		context.Background(), "port_grpc", p, lookup, xrpllog.Discard(), systemListen,
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
	if dataResp.LedgerIndex != 0 || len(dataResp.LedgerHash) != 0 {
		t.Errorf("ledger identity fields must be unset: index=%d hash=%x", dataResp.LedgerIndex, dataResp.LedgerHash)
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

func TestGRPCServer_TLSRoundTripRejectsPlaintext(t *testing.T) {
	certPath, keyPath, roots := writeGRPCTestCertificate(t)
	lookup := &stubLookup{validated: newStubLedger(t)}
	p := config.PortConfig{
		Port: 0, IP: "127.0.0.1", Protocol: "grpc",
		SSLCert: certPath, SSLKey: keyPath,
	}
	errCh := make(chan error, 1)
	bound, err := prepareGRPCServer(context.Background(), "port_grpc", p, lookup, xrpllog.Discard(), systemListen)
	if err != nil {
		t.Fatalf("prepareGRPCServer: %v", err)
	}
	bound.serve(xrpllog.Discard(), errCh, nil)
	t.Cleanup(bound.server.Stop)

	tlsConn, err := googlegrpc.NewClient(bound.address, googlegrpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12,
	})))
	if err != nil {
		t.Fatalf("TLS client: %v", err)
	}
	t.Cleanup(func() { _ = tlsConn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := rpcv1.NewXRPLedgerAPIServiceClient(tlsConn).GetLedger(ctx, &rpcv1.GetLedgerRequest{}); err != nil {
		t.Fatalf("TLS GetLedger: %v", err)
	}

	plainConn, err := googlegrpc.NewClient(bound.address, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("plaintext client: %v", err)
	}
	t.Cleanup(func() { _ = plainConn.Close() })
	plainCtx, plainCancel := context.WithTimeout(context.Background(), time.Second)
	defer plainCancel()
	if _, err := rpcv1.NewXRPLedgerAPIServiceClient(plainConn).GetLedger(plainCtx, &rpcv1.GetLedgerRequest{}); err == nil {
		t.Fatal("plaintext request succeeded against TLS gRPC listener")
	}
}

func writeGRPCTestCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("append test certificate")
	}
	return certPath, keyPath, roots
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
	_, err := prepareGRPCServer(
		context.Background(), "port_grpc", p, lookup, xrpllog.Discard(), systemListen,
	)
	if err == nil {
		t.Fatal("expected prepareGRPCServer to reject unspecified secure_gateway IP")
	}
}

func TestGRPCServer_RejectsSecureGatewayCIDR(t *testing.T) {
	lookup := &stubLookup{validated: newStubLedger(t)}
	p := config.PortConfig{
		Port: 0, IP: "127.0.0.1", Protocol: "grpc",
		SecureGateway: []string{"127.0.0.0/8"},
	}
	_, err := prepareGRPCServer(context.Background(), "port_grpc", p, lookup, xrpllog.Discard(), systemListen)
	if err == nil {
		t.Fatal("expected secure_gateway CIDR validation error")
	}
}

func TestGRPCServer_RejectsEmptySecureGateway(t *testing.T) {
	lookup := &stubLookup{validated: newStubLedger(t)}
	p := config.PortConfig{
		Port: 0, IP: "127.0.0.1", Protocol: "grpc",
		SecureGateway: []string{""},
	}
	_, err := prepareGRPCServer(context.Background(), "port_grpc", p, lookup, xrpllog.Discard(), systemListen)
	if err == nil {
		t.Fatal("expected empty secure_gateway validation error")
	}
}

func TestGRPCServer_StopBeforeServeIsNotFatal(t *testing.T) {
	lookup := &stubLookup{validated: newStubLedger(t)}
	p := config.PortConfig{Port: 0, IP: "127.0.0.1", Protocol: "grpc"}
	bound, err := prepareGRPCServer(
		context.Background(),
		"port_grpc",
		p,
		lookup,
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
