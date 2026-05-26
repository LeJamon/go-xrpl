package handlers

import (
	"context"
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
// Admins may set/clear a veto via the `vetoed` param, and see per-amendment
// vote tallies (count/validations/threshold) for amendments not yet enabled.
// Reference: rippled Feature1.cpp
type FeatureMethod struct{ AdminHandler }

// amendmentVoteController is the optional capability the live ledger-service
// adapter implements to expose the amendment table and accept vote mutations.
// Test mocks don't implement it, so handlers degrade gracefully.
type amendmentVoteController interface {
	AmendmentTable() *amendment.AmendmentTable
	SetAmendmentVote(ctx context.Context, id [32]byte, vetoed bool) error
}

func (m *FeatureMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Feature string `json:"feature,omitempty"`
		Vetoed  *bool  `json:"vetoed,omitempty"`
	}
	if params != nil {
		_ = json.Unmarshal(params, &request)
	}

	enabledSet := m.getEnabledAmendments(ctx.Services)
	ctrl := amendmentController(ctx.Services)
	var tbl *amendment.AmendmentTable
	if ctrl != nil {
		tbl = ctrl.AmendmentTable()
	}
	var lastVote *amendment.LastVote
	if tbl != nil {
		lastVote = tbl.LastVote()
	}

	// Admin vote mutation: set or clear a veto on a specific amendment.
	if request.Vetoed != nil {
		if request.Feature == "" {
			return nil, types.RpcErrorInvalidParams("feature required to set a vote")
		}
		f := resolveFeature(request.Feature)
		if f == nil {
			return nil, types.RpcErrorInvalidParams("Feature not found: " + request.Feature)
		}
		if ctrl == nil || tbl == nil {
			return nil, types.RpcErrorNotSupported("amendment voting is not available on this server")
		}
		if err := ctrl.SetAmendmentVote(ctx.Context, f.ID, *request.Vetoed); err != nil {
			return nil, types.RpcErrorInternal("failed to record amendment vote: " + err.Error())
		}
		hexID := strings.ToUpper(hex.EncodeToString(f.ID[:]))
		return map[string]interface{}{
			hexID: buildFeatureInfo(f, enabledSet, tbl, lastVote, ctx.IsAdmin),
		}, nil
	}

	// Single feature lookup.
	if request.Feature != "" {
		f := resolveFeature(request.Feature)
		if f == nil {
			return nil, types.RpcErrorInvalidParams("Feature not found: " + request.Feature)
		}
		hexID := strings.ToUpper(hex.EncodeToString(f.ID[:]))
		return map[string]interface{}{
			hexID: buildFeatureInfo(f, enabledSet, tbl, lastVote, ctx.IsAdmin),
		}, nil
	}

	// All features wrapped in "features" (matches rippled).
	allFeatures := amendment.AllFeatures()
	features := make(map[string]interface{}, len(allFeatures))
	for _, f := range allFeatures {
		hexID := strings.ToUpper(hex.EncodeToString(f.ID[:]))
		features[hexID] = buildFeatureInfo(f, enabledSet, tbl, lastVote, ctx.IsAdmin)
	}
	return map[string]interface{}{
		"features": features,
	}, nil
}

// resolveFeature looks up an amendment by registry name or 64-char hex ID.
func resolveFeature(feature string) *amendment.Feature {
	if f := amendment.GetFeatureByName(feature); f != nil {
		return f
	}
	if idBytes, err := hex.DecodeString(feature); err == nil && len(idBytes) == 32 {
		var id [32]byte
		copy(id[:], idBytes)
		return amendment.GetFeature(id)
	}
	return nil
}

// amendmentController returns the live amendment-vote controller if the wired
// ledger service exposes one, else nil (e.g. test mocks).
func amendmentController(services *types.ServiceContainer) amendmentVoteController {
	if services == nil || services.Ledger == nil {
		return nil
	}
	if c, ok := services.Ledger.(amendmentVoteController); ok {
		return c
	}
	return nil
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
// `enabledSet` (nil ⇒ fall back to registry defaults) gives the on-ledger
// enabled status; `tbl` (nil ⇒ registry defaults) provides operator veto/upvote
// overrides; when admin and the amendment is not yet enabled, `lastVote`
// supplies the vote tallies.
func buildFeatureInfo(f *amendment.Feature, enabledSet map[[32]byte]bool, tbl *amendment.AmendmentTable, lastVote *amendment.LastVote, isAdmin bool) map[string]interface{} {
	supported := f.Supported == amendment.SupportedYes

	var enabled bool
	if enabledSet != nil {
		enabled = enabledSet[f.ID]
	} else {
		enabled = supported && f.Vote == amendment.VoteDefaultYes
	}

	info := map[string]interface{}{
		"name":      f.Name,
		"enabled":   enabled,
		"supported": supported,
		"vetoed":    featureVetoed(f, tbl, supported),
	}

	// Admin-only vote tallies for amendments not yet enabled.
	if isAdmin && !enabled && lastVote != nil {
		info["count"] = lastVote.Votes[f.ID]
		info["validations"] = lastVote.TrustedValidations
		if lastVote.Threshold > 0 {
			info["threshold"] = lastVote.Threshold
		}
	}

	return info
}

// featureVetoed reports an amendment's veto status: "Obsolete", or a bool. The
// operator table (when present) takes precedence over the registry default.
func featureVetoed(f *amendment.Feature, tbl *amendment.AmendmentTable, supported bool) interface{} {
	if f.Vote == amendment.VoteObsolete {
		return "Obsolete"
	}
	if tbl != nil {
		if tbl.IsVetoed(f.ID) {
			return true
		}
		if tbl.IsUpVoted(f.ID) {
			return false
		}
	}
	return f.Vote == amendment.VoteDefaultNo && supported
}
