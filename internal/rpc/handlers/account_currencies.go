package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountCurrenciesMethod handles account_currencies: it reports the
// currencies the account can send and receive, derived from its trust lines.
type AccountCurrenciesMethod struct{ BaseHandler }

func (m *AccountCurrenciesMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	rawFields, fieldsErr := rawJSONFields(params)
	if fieldsErr != nil {
		return nil, fieldsErr
	}

	account := ""
	if accountRaw, ok := rawFields["account"]; ok {
		var valid bool
		account, valid = rawJSONString(accountRaw)
		if !valid {
			return nil, types.RPCErrorInvalidField("account")
		}
	} else if identRaw, ok := rawFields["ident"]; ok {
		var valid bool
		account, valid = rawJSONString(identRaw)
		if !valid {
			return nil, types.RPCErrorInvalidField("ident")
		}
	} else {
		return nil, types.RPCErrorMissingField("account")
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	ledger, validated, lookupErr := LookupLedger(ctx, params)
	if lookupErr != nil {
		return nil, lookupErr
	}
	ledgerIndex := strconv.FormatUint(uint64(ledger.Sequence()), 10)
	ledgerFields := ledgerEntryResponseFields(ledger, validated)
	if !types.IsValidClassicAddress(account) {
		return nil, types.RPCErrorActMalformed("Account malformed.").WithExtra(ledgerFields)
	}

	// Get account currencies from the ledger service
	result, err := ctx.Services.Ledger.GetAccountCurrencies(
		ctx.Context,
		account,
		ledgerIndex,
	)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		if errors.Is(err, svcerr.ErrAccountMalformed) {
			return nil, types.RPCErrorActMalformed("Account malformed.")
		}
		return nil, mapAccountQueryErr(err, fmt.Sprintf("Failed to get account currencies: %v", err))
	}

	// Build response
	response := ledgerFields
	response["receive_currencies"] = result.ReceiveCurrencies
	response["send_currencies"] = result.SendCurrencies

	return response, nil
}
