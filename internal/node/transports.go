package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	xrplgrpc "github.com/LeJamon/go-xrpl/internal/grpc"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type listenFunc func(context.Context, string, string) (net.Listener, error)

func systemListen(ctx context.Context, network, address string) (net.Listener, error) {
	var cfg net.ListenConfig
	return cfg.Listen(ctx, network, address)
}

type boundHTTPServer struct {
	name      string
	protocol  string
	address   string
	listener  net.Listener
	ready     <-chan struct{}
	markReady func()
	server    *http.Server
}

type boundRPCTransports struct {
	http      []*boundHTTPServer
	ws        []*boundHTTPServer
	grpc      *boundGRPCServer
	errors    chan error
	serveWG   sync.WaitGroup
	requestMu sync.Mutex
	requestWG sync.WaitGroup
	stopping  bool
}

type serveReadyListener struct {
	net.Listener
	once  sync.Once
	ready chan struct{}
}

func newServeReadyListener(listener net.Listener) *serveReadyListener {
	return &serveReadyListener{Listener: listener, ready: make(chan struct{})}
}

func (l *serveReadyListener) Accept() (net.Conn, error) {
	l.markReady()
	return l.Listener.Accept()
}

func (l *serveReadyListener) markReady() {
	l.once.Do(func() { close(l.ready) })
}

