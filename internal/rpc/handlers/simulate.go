package handlers

import (
	"encoding/json"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// SimulateMethod handles the simulate RPC method.
// Runs a transaction against a snapshot of the open ledger without committing.
// Reference: rippled Simulate.cpp
type SimulateMethod struct{}

func (m *SimulateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		TxBlob string                 `json:"tx_blob,omitempty"`
		TxJSON map[string]interface{} `json:"tx_json,omitempty"`
		Binary bool                   `json:"binary,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	hasTxBlob := request.TxBlob != ""
	hasTxJSON := request.TxJSON != nil && len(request.TxJSON) > 0

	if hasTxBlob && hasTxJSON {
		return nil, types.RpcErrorInvalidParams("Can only include one of `tx_blob` and `tx_json`")
	}
	if !hasTxBlob && !hasTxJSON {
		return nil, types.RpcErrorInvalidParams("Neither `tx_blob` nor `tx_json` included")
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	var txJSON []byte
	var txJsonMap map[string]interface{}

	if hasTxBlob {
		// Decode tx_blob to get tx_json
		decoded, err := binarycodec.Decode(request.TxBlob)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid tx_blob: " + err.Error())
		}
		txJsonMap = decoded
		txJSON, err = json.Marshal(decoded)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to marshal decoded tx_blob")
		}
	} else {
		txJsonMap = request.TxJSON
		var err error
		txJSON, err = json.Marshal(request.TxJSON)
		if err != nil {
			return nil, types.RpcErrorInternal("Failed to marshal tx_json")
		}
	}

	// Run the transaction in simulation mode (snapshot, no commit)
	result, err := types.Services.Ledger.SimulateTransaction(txJSON)
	if err != nil {
		return nil, types.RpcErrorInternal("Simulation failed: " + err.Error())
	}

	response := map[string]interface{}{
		"engine_result":         result.EngineResult,
		"engine_result_code":    result.EngineResultCode,
		"engine_result_message": result.EngineResultMessage,
		"applied":               result.Applied,
		"ledger_index":          result.CurrentLedger,
	}

	if request.Binary {
		if encoded, err := binarycodec.Encode(txJsonMap); err == nil {
			response["tx_blob"] = encoded
		}
	} else {
		response["tx_json"] = txJsonMap
	}

	return response, nil
}

func (m *SimulateMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *SimulateMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *SimulateMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
