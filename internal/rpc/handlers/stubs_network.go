package handlers

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// FetchInfoMethod handles the fetch_info RPC method.
// STUB: Returns empty info. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Reference: rippled FetchInfo.cpp → context.app.getFetchPack()
//   - Returns info about current fetch operations for missing ledger data
//   - Params: clear (bool) — resets fetch counters
type FetchInfoMethod struct{ AdminHandler }

func (m *FetchInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Clear bool `json:"clear,omitempty"`
	}
	if params != nil {
		_ = json.Unmarshal(params, &request)
	}

	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	response := make(map[string]interface{})
	if request.Clear {
		response["clear"] = true
	}
	response["info"] = map[string]interface{}{}

	return response, nil
}

// LedgerRequestMethod handles the ledger_request RPC method.
// STUB: Returns error. Network-only — requests missing ledgers from peers.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Reference: rippled LedgerRequest.cpp
//   - Triggers a fetch of a specific ledger from the network
//   - In standalone mode, correctly returns notSynced
type LedgerRequestMethod struct{ AdminHandler }

func (m *LedgerRequestMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	if ctx.Services.Ledger.IsStandalone() {
		return nil, types.NewRpcError(types.RpcNOT_SYNCED, "notSynced", "notSynced",
			"Not synced to the network")
	}

	return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"ledger_request is not yet implemented — requires network ledger fetching")
}

// TxReduceRelayMethod handles the tx_reduce_relay RPC method.
// STUB: Returns zero counters. Network-only relay optimization.
//
// TODO [network]: Implement when adding P2P transaction relay.
//   - Reference: rippled TxReduceRelay.cpp
//   - Returns statistics about reduced transaction relay (squelching)
//   - Requires: Transaction relay subsystem with squelch tracking
type TxReduceRelayMethod struct{}

func (m *TxReduceRelayMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	return map[string]interface{}{
		"transactions": map[string]interface{}{
			"total_relayed":   0,
			"total_squelched": 0,
		},
	}, nil
}

func (m *TxReduceRelayMethod) RequiredRole() types.Role {
	return types.RoleUser // rippled: Role::USER (Handler.cpp line 179)
}

func (m *TxReduceRelayMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *TxReduceRelayMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// ConnectMethod handles the connect RPC method. When the overlay is wired it
// initiates a real background outbound connection (rippled Connect.cpp →
// overlay().connect()); otherwise it reports that peers are unavailable.
type ConnectMethod struct{ AdminHandler }

func (m *ConnectMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		IP   string `json:"ip"`
		Port int    `json:"port,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	// When the overlay is wired (consensus mode, i.e. rippled's
	// non-standalone path) initiate a real outbound connection. rippled's
	// overlay().connect() schedules the attempt and returns immediately, so
	// run the handshake in the background and reply right away (Connect.cpp).
	if ctx.Services != nil && ctx.Services.PeerConnect != nil {
		if request.IP == "" {
			return nil, types.RpcErrorInvalidParams("Missing required parameter: ip")
		}
		port := connectPort(request.Port)
		addr := net.JoinHostPort(request.IP, strconv.Itoa(port))
		go func() { _ = ctx.Services.PeerConnect(addr) }()
		return connectMessage(request.IP, port), nil
	}

	// No overlay wired. Mirror rippled's standalone guard, which precedes the
	// ip check (Connect.cpp:41), so connect in standalone reports notSynced
	// regardless of the supplied params.
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}
	if ctx.Services.Ledger.IsStandalone() {
		return nil, types.NewRpcError(types.RpcNOT_SYNCED, "notSynced", "notSynced",
			"Not synced to the network.")
	}
	if request.IP == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: ip")
	}
	return connectMessage(request.IP, connectPort(request.Port)), nil
}

// connectPort applies the default peer port when the caller omits it. rippled
// uses DEFAULT_PEER_PORT (Connect.cpp:60); goXRPL's peer protocol listens on
// 51235 network-wide (peermanagement.DefaultListenAddr and the bootstrap
// hubs), so the connect default mirrors "use the system peer port" with
// goXRPL's deployed value rather than rippled's IANA-registered 2459.
func connectPort(port int) int {
	if port == 0 {
		return 51235
	}
	return port
}

// connectMessage formats the reply rippled returns from doConnect
// (Connect.cpp:68-70).
func connectMessage(ip string, port int) map[string]interface{} {
	return map[string]interface{}{
		"message": fmt.Sprintf("attempting connection to IP:%s port: %d", ip, port),
	}
}

// UnlListMethod handles the unl_list RPC method.
// Mirrors rippled UNLList.cpp doUnlList: iterates every listed validator
// (ValidatorList::for_each_listed) and emits a {pubkey_validator, trusted}
// entry, where trusted reflects whether the key is in the effective UNL. With
// no publisher-trust subsystem configured (e.g. standalone) the list is empty.
type UnlListMethod struct{ AdminHandler }

func (m *UnlListMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	unl := make([]interface{}, 0)
	if ctx.Services != nil && ctx.Services.ValidatorList != nil {
		for _, v := range ctx.Services.ValidatorList.ListedValidators() {
			enc, err := addresscodec.EncodeNodePublicKey(v.MasterKey[:])
			if err != nil {
				continue
			}
			unl = append(unl, map[string]interface{}{
				"pubkey_validator": enc,
				"trusted":          v.Trusted,
			})
		}
	}

	return map[string]interface{}{
		"unl": unl,
	}, nil
}

// BlackListMethod handles the black_list (blacklist) RPC method.
// Mirrors rippled BlackList.cpp: returns the overlay resource manager's
// per-endpoint reputation table, optionally filtered by a `threshold` score.
// The response is keyed by endpoint address (rippled returns the getJson
// object directly). Empty when no overlay is wired (standalone / RPC-only).
type BlackListMethod struct{ AdminHandler }

func (m *BlackListMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Threshold *int `json:"threshold,omitempty"`
	}
	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	if ctx.Services != nil && ctx.Services.ResourceBlacklist != nil {
		if result := ctx.Services.ResourceBlacklist(request.Threshold); result != nil {
			return result, nil
		}
	}

	return map[string]interface{}{}, nil
}
