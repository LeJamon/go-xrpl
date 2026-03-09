package grpc

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"google.golang.org/grpc"
)

// LedgerServiceInterface defines the interface for ledger operations needed by gRPC handlers.
// This interface is implemented by *service.Service.
type LedgerServiceInterface interface {
	// GetOpenLedger returns the current open ledger
	GetOpenLedger() *ledger.Ledger

	// GetClosedLedger returns the last closed ledger
	GetClosedLedger() *ledger.Ledger

	// GetValidatedLedger returns the highest validated ledger
	GetValidatedLedger() *ledger.Ledger

	// GetLedgerBySequence returns a ledger by its sequence number
	GetLedgerBySequence(seq uint32) (*ledger.Ledger, error)

	// GetLedgerByHash returns a ledger by its hash
	GetLedgerByHash(hash [32]byte) (*ledger.Ledger, error)

	// GetCurrentLedgerIndex returns the current open ledger index
	GetCurrentLedgerIndex() uint32

	// GetClosedLedgerIndex returns the last closed ledger index
	GetClosedLedgerIndex() uint32

	// GetValidatedLedgerIndex returns the highest validated ledger index
	GetValidatedLedgerIndex() uint32

	// GetLedgerEntry retrieves a specific ledger entry by its index/key
	GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*service.LedgerEntryResult, error)

	// GetLedgerData retrieves all ledger state entries with optional pagination
	GetLedgerData(ledgerIndex string, limit uint32, marker string) (*service.LedgerDataResult, error)
}

// Server represents the gRPC server for XRPL operations.
type Server struct {
	mu sync.RWMutex

	// grpcServer is the underlying gRPC server
	grpcServer *grpc.Server

	// ledgerService provides access to ledger operations
	ledgerService LedgerServiceInterface

	// config holds the server configuration
	config *ServerConfig

	// listener is the network listener
	listener net.Listener

	// running indicates if the server is currently running
	running bool
}

// ServerOption is a function that configures a Server.
type ServerOption func(*Server)

// WithLedgerService sets the ledger service for the server.
func WithLedgerService(svc LedgerServiceInterface) ServerOption {
	return func(s *Server) {
		s.ledgerService = svc
	}
}

// WithConfig sets the configuration for the server.
func WithConfig(cfg *ServerConfig) ServerOption {
	return func(s *Server) {
		s.config = cfg
	}
}

// NewServer creates a new gRPC server with the given configuration.
func NewServer(cfg *ServerConfig, ledgerSvc LedgerServiceInterface) (*Server, error) {
	if cfg == nil {
		cfg = DefaultServerConfig()
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Create gRPC server options
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.MaxRecvMsgSize),
		grpc.MaxSendMsgSize(cfg.MaxSendMsgSize),
	}

	// Create the gRPC server
	grpcServer := grpc.NewServer(opts...)

	server := &Server{
		grpcServer:    grpcServer,
		ledgerService: ledgerSvc,
		config:        cfg,
		running:       false,
	}

	return server, nil
}

// Start starts the gRPC server and begins accepting connections.
// This method blocks until the server is stopped or an error occurs.
func (s *Server) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("server is already running")
	}

	// Create listener
	listener, err := net.Listen("tcp", s.config.Address)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.listener = listener
	s.running = true
	s.mu.Unlock()

	// Start serving (this blocks)
	return s.grpcServer.Serve(listener)
}

// StartAsync starts the gRPC server in a goroutine and returns immediately.
// Returns an error if the server is already running or fails to start.
func (s *Server) StartAsync() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("server is already running")
	}

	// Create listener
	listener, err := net.Listen("tcp", s.config.Address)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.listener = listener
	s.running = true
	s.mu.Unlock()

	// Start serving in goroutine
	go func() {
		if err := s.grpcServer.Serve(listener); err != nil {
			// Log error but don't return it since we're in a goroutine
			// In production, this should use proper logging
			_ = err
		}
	}()

	return nil
}

// Stop gracefully stops the gRPC server.
// It stops accepting new connections and waits for existing connections to complete.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	s.grpcServer.GracefulStop()
	s.running = false
}

// StopNow immediately stops the gRPC server without waiting for connections.
func (s *Server) StopNow() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	s.grpcServer.Stop()
	s.running = false
}

// IsRunning returns true if the server is currently running.
func (s *Server) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// Address returns the address the server is listening on.
// Returns empty string if the server is not running.
func (s *Server) Address() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// GetGRPCServer returns the underlying grpc.Server.
// This can be used to register additional services.
func (s *Server) GetGRPCServer() *grpc.Server {
	return s.grpcServer
}

// SetLedgerService updates the ledger service.
// This should only be called before starting the server.
func (s *Server) SetLedgerService(svc LedgerServiceInterface) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ledgerService = svc
}

// UnaryServerInterceptor creates an interceptor for logging and metrics.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		// Pre-processing: could add logging, metrics, authentication here

		// Call the handler
		resp, err := handler(ctx, req)

		// Post-processing: could add logging, metrics here

		return resp, err
	}
}

// StreamServerInterceptor creates an interceptor for streaming RPCs.
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		// Pre-processing

		// Call the handler
		err := handler(srv, ss)

		// Post-processing

		return err
	}
}

// NewServerWithInterceptors creates a new gRPC server with interceptors.
func NewServerWithInterceptors(cfg *ServerConfig, ledgerSvc LedgerServiceInterface) (*Server, error) {
	if cfg == nil {
		cfg = DefaultServerConfig()
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Create gRPC server options with interceptors
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.MaxRecvMsgSize),
		grpc.MaxSendMsgSize(cfg.MaxSendMsgSize),
		grpc.UnaryInterceptor(UnaryServerInterceptor()),
		grpc.StreamInterceptor(StreamServerInterceptor()),
	}

	// Create the gRPC server
	grpcServer := grpc.NewServer(opts...)

	server := &Server{
		grpcServer:    grpcServer,
		ledgerService: ledgerSvc,
		config:        cfg,
		running:       false,
	}

	return server, nil
}
