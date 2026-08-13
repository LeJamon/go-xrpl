package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// FetchInfoMethod handles the fetch_info RPC method. Mirrors rippled
// FetchInfo.cpp: reports the ledgers currently being acquired from peers
// (NetworkOPs::getLedgerFetchInfo → InboundLedgers::getInfo), and honors the
// `clear` param by resetting the acquisition counters. The `info` object is
// empty when the node isn't acquiring (e.g. standalone / RPC-only), which is
// rippled's behavior too.
type FetchInfoMethod struct{ adminHandler }

func (m *FetchInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	var request struct {
		Clear jsonCppBoolField `json:"clear"`
	}
	if rpcErr := decodeRequestObject(params, &request); rpcErr != nil {
		return nil, rpcErr
	}

	response := make(map[string]any)

	if request.Clear.value {
		if ctx.Services != nil && ctx.Services.FetchInfoClear() != nil {
			ctx.Services.FetchInfoClear()()
		}
		response["clear"] = true
	}

	info := map[string]any{}
	if ctx.Services != nil && ctx.Services.FetchInfo() != nil {
		if snap := ctx.Services.FetchInfo()(); snap != nil {
			info = snap
		}
	}
	response["info"] = info

	return response, nil
}

// TxReduceRelayMethod handles the tx_reduce_relay RPC method.
// Mirrors rippled TxReduceRelay.cpp (returns overlay().txMetrics()): the
// txr_* rolling-average metrics from rippled metrics::TxMetrics, emitted as
// decimal strings. go-xrpl feeds the inbound TMTransaction / TMHaveTransactions
// / TMTransactions counts and the missing-tx frequency; the getLedger /
// ledgerData and peer-selection averages are reported as 0 until those
// subsystems exist (see peermanagement.txMetrics). Zeros throughout when no
// overlay is wired (standalone / RPC-only).
type TxReduceRelayMethod struct{ baseHandler }

func (m *TxReduceRelayMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	var metrics types.TxReduceRelayMetrics
	if ctx.Services != nil && ctx.Services.TxReduceRelayMetrics() != nil {
		metrics = ctx.Services.TxReduceRelayMetrics()()
	}
	return metrics.JSON(), nil
}

func (m *TxReduceRelayMethod) RequiredRole() types.Role {
	return types.RoleUser // rippled: Role::USER (Handler.cpp line 179)
}

// ConnectMethod handles the connect RPC method. A live runtime admits the
// request to its bounded peer-connect scheduler and returns immediately.
type ConnectMethod struct{ adminHandler }

