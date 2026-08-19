package grpc

import (
	"context"
	"errors"
	"net"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	rpcv1 "github.com/LeJamon/go-xrpl/internal/grpc/pb/org/xrpl/rpc/v1"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type ServerConfig struct {
	Logger           xrpllog.Logger
	ResourceManager  *resource.Manager
	SecureGatewayIPs []net.IP
}

type Server struct {
	rpcv1.UnimplementedXRPLedgerAPIServiceServer
	lookup           LedgerLookup
	log              xrpllog.Logger
	resourceManager  *resource.Manager
	secureGatewayIPs []net.IP
}

func NewServer(lookup LedgerLookup, configs ...ServerConfig) *Server {
	config := ServerConfig{Logger: xrpllog.Discard()}
	if len(configs) > 0 {
		config = configs[0]
		if config.Logger == nil {
			config.Logger = xrpllog.Discard()
		}
	}
	return &Server{
		lookup:           lookup,
		log:              config.Logger,
		resourceManager:  config.ResourceManager,
		secureGatewayIPs: cloneIPs(config.SecureGatewayIPs),
	}
}

func cloneIPs(ips []net.IP) []net.IP {
	out := make([]net.IP, len(ips))
	for i, ip := range ips {
		out[i] = append(net.IP(nil), ip...)
	}
	return out
}

func (s *Server) grpcStorageError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	s.log.Error("gRPC request failed", "operation", operation, "err", err)
	return status.Error(codes.Internal, "Internal error.")
}

type requestUser interface {
	GetUser() string
}

func (s *Server) beginRequest(ctx context.Context, req any, method string) (bool, func(), error) {
	clientIP, err := grpcPeerIP(ctx)
	if err != nil {
		if s.resourceManager == nil {
			return false, func() {}, nil
		}
		return false, func() {}, s.grpcStorageError("identifying client", err)
	}

	unlimited := false
	if withUser, ok := req.(requestUser); ok && withUser.GetUser() != "" {
		for _, gatewayIP := range s.secureGatewayIPs {
			if gatewayIP.Equal(clientIP) {
				unlimited = true
				break
			}
		}
	}
	if s.resourceManager == nil {
		return unlimited, func() {}, nil
	}

	var admission *resource.Admission
	var disposition resource.Disposition
	if unlimited {
		admission, disposition = s.resourceManager.AdmitUnlimited(clientIP.String())
	} else {
		admission, disposition = s.resourceManager.AdmitInbound(clientIP.String(), resource.FeeReferenceRPC())
	}
	if admission == nil || disposition == resource.Drop {
		s.log.Warn("gRPC request dropped: client over load threshold", "client", clientIP.String(), "method", method)
		return unlimited, func() {}, status.Error(codes.ResourceExhausted, "usage balance exceeds threshold")
	}

	return unlimited, func() {
		completion := admission.Finish(resource.FeeMediumBurdenRPC(), method)
		if unlimited {
			consumer := s.resourceManager.NewInboundEndpoint(clientIP.String())
			if consumer == nil {
				s.log.Warn("gRPC unlimited request charge could not acquire client", "client", clientIP.String(), "method", method)
				return
			}
			completion.Disposition = consumer.Charge(resource.FeeMediumBurdenRPC(), method)
			completion.Balance = consumer.Balance()
			consumer.Release()
		}
		switch completion.Disposition {
		case resource.Drop:
			s.log.Warn("gRPC client crossed drop threshold", "client", clientIP.String(), "method", method, "balance", completion.Balance)
		case resource.Warn:
			s.log.Info("gRPC client over warn threshold", "client", clientIP.String(), "method", method, "balance", completion.Balance)
		}
	}, nil
}

func grpcPeerIP(ctx context.Context) (net.IP, error) {
	peerInfo, ok := peer.FromContext(ctx)
	if !ok || peerInfo.Addr == nil {
		return nil, errors.New("gRPC peer address unavailable")
	}
	host, _, err := net.SplitHostPort(peerInfo.Addr.String())
	if err != nil {
		host = peerInfo.Addr.String()
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, errors.New("gRPC peer address is not an IP endpoint")
	}
	return ip, nil
}
