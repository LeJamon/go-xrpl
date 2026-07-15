package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
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
		PeerReservationAdd: func(key, desc string) (string, bool, error) {
			prev, ok := m[key]
			m[key] = desc
			return prev, ok, nil
		},
		PeerReservationDel: func(key string) (string, bool, error) {
			prev, ok := m[key]
			if ok {
				delete(m, key)
			}
			return prev, ok, nil
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
		assert.Equal(t, "Missing field 'public_key'.", rpcErr.Message)
	})

	// rippled returns rpcPUBLIC_MALFORMED (not rpcINVALID_PARAMS) for a key that
	// fails to parse — Reservations.cpp:73-74 → rpcError(rpcPUBLIC_MALFORMED).
	t.Run("malformed public_key", func(t *testing.T) {
		_, rpcErr := method.Handle(ctx, json.RawMessage(`{"public_key":"not-a-node-key"}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcPUBLIC_MALFORMED, rpcErr.Code)
		assert.Equal(t, "Public key is malformed.", rpcErr.Message)
	})

	// An empty string is a valid string (passes rippled's isString check) but
	// fails parseBase58 → rpcPUBLIC_MALFORMED, not a missing-field error.
	t.Run("empty public_key", func(t *testing.T) {
		_, rpcErr := method.Handle(ctx, json.RawMessage(`{"public_key":""}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcPUBLIC_MALFORMED, rpcErr.Code)
	})

	// A 33-byte NodePublic-prefixed blob with an invalid key-type byte is
	// rejected by rippled's publicKeyType (PublicKey.cpp:224-236).
	t.Run("invalid key-type byte", func(t *testing.T) {
		raw := make([]byte, 33)
		raw[0] = 0x05 // neither 0xED nor 0x02/0x03
		enc := addresscodec.Base58CheckEncode(raw, addresscodec.NodePublicKeyPrefix)
		body, _ := json.Marshal(map[string]any{"public_key": enc})
		_, rpcErr := method.Handle(ctx, body)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcPUBLIC_MALFORMED, rpcErr.Code)
	})

	// rippled diagnoses a non-string public_key with expected_field_error.
	t.Run("non-string public_key", func(t *testing.T) {
		_, rpcErr := method.Handle(ctx, json.RawMessage(`{"public_key":123}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid field 'public_key', not a string.", rpcErr.Message)
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
	assert.NotContains(t, res1.(map[string]any), "previous")

	// Replace: previous is reported with the prior description.
	p2, _ := json.Marshal(map[string]any{"public_key": key, "description": "second"})
	res2, rpcErr := add.Handle(ctx, p2)
	require.Nil(t, rpcErr)
	prev := res2.(map[string]any)["previous"].(map[string]any)
	assert.Equal(t, key, prev["node"])
	assert.Equal(t, "first", prev["description"])

	// List reflects the current entry.
	resL, rpcErr := list.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	entries := resL.(map[string]any)["reservations"].([]any)
	require.Len(t, entries, 1)
	entry := entries[0].(map[string]any)
	assert.Equal(t, key, entry["node"])
	assert.Equal(t, "second", entry["description"])

	// Delete returns the removed entry and empties the list.
	pd, _ := json.Marshal(map[string]any{"public_key": key})
	resD, rpcErr := del.Handle(ctx, pd)
	require.Nil(t, rpcErr)
	delPrev := resD.(map[string]any)["previous"].(map[string]any)
	assert.Equal(t, "second", delPrev["description"])

	resL2, _ := list.Handle(ctx, nil)
	assert.Empty(t, resL2.(map[string]any)["reservations"].([]any))
}

// rippled's PeerReservationTable::list() sorts ascending by nodeId
// (PeerReservationTable.cpp:57); the handler must reproduce that order
// regardless of the backend's iteration order.
func TestPeerReservationsListSorted(t *testing.T) {
	svc := reservationServices()
	ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleAdmin, Services: svc}
	add := &handlers.PeerReservationsAddMethod{}

	for _, seed := range []byte{40, 10, 30, 20} {
		body, _ := json.Marshal(map[string]any{"public_key": testNodePublic(t, seed)})
		_, rpcErr := add.Handle(ctx, body)
		require.Nil(t, rpcErr)
	}

	resL, rpcErr := (&handlers.PeerReservationsListMethod{}).Handle(ctx, nil)
	require.Nil(t, rpcErr)
	entries := resL.(map[string]any)["reservations"].([]any)
	require.Len(t, entries, 4)

	prev := ""
	for i, e := range entries {
		node := e.(map[string]any)["node"].(string)
		if i > 0 {
			assert.Less(t, prev, node, "reservations must be sorted ascending by node")
		}
		prev = node
	}
}

// A persistence failure surfaces as an internal error, mirroring rippled's
// insert_or_assign throwing on a failed DB write.
func TestPeerReservationsAddPersistenceError(t *testing.T) {
	ctx := &types.RpcContext{
		Context: context.Background(),
		Role:    types.RoleAdmin,
		Services: &types.ServiceContainer{
			PeerReservationAdd: func(string, string) (string, bool, error) {
				return "", false, errors.New("disk full")
			},
		},
	}
	body, _ := json.Marshal(map[string]any{"public_key": testNodePublic(t, 3)})
	_, rpcErr := (&handlers.PeerReservationsAddMethod{}).Handle(ctx, body)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
}

func TestPeerReservationsEmptyWhenUnwired(t *testing.T) {
	ctx := &types.RpcContext{Context: context.Background(), Role: types.RoleAdmin, Services: &types.ServiceContainer{}}

	// list returns an empty array, and add is a no-op that reports no previous.
	resL, rpcErr := (&handlers.PeerReservationsListMethod{}).Handle(ctx, nil)
	require.Nil(t, rpcErr)
	assert.Empty(t, resL.(map[string]any)["reservations"].([]any))

	p, _ := json.Marshal(map[string]any{"public_key": testNodePublic(t, 9)})
	resA, rpcErr := (&handlers.PeerReservationsAddMethod{}).Handle(ctx, p)
	require.Nil(t, rpcErr)
	assert.NotContains(t, resA.(map[string]any), "previous")
}
