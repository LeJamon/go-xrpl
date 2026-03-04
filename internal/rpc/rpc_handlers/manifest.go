package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// ManifestMethod handles the manifest RPC method.
// STUB: Returns placeholder data. Requires validator manifest infrastructure.
//
// TODO [validator]: Implement validator manifest retrieval.
//   - Requires: ValidatorManifests service (stores master→ephemeral key mappings)
//   - Reference: rippled Manifest.cpp → context.app.validatorManifests()
//   - Steps:
//     1. Parse public_key param (required)
//     2. Call validatorManifests.getMasterKey(publicKey) to resolve ephemeral→master
//     3. Call validatorManifests.getManifest(masterKey) to get raw manifest
//     4. Decode manifest to extract: sequence, master_key, signing_key, domain, signature
//     5. Return { details: {domain, ephemeral_key, master_key, seq}, manifest: base64, requested: key }
//   - If key not found, return empty details + "requested" field only
type ManifestMethod struct{}

func (m *ManifestMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	var request struct {
		PublicKey string `json:"public_key"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, rpc_types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if request.PublicKey == "" {
		return nil, rpc_types.RpcErrorInvalidParams("Missing required parameter: public_key")
	}

	// Return empty details (no validator manifest infrastructure yet)
	response := map[string]interface{}{
		"requested": request.PublicKey,
	}

	return response, nil
}

func (m *ManifestMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *ManifestMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
