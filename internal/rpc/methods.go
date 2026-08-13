package rpc

import (
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func defaultMethodRegistry() *types.MethodRegistry {
	registry, err := handlers.BuildRegistry()
	if err != nil {
		panic(err)
	}
	return registry
}
