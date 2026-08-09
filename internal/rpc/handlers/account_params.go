package handlers

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func rawJSONFields(params json.RawMessage) (map[string]json.RawMessage, *types.RpcError) {
	fields := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(params)) == 0 {
		return fields, nil
	}
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	return fields, nil
}

func rawJSONString(raw json.RawMessage) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	stringValue, ok := value.(string)
	return stringValue, ok
}

func rawJSONBool(raw json.RawMessage) (bool, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	boolValue, ok := value.(bool)
	return boolValue, ok
}

func requireAccountExists(ctx *types.RpcContext, account, ledgerIndex string) *types.RpcError {
	_, err := ctx.Services.Ledger().GetAccountInfo(ctx.Context, account, ledgerIndex)
	if err == nil {
		return nil
	}
	if errors.Is(err, svcerr.ErrAccountNotFound) {
		return types.RpcErrorActNotFound("Account not found.")
	}
	if rpcErr := mapLedgerLookupErr(err); rpcErr != nil {
		return rpcErr
	}
	return rpcInternalError("account query: account lookup failed", err)
}

func decodeRawJSONValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func jsonCppBoolRaw(raw json.RawMessage) bool {
	value, err := decodeRawJSONValue(raw)
	if err != nil {
		return false
	}
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case json.Number:
		number, err := value.Float64()
		return err == nil && number != 0
	case string:
		return value != ""
	case []any:
		return len(value) != 0
	case map[string]any:
		return len(value) != 0
	default:
		return false
	}
}

func jsonCppStringRaw(raw json.RawMessage) (string, bool) {
	value, err := decodeRawJSONValue(raw)
	if err != nil {
		return "", false
	}
	switch value := value.(type) {
	case nil:
		return "", true
	case string:
		return value, true
	case bool:
		if value {
			return "true", true
		}
		return "false", true
	case json.Number:
		return value.String(), true
	default:
		return "", false
	}
}
