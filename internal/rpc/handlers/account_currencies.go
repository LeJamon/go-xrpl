package handlers

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountCurrenciesMethod handles account_currencies: it reports the
// currencies the account can send and receive, derived from its trust lines.
type AccountCurrenciesMethod struct{ baseHandler }

func (m *AccountCurrenciesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	rawFields, fieldsErr := rawJSONFields(params)
	if fieldsErr != nil {
		return nil, fieldsErr
	}
	selection, selErr := parseLedgerSelectorParams(params, ledgerselector.Current())
	if selErr != nil {
		return nil, selErr
	}

	account := ""
	if accountRaw, ok := rawFields["account"]; ok {
		var valid bool
		account, valid = rawJSONString(accountRaw)
		if !valid {
			return nil, rpcerrors.RpcErrorInvalidField("account")
		}
	} else if identRaw, ok := rawFields["ident"]; ok {
		var valid bool
		account, valid = rawJSONString(identRaw)
		if !valid {
			return nil, rpcerrors.RpcErrorInvalidField("ident")
		}
	} else {
		return nil, rpcerrors.RpcErrorMissingField("account")
	}

	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	resolved, lookupErr := resolveLedgerSelection(ctx, selection)
	if lookupErr != nil {
		return nil, lookupErr
	}
	ledger := resolved.Value
	ledgerIndex := strconv.FormatUint(uint64(ledger.Sequence()), 10)
	ledgerFields := ledgerEntryResponseFields(ledger, resolved.Validated)
	if !types.IsValidClassicAddress(account) {
		return nil, rpcerrors.RpcErrorActMalformed("Account malformed.").WithExtra(ledgerFields)
	}

	// Get account currencies from the ledger service
	result, err := ctx.Services.Ledger().GetAccountCurrencies(
		ctx.Context,
		account,
		ledgerIndex,
	)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, rpcerrors.RpcErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrAccountMalformed) {
			return nil, rpcerrors.RpcErrorActMalformed("Account malformed.")
		}
		return nil, rpcInternalError("account_currencies: ledger query failed", err)
	}

	// Build response
	response := ledgerFields
	response["receive_currencies"] = result.ReceiveCurrencies
	response["send_currencies"] = result.SendCurrencies

	return response, nil
}
