package handlers

import (
	"encoding/json"
	"strconv"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// SignMethod handles the sign RPC method
type SignMethod struct{}

func (m *SignMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Parse fee_mult_max / fee_div_max first with proper type validation,
	// matching rippled's checkFee() in TransactionSign.cpp.
	feeOpts, rpcErr := parseFeeOptions(params)
	if rpcErr != nil {
		return nil, rpcErr
	}

	var request struct {
		TxJson     json.RawMessage `json:"tx_json"`
		Secret     string          `json:"secret,omitempty"`
		Seed       string          `json:"seed,omitempty"`
		SeedHex    string          `json:"seed_hex,omitempty"`
		Passphrase string          `json:"passphrase,omitempty"`
		KeyType    string          `json:"key_type,omitempty"`
		Offline    bool            `json:"offline,omitempty"`
		BuildPath  bool            `json:"build_path,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if len(request.TxJson) == 0 {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: tx_json")
	}

	// Sign the transaction using the shared helper
	signed, rpcErr := signTransactionJSON(ctx.Services, request.TxJson, signCredentials{
		Secret:     request.Secret,
		Seed:       request.Seed,
		SeedHex:    request.SeedHex,
		Passphrase: request.Passphrase,
		KeyType:    request.KeyType,
	}, request.Offline, ctx.ApiVersion, feeOpts)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Inject DeliverMax for Payment transactions, matching rippled's
	// RPC::insertDeliverMax in transactionFormatResultImpl.
	injectDeliverMax(signed.TxMap, ctx.ApiVersion)

	response := map[string]interface{}{
		"tx_blob": signed.TxBlob,
		"tx_json": signed.TxMap,
	}

	// API v2+: add hash at root level of response, matching rippled's
	// transactionFormatResultImpl in TransactionSign.cpp.
	if ctx.ApiVersion > 1 {
		if hash, ok := signed.TxMap["hash"].(string); ok {
			response["hash"] = hash
		}
	}

	return response, nil
}

// formatUint64AsString formats a uint64 as a decimal string
func formatUint64AsString(v uint64) string {
	return strconv.FormatUint(v, 10)
}

func (m *SignMethod) RequiredRole() types.Role {
	return types.RoleUser // Signing requires user privileges
}

func (m *SignMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *SignMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
