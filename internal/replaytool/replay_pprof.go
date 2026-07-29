package replaytool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/observability"
)

const replayPProfShutdownTimeout = 5 * time.Second

type replayPProfServer struct {
	addr         string
	server       *http.Server
	done         chan error
	shutdownOnce sync.Once
	shutdownErr  error
}

func startReplayPProf(ctx context.Context, cancel context.CancelCauseFunc) (*replayPProfServer, error) {
	var listenConfig net.ListenConfig
	return startReplayPProfWithDependencies(
		cancel,
		os.Getenv,
		func(network, address string) (net.Listener, error) {
			return listenConfig.Listen(ctx, network, address)
		},
		observability.EnablePProf,
	)
}

func startReplayPProfWithDependencies(
	cancel context.CancelCauseFunc,
	getenv func(string) string,
	listen func(string, string) (net.Listener, error),
	enable func(),
) (*replayPProfServer, error) {
	raw := strings.TrimSpace(getenv("GOXRPL_PPROF"))
	if raw == "" {
		return nil, nil
	}

	allowUnsafe, err := replayPProfAllowUnsafe(getenv("GOXRPL_PPROF_ALLOW_UNSAFE"))
	if err != nil {
		return nil, err
	}
	addr, err := normalizeReplayPProfAddress(raw, allowUnsafe)
	if err != nil {
		return nil, err
	}
	listener, err := listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind pprof server on %s: %w", addr, err)
	}

	profiler := &replayPProfServer{
		addr: listener.Addr().String(),
		server: &http.Server{
			Addr:              addr,
			Handler:           observability.PProfHandler(),
			ReadHeaderTimeout: 5 * time.Second,
		},
		done: make(chan error, 1),
	}
	enable()
	go profiler.serve(cancel, listener)
	return profiler, nil
}

func (p *replayPProfServer) Addr() string {
	return p.addr
}

func (p *replayPProfServer) serve(
	cancel context.CancelCauseFunc,
	listener net.Listener,
) {
	err := p.server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		err = fmt.Errorf("pprof server on %s failed: %w", p.addr, err)
		cancel(err)
	} else {
		err = nil
	}
	p.done <- err
}

func (p *replayPProfServer) Shutdown() error {
	if p == nil {
		return nil
	}
	p.shutdownOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), replayPProfShutdownTimeout)
		defer cancel()

		var errs []error
		if err := observability.ShutdownHTTPServer(ctx, p.server); err != nil {
			errs = append(errs, fmt.Errorf("shutdown pprof server: %w", err))
		}
		select {
		case serveErr := <-p.done:
			if serveErr != nil {
				errs = append(errs, serveErr)
			}
		case <-ctx.Done():
			errs = append(errs, errors.New("timed out waiting for pprof server to stop"))
		}
		p.shutdownErr = errors.Join(errs...)
	})
	return p.shutdownErr
}

func replayPProfAllowUnsafe(value string) (bool, error) {
	switch value {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, errors.New(`GOXRPL_PPROF_ALLOW_UNSAFE must be "true" or "false"`)
	}
}

func normalizeReplayPProfAddress(raw string, allowUnsafe bool) (string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("invalid GOXRPL_PPROF address %q: %w", raw, err)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return "", fmt.Errorf("invalid GOXRPL_PPROF address %q: port must be a number from 0 to 65535", raw)
	}

	if host == "" {
		host = "127.0.0.1"
	} else if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if !allowUnsafe {
			if ip.To4() != nil {
				host = "127.0.0.1"
			} else {
				host = "::1"
			}
		}
	}
	if !allowUnsafe && !isReplayPProfLoopback(host) {
		return "", fmt.Errorf(
			"GOXRPL_PPROF address %q is not loopback; set GOXRPL_PPROF_ALLOW_UNSAFE=true to expose pprof",
			raw,
		)
	}
	return net.JoinHostPort(host, strconv.FormatUint(portNumber, 10)), nil
}

func isReplayPProfLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
