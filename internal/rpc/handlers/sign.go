package handlers

import (
	"encoding/json"
	"strconv"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// SignMethod handles the sign RPC method
type SignMethod struct{}

func (m *SignMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		TxJson     json.RawMessage `json:"tx_json"`
		Secret     string          `json:"secret,omitempty"`
		Seed       string          `json:"seed,omitempty"`
		SeedHex    string          `json:"seed_hex,omitempty"`
		Passphrase string          `json:"passphrase,omitempty"`
		KeyType    string          `json:"key_type,omitempty"`
		Offline    bool            `json:"offline,omitempty"`
		BuildPath  bool            `json:"build_path,omitempty"`
		FeeMultMax uint32          `json:"fee_mult_max,omitempty"`
		FeeDivMax  uint32          `json:"fee_div_max,omitempty"`
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
	signed, rpcErr := signTransactionJSON(request.TxJson, signCredentials{
		Secret:     request.Secret,
		Seed:       request.Seed,
		SeedHex:    request.SeedHex,
		Passphrase: request.Passphrase,
		KeyType:    request.KeyType,
	}, request.Offline, ctx.ApiVersion)
	if rpcErr != nil {
		return nil, rpcErr
	}

	response := map[string]interface{}{
		"tx_blob": signed.TxBlob,
		"tx_json": signed.TxMap,
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
