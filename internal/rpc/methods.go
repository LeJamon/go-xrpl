package rpc

import (
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
)

// registerAllMethods registers every XRPL RPC method on the server's
// registry. The canonical list lives in handlers.RegisterAll so the HTTP
// server, the WebSocket server, and the CLI client share one source of
// truth.
func (s *Server) registerAllMethods() {
	handlers.RegisterAll(s.registry)
}