func bindRPCTransports(
	ctx context.Context,
	log xrpllog.Logger,
	cfg *config.Config,
	httpHandler http.Handler,
	wsServer *rpc.WebSocketServer,
	grpcLookup xrplgrpc.LedgerLookup,
	listen listenFunc,
) (_ *boundRPCTransports, err error) {
	connLimiter := rpc.NewConnLimiter()
	if cfg.Server.MaxConnections != 0 {
		connLimiter.SetGlobalLimit(cfg.Server.MaxConnections)
	}
	wsServer.SetConnLimiter(connLimiter)

	httpMux := http.NewServeMux()
	httpMux.Handle("/", httpHandler)
	httpMux.Handle("/rpc", httpHandler)
	httpMux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"go-xrpl"}`))
	})

	httpPorts := cfg.HTTPPorts()
	wsPorts := cfg.WebSocketPorts()
	if len(httpPorts) == 0 {
		return nil, fmt.Errorf("no HTTP ports configured — at least one HTTP port is required")
	}

	httpNames := sortedPortNames(httpPorts)
	wsNames := sortedPortNames(wsPorts)
	bound := &boundRPCTransports{
		http:   make([]*boundHTTPServer, 0, len(httpNames)),
		ws:     make([]*boundHTTPServer, 0, len(wsNames)),
		errors: make(chan error, 2+len(wsNames)+len(httpNames)),
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, bound.closeListeners())
		}
	}()

	for _, name := range wsNames {
		p := wsPorts[name]
		pc, parseErr := parsePortConfig("ws", name, p)
		if parseErr != nil {
			return nil, parseErr
		}
		mux := http.NewServeMux()
		mux.Handle("/", bound.trackHandler(rpc.PortMiddleware(pc, connLimiter, wsServer)))
		srv := &http.Server{
			Addr:              p.BindAddress(),
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			BaseContext:       func(net.Listener) context.Context { return ctx },
		}
		bound.ws = append(bound.ws, &boundHTTPServer{
			name: name, protocol: "ws", address: srv.Addr, server: srv,
		})
	}

	for _, name := range httpNames {
		p := httpPorts[name]
		pc, parseErr := parsePortConfig("http", name, p)
		if parseErr != nil {
			return nil, parseErr
		}
		mux := http.NewServeMux()
		mux.Handle("/", bound.trackHandler(rpc.PortMiddleware(pc, connLimiter, httpMux)))
		srv := &http.Server{
			Addr:              p.BindAddress(),
			Handler:           mux,
			ReadHeaderTimeout: httpReadHeaderTimeout,
			ReadTimeout:       httpReadTimeout,
			WriteTimeout:      httpWriteTimeout,
			IdleTimeout:       httpIdleTimeout,
			BaseContext:       func(net.Listener) context.Context { return ctx },
		}
		bound.http = append(bound.http, &boundHTTPServer{
			name: name, protocol: "http", address: srv.Addr, server: srv,
		})
	}

	grpcName, grpcPort, hasGRPC := cfg.GRPCPort()
	if hasGRPC {
		if validateErr := validateGRPCPort(grpcName, grpcPort); validateErr != nil {
			return nil, validateErr
		}
	}
	for _, server := range append(append([]*boundHTTPServer(nil), bound.ws...), bound.http...) {
		log.Info("Port configured", "protocol", server.protocol, "name", server.name, "addr", server.address)
	}
	if _, peerPort, ok := cfg.PeerPort(); ok {
		log.Info("Port configured", "protocol", "peer", "addr", peerPort.BindAddress())
	}
	if hasGRPC {
		log.Info("Port configured", "protocol", "grpc", "name", grpcName, "addr", grpcPort.BindAddress())
	}

	for _, server := range append(append([]*boundHTTPServer(nil), bound.ws...), bound.http...) {
		listener, listenErr := listen(ctx, "tcp", server.address)
		if listenErr != nil {
			return nil, fmt.Errorf("%s %s listen on %s: %w", server.protocol, server.name, server.address, listenErr)
		}
		readyListener := newServeReadyListener(listener)
		server.listener = readyListener
		server.ready = readyListener.ready
		server.markReady = readyListener.markReady
		server.address = listener.Addr().String()
	}

	if hasGRPC {
		grpcServer, grpcErr := bindGRPCServer(ctx, grpcName, grpcPort, grpcLookup, log, listen)
		if grpcErr != nil {
			return nil, grpcErr
		}
		bound.grpc = grpcServer
	}

	return bound, nil
}

func sortedPortNames(ports map[string]config.PortConfig) []string {
	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (t *boundRPCTransports) serve(log xrpllog.Logger) error {
	servers := append(append([]*boundHTTPServer(nil), t.ws...), t.http...)
	t.serveWG.Add(len(servers))
	for _, bound := range servers {
		go func(s *boundHTTPServer) {
			defer t.serveWG.Done()
			defer s.markReady()
			log.Info("Listening", "protocol", s.protocol, "name", s.name, "addr", s.address)
			if err := s.server.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error(s.protocol+" server failed", "name", s.name, "addr", s.address, "err", err)
				select {
				case t.errors <- fmt.Errorf("%s %s (%s): %w", s.protocol, s.name, s.address, err):
				default:
				}
			}
		}(bound)
	}
	if t.grpc != nil {
		t.serveWG.Add(1)
		t.grpc.serve(log, t.errors, t.serveWG.Done)
	}
	for _, server := range servers {
		<-server.ready
	}
	if t.grpc != nil {
		<-t.grpc.ready
	}
	select {
	case err := <-t.errors:
		return err
	default:
		return nil
	}
}

func (t *boundRPCTransports) wait() {
	if t != nil {
		t.serveWG.Wait()
	}
}

func (t *boundRPCTransports) trackHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.requestMu.Lock()
		if t.stopping {
			t.requestMu.Unlock()
			http.Error(w, "server shutting down", http.StatusServiceUnavailable)
			return
		}
		t.requestWG.Add(1)
		t.requestMu.Unlock()
		defer t.requestWG.Done()

		next.ServeHTTP(w, r)
	})
}

func (t *boundRPCTransports) stopRequests() {
	if t == nil {
		return
	}
	t.requestMu.Lock()
	t.stopping = true
	t.requestMu.Unlock()
	if t.grpc != nil {
		t.grpc.stopRequests()
	}
}

func (t *boundRPCTransports) waitRequests() {
	if t != nil {
		t.requestWG.Wait()
		if t.grpc != nil {
			t.grpc.waitRequests()
		}
	}
}

func (t *boundRPCTransports) closeListeners() error {
	if t == nil {
		return nil
	}
	var errs []error
	for _, bound := range append(append([]*boundHTTPServer(nil), t.ws...), t.http...) {
		if bound.listener == nil {
			continue
		}
		if err := bound.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if t.grpc != nil {
		if err := t.grpc.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
