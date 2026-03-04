package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// ValidationCreateMethod handles the validation_create RPC method.
// STUB: Returns error. Requires crypto key generation wiring.
//
// TODO [validator]: Implement validation key generation.
//   - Requires: Crypto key generation (already have secp256k1/ed25519 in internal/crypto)
//   - Reference: rippled ValidationCreate.cpp
//   - Steps:
//     1. Parse optional params: secret (string), key_type ("secp256k1" or "ed25519")
//     2. If secret provided: derive seed from secret using parseBase58<Seed>()
//     3. If no secret: generate random seed
//     4. Derive keypair from seed using key_type (default secp256k1)
//     5. Encode as: validation_key (RFC 1751), validation_public_key (base58),
//        validation_seed (base58 seed)
//   - This is admin-only and generates keys for validator configuration
//   - Note: The crypto primitives already exist in internal/crypto/; this just needs
//     wiring to RPC format (base58 encoding of seeds/keys)
type ValidationCreateMethod struct{}

func (m *ValidationCreateMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return nil, rpc_types.NewRpcError(rpc_types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"validation_create requires validator key generation wiring")
}

func (m *ValidationCreateMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *ValidationCreateMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
