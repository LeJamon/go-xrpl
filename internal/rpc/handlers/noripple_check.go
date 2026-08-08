package handlers

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// NoRippleCheckMethod handles the noripple_check RPC method
// Reference: rippled/src/xrpld/rpc/handlers/NoRippleCheck.cpp
type NoRippleCheckMethod struct{ baseHandler }

func (m *NoRippleCheckMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	rawFields, fieldsErr := rawJSONFields(params)
	if fieldsErr != nil {
		return nil, fieldsErr
	}
	accountRaw, hasAccount := rawFields["account"]
	if !hasAccount {
		return nil, types.RpcErrorMissingField("account")
	}
	roleRaw, hasRole := rawFields["role"]
	if !hasRole {
		return nil, types.RpcErrorMissingField("role")
	}
	account, validAccount := rawJSONString(accountRaw)
	if !validAccount {
		return nil, types.RpcErrorInvalidField("account")
	}
	role, validRole := jsonCppStringRaw(roleRaw)
	if !validRole || (role != "gateway" && role != "user") {
		return nil, types.RpcErrorInvalidField("role")
	}

	limit, limitErr := readLimitField(params, limitNoRippleCheck, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}

	transactions := false
	if transactionsRaw, ok := rawFields["transactions"]; ok {
		if ctx.ApiVersion > 1 {
			var valid bool
			transactions, valid = rawJSONBool(transactionsRaw)
			if !valid {
				return nil, types.RpcErrorInvalidField("transactions")
			}
		} else {
			transactions = jsonCppBoolRaw(transactionsRaw)
		}
	}

	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Determine ledger index to use. rippled's lookupLedger defaults to the
	// open ("current") ledger in the absence of ledger_index/ledger_hash.
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	ledger, validated, lookupErr := lookupLedger(ctx, parsedLedgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}
	ledgerIndex := strconv.FormatUint(uint64(ledger.Sequence()), 10)
	response := ledgerEntryResponseFields(ledger, validated)
	if transactions {
		response["transactions"] = []map[string]any{}
	}
	if !types.IsValidClassicAddress(account) {
		return nil, types.RpcErrorActMalformed("Account malformed.").WithExtra(response)
	}

	result, err := ctx.Services.Ledger.GetNoRippleCheck(
		ctx.Context,
		account,
		role,
		ledgerIndex,
		limit,
		transactions,
	)
	if err != nil {
		if errors.Is(err, svcerr.ErrAccountMalformed) {
			return nil, types.RpcErrorActMalformed("Account malformed.")
		}
		if errors.Is(err, svcerr.ErrLedgerNotFound) {
			return nil, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return nil, mapAccountQueryErr(err, "noripple_check: ledger query failed")
	}

	// Build response matching rippled's NoRippleCheck.cpp format
	// Problems is always present (may be empty array)
	// Reference: NoRippleCheck.cpp line 123: result["problems"] = Json::arrayValue
	if result.Problems != nil {
		response["problems"] = result.Problems
	} else {
		response["problems"] = []string{}
	}

	// When transactions=true, rippled always includes the transactions array
	// even if empty. Reference: NoRippleCheck.cpp line 108:
	//   jvTransactions = transactions ? (result[jss::transactions] = Json::arrayValue) : dummy;
	if transactions {
		if len(result.Transactions) > 0 {
			transactions := make([]map[string]any, len(result.Transactions))
			for i, tx := range result.Transactions {
				txMap := map[string]any{
					"TransactionType": tx.TransactionType,
					"Account":         tx.Account,
					"Fee":             tx.Fee,
					"Sequence":        tx.Sequence,
				}
				if tx.SetFlag != 0 {
					txMap["SetFlag"] = tx.SetFlag
				}
				if tx.Flags != 0 {
					txMap["Flags"] = tx.Flags
				}
				if tx.LimitAmount != nil {
					txMap["LimitAmount"] = tx.LimitAmount
				}
				transactions[i] = txMap
			}
			response["transactions"] = transactions
		} else {
			response["transactions"] = []map[string]any{}
		}
	}

	return response, nil
}
