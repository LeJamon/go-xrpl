package handlers

import (
	"encoding/json"
	"fmt"

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
	var request struct {
		PublicKey   string `json:"public_key"`
		Description string `json:"description,omitempty"`
	}
	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	key, rpcErr := parseNodePublic(request.PublicKey)
	if rpcErr != nil {
		return nil, rpcErr
	}

	result := map[string]interface{}{}
	if ctx.Services != nil && ctx.Services.PeerReservationAdd != nil {
		if prevDesc, replaced := ctx.Services.PeerReservationAdd(key, request.Description); replaced {
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
	var request struct {
		PublicKey string `json:"public_key"`
	}
	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	key, rpcErr := parseNodePublic(request.PublicKey)
	if rpcErr != nil {
		return nil, rpcErr
	}

	result := map[string]interface{}{}
	if ctx.Services != nil && ctx.Services.PeerReservationDel != nil {
		if prevDesc, existed := ctx.Services.PeerReservationDel(key); existed {
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
		for _, r := range ctx.Services.PeerReservationList() {
			reservations = append(reservations, reservationJSON(r.NodePublic, r.Description))
		}
	}
	return map[string]interface{}{
		"reservations": reservations,
	}, nil
}

// parseNodePublic validates a base58 NodePublic key and returns its canonical
// encoding (matching what the overlay stores), mirroring rippled's
// parseBase58<PublicKey>(TokenType::NodePublic, ...).
func parseNodePublic(publicKey string) (string, *types.RpcError) {
	if publicKey == "" {
		return "", types.RpcErrorMissingField("public_key")
	}
	raw, err := addresscodec.DecodeNodePublicKey(publicKey)
	if err != nil {
		return "", types.RpcErrorInvalidField("public_key")
	}
	canonical, err := addresscodec.EncodeNodePublicKey(raw)
	if err != nil {
		return "", types.RpcErrorInvalidField("public_key")
	}
	return canonical, nil
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
