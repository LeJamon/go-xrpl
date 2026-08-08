package rpc

import (
	"encoding/json"
	"slices"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// defaultBookSnapshotLimit caps the synthetic snapshot returned in the
// subscribe acknowledgement so a noisy market cannot exceed the frame limit.
const defaultBookSnapshotLimit uint32 = 60

func (ws *WebSocketServer) executeSubscribe(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (any, *types.RpcError) {
	var request types.SubscriptionRequest
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
	}
	// url requests are server-to-server (RPCSub) subscriptions: events go
	// to the url's subscriber, not to this WebSocket connection.
	if request.HasURL() {
		if !ctx.IsAdmin {
			return nil, types.RpcErrorNoPermission("subscribe")
		}
		result, rpcErr := ws.urlSubs.Subscribe(ctx, request)
		if rpcErr != nil {
			return nil, rpcErr
		}
		setSubscriptionLoadCost(ctx, request)
		return result, nil
	}

	// The embedded canonical connection is the same object the subscription
	// manager already tracks and carries the bounded queue and disconnect
	// callback.
	prefix, err := subscriptionRequestExcluding(cmd.Params, "books", "account_history_tx_stream")
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid subscription parameters.")
	}
	prefix.ApiVersion = ctx.ApiVersion
	if rpcErr := ws.subscriptionManager.HandleSubscribe(wsConn.Connection, prefix, ctx.IsAdmin); rpcErr != nil {
		return nil, rpcErr
	}
	historyWarning, rpcErr := applyAccountHistorySubscribe(ctx, wsConn.Connection, request)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := applySubscriptionBooks(request.WireArrays().Books, func(bookRequest types.SubscriptionRequest) *types.RpcError {
		bookRequest.ApiVersion = ctx.ApiVersion
		if rpcErr := ws.subscriptionManager.HandleSubscribe(wsConn.Connection, bookRequest, ctx.IsAdmin); rpcErr != nil {
			return rpcErr
		}
		setSubscriptionLoadCost(ctx, bookRequest)
		return nil
	}); rpcErr != nil {
		return nil, rpcErr
	}

	result := ws.buildSubscribeAck(ctx, request)
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
func applySubscriptionBooks(raw json.RawMessage, apply func(types.SubscriptionRequest) *types.RpcError) *types.RpcError {
	if raw == nil {
		return nil
	}
	if rawJSONNull(raw) {
		request, err := subscriptionRequestForBooks(raw)
		if err != nil {
			return types.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
		return apply(request)
	}
	var books []json.RawMessage
	if err := json.Unmarshal(raw, &books); err != nil {
		request, decodeErr := subscriptionRequestForBooks(raw)
		if decodeErr != nil {
			return types.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
		return apply(request)
	}
	for _, book := range books {
		request, err := subscriptionRequestForBooks(json.RawMessage("[" + string(book) + "]"))
		if err != nil {
			return types.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
		if rpcErr := apply(request); rpcErr != nil {
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
func (ws *WebSocketServer) finishUnsubscribe(wsConn *websocketConnection, request types.SubscriptionRequest, params json.RawMessage, ctx *types.RpcContext) *types.RpcError {
	prefix, err := subscriptionRequestExcluding(params, "books", "account_history_tx_stream")
	if err != nil {
		return types.RpcErrorInvalidParams("Invalid unsubscription parameters.")
	}
	if rpcErr := ws.subscriptionManager.HandleUnsubscribe(wsConn.Connection, prefix, ctx.IsAdmin); rpcErr != nil {
		return rpcErr
	}
	if rpcErr := applyAccountHistoryUnsubscribe(ctx, wsConn.Connection, request); rpcErr != nil {
		return rpcErr
	}
	return applySubscriptionBooks(request.WireArrays().Books, func(bookRequest types.SubscriptionRequest) *types.RpcError {
		return ws.subscriptionManager.HandleUnsubscribe(wsConn.Connection, bookRequest, ctx.IsAdmin)
	})
}
func setSubscriptionLoadCost(ctx *types.RpcContext, request types.SubscriptionRequest) {
	for _, book := range request.Books {
		if book.Snapshot || book.StateNow {
			ctx.LoadCost = uint32(resource.FeeMediumBurdenRPC.Cost())
			return
		}
	}
}
func (ws *WebSocketServer) executeUnsubscribe(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (any, *types.RpcError) {
	var request types.SubscriptionRequest
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid unsubscription parameters.")
		}
	}
	// See handleSubscribe: url requests target the RPCSub registry.
	if request.HasURL() {
		if !ctx.IsAdmin {
			return nil, types.RpcErrorNoPermission("unsubscribe")
		}
		result, rpcErr := ws.urlSubs.Unsubscribe(ctx, request)
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
func (ws *WebSocketServer) buildSubscribeAck(ctx *types.RpcContext, request types.SubscriptionRequest) map[string]any {
	result := make(map[string]any)

	if slices.Contains(request.Streams, types.SubLedger) {
		if ws.ledgerInfoProvider != nil {
			info := ws.ledgerInfoProvider.GetCurrentLedgerInfo()
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
		if (!book.Snapshot && !book.StateNow) || ctx.Services == nil || ctx.Services.Ledger == nil {
			continue
		}
		var takerGets, takerPays types.CurrencySpec
		if err := json.Unmarshal(book.TakerGets, &takerGets); err != nil {
			continue
		}
		if err := json.Unmarshal(book.TakerPays, &takerPays); err != nil {
			continue
		}
		gets := types.Amount{Currency: takerGets.Currency, Issuer: takerGets.Issuer}
		pays := types.Amount{Currency: takerPays.Currency, Issuer: takerPays.Issuer}
		if book.Both || book.BothSides {
			bids, _ := ws.snapshotBook(ctx, gets, pays, book.Taker, book.Domain)
			asks, _ := ws.snapshotBook(ctx, pays, gets, book.Taker, book.Domain)
			if bids != nil {
				result["bids"] = appendOffers(result["bids"], bids)
			}
			if asks != nil {
				result["asks"] = appendOffers(result["asks"], asks)
			}
			continue
		}
		offers, _ := ws.snapshotBook(ctx, gets, pays, book.Taker, book.Domain)
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
	if ctx == nil || ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, nil
	}
	res, err := ctx.Services.Ledger.GetBookOffers(ctx.Context, takerGets, takerPays, taker, domain, "validated", defaultBookSnapshotLimit, "", false)
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
