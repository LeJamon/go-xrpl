package handlers_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePeerSource struct {
	peers   []map[string]any
	cluster map[string]any
}

func (f *fakePeerSource) PeersJSON() []map[string]any { return f.peers }
func (f *fakePeerSource) ClusterJSON() map[string]any { return f.cluster }

func TestPeersMethod_NilSourceReturnsEmptyList(t *testing.T) {
	m := &handlers.PeersMethod{}
	ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleAdmin, IsAdmin: true}

	result, rpcErr := m.Handle(ctx, json.RawMessage(`{}`))
	require.Nil(t, rpcErr)

	resp, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []map[string]any{}, resp["peers"])
	assert.Equal(t, map[string]any{}, resp["cluster"],
		"rippled doPeers (Peers.cpp:59) always emits a cluster object")
}

func TestPeersMethod_PassesThroughSource(t *testing.T) {
	src := &fakePeerSource{peers: []map[string]any{
		{
			"address":       "192.0.2.1:51235",
			"public_key":    "nHB1...",
			"server_domain": "validator.example.com",
			"ledger":        "ABCD",
		},
	}}
	m := &handlers.PeersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		IsAdmin:    true,
		PeerSource: src,
	}

	result, rpcErr := m.Handle(ctx, json.RawMessage(`{}`))
	require.Nil(t, rpcErr)

	resp, ok := result.(map[string]any)
	require.True(t, ok)
	peers, ok := resp["peers"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, peers, 1)
	assert.Equal(t, "validator.example.com", peers[0]["server_domain"])
	assert.Equal(t, "ABCD", peers[0]["ledger"])
	assert.NotContains(t, peers[0], "closed_ledger", "rippled uses 'ledger' for the closed-ledger hash")
	assert.NotContains(t, peers[0], "inbound", "inbound is only emitted when true")
}

func TestPeersMethod_RelaysClusterMap(t *testing.T) {
	src := &fakePeerSource{
		peers: []map[string]any{
			{
				"address":    "192.0.2.50:51235",
				"public_key": "nHB...",
				"cluster":    true,
				"name":       "primary",
			},
		},
		cluster: map[string]any{
			"nMate1": map[string]any{"tag": "mate-name"},
		},
	}
	m := &handlers.PeersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		IsAdmin:    true,
		PeerSource: src,
	}

	result, rpcErr := m.Handle(ctx, json.RawMessage(`{}`))
	require.Nil(t, rpcErr)

	resp := result.(map[string]any)
	peers := resp["peers"].([]map[string]any)
	require.Len(t, peers, 1)
	assert.Equal(t, true, peers[0]["cluster"])
	assert.Equal(t, "primary", peers[0]["name"])

	cluster := resp["cluster"].(map[string]any)
	mate := cluster["nMate1"].(map[string]any)
	assert.Equal(t, "mate-name", mate["tag"])
}
