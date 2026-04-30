package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/LeJamon/goXRPLd/keylet"
)

// FeatureMethod handles the feature RPC method.
// Returns information about amendments including their status, support, and voting.
// Reference: rippled Feature1.cpp
type FeatureMethod struct{ AdminHandler }

func (m *FeatureMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Feature string `json:"feature,omitempty"`
		Vetoed  *bool  `json:"vetoed,omitempty"`
	}
	if params != nil {
		_ = json.Unmarshal(params, &request)
	}

	// Read the enabled amendments from the ledger.
	enabledSet := m.getEnabledAmendments(ctx.Services)

	// If a specific feature is requested, return just that one
	if request.Feature != "" {
		return m.handleSingleFeature(request.Feature, enabledSet)
	}

	// Return all features wrapped in "features" key (matches rippled)
	allFeatures := amendment.AllFeatures()
	features := make(map[string]interface{}, len(allFeatures))

	for _, f := range allFeatures {
		hexID := strings.ToUpper(hex.EncodeToString(f.ID[:]))
		features[hexID] = buildFeatureInfo(f, enabledSet)
	}

	return map[string]interface{}{
		"features": features,
	}, nil
}

// handleSingleFeature looks up a single feature by name or hex ID.
func (m *FeatureMethod) handleSingleFeature(feature string, enabledSet map[[32]byte]bool) (interface{}, *types.RpcError) {
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
		return nil, types.RpcErrorInvalidParams("Feature not found: " + feature)
	}

	hexID := strings.ToUpper(hex.EncodeToString(f.ID[:]))
	return map[string]interface{}{
		hexID: buildFeatureInfo(f, enabledSet),
	}, nil
}

// getEnabledAmendments reads the Amendments SLE from the closed ledger and returns
// the set of amendment hashes that are actually enabled on-ledger.
// Returns nil if the ledger is unavailable, meaning the caller should fall back
// to deriving enabled status from the registry defaults.
func (m *FeatureMethod) getEnabledAmendments(services *types.ServiceContainer) map[[32]byte]bool {
	if services == nil || services.Ledger == nil {
		return nil
	}

	view, err := services.Ledger.GetClosedLedgerView()
	if err != nil || view == nil {
		return nil
	}

	data, err := view.Read(keylet.Amendments())
	if err != nil || data == nil {
		return nil
	}

	sle, err := pseudo.ParseAmendmentsSLE(data)
	if err != nil || sle == nil {
		return nil
	}

	enabled := make(map[[32]byte]bool, len(sle.Amendments))
	for _, hash := range sle.Amendments {
		enabled[hash] = true
	}
	return enabled
}

// buildFeatureInfo constructs the response map for a single amendment feature.
// If enabledSet is non-nil, the "enabled" field is looked up from the ledger.
// If enabledSet is nil (ledger unavailable), it falls back to registry defaults.
func buildFeatureInfo(f *amendment.Feature, enabledSet map[[32]byte]bool) map[string]interface{} {
	supported := f.Supported == amendment.SupportedYes

	// Determine vetoed status.
	// In rippled, "vetoed" can be true, false, or "Obsolete".
	var vetoed interface{}
	if f.Vote == amendment.VoteObsolete {
		vetoed = "Obsolete"
	} else if f.Vote == amendment.VoteDefaultNo && supported {
		vetoed = true
	} else {
		vetoed = false
	}

	// Determine enabled status from the ledger if available,
	// otherwise fall back to the registry default.
	var enabled bool
	if enabledSet != nil {
		enabled = enabledSet[f.ID]
	} else {
		enabled = supported && f.Vote == amendment.VoteDefaultYes
	}

	return map[string]interface{}{
		"name":      f.Name,
		"enabled":   enabled,
		"supported": supported,
		"vetoed":    vetoed,
	}
}
