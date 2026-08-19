package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

func grpcTestPeerContext(ip string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 12345},
	})
}

func TestBeginRequestEnforcesPerClientInflightLimit(t *testing.T) {
	limits := resource.DefaultLimits()
	limits.MaxInflightPerConsumer = 1
	manager := resource.NewManagerWithLimits(nil, nil, limits)
	srv := NewServer(&fakeLookup{}, ServerConfig{ResourceManager: manager})
	ctx := grpcTestPeerContext("192.0.2.10")

	_, finish, err := srv.beginRequest(ctx, &rpcv1.GetLedgerRequest{}, "GetLedger")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if _, _, err := srv.beginRequest(ctx, &rpcv1.GetLedgerDataRequest{}, "GetLedgerData"); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second request = %v, want ResourceExhausted", err)
	}
	finish()

	_, finish, err = srv.beginRequest(ctx, &rpcv1.GetLedgerEntryRequest{}, "GetLedgerEntry")
	if err != nil {
		t.Fatalf("request after completion: %v", err)
	}
	finish()
	if stats := manager.Stats(); stats.Inflight != 0 || stats.InflightRejections != 1 {
		t.Fatalf("resource stats = %+v", stats)
	}
}

func TestBeginRequestChargesMediumBurden(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	manager := resource.NewManagerWithLimits(func() time.Time { return now }, nil, resource.DefaultLimits())
	srv := NewServer(&fakeLookup{}, ServerConfig{ResourceManager: manager})
	ctx := grpcTestPeerContext("192.0.2.40")

	_, finish, err := srv.beginRequest(ctx, &rpcv1.GetLedgerDiffRequest{}, "GetLedgerDiff")
	if err != nil {
		t.Fatalf("begin request: %v", err)
	}
	finish()
	consumer := manager.NewInboundEndpoint("192.0.2.40")
	if consumer == nil {
		t.Fatal("acquire charged consumer")
	}
	defer consumer.Release()
	want := int64(resource.FeeMediumBurdenRPC().Cost() / resource.DecayWindowSeconds)
	if got := consumer.Balance(); got != want {
		t.Fatalf("request charge balance = %d, want medium burden %d", got, want)
	}
}

func TestBeginRequestUsesActualPeerForSecureGateway(t *testing.T) {
	limits := resource.DefaultLimits()
	limits.MaxInflightPerConsumer = 1
	manager := resource.NewManagerWithLimits(nil, nil, limits)
	srv := NewServer(&fakeLookup{}, ServerConfig{
		ResourceManager:  manager,
		SecureGatewayIPs: []net.IP{net.ParseIP("192.0.2.20")},
	})

	trusted := grpcTestPeerContext("192.0.2.20")
	unlimited, first, err := srv.beginRequest(trusted, &rpcv1.GetLedgerRequest{User: "clio"}, "GetLedger")
	if err != nil || !unlimited {
		t.Fatalf("trusted request = (unlimited=%v, err=%v)", unlimited, err)
	}
	unlimited, second, err := srv.beginRequest(trusted, &rpcv1.GetLedgerDataRequest{User: "clio"}, "GetLedgerData")
	if err != nil || !unlimited {
		t.Fatalf("second trusted request = (unlimited=%v, err=%v)", unlimited, err)
	}
	first()
	second()
	charged := manager.NewInboundEndpoint("192.0.2.20")
	if charged == nil {
		t.Fatal("acquire unlimited request charge")
	}
	if charged.Balance() == 0 {
		t.Fatal("unlimited requests were not charged medium burden")
	}
	charged.Release()

	untrusted := grpcTestPeerContext("192.0.2.21")
	unlimited, finish, err := srv.beginRequest(untrusted, &rpcv1.GetLedgerRequest{
		User:     "clio",
		ClientIp: "192.0.2.20",
	}, "GetLedger")
	if err != nil {
		t.Fatalf("untrusted request: %v", err)
	}
	finish()
	if unlimited {
		t.Fatal("request client_ip granted unlimited status")
	}

	unlimited, finish, err = srv.beginRequest(trusted, &rpcv1.GetLedgerRequest{}, "GetLedger")
	if err != nil {
		t.Fatalf("trusted peer without user: %v", err)
	}
	finish()
	if unlimited {
		t.Fatal("trusted peer without user granted unlimited status")
	}
}

func TestSecureGatewayResponsesReportUnlimited(t *testing.T) {
	l := newTestLedger(t, 7, nil, nil)
	srv := NewServer(&fakeLookup{openLedger: l}, ServerConfig{
		SecureGatewayIPs: []net.IP{net.ParseIP("192.0.2.30")},
	})
	ctx := grpcTestPeerContext("192.0.2.30")

	ledgerResp, err := srv.GetLedger(ctx, &rpcv1.GetLedgerRequest{User: "clio"})
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if !ledgerResp.GetIsUnlimited() {
		t.Fatal("GetLedger did not report unlimited status")
	}
	dataResp, err := srv.GetLedgerData(ctx, &rpcv1.GetLedgerDataRequest{User: "clio"})
	if err != nil {
		t.Fatalf("GetLedgerData: %v", err)
	}
	if !dataResp.GetIsUnlimited() {
		t.Fatal("GetLedgerData did not report unlimited status")
	}
}
