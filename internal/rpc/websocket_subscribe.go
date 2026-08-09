package rpc

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// defaultBookSnapshotLimit caps the synthetic snapshot returned in the
// subscribe acknowledgement so a noisy market cannot exceed the frame limit.
const defaultBookSnapshotLimit uint32 = 60

func (ws *WebSocketServer) executeSubscribe(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (any, *rpcerrors.RpcError) {
	var request types.SubscriptionRequest
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &request); err != nil {
			return nil, rpcerrors.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
	}
	// url requests are server-to-server (RPCSub) subscriptions: events go
	// to the url's subscriber, not to this WebSocket connection.
	if request.HasURL() {
		if !ctx.Role.IsAdmin() {
			return nil, rpcerrors.RpcErrorNoPermission("subscribe")
		}
		result, rpcErr := ws.urlSubscriptions.Subscribe(ctx, request)
		if rpcErr != nil {
			return nil, rpcErr
		}
		setSubscriptionLoadCost(ctx, request)
		return result, nil
	}
	serverState := sampleServerSubscriptionState(ctx, request)

	prefix, err := subscriptionRequestExcluding(cmd.Params, "books", "account_history_tx_stream")
	if err != nil {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid subscription parameters.")
	}
	prefix.ApiVersion = ctx.ApiVersion
	scope := ws.subscriptionManager.NewRequestScope()
	if rpcErr := ws.subscriptionManager.HandleSubscribeScoped(wsConn.registration, scope, prefix, ctx.Role.IsAdmin()); rpcErr != nil {
		return nil, rpcErr
	}
	historyWarning, rpcErr := applyAccountHistorySubscribe(ctx, wsConn.Connection, request)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := applyRequestBooks(request, func(bookRequest types.SubscriptionRequest) *rpcerrors.RpcError {
		bookRequest.ApiVersion = ctx.ApiVersion
		if rpcErr := ws.subscriptionManager.HandleSubscribeScoped(wsConn.registration, scope, bookRequest, ctx.Role.IsAdmin()); rpcErr != nil {
			return rpcErr
		}
		setSubscriptionLoadCost(ctx, bookRequest)
		return nil
	}); rpcErr != nil {
		return nil, rpcErr
	}

	result := ws.buildSubscribeAckSampled(ctx, request, serverState)
	if historyWarning != "" {
		result["warning"] = historyWarning
	}
	return result, nil
}
func subscriptionRequestExcluding(params json.RawMessage, fields ...string) (types.SubscriptionRequest, error) {
	var request types.SubscriptionRequest
	if len(params) == 0 {
		return request, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil {
		return request, err
	}
	for _, field := range fields {
		delete(raw, field)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return request, err
	}
	err = json.Unmarshal(data, &request)
	return request, err
}
func applySubscriptionBooks(raw json.RawMessage, apply func(types.SubscriptionRequest) *rpcerrors.RpcError) *rpcerrors.RpcError {
	if raw == nil {
		return nil
	}
	if rawJSONNull(raw) {
		request, err := subscriptionRequestForBooks(raw)
		if err != nil {
			return rpcerrors.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
		return apply(request)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		request, decodeErr := subscriptionRequestForBooks(raw)
		if decodeErr != nil {
			return rpcerrors.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
		return apply(request)
	}
	for decoder.More() {
		var book json.RawMessage
		if err := decoder.Decode(&book); err != nil {
			return rpcerrors.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
		request, err := subscriptionRequestForBooks(json.RawMessage("[" + string(book) + "]"))
		if err != nil {
			return rpcerrors.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
		if rpcErr := apply(request); rpcErr != nil {
			return rpcErr
		}
	}
	if _, err := decoder.Token(); err != nil {
		return rpcerrors.RpcErrorInvalidParams("Invalid subscription parameters.")
	}
	return nil
}

func applyRequestBooks(request types.SubscriptionRequest, apply func(types.SubscriptionRequest) *rpcerrors.RpcError) *rpcerrors.RpcError {
	wire := request.WireArrays()
	if wire.Present {
		return applySubscriptionBooks(wire.Books, apply)
	}
	for _, book := range request.Books {
		if rpcErr := apply(types.SubscriptionRequest{Books: []types.BookRequest{book}}); rpcErr != nil {
			return rpcErr
		}
	}
	return nil
}

func subscriptionRequestForBooks(books json.RawMessage) (types.SubscriptionRequest, error) {
	data, err := json.Marshal(map[string]json.RawMessage{"books": books})
	if err != nil {
		return types.SubscriptionRequest{}, err
	}
	var request types.SubscriptionRequest
	err = json.Unmarshal(data, &request)
	return request, err
}
func (ws *WebSocketServer) finishUnsubscribe(wsConn *websocketConnection, request types.SubscriptionRequest, params json.RawMessage, ctx *types.RpcContext) *rpcerrors.RpcError {
	prefix, err := subscriptionRequestExcluding(params, "books", "account_history_tx_stream")
	if err != nil {
		return rpcerrors.RpcErrorInvalidParams("Invalid unsubscription parameters.")
	}
	scope := ws.subscriptionManager.NewRequestScope()
	if rpcErr := ws.subscriptionManager.HandleUnsubscribeScoped(wsConn.registration, scope, prefix); rpcErr != nil {
		return rpcErr
	}
	if rpcErr := applyAccountHistoryUnsubscribe(ctx, wsConn.Connection, request); rpcErr != nil {
		return rpcErr
	}
	return applyRequestBooks(request, func(bookRequest types.SubscriptionRequest) *rpcerrors.RpcError {
		return ws.subscriptionManager.HandleUnsubscribeScoped(wsConn.registration, scope, bookRequest)
	})
}
func setSubscriptionLoadCost(ctx *types.RpcContext, request types.SubscriptionRequest) {
	for _, book := range request.Books {
		if book.Snapshot || book.StateNow {
			ctx.LoadCost = uint32(resource.FeeMediumBurdenRPC().Cost())
			return
		}
	}
}
func (ws *WebSocketServer) executeUnsubscribe(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (any, *rpcerrors.RpcError) {
	var request types.SubscriptionRequest
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &request); err != nil {
			return nil, rpcerrors.RpcErrorInvalidParams("Invalid unsubscription parameters.")
		}
	}
	// See handleSubscribe: url requests target the RPCSub registry.
	if request.HasURL() {
		if !ctx.Role.IsAdmin() {
			return nil, rpcerrors.RpcErrorNoPermission("unsubscribe")
		}
		result, rpcErr := ws.urlSubscriptions.Unsubscribe(ctx, request)
		if rpcErr != nil {
			return nil, rpcErr
		}
		return result, nil
	}

	if rpcErr := ws.finishUnsubscribe(wsConn, request, cmd.Params, ctx); rpcErr != nil {
		return nil, rpcErr
	}

	return map[string]any{}, nil
}

// buildSubscribeAck assembles the subscribe response payload shared by the
// WebSocket and url (RPCSub) subscribe paths: current ledger info when the
// ledger stream is among the requested streams, and a synthetic book-offers
// snapshot for any `snapshot:true` book.
//
// The ledger ack field set mirrors rippled subLedger: fee_ref only while
// XRPFees is disabled, network_id is present when a validated ledger exists; per-ledger pubLedger
// events (LedgerCloseEvent) carry txn_count separately. The snapshot block
// mirrors rippled
// Subscribe.cpp:339-394: when snapshot is set, the response carries `offers`
// (or `bids`/`asks` if `both` is set) populated by NetworkOPs::getBookPage.
// It reuses the ledger service's GetBookOffers — the same code path the
// book_offers RPC uses — so the snapshot a subscriber gets in the ack is
// identical to what they would have read with a separate book_offers call.
func sampleServerSubscriptionState(ctx *types.RpcContext, request types.SubscriptionRequest) map[string]any {
	if !slices.Contains(request.Streams, types.SubServer) {
		return nil
	}
	return handlers.ServerSubscriptionState(ctx.Services, ctx.Role.IsAdmin())
}

func (ws *WebSocketServer) buildSubscribeAck(ctx *types.RpcContext, request types.SubscriptionRequest) map[string]any {
	return ws.buildSubscribeAckSampled(ctx, request, sampleServerSubscriptionState(ctx, request))
}

func (ws *WebSocketServer) buildSubscribeAckSampled(ctx *types.RpcContext, request types.SubscriptionRequest, serverState map[string]any) map[string]any {
	return buildSubscribeAckSampled(ws.ledgerInfoProvider, ctx, request, serverState)
}

func buildSubscribeAckSampled(ledgerInfoProvider types.LedgerInfoProvider, ctx *types.RpcContext, request types.SubscriptionRequest, serverState map[string]any) map[string]any {
	result := make(map[string]any)
	for key, value := range serverState {
		result[key] = value
	}

	if slices.Contains(request.Streams, types.SubLedger) {
		if ledgerInfoProvider != nil {
			info := ledgerInfoProvider.GetCurrentLedgerInfo()
			if info != nil {
				if info.LedgerAvailable {
					result["ledger_index"] = info.LedgerIndex
					result["ledger_hash"] = info.LedgerHash
					result["ledger_time"] = info.LedgerTime
					result["fee_base"] = info.FeeBase
					// rippled emits the deprecated fee_ref only while XRPFees
					// is disabled; network_id is present with ledger fields.
					if !info.XRPFeesEnabled {
						result["fee_ref"] = info.FeeRef
					}
					result["reserve_base"] = info.ReserveBase
					result["reserve_inc"] = info.ReserveInc
					result["network_id"] = info.NetworkID
				}
				if info.ValidatedLedgersPresent {
					result["validated_ledgers"] = info.ValidatedLedgers
				}
			}
		}
	}

	for _, book := range request.Books {
		if (!book.Snapshot && !book.StateNow) || ctx.Services == nil || ctx.Services.Ledger() == nil {
			continue
		}
		pays, gets, domain, rpcErr := subscription.SnapshotBook(book)
		if rpcErr != nil {
			continue
		}
		if book.Both || book.BothSides {
			bids, _ := snapshotBook(ctx, gets, pays, book.Taker, domain)
			asks, _ := snapshotBook(ctx, pays, gets, book.Taker, domain)
			if bids != nil {
				result["bids"] = appendOffers(result["bids"], bids)
			}
			if asks != nil {
				result["asks"] = appendOffers(result["asks"], asks)
			}
			continue
		}
		offers, _ := snapshotBook(ctx, gets, pays, book.Taker, domain)
		if offers != nil {
			result["offers"] = appendOffers(result["offers"], offers)
		}
	}

	return result
}

// snapshotBook is the WS-side shim around the LedgerService's
// GetBookOffers. Returns the offers slice ready to embed in the
// subscribe ack. Errors are squashed — a snapshot failure mustn't
// reject the entire subscribe (rippled Subscribe.cpp:339-394 ignores
// the snapshot block on lookup failure too).
func (ws *WebSocketServer) snapshotBook(ctx *types.RpcContext, takerGets, takerPays types.Amount, taker, domain string) ([]types.BookOffer, error) {
	return snapshotBook(ctx, takerGets, takerPays, taker, domain)
}

func snapshotBook(ctx *types.RpcContext, takerGets, takerPays types.Amount, taker, domain string) ([]types.BookOffer, error) {
	if ctx == nil || ctx.Services == nil || ctx.Services.Ledger() == nil {
		return nil, nil
	}
	res, err := ctx.Services.Ledger().GetBookOffers(ctx.Context, takerGets, takerPays, taker, domain, "validated", defaultBookSnapshotLimit, "", false)
	if err != nil || res == nil {
		return nil, err
	}
	return res.Offers, nil
}
func appendOffers(prev any, more []types.BookOffer) []types.BookOffer {
	if prev == nil {
		return more
	}
	if existing, ok := prev.([]types.BookOffer); ok {
		return append(existing, more...)
	}
	return more
}
