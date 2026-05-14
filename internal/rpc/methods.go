package rpc

import (
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
)

func (s *Server) registerAllMethods() {
	handlers.RegisterAll(s.registry)
}
