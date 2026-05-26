package rpc

import (
	"context"
	"encoding/json"
	"testing"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testNodePublic(t *testing.T, seed byte) string {
	t.Helper()
	raw := make([]byte, 33)
	raw[0] = 0xED
	for i := 1; i < 33; i++ {
		raw[i] = seed + byte(i)
	}
	enc, err := addresscodec.EncodeNodePublicKey(raw)
	require.NoError(t, err)
	return enc
}

// reservationServices builds a ServiceContainer whose peer_reservations_*
// closures are backed by an in-memory map, standing in for the overlay table.
func reservationServices() *types.ServiceContainer {
	m := map[string]string{}
	return &types.ServiceContainer{
		PeerReservationAdd: func(key, desc string) (string, bool) {
			prev, ok := m[key]
			m[key] = desc
			return prev, ok
		},
		PeerReservationDel: func(key string) (string, bool) {
			prev, ok := m[key]
			if ok {
				delete(m, key)
			}
			return prev, ok
		},
		PeerReservationList: func() []types.PeerReservationEntry {
			out := make([]types.PeerReservationEntry, 0, len(m))
			for k, d := range m {
				out = append(out, types.PeerReservationEntry{NodePublic: k, Description: d})
			}
			return out
		},
	}
}

func TestPeerReservationsAddValidation(t *testing.T) {
	method := &handlers.PeerReservationsAddMethod{}
	ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleAdmin, Services: &types.ServiceContainer{}}

	t.Run("missing public_key", func(t *testing.T) {
		_, rpcErr := method.Handle(ctx, json.RawMessage(`{}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("malformed public_key", func(t *testing.T) {
		_, rpcErr := method.Handle(ctx, json.RawMessage(`{"public_key":"not-a-node-key"}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})
}

func TestPeerReservationsRoundTrip(t *testing.T) {
	key := testNodePublic(t, 1)
	svc := reservationServices()
	ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleAdmin, Services: svc}

	add := &handlers.PeerReservationsAddMethod{}
	del := &handlers.PeerReservationsDelMethod{}
	list := &handlers.PeerReservationsListMethod{}

	// First add: no previous reservation.
	p1, _ := json.Marshal(map[string]any{"public_key": key, "description": "first"})
	res1, rpcErr := add.Handle(ctx, p1)
	require.Nil(t, rpcErr)
	assert.NotContains(t, res1.(map[string]interface{}), "previous")

	// Replace: previous is reported with the prior description.
	p2, _ := json.Marshal(map[string]any{"public_key": key, "description": "second"})
	res2, rpcErr := add.Handle(ctx, p2)
	require.Nil(t, rpcErr)
	prev := res2.(map[string]interface{})["previous"].(map[string]interface{})
	assert.Equal(t, key, prev["node"])
	assert.Equal(t, "first", prev["description"])

	// List reflects the current entry.
	resL, rpcErr := list.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	entries := resL.(map[string]interface{})["reservations"].([]interface{})
	require.Len(t, entries, 1)
	entry := entries[0].(map[string]interface{})
	assert.Equal(t, key, entry["node"])
	assert.Equal(t, "second", entry["description"])

	// Delete returns the removed entry and empties the list.
	pd, _ := json.Marshal(map[string]any{"public_key": key})
	resD, rpcErr := del.Handle(ctx, pd)
	require.Nil(t, rpcErr)
	delPrev := resD.(map[string]interface{})["previous"].(map[string]interface{})
	assert.Equal(t, "second", delPrev["description"])

	resL2, _ := list.Handle(ctx, nil)
	assert.Empty(t, resL2.(map[string]interface{})["reservations"].([]interface{}))
}

func TestPeerReservationsEmptyWhenUnwired(t *testing.T) {
	ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleAdmin, Services: &types.ServiceContainer{}}

	// list returns an empty array, and add is a no-op that reports no previous.
	resL, rpcErr := (&handlers.PeerReservationsListMethod{}).Handle(ctx, nil)
	require.Nil(t, rpcErr)
	assert.Empty(t, resL.(map[string]interface{})["reservations"].([]interface{}))

	p, _ := json.Marshal(map[string]any{"public_key": testNodePublic(t, 9)})
	resA, rpcErr := (&handlers.PeerReservationsAddMethod{}).Handle(ctx, p)
	require.Nil(t, rpcErr)
	assert.NotContains(t, resA.(map[string]interface{}), "previous")
}
