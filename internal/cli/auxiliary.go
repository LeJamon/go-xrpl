package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/observability"
)

const auxiliaryShutdownTimeout = 5 * time.Second

type auxiliaryServer struct {
	name     string
	addr     string
	listener net.Listener
	server   *http.Server
}

type auxiliaryServers struct {
	servers      []auxiliaryServer
	pprof        bool
	serveWG      sync.WaitGroup
	startOnce    sync.Once
	shutdownOnce sync.Once
	lifecycleMu  sync.Mutex
	shutdownErr  error
	stopping     atomic.Bool
}

type auxiliaryServerSpec struct {
	name    string
	addr    string
	handler http.Handler
	pprof   bool
}

func startAuxiliaryServers(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	getenv func(string) string,
	listen func(string, string) (net.Listener, error),
) (*auxiliaryServers, error) {
	aux, err := bindAuxiliaryServers(getenv, listen)
	if err != nil {
		return nil, err
	}
	aux.Start(ctx, cancel)
	return aux, nil
}

func bindAuxiliaryServers(
	getenv func(string) string,
	listen func(string, string) (net.Listener, error),
) (*auxiliaryServers, error) {
	if getenv == nil {
		return nil, errors.New("auxiliary servers: getenv dependency is nil")
	}
	if listen == nil {
		return nil, errors.New("auxiliary servers: listen dependency is nil")
	}

	allowUnsafe, err := parseStrictBool("GOXRPL_PPROF_ALLOW_UNSAFE", getenv("GOXRPL_PPROF_ALLOW_UNSAFE"))
	if err != nil {
		return nil, err
	}

	var specs []auxiliaryServerSpec
	if raw := strings.TrimSpace(getenv("GOXRPL_PPROF")); raw != "" {
		addr, err := normalizeAuxiliaryAddress("pprof", raw, allowUnsafe)
		if err != nil {
			return nil, err
		}
		specs = append(specs, auxiliaryServerSpec{
			name:    "pprof",
			addr:    addr,
			handler: observability.PProfHandler(),
			pprof:   true,
		})
	}
	if raw := strings.TrimSpace(getenv("GOXRPL_METRICS")); raw != "" {
		addr, err := normalizeAuxiliaryAddress("metrics", raw, false)
		if err != nil {
			return nil, err
		}
		specs = append(specs, auxiliaryServerSpec{
			name:    "metrics",
			addr:    addr,
			handler: newMetricsHandler(),
		})
	}

	aux := &auxiliaryServers{}
	for _, spec := range specs {
		listener, err := listen("tcp", spec.addr)
		if err != nil {
			aux.closeListeners()
			return nil, fmt.Errorf("bind %s server on %s: %w", spec.name, spec.addr, err)
		}
		aux.servers = append(aux.servers, auxiliaryServer{
			name:     spec.name,
			addr:     listener.Addr().String(),
			listener: listener,
			server: &http.Server{
				Addr:              spec.addr,
				Handler:           spec.handler,
				ReadHeaderTimeout: 5 * time.Second,
			},
		})
		aux.pprof = aux.pprof || spec.pprof
	}
	return aux, nil
}

func (a *auxiliaryServers) Start(ctx context.Context, cancel context.CancelCauseFunc) {
	if a == nil {
		return
	}
	a.startOnce.Do(func() {
		a.lifecycleMu.Lock()
		defer a.lifecycleMu.Unlock()
		if a.stopping.Load() {
			return
		}
		if a.pprof {
			observability.EnablePProf()
		}

		for i := range a.servers {
			entry := &a.servers[i]
			a.serveWG.Add(1)
			go func() {
				defer a.serveWG.Done()
				if err := entry.server.Serve(entry.listener); err != nil &&
					!errors.Is(err, http.ErrServerClosed) &&
					!a.stopping.Load() &&
					ctx.Err() == nil {
					cancel(fmt.Errorf("%s server on %s failed: %w", entry.name, entry.addr, err))
				}
			}()
		}
		if len(a.servers) != 0 {
			go func() {
				<-ctx.Done()
				_ = a.Shutdown()
			}()
		}
	})
}

func (a *auxiliaryServers) Addresses() map[string]string {
	addresses := make(map[string]string, len(a.servers))
	for _, server := range a.servers {
		addresses[server.name] = server.addr
	}
	return addresses
}

func (a *auxiliaryServers) Shutdown() error {
	if a == nil {
		return nil
	}
	a.shutdownOnce.Do(func() {
		a.lifecycleMu.Lock()
		a.stopping.Store(true)
		a.lifecycleMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), auxiliaryShutdownTimeout)
		defer cancel()

		var errs []error
		for i := range a.servers {
			if err := observability.ShutdownHTTPServer(ctx, a.servers[i].server); err != nil {
				errs = append(errs, fmt.Errorf("shutdown %s server: %w", a.servers[i].name, err))
			}
		}
		a.closeListeners()
		serveDone := make(chan struct{})
		go func() {
			a.serveWG.Wait()
			close(serveDone)
		}()
		select {
		case <-serveDone:
		case <-ctx.Done():
			errs = append(errs, errors.New("timed out waiting for auxiliary servers to stop"))
		}
		a.shutdownErr = errors.Join(errs...)
	})
	return a.shutdownErr
}

func (a *auxiliaryServers) closeListeners() {
	for i := range a.servers {
		_ = a.servers[i].listener.Close()
	}
}

func parseStrictBool(name, value string) (bool, error) {
	switch value {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be \"true\" or \"false\"", name)
	}
}

func normalizeAuxiliaryAddress(name, raw string, allowUnsafe bool) (string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("invalid GOXRPL_%s address %q: %w", strings.ToUpper(name), raw, err)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return "", fmt.Errorf("invalid GOXRPL_%s address %q: port must be a number from 0 to 65535", strings.ToUpper(name), raw)
	}

	if name == "pprof" && host != "" && !allowUnsafe {
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			return "", unsafePProfAddressError(name, raw)
		}
	}
	host = normalizeAuxiliaryHost(name, host, allowUnsafe)
	if name == "pprof" && !allowUnsafe && !isLoopbackHost(host) {
		return "", unsafePProfAddressError(name, raw)
	}
	return net.JoinHostPort(host, strconv.FormatUint(portNumber, 10)), nil
}

func unsafePProfAddressError(name, raw string) error {
	return fmt.Errorf(
		"GOXRPL_%s address %q is not loopback; set GOXRPL_PPROF_ALLOW_UNSAFE=true to expose pprof",
		strings.ToUpper(name),
		raw,
	)
}

func normalizeAuxiliaryHost(name, host string, allowUnsafe bool) string {
	if host == "" {
		return "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsUnspecified() {
		return host
	}
	if name == "pprof" && allowUnsafe {
		return host
	}
	if ip.To4() != nil {
		return "127.0.0.1"
	}
	return "::1"
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