func (m *ConnectMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	// The ledger invariant and standalone guard intentionally precede request
	// decoding. This preserves rippled's notSynced result for every standalone
	// request, including malformed parameters.
	if ctx.Services == nil || ctx.Services.Ledger() == nil {
		return nil, rpcInternalInvariantError("connect: ledger service unavailable")
	}
	if ctx.Services.Ledger().IsStandalone() {
		return nil, rpcerrors.RpcErrorNotSynced("")
	}

	request, rpcErr := decodeConnectRequest(params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	ip, ok := parseConnectEndpoint(request.ip)
	if !ok || ip.IsUnspecified() {
		return connectMessage(request.ip, request.port), nil
	}
	if ctx.Services.PeerConnect() == nil {
		return nil, rpcerrors.RpcErrorNotEnabled("")
	}

	addr := netip.AddrPortFrom(ip, uint16(request.port)).String()
	if err := ctx.Services.PeerConnect()(addr); err != nil {
		switch {
		case errors.Is(err, types.ErrPeerConnectQueueFull):
			return nil, rpcerrors.RpcErrorTooBusy()
		case errors.Is(err, types.ErrPeerConnectClosed), errors.Is(err, types.ErrPeerConnectUnavailable):
			return nil, rpcerrors.RpcErrorNotEnabled("")
		default:
			return nil, rpcInternalError("connect: enqueue failed", err)
		}
	}
	return connectMessage(request.ip, request.port), nil
}

func parseConnectEndpoint(value string) (netip.Addr, bool) {
	if len(value) > 64 {
		return netip.Addr{}, false
	}

	value = strings.TrimSpace(value)
	if ip, err := netip.ParseAddr(value); err == nil {
		return ip, true
	}
	if endpoint, err := netip.ParseAddrPort(value); err == nil {
		return endpoint.Addr(), true
	}
	if ip, ok := parseBracketedConnectEndpoint(value); ok {
		return ip, true
	}
	if strings.HasSuffix(value, ":") {
		if ip, err := netip.ParseAddr(strings.TrimSuffix(value, ":")); err == nil {
			return ip, true
		}
	}

	fields := strings.Fields(value)
	if len(fields) != 2 {
		return netip.Addr{}, false
	}
	if _, err := strconv.ParseUint(fields[1], 10, 16); err != nil {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(fields[0])
	return ip, err == nil
}

func parseBracketedConnectEndpoint(value string) (netip.Addr, bool) {
	if len(value) < 2 || value[0] != '[' {
		return netip.Addr{}, false
	}
	closeBracket := strings.IndexByte(value, ']')
	if closeBracket < 2 {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(value[1:closeBracket])
	if err != nil {
		return netip.Addr{}, false
	}

	suffix := value[closeBracket+1:]
	if suffix == "" {
		return ip, true
	}
	if suffix == ":" {
		return ip, true
	}
	if suffix[0] != ':' && strings.TrimSpace(suffix[:1]) != "" {
		return netip.Addr{}, false
	}
	if _, err := strconv.ParseUint(strings.TrimSpace(suffix[1:]), 10, 16); err != nil {
		return netip.Addr{}, false
	}
	return ip, true
}

type connectRequest struct {
	ip   string
	port int
}

func decodeConnectRequest(params json.RawMessage) (connectRequest, *rpcerrors.RpcError) {
	fields := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(params)) != 0 {
		if err := json.Unmarshal(params, &fields); err != nil {
			return connectRequest{}, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
	}

	rawIP, ok := fields["ip"]
	if !ok {
		return connectRequest{}, rpcerrors.RpcErrorMissingField("ip")
	}

	port := 51235
	if rawPort, supplied := fields["port"]; supplied {
		var rpcErr *rpcerrors.RpcError
		port, rpcErr = parseConnectPort(rawPort)
		if rpcErr != nil {
			return connectRequest{}, rpcErr
		}
	}
	ip, rpcErr := parseConnectIP(rawIP)
	if rpcErr != nil {
		return connectRequest{}, rpcErr
	}
	return connectRequest{ip: ip, port: port}, nil
}

func parseConnectIP(raw json.RawMessage) (string, *rpcerrors.RpcError) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	switch value := value.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	case bool:
		return strconv.FormatBool(value), nil
	case json.Number:
		if !strings.ContainsAny(value.String(), ".eE") {
			return value.String(), nil
		}
		number, err := strconv.ParseFloat(value.String(), 64)
		if err != nil {
			return "", rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
		return strconv.FormatFloat(number, 'f', 6, 64), nil
	default:
		return "", rpcInternalError("connect: ip coercion failed", errors.New("value cannot be converted to a string"))
	}
}

func parseConnectPort(raw json.RawMessage) (int, *rpcerrors.RpcError) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	switch value := value.(type) {
	case nil:
		return 0, nil
	case bool:
		if value {
			return 1, nil
		}
		return 0, nil
	case json.Number:
		if port, err := strconv.ParseInt(value.String(), 10, 64); err == nil {
			if port >= math.MinInt32 && port <= math.MaxInt32 {
				return int(port), nil
			}
			return 0, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
		if port, err := strconv.ParseFloat(value.String(), 64); err == nil &&
			port >= math.MinInt32 && port <= math.MaxInt32 {
			return int(port), nil
		}
	}
	return 0, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
}

// connectMessage formats the reply rippled returns from doConnect
// (Connect.cpp:68-70).
func connectMessage(ip string, port int) map[string]any {
	return map[string]any{
		"message": fmt.Sprintf("attempting connection to IP:%s port: %d", ip, port),
	}
}

// UnlListMethod handles the unl_list RPC method.
// Mirrors rippled UNLList.cpp doUnlList: iterates every listed validator
// (ValidatorList::for_each_listed) and emits a {pubkey_validator, trusted}
// entry, where trusted reflects whether the key is in the effective UNL. With
// no publisher-trust subsystem configured (e.g. standalone) the list is empty.
type UnlListMethod struct{ adminHandler }

func (m *UnlListMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	unl := make([]any, 0)
	if ctx.Services != nil && ctx.Services.ValidatorList() != nil {
		for _, v := range ctx.Services.ValidatorList().ListedValidators() {
			enc, err := addresscodec.EncodeNodePublicKey(v.MasterKey[:])
			if err != nil {
				continue
			}
			unl = append(unl, map[string]any{
				"pubkey_validator": enc,
				"trusted":          v.Trusted,
			})
		}
	}

	return map[string]any{
		"unl": unl,
	}, nil
}

// BlackListMethod handles the black_list (blacklist) RPC method.
// Mirrors rippled BlackList.cpp: returns the overlay resource manager's
// per-endpoint reputation table, optionally filtered by a `threshold` score.
// The response is keyed by endpoint address (rippled returns the getJson
// object directly). Empty when no overlay is wired (standalone / RPC-only).
type BlackListMethod struct{ adminHandler }

func (m *BlackListMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	var request struct {
		Threshold *int `json:"threshold,omitempty"`
	}
	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	if ctx.Services != nil && ctx.Services.ResourceBlacklist() != nil {
		if result := ctx.Services.ResourceBlacklist()(request.Threshold); result != nil {
			return result, nil
		}
	}

	return map[string]any{}, nil
}
