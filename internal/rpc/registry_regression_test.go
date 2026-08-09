package rpc

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func TestRemovedShardMethodsAreUnknownCommands(t *testing.T) {
	registry := types.NewMethodRegistry()
	handlers.RegisterAll(registry)
	for _, name := range []string{"download_shard", "crawl_shards"} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("removed RPC method %q is still registered", name)
		}
		result, rpcErr := dispatchNestedMethod(registry, &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}, name, nil, rpcLog())
		if result != nil || rpcErr == nil || rpcErr.ErrorString != "unknownCmd" {
			t.Fatalf("dispatch %q = (%v, %v), want unknownCmd", name, result, rpcErr)
		}
	}
}
