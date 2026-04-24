package handlers

import (
	"encoding/base64"
	"encoding/json"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// ManifestMethod handles the `manifest` RPC method.
//
// Rippled reference: src/xrpld/rpc/handlers/DoManifest.cpp:29-76.
//
// Given a base58 node public key (either a master key OR an ephemeral
// signing key), the handler resolves it to the master key via the
// cached manifest and returns the stored manifest's details plus the
// raw serialized manifest as base64. Keys with no recorded manifest
// return only the `requested` field.
type ManifestMethod struct{}

// manifestResponse mirrors rippled's DoManifest response shape.
type manifestResponse struct {
	Requested string                 `json:"requested"`
	Manifest  string                 `json:"manifest,omitempty"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

func (m *ManifestMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		PublicKey string `json:"public_key"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if request.PublicKey == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: public_key")
	}

	// Parse the base58 NodePublic key. rippled DoManifest.cpp:36
	// rejects non-base58 or wrong-type inputs with rpcPUBLIC_MALFORMED.
	rawKey, err := addresscodec.DecodeNodePublicKey(request.PublicKey)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("invalid node public key: " + err.Error())
	}
	if len(rawKey) != 33 {
		return nil, types.RpcErrorInvalidParams("node public key must be 33 bytes")
	}
	var keyArr [33]byte
	copy(keyArr[:], rawKey)

	resp := manifestResponse{Requested: request.PublicKey}

	// Manifest cache isn't wired in standalone / pre-consensus setups
	// — return the sparse response so clients can still probe the
	// method without a 500.
	if types.Services == nil || types.Services.Manifests == nil {
		return resp, nil
	}
	cache := types.Services.Manifests

	// Callers may pass EITHER the master key or its ephemeral signing
	// key. GetMasterKey collapses both into the master; if the input
	// was unknown it returns the input unchanged.
	master := cache.GetMasterKey(keyArr)

	// Try to find a stored manifest under the resolved master.
	serialized, ok := cache.GetManifest(master)
	if !ok {
		// No manifest recorded (or the master is revoked). Rippled
		// returns requested + empty details in this case.
		return resp, nil
	}

	ephemeral, ephOK := cache.GetSigningKey(master)
	seq, _ := cache.GetSequence(master)
	domain, _ := cache.GetDomain(master)

	masterB58, err := addresscodec.EncodeNodePublicKey(master[:])
	if err != nil {
		return nil, types.RpcErrorInternal("encode master key: " + err.Error())
	}

	details := map[string]interface{}{
		"master_key": masterB58,
		"seq":        seq,
	}
	if ephOK {
		ephB58, err := addresscodec.EncodeNodePublicKey(ephemeral[:])
		if err != nil {
			return nil, types.RpcErrorInternal("encode ephemeral key: " + err.Error())
		}
		details["ephemeral_key"] = ephB58
	}
	if domain != "" {
		details["domain"] = domain
	}

	resp.Manifest = base64.StdEncoding.EncodeToString(serialized)
	resp.Details = details
	return resp, nil
}

func (m *ManifestMethod) RequiredRole() types.Role {
	return types.RoleUser // rippled: Role::USER (Handler.cpp line 136)
}

func (m *ManifestMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *ManifestMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
