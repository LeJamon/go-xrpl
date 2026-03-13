package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
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
type ValidatorsMethod struct{ AdminHandler }

func (m *ValidatorsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{
		"trusted_validator_keys": []interface{}{},
		"publisher_lists":        []interface{}{},
		"validation_quorum":      0,
	}, nil
}

// ValidatorListSitesMethod handles the validator_list_sites RPC method.
// STUB: Returns empty list. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding validator list fetching.
//   - Requires: ValidatorSite service that fetches validator lists from URLs
//   - Reference: rippled ValidatorListSites.cpp
//   - Returns: array of configured validator list sites with their fetch status
type ValidatorListSitesMethod struct{ AdminHandler }

func (m *ValidatorListSitesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{"validator_sites": []interface{}{}}, nil
}
