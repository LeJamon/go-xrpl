package rpc

import (
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
)

func (s *Server) registerAllMethods() {
	handlers.RegisterAll(s.registry)
}
