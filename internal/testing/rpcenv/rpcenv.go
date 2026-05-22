// Package rpcenv wires the in-memory test ledger built by internal/testing
// into the same RPC handler registry the production server uses, so
// handlers can be exercised end-to-end against real ledger state. Mirrors
// rippled's jtx::Env.
package rpcenv

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	xrpltesting "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/protocol"
)

// Env pairs a live testing.TestEnv with the production RPC handler
// registry. Embedding TestEnv keeps every fund/submit/close/query helper
// available alongside RPC dispatch.
type Env struct {
	*xrpltesting.TestEnv

	t        testing.TB
	services *types.ServiceContainer
	registry *types.MethodRegistry
}

func New(t testing.TB) *Env {
	t.Helper()
	return Wrap(t, xrpltesting.NewTestEnv(t))
}

// Wrap layers RPC dispatch on top of an existing TestEnv — for fixtures
// with custom genesis, TxQ, etc.
func Wrap(t testing.TB, env *xrpltesting.TestEnv) *Env {
	t.Helper()
	registry := types.NewMethodRegistry()
	handlers.RegisterAll(registry)
	adapter := newLedgerAdapter(env)
	return &Env{
		TestEnv:  env,
		t:        t,
		services: types.NewServiceContainer(adapter),
		registry: registry,
	}
}

// Services exposes the container so callers can attach additional facets
// (manifest cache, validator-list reader, ...) for tests that exercise
// admin/manifest handlers.
func (e *Env) Services() *types.ServiceContainer { return e.services }

// RPC dispatches a method through the production registry. Defaults to
// admin role to match rippled jtx::Env's local-loopback rpcClient, which
// authenticates via admin_user/admin_password (see rippled
// RPCCall.cpp:1530). Use RPCAs to downgrade. params may be a struct, a
// map, or a json.RawMessage — anything else is marshaled to JSON.
func (e *Env) RPC(method string, params any) (any, *types.RpcError) {
	return e.RPCAs(method, params, types.RoleAdmin, types.DefaultApiVersion)
}

// RPCAs is RPC with explicit role/version control.
func (e *Env) RPCAs(method string, params any, role types.Role, apiVersion int) (any, *types.RpcError) {
	e.t.Helper()

	handler, ok := e.registry.Get(method)
	if !ok {
		return nil, types.RpcErrorMethodNotFound(method)
	}

	var raw json.RawMessage
	switch v := params.(type) {
	case nil:
		raw = json.RawMessage("{}")
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	default:
		b, err := json.Marshal(params)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("rpcenv: marshal params: " + err.Error())
		}
		raw = b
	}

	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       role,
		ApiVersion: apiVersion,
		IsAdmin:    role == types.RoleAdmin,
		Unlimited:  role.IsUnlimited(),
		Services:   e.services,
	}
	return handler.Handle(ctx, raw)
}

func rippleEpochSeconds(t time.Time) int64 {
	return t.Unix() - protocol.RippleEpochUnix
}
