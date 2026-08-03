package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
)

// FeatureMethod handles the feature RPC method.
// Returns information about amendments including their status, support, and voting.
// Registered at USER role (rippled Handler.cpp: {"feature", ..., Role::USER}): any
// caller may read amendment status, but the admin-only fields (vetoed, count,
// validations, threshold) and the `vetoed` mutation require admin.
// Reference: rippled Feature1.cpp
type FeatureMethod struct{ BaseHandler }

// amendmentVoteController is the optional capability the live ledger-service
// adapter implements to expose the amendment table and accept vote mutations.
// Test mocks don't implement it, so handlers degrade gracefully.
type amendmentVoteController interface {
	Table() *amendment.Table
	SetAmendmentVote(ctx context.Context, id [32]byte, vetoed bool) error
}

func (m *FeatureMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	var request struct {
		Feature requestField[string] `json:"feature"`
		Vetoed  jsonCppBoolField     `json:"vetoed"`
	}
	if rpcErr := decodeRequestObject(params, &request); rpcErr != nil {
		return nil, rpcErr
	}

	enabledSet, majorities := m.getAmendmentState(ctx.Services)
	ctrl := amendmentController(ctx.Services)
	var tbl *amendment.Table
	if ctrl != nil {
		tbl = ctrl.Table()
	}
	var lastVote *amendment.LastVote
	if tbl != nil {
		lastVote = tbl.LastVote()
	}

	if !request.Feature.present {
		allFeatures := amendment.AllFeatures()
		features := make(map[string]any, len(allFeatures))
		for _, f := range allFeatures {
			hexID := strings.ToUpper(hex.EncodeToString(f.ID[:]))
			features[hexID] = buildFeatureInfo(f, enabledSet, majorities, tbl, lastVote, ctx.IsAdmin)
		}
		return map[string]any{
			"features": features,
		}, nil
	}

	f := resolveFeature(request.Feature.value)
	if f == nil {
		return nil, types.RpcErrorBadFeature("Feature not found: " + request.Feature.value)
	}

	// Admin vote mutation: set or clear a veto on a specific amendment.
	if request.Vetoed.present {
		if !ctx.IsAdmin {
			return nil, types.RpcErrorNoPermission("feature")
		}
		if ctrl == nil || tbl == nil {
			return nil, types.RpcErrorNotSupported("amendment voting is not available on this server")
		}
		if err := ctrl.SetAmendmentVote(ctx.Context, f.ID, request.Vetoed.value); err != nil {
			return nil, rpcInternalError("feature: recording amendment vote failed", err)
		}
		hexID := strings.ToUpper(hex.EncodeToString(f.ID[:]))
		return map[string]any{
			hexID: buildFeatureInfo(f, enabledSet, majorities, tbl, lastVote, ctx.IsAdmin),
		}, nil
	}

	hexID := strings.ToUpper(hex.EncodeToString(f.ID[:]))
	return map[string]any{
		hexID: buildFeatureInfo(f, enabledSet, majorities, tbl, lastVote, ctx.IsAdmin),
	}, nil
}

// resolveFeature looks up an amendment by registry name or 64-char hex ID.
func resolveFeature(feature string) *amendment.Feature {
	if f := amendment.FeatureByName(feature); f != nil {
		return f
	}
	if idBytes, err := hex.DecodeString(feature); err == nil && len(idBytes) == 32 {
		var id [32]byte
		copy(id[:], idBytes)
		return amendment.FeatureByID(id)
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

// getAmendmentState reads the Amendments SLE from the closed ledger and returns
// the set of amendment hashes enabled on-ledger together with the majority map
// (amendment hash → close time at which it reached majority, XRPL epoch seconds).
// Both are nil if the ledger is unavailable, meaning the caller should fall back
// to deriving enabled status from the registry defaults.
func (m *FeatureMethod) getAmendmentState(services *types.ServiceContainer) (enabled map[[32]byte]bool, majorities map[[32]byte]uint32) {
	if services == nil || services.Ledger == nil {
		return nil, nil
	}

	view, err := services.Ledger.GetClosedLedgerView()
	if err != nil || view == nil {
		return nil, nil
	}

	data, err := view.Read(keylet.Amendments())
	if err != nil || data == nil {
		return nil, nil
	}

	sle, err := pseudo.ParseAmendmentsSLE(data)
	if err != nil || sle == nil {
		return nil, nil
	}

	enabled = make(map[[32]byte]bool, len(sle.Amendments))
	for _, hash := range sle.Amendments {
		enabled[hash] = true
	}
	// Retired amendments are permanently enabled but never written to the
	// Amendments object, so fold them in so `feature` reports them enabled.
	for _, id := range amendment.PermanentlyEnabledIDs() {
		enabled[id] = true
	}
	majorities = make(map[[32]byte]uint32, len(sle.Majorities))
	for _, mj := range sle.Majorities {
		majorities[mj.Amendment] = mj.CloseTime
	}
	return enabled, majorities
}

// buildFeatureInfo constructs the response map for a single amendment feature.
// `enabledSet` (nil ⇒ fall back to registry defaults) gives the on-ledger enabled
// status; `majorities` (amendment hash → majority close time) yields the `majority`
// field for amendments holding ledger majority; `tbl` (nil ⇒ registry defaults)
// provides operator veto/upvote overrides; when admin and the amendment is not yet
// enabled, `lastVote` supplies the vote tallies. Field emission mirrors rippled
// AmendmentTableImpl::injectJson + doFeature: `name`/`enabled`/`supported` always;
// `vetoed` and `count`/`validations`/`threshold` only for an admin viewing a
// not-yet-enabled amendment; `majority` whenever the amendment is in the majority set.
func buildFeatureInfo(f *amendment.Feature, enabledSet map[[32]byte]bool, majorities map[[32]byte]uint32, tbl *amendment.Table, lastVote *amendment.LastVote, isAdmin bool) map[string]any {
	supported := f.Supported == amendment.SupportedYes

	var enabled bool
	if enabledSet != nil {
		enabled = enabledSet[f.ID]
	} else {
		// Retired amendments are permanently enabled; otherwise fall back to
		// the default-yes registry vote.
		enabled = supported && (f.Vote == amendment.VoteDefaultYes || f.Retired)
	}

	info := map[string]any{
		"enabled":   enabled,
		"supported": supported,
	}
	if f.Name != "" {
		info["name"] = f.Name
	}

	// `vetoed` is admin-only and only for amendments not yet enabled.
	if !enabled && isAdmin {
		info["vetoed"] = featureVetoed(f, tbl, supported)
	}

	// `majority` is the close time at which the amendment reached majority,
	// present only for amendments currently in the ledger's sfMajorities set.
	if ct, ok := majorities[f.ID]; ok {
		info["majority"] = ct
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
func featureVetoed(f *amendment.Feature, tbl *amendment.Table, supported bool) any {
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
