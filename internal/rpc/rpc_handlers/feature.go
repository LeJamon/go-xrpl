package rpc_handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// FeatureMethod handles the feature RPC method.
// Returns information about amendments including their status, support, and voting.
// Reference: rippled Feature.cpp
type FeatureMethod struct{}

func (m *FeatureMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	var request struct {
		Feature string `json:"feature,omitempty"`
	}
	if params != nil {
		_ = json.Unmarshal(params, &request)
	}

	// If a specific feature is requested, return just that one
	if request.Feature != "" {
		return m.handleSingleFeature(request.Feature)
	}

	// Return all features
	allFeatures := amendment.AllFeatures()
	response := make(map[string]interface{}, len(allFeatures))

	for _, f := range allFeatures {
		hexID := strings.ToUpper(hex.EncodeToString(f.ID[:]))
		supported := f.Supported == amendment.SupportedYes

		// Determine vetoed status
		// In rippled, "vetoed" can be true, false, or "Obsolete"
		var vetoed interface{}
		if f.Vote == amendment.VoteObsolete {
			vetoed = "Obsolete"
		} else if f.Vote == amendment.VoteDefaultNo && supported {
			vetoed = true
		} else {
			vetoed = false
		}

		// Determine enabled status
		// In standalone mode, supported features with default-yes vote are enabled
		enabled := supported && f.Vote == amendment.VoteDefaultYes

		response[hexID] = map[string]interface{}{
			"name":      f.Name,
			"enabled":   enabled,
			"supported": supported,
			"vetoed":    vetoed,
		}
	}

	return response, nil
}

// handleSingleFeature looks up a single feature by name or hex ID.
func (m *FeatureMethod) handleSingleFeature(feature string) (interface{}, *rpc_types.RpcError) {
	var f *amendment.Feature

	// Try by name first
	f = amendment.GetFeatureByName(feature)

	// Try by hex ID
	if f == nil {
		idBytes, err := hex.DecodeString(feature)
		if err == nil && len(idBytes) == 32 {
			var id [32]byte
			copy(id[:], idBytes)
			f = amendment.GetFeature(id)
		}
	}

	if f == nil {
		return nil, rpc_types.RpcErrorInvalidParams("Feature not found: " + feature)
	}

	hexID := strings.ToUpper(hex.EncodeToString(f.ID[:]))
	supported := f.Supported == amendment.SupportedYes

	var vetoed interface{}
	if f.Vote == amendment.VoteObsolete {
		vetoed = "Obsolete"
	} else if f.Vote == amendment.VoteDefaultNo && supported {
		vetoed = true
	} else {
		vetoed = false
	}

	enabled := supported && f.Vote == amendment.VoteDefaultYes

	response := map[string]interface{}{
		hexID: map[string]interface{}{
			"name":      f.Name,
			"enabled":   enabled,
			"supported": supported,
			"vetoed":    vetoed,
		},
	}

	return response, nil
}

func (m *FeatureMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *FeatureMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
