package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// ValidatorsMethod handles the validators RPC method.
// STUB: Returns empty list. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding consensus/validator tracking.
//   - Requires: ValidatorList service tracking trusted validators
//   - Reference: rippled Validators.cpp → context.app.validators()
//   - Returns: list of trusted validators with their public keys, manifests,
//     and current validation status
//   - Also returns publisher_lists with their public keys, sequence, expiration
type ValidatorsMethod struct{}

func (m *ValidatorsMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return map[string]interface{}{
		"trusted_validator_keys": []interface{}{},
		"publisher_lists":        []interface{}{},
		"validation_quorum":      0,
	}, nil
}

func (m *ValidatorsMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *ValidatorsMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}

// ValidatorListSitesMethod handles the validator_list_sites RPC method.
// STUB: Returns empty list. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding validator list fetching.
//   - Requires: ValidatorSite service that fetches validator lists from URLs
//   - Reference: rippled ValidatorListSites.cpp
//   - Returns: array of configured validator list sites with their fetch status
type ValidatorListSitesMethod struct{}

func (m *ValidatorListSitesMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return map[string]interface{}{"validator_sites": []interface{}{}}, nil
}

func (m *ValidatorListSitesMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *ValidatorListSitesMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
