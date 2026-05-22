// Package rpcenv wires the in-memory test ledger built by internal/testing
// into the same RPC handler registry used by the production server, so
// handlers can be exercised end-to-end against real ledger state.
//
// This mirrors rippled's jtx::Env, which gives tx-engine tests and RPC
// tests one shared in-process Application. The handler registry is the
// same one HTTP/WebSocket dispatch use (handlers.RegisterAll), so
// integration tests catch the class of bug #482 hit — where a handler
// passes its unit tests but reads ledger state in the wrong shape — by
// asserting the response produced against a freshly-built ledger.
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

// Env is a test harness that pairs a live testing.TestEnv with a
// ServiceContainer wired to handle real RPC calls. Embedding TestEnv keeps
// every fund/submit/close/query helper available; the additional RPC method
// dispatches through the production handler registry.
type Env struct {
	*xrpltesting.TestEnv

	t        testing.TB
	services *types.ServiceContainer
	registry *types.MethodRegistry
}

// New constructs an Env on top of a fresh TestEnv.
func New(t testing.TB) *Env {
	t.Helper()
	return Wrap(t, xrpltesting.NewTestEnv(t))
}

// Wrap turns an existing TestEnv into an Env. Useful when a fixture already
// has a custom TestEnv (custom genesis, TxQ, etc.) and the test needs to
// add RPC dispatch on top.
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

// Services returns the underlying container, mainly so callers can attach
// additional facets (manifest cache, validator-list reader, ...) for tests
// that exercise admin/manifest handlers.
func (e *Env) Services() *types.ServiceContainer { return e.services }

// RPC dispatches an RPC method through the production registry. params is
// marshaled to JSON the same way the HTTP server hands it to the handler,
// so callers can pass a struct, a map[string]any, or pre-built RawMessage.
//
// The default RpcContext uses the user role and the default API version —
// enough for any non-admin method.
func (e *Env) RPC(method string, params any) (any, *types.RpcError) {
	return e.RPCAs(method, params, types.RoleUser, types.DefaultApiVersion)
}

// RPCAs is RPC with explicit role/version control for tests that need
// admin or a specific API version.
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

// rippleEpochSeconds converts a time.Time to seconds since the XRPL epoch.
// Kept private to the package — RPC handlers convert at their own boundary.
func rippleEpochSeconds(t time.Time) int64 {
	return t.Unix() - protocol.RippleEpochUnix
}
