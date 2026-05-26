package handlers

import (
	"encoding/json"
	"fmt"
	"sort"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// PeersMethod returns peers from ctx.PeerSource (rippled Peers.cpp).
// Empty list when no source is wired (standalone mode). The "cluster"
// field mirrors rippled's doPeers (Peers.cpp:59-80) which always emits
// an object — empty when no [cluster_nodes] are configured.
type PeersMethod struct{ AdminHandler }

func (m *PeersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var (
		peers   []map[string]any
		cluster map[string]any
	)
	if ctx.PeerSource != nil {
		peers = ctx.PeerSource.PeersJSON()
		cluster = ctx.PeerSource.ClusterJSON()
	}
	if peers == nil {
		peers = []map[string]any{}
	}
	if cluster == nil {
		cluster = map[string]any{}
	}
	return map[string]any{
		"peers":   peers,
		"cluster": cluster,
	}, nil
}

// PeerReservationsAddMethod handles the peer_reservations_add RPC method.
// Mirrors rippled Reservations.cpp doPeerReservationsAdd: inserts or replaces a
// reservation for a base58 NodePublic key, returning the previous reservation
// (if any) under "previous". Empty result when no overlay is wired.
type PeerReservationsAddMethod struct{ AdminHandler }

func (m *PeerReservationsAddMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	fields, rpcErr := objectParams(params)
	if rpcErr != nil {
		return nil, rpcErr
	}

	pkStr, rpcErr := requiredStringField(fields, "public_key")
	if rpcErr != nil {
		return nil, rpcErr
	}
	desc, rpcErr := optionalStringField(fields, "description")
	if rpcErr != nil {
		return nil, rpcErr
	}

	key, rpcErr := parseNodePublic(pkStr)
	if rpcErr != nil {
		return nil, rpcErr
	}

	result := map[string]interface{}{}
	if ctx.Services != nil && ctx.Services.PeerReservationAdd != nil {
		prevDesc, replaced, err := ctx.Services.PeerReservationAdd(key, desc)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to persist peer reservation: " + err.Error())
		}
		if replaced {
			result["previous"] = reservationJSON(key, prevDesc)
		}
	}
	return result, nil
}

// PeerReservationsDelMethod handles the peer_reservations_del RPC method.
// Mirrors rippled doPeerReservationsDel: removes a reservation by base58
// NodePublic key, returning the erased reservation (if any) under "previous".
type PeerReservationsDelMethod struct{ AdminHandler }

func (m *PeerReservationsDelMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	fields, rpcErr := objectParams(params)
	if rpcErr != nil {
		return nil, rpcErr
	}

	pkStr, rpcErr := requiredStringField(fields, "public_key")
	if rpcErr != nil {
		return nil, rpcErr
	}

	key, rpcErr := parseNodePublic(pkStr)
	if rpcErr != nil {
		return nil, rpcErr
	}

	result := map[string]interface{}{}
	if ctx.Services != nil && ctx.Services.PeerReservationDel != nil {
		prevDesc, existed, err := ctx.Services.PeerReservationDel(key)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to persist peer reservation: " + err.Error())
		}
		if existed {
			result["previous"] = reservationJSON(key, prevDesc)
		}
	}
	return result, nil
}

// PeerReservationsListMethod handles the peer_reservations_list RPC method.
// Mirrors rippled doPeerReservationsList: returns all reservations under
// "reservations". Empty when no overlay is wired.
type PeerReservationsListMethod struct{ AdminHandler }

func (m *PeerReservationsListMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	reservations := make([]interface{}, 0)
	if ctx.Services != nil && ctx.Services.PeerReservationList != nil {
		entries := ctx.Services.PeerReservationList()
		// Rippled's PeerReservationTable::list() sorts ascending by nodeId
		// (PeerReservationTable.cpp:57) so the wire order is deterministic.
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].NodePublic < entries[j].NodePublic
		})
		for _, r := range entries {
			reservations = append(reservations, reservationJSON(r.NodePublic, r.Description))
		}
	}
	return map[string]interface{}{
		"reservations": reservations,
	}, nil
}

// objectParams decodes the request params into a field map. Absent params are
// treated as an empty object so that missing required fields are diagnosed by
// requiredStringField rather than here.
func objectParams(params json.RawMessage) (map[string]json.RawMessage, *types.RpcError) {
	fields := map[string]json.RawMessage{}
	if len(params) == 0 {
		return fields, nil
	}
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
	}
	return fields, nil
}

// requiredStringField mirrors rippled's missing_field_error / expected_field_error
// pattern: absent field → missing_field_error, present-but-not-string →
// expected_field_error(name, "a string").
func requiredStringField(fields map[string]json.RawMessage, name string) (string, *types.RpcError) {
	raw, ok := fields[name]
	if !ok {
		return "", types.RpcErrorMissingField(name)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", types.RpcErrorExpectedField(name, "a string")
	}
	return s, nil
}

// optionalStringField returns "" when the field is absent and an
// expected_field_error when present but not a string, matching rippled's
// "if field F is present, make sure it has type T" handling.
func optionalStringField(fields map[string]json.RawMessage, name string) (string, *types.RpcError) {
	raw, ok := fields[name]
	if !ok {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", types.RpcErrorExpectedField(name, "a string")
	}
	return s, nil
}

// parseNodePublic validates a base58 NodePublic key and returns its canonical
// encoding (matching what the overlay stores), mirroring rippled's
// parseBase58<PublicKey>(TokenType::NodePublic, ...): a key that fails to decode,
// has the wrong length, or carries an invalid key-type byte yields
// rpcPUBLIC_MALFORMED (Reservations.cpp:73-74).
func parseNodePublic(publicKey string) (string, *types.RpcError) {
	raw, err := addresscodec.DecodeNodePublicKey(publicKey)
	if err != nil || !validPublicKeyType(raw) {
		return "", types.RpcErrorPublicMalformed()
	}
	canonical, err := addresscodec.EncodeNodePublicKey(raw)
	if err != nil {
		return "", types.RpcErrorPublicMalformed()
	}
	return canonical, nil
}

// validPublicKeyType mirrors rippled's publicKeyType (PublicKey.cpp:224-236):
// 33 bytes with a leading byte of 0xED (ed25519) or 0x02/0x03 (secp256k1).
func validPublicKeyType(raw []byte) bool {
	return len(raw) == 33 && (raw[0] == 0xED || raw[0] == 0x02 || raw[0] == 0x03)
}

// reservationJSON renders a reservation the way rippled's PeerReservation::toJson
// does: a "node" key plus an optional "description".
func reservationJSON(nodePublic, description string) map[string]interface{} {
	entry := map[string]interface{}{"node": nodePublic}
	if description != "" {
		entry["description"] = description
	}
	return entry
}
