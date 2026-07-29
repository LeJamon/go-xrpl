package observability

import (
	"context"
	"errors"
	"net/http"
)

// ShutdownHTTPServer bounds both graceful shutdown and the forced-close fallback
// even when a listener does not return promptly from Close.
func ShutdownHTTPServer(ctx context.Context, server *http.Server) error {
	shutdownResult := make(chan error, 1)
	go func() {
		shutdownResult <- server.Shutdown(ctx)
	}()

	select {
	case err := <-shutdownResult:
		if err == nil {
			return nil
		}
		closeResult := make(chan error, 1)
		go func() {
			closeResult <- server.Close()
		}()
		select {
		case closeErr := <-closeResult:
			return errors.Join(err, closeErr)
		case <-ctx.Done():
			return errors.Join(err, context.Cause(ctx))
		}
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
