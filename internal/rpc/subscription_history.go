package rpc

import (
	"bytes"
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

const accountHistoryWarning = "account_history_tx_stream is an experimental feature and likely to be removed in the future"

type preparedAccountHistorySubscribe struct {
	service types.AccountHistorySubscriptionService
	account string
}

func prepareAccountHistorySubscribe(ctx *types.RpcContext, request types.SubscriptionRequest) (*preparedAccountHistorySubscribe, *rpcerrors.RpcError) {
	present := accountHistoryPresent(request)
	if !present {
		return nil, nil
	}
	services := rpcServices(ctx)
	if services == nil || services.Ledger() == nil {
		return nil, rpcerrors.RpcErrorNotEnabled("")
	}
	if tables, ok := services.Ledger().(types.TxTablesProvider); ok && !tables.UseTxTables() {
		return nil, rpcerrors.RpcErrorNotEnabled("")
	}
	if services.AccountHistorySubscriptions() == nil {
		return nil, rpcerrors.RpcErrorNotEnabled("")
	}
	if ctx != nil {
		ctx.LoadCost = uint32(resource.FeeMediumBurdenRPC().Cost())
	}
	account, _, rpcErr := parseAccountHistoryRequest(request, false)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return &preparedAccountHistorySubscribe{
		service: services.AccountHistorySubscriptions(),
		account: account,
	}, nil
}

func (p *preparedAccountHistorySubscribe) validate(conn *subscription.Connection) *rpcerrors.RpcError {
	if p == nil {
		return nil
	}
	return p.service.ValidateSubscribe(conn, p.account)
}

func (p *preparedAccountHistorySubscribe) apply(conn *subscription.Connection) {
	if p != nil {
		p.service.Subscribe(conn, p.account)
	}
}

func applyAccountHistorySubscribe(ctx *types.RpcContext, conn *subscription.Connection, request types.SubscriptionRequest) (string, *rpcerrors.RpcError) {
	prepared, rpcErr := prepareAccountHistorySubscribe(ctx, request)
	if rpcErr != nil {
		return "", rpcErr
	}
	if prepared == nil {
		return "", nil
	}
	if rpcErr := prepared.validate(conn); rpcErr != nil {
		return "", rpcErr
	}
	prepared.apply(conn)
	return accountHistoryWarning, nil
}

func applyAccountHistoryUnsubscribe(ctx *types.RpcContext, conn *subscription.Connection, request types.SubscriptionRequest) *rpcerrors.RpcError {
	prepared, rpcErr := prepareAccountHistoryUnsubscribe(ctx, request)
	if rpcErr != nil {
		return rpcErr
	}
	prepared.apply(conn)
	return nil
}

type preparedAccountHistoryUnsubscribe struct {
	service     types.AccountHistorySubscriptionService
	account     string
	historyOnly bool
}

func prepareAccountHistoryUnsubscribe(ctx *types.RpcContext, request types.SubscriptionRequest) (*preparedAccountHistoryUnsubscribe, *rpcerrors.RpcError) {
	if !accountHistoryPresent(request) {
		return nil, nil
	}
	account, historyOnly, rpcErr := parseAccountHistoryRequest(request, true)
	if rpcErr != nil {
		return nil, rpcErr
	}
	services := rpcServices(ctx)
	var service types.AccountHistorySubscriptionService
	if services != nil {
		service = services.AccountHistorySubscriptions()
	}
	return &preparedAccountHistoryUnsubscribe{
		service:     service,
		account:     account,
		historyOnly: historyOnly,
	}, nil
}

func (p *preparedAccountHistoryUnsubscribe) apply(conn *subscription.Connection) {
	if p != nil && p.service != nil {
		p.service.Unsubscribe(conn, p.account, p.historyOnly)
	}
}

func accountHistoryPresent(request types.SubscriptionRequest) bool {
	wire := request.WireArrays()
	if wire.Present {
		return wire.AccountHistory != nil
	}
	return request.AccountHistory != nil
}

func parseAccountHistoryRequest(request types.SubscriptionRequest, unsubscribe bool) (string, bool, *rpcerrors.RpcError) {
	wire := request.WireArrays()
	if !wire.Present {
		if request.AccountHistory == nil || !types.IsValidClassicAddress(request.AccountHistory.Account) {
			return "", false, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
		return request.AccountHistory.Account, unsubscribe && request.AccountHistory.StopHistoryTxOnly, nil
	}

	var nested map[string]json.RawMessage
	if err := json.Unmarshal(wire.AccountHistory, &nested); err != nil || nested == nil {
		return "", false, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	rawAccount, ok := nested["account"]
	if !ok {
		return "", false, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	var account string
	if err := json.Unmarshal(rawAccount, &account); err != nil || !types.IsValidClassicAddress(account) {
		return "", false, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}

	historyOnly := false
	if unsubscribe {
		if rawStop, ok := nested["stop_history_tx_only"]; ok {
			trimmed := bytes.TrimSpace(rawStop)
			if !bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false")) {
				return "", false, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
			}
			historyOnly = bytes.Equal(trimmed, []byte("true"))
		}
	}
	return account, historyOnly, nil
}

func rpcServices(ctx *types.RpcContext) *types.ServiceGraph {
	if ctx == nil {
		return nil
	}
	return ctx.Services
}

func removeAccountHistoryConnection(services *types.ServiceGraph, conn *subscription.Connection) {
	if services != nil && services.AccountHistorySubscriptions() != nil {
		services.AccountHistorySubscriptions().RemoveConnection(conn)
	}
}

func hasAccountHistorySubscriptions(services *types.ServiceGraph, conn *subscription.Connection) bool {
	return services != nil && services.AccountHistorySubscriptions() != nil && services.AccountHistorySubscriptions().HasSubscriptions(conn)
}
