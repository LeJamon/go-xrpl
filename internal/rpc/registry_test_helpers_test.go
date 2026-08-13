package rpc

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func mustTestMethodRegistry(t testing.TB, methods map[string]types.MethodHandler) *types.MethodRegistry {
	t.Helper()
	builder := types.NewMethodRegistryBuilder()
	for name, handler := range methods {
		if err := builder.Register(name, handler); err != nil {
			t.Fatalf("register test RPC method %q: %v", name, err)
		}
	}
	registry, err := builder.Build()
	if err != nil {
		t.Fatalf("build test RPC method registry: %v", err)
	}
	return registry
}

func mustTestMethodRegistryWithOverrides(t testing.TB, methods map[string]types.MethodHandler) *types.MethodRegistry {
	t.Helper()
	registry, err := handlers.BuildRegistryWithOverrides(methods)
	if err != nil {
		t.Fatalf("build test RPC method registry with overrides: %v", err)
	}
	return registry
}
