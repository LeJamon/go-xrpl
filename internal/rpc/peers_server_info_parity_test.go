package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubPeerSource struct {
	peers   []map[string]any
	cluster map[string]any
}

func (s *stubPeerSource) PeersJSON() []map[string]any { return s.peers }
func (s *stubPeerSource) ClusterJSON() map[string]any { return s.cluster }
func (s *stubPeerSource) PeerCount() int              { return len(s.peers) }

func TestPeersAndServerInfoShareSource(t *testing.T) {
	src := &stubPeerSource{
		peers: []map[string]any{
			{"address": "192.0.2.1:51235", "public_key": "nHB1"},
			{"address": "192.0.2.2:51235", "public_key": "nHB2"},
			{"address": "192.0.2.3:51235", "public_key": "nHB3"},
		},
	}

	ledger := &mockLedgerService{}
	services := &types.ServiceContainer{Ledger: ledger}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    true,
		PeerSource: src,
		Services:   services,
	}

	peersRes, err := (&handlers.PeersMethod{}).Handle(ctx, nil)
	require.Nil(t, err)
	infoRes, err := (&handlers.ServerInfoMethod{}).Handle(ctx, nil)
	require.Nil(t, err)

	peersList := peersRes.(map[string]any)["peers"].([]map[string]any)
	infoPeers := serverInfoPeerCount(t, infoRes)

	assert.Equal(t, len(src.peers), infoPeers,
		"server_info.peers must reflect the live overlay count")
	assert.Equal(t, len(peersList), infoPeers,
		"len(peers RPC result) must equal server_info.peers — single source of truth (issue #419)")
}

func TestPeersAndServerInfoBothEmptyWithoutSource(t *testing.T) {
	ledger := &mockLedgerService{}
	services := &types.ServiceContainer{Ledger: ledger}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    true,
		Services:   services,
	}

	peersRes, err := (&handlers.PeersMethod{}).Handle(ctx, nil)
	require.Nil(t, err)
	infoRes, err := (&handlers.ServerInfoMethod{}).Handle(ctx, nil)
	require.Nil(t, err)

	peersList := peersRes.(map[string]any)["peers"].([]map[string]any)
	infoPeers := serverInfoPeerCount(t, infoRes)

	assert.Equal(t, 0, infoPeers)
	assert.Equal(t, 0, len(peersList))
}

func serverInfoPeerCount(t *testing.T, infoRes any) int {
	t.Helper()
	raw, err := json.Marshal(infoRes)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(raw, &parsed))
	info := parsed["info"].(map[string]any)
	return int(info["peers"].(float64))
}
