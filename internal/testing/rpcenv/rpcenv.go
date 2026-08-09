// Package rpcenv wires the in-memory test ledger built by internal/testing
// into the same RPC handler registry the production server uses, so
// handlers can be exercised end-to-end against real ledger state. Mirrors
// rippled's jtx::Env.
package rpcenv

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

// Env pairs a live testing.TestEnv with the production RPC handler
// registry. Embedding TestEnv keeps every fund/submit/close/query helper
// available alongside RPC dispatch.
type Env struct {
	*jtx.TestEnv

	t        testing.TB
	adapter  *ledgerAdapter
	services *types.ServiceContainer
	registry *types.MethodRegistry
}

func New(t testing.TB) *Env {
	t.Helper()
	return Wrap(t, jtx.NewTestEnv(t))
}

// Wrap layers RPC dispatch on top of an existing TestEnv — for fixtures
// with custom genesis, TxQ, etc.
func Wrap(t testing.TB, env *jtx.TestEnv) *Env {
	t.Helper()
	registry, err := handlers.BuildRegistry()
	if err != nil {
		t.Fatalf("build RPC registry: %v", err)
	}
	adapter := newLedgerAdapter(env)
	services := types.NewServiceContainer(adapter)
	services.Capabilities.PathSearchMax = 3
	return &Env{
		TestEnv:  env,
		t:        t,
		adapter:  adapter,
		services: services,
		registry: registry,
	}
}

func (e *Env) Close() {
	e.TestEnv.Close()
	e.adapter.recordClosedLedger()
}

func (e *Env) Submit(transaction any) jtx.TxResult {
	e.t.Helper()
	result := e.TestEnv.Submit(transaction)
	if result.Metadata == nil || (!result.Success && !result.IsClaimed()) {
		return result
	}
	txn, ok := transaction.(txcore.Transaction)
	if !ok {
		e.t.Fatalf("rpcenv: transaction does not implement tx.Transaction")
	}
	txBlob, err := txcore.SerializeTransaction(txn)
	if err != nil {
		e.t.Fatalf("rpcenv: serialize transaction: %v", err)
	}
	hash, err := txcore.ComputeTransactionHash(txn)
	if err != nil {
		e.t.Fatalf("rpcenv: hash transaction: %v", err)
	}
	txWithMeta, err := txcore.CreateTxWithMetaBlob(txBlob, result.Metadata)
	if err != nil {
		e.t.Fatalf("rpcenv: serialize transaction metadata: %v", err)
	}
	if err := e.Ledger().AddTransactionWithMeta(hash, txWithMeta); err != nil {
		e.t.Fatalf("rpcenv: record transaction: %v", err)
	}
	return result
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
		return nil, types.RpcErrorMethodNotFound()
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
		Services:   types.NewTestServiceGraph(e.services),
	}
	return handler.Handle(ctx, raw)
}
