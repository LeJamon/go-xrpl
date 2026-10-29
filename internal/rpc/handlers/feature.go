package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

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
type FeatureMethod struct{ baseHandler }

// amendmentVoteController is the optional capability the live ledger-service
// adapter implements to expose the amendment table and accept vote mutations.
// Test mocks don't implement it, so handlers degrade gracefully.
type amendmentVoteController interface {
	Table() *amendment.Table
	SetAmendmentVote(ctx context.Context, id [32]byte, vetoed bool) error
}

func (m *FeatureMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
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
	isAdmin := ctx.Role.IsAdmin()

	if !request.Feature.present {
		features := make(map[string]any)
		for _, id := range listedFeatureIDs(enabledSet, majorities, tbl) {
			info := buildFeatureInfo(id, enabledSet, tbl, lastVote, isAdmin)
			if ct, ok := majorities[id]; ok {
				info["majority"] = ct
			}
			features[featureHex(id)] = info
		}
		return map[string]any{
			"features": features,
		}, nil
	}

	id, ok := resolveFeatureID(request.Feature.value, tbl)
	if !ok {
		return nil, rpcerrors.RpcErrorBadFeature(badFeatureMessage)
	}

	// Admin vote mutation (rippled AmendmentTableImpl::veto / unVeto).
	if request.Vetoed.present {
		if !isAdmin {
			return nil, rpcerrors.RpcErrorNoPermission("feature")
		}
		if !request.Vetoed.isBool {
			return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
		if ctrl == nil || tbl == nil {
			return nil, rpcerrors.RpcErrorNotSupported("amendment voting is not available on this server")
		}
		listed := featureListed(id, enabledSet, majorities, tbl)
		vote := featureVote(id, tbl)
		if request.Vetoed.value {
			if knownFeature(id) == nil {
				tbl.AddEntry(id, "")
			}
			if vote == featureVoteUp {
				if err := ctrl.SetAmendmentVote(ctx.Context, id, true); err != nil {
					return nil, rpcInternalError("feature: recording amendment vote failed", err)
				}
			}
		} else if listed && vote == featureVoteDown {
			if err := ctrl.SetAmendmentVote(ctx.Context, id, false); err != nil {
				return nil, rpcInternalError("feature: recording amendment vote failed", err)
			}
		}
	}

	if !featureListed(id, enabledSet, majorities, tbl) {
		return nil, rpcerrors.RpcErrorBadFeature(badFeatureMessage)
	}
	response := map[string]any{
		featureHex(id): buildFeatureInfo(id, enabledSet, tbl, lastVote, isAdmin),
	}
	// rippled doFeature sets `majority` beside the entry, not inside it.
	if ct, ok := majorities[id]; ok {
		response["majority"] = ct
	}
	return response, nil
}

const badFeatureMessage = "Feature unknown or invalid."

func featureHex(id [32]byte) string {
	return strings.ToUpper(hex.EncodeToString(id[:]))
}

// knownFeature returns the registry feature rippled's amendment table seeds
// with a name: a supported amendment. Unsupported and subsumed registry
// features are unknown to it until a ledger, a majority or a vote names them.
func knownFeature(id [32]byte) *amendment.Feature {
	f := amendment.FeatureByID(id)
	if f == nil || f.Supported != amendment.SupportedYes || f.Subsumed {
		return nil
	}
	return f
}

// featureListed reports whether rippled's amendment table holds an entry for
// the amendment: every supported amendment, plus any enabled or holding a
// majority on the ledger, upvoted by the operator, or named by a veto request.
func featureListed(id [32]byte, enabledSet map[[32]byte]bool, majorities map[[32]byte]uint32, tbl *amendment.Table) bool {
	if knownFeature(id) != nil || enabledSet[id] {
		return true
	}
	if _, ok := majorities[id]; ok {
		return true
	}
	if tbl == nil {
		return false
	}
	_, named := tbl.EntryName(id)
	return named || tbl.IsUpVoted(id)
}

// listedFeatureIDs returns every amendment featureListed accepts.
func listedFeatureIDs(enabledSet map[[32]byte]bool, majorities map[[32]byte]uint32, tbl *amendment.Table) [][32]byte {
	seen := make(map[[32]byte]bool)
	var ids [][32]byte
	add := func(id [32]byte) {
		if !seen[id] && featureListed(id, enabledSet, majorities, tbl) {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, f := range amendment.AllFeatures() {
		add(f.ID)
	}
	for id := range enabledSet {
		add(id)
	}
	for id := range majorities {
		add(id)
	}
	if tbl != nil {
		for _, id := range tbl.EntryIDs() {
			add(id)
		}
	}
	return ids
}

// featureName returns the name rippled's table holds for the amendment: a
// supported amendment's own name, or the name the vote that added it carried.
func featureName(id [32]byte, tbl *amendment.Table) string {
	if f := knownFeature(id); f != nil {
		return f.Name
	}
	if tbl != nil {
		if name, ok := tbl.EntryName(id); ok {
			return name
		}
	}
	return ""
}

// resolveFeatureID looks up an amendment by the name rippled's table holds, or
// by its 64-character hex ID.
func resolveFeatureID(feature string, tbl *amendment.Table) ([32]byte, bool) {
	if f := amendment.FeatureByName(feature); f != nil && featureName(f.ID, tbl) == feature {
		return f.ID, true
	}
	var id [32]byte
	if len(feature) != 64 {
		return id, false
	}
	idBytes, err := hex.DecodeString(feature)
	if err != nil {
		return id, false
	}
	copy(id[:], idBytes)
	return id, true
}

// amendmentController returns the live amendment-vote controller if the wired
// ledger service exposes one, else nil (e.g. test mocks).
func amendmentController(services *types.ServiceGraph) amendmentVoteController {
	if services == nil || services.Ledger() == nil {
		return nil
	}
	if c, ok := services.Ledger().(amendmentVoteController); ok {
		return c
	}
	return nil
}

// getAmendmentState reads the Amendments SLE from the closed ledger and returns
// the set of amendment hashes enabled on-ledger together with the majority map
// (amendment hash → close time at which it reached majority, XRPL epoch seconds).
// Both are nil if the ledger is unavailable, meaning the caller should fall back
// to deriving enabled status from the registry defaults.
func (m *FeatureMethod) getAmendmentState(services *types.ServiceGraph) (enabled map[[32]byte]bool, majorities map[[32]byte]uint32) {
	if services == nil || services.Ledger() == nil {
		return nil, nil
	}

	view, err := services.ClosedLedgerState()
	if err != nil || view == nil {
		return nil, nil
	}

	data, err := view.Read(keylet.Amendments())
	if err != nil {
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
	majorities = make(map[[32]byte]uint32, len(sle.Majorities))
	for _, mj := range sle.Majorities {
		majorities[mj.Amendment] = mj.CloseTime
	}
	return enabled, majorities
}

type featureVoteState int

const (
	featureVoteDown featureVoteState = iota
	featureVoteUp
	featureVoteObsolete
)

// featureVote mirrors rippled's AmendmentState::vote: obsolete amendments keep
// their vote; otherwise an operator veto or upvote wins over the default, which
// is up only for a supported DefaultYes amendment.
func featureVote(id [32]byte, tbl *amendment.Table) featureVoteState {
	f := knownFeature(id)
	if f != nil && f.Vote == amendment.VoteObsolete {
		return featureVoteObsolete
	}
	if tbl != nil {
		if tbl.IsVetoed(id) {
			return featureVoteDown
		}
		if tbl.IsUpVoted(id) {
			return featureVoteUp
		}
	}
	if f != nil && f.Vote == amendment.VoteDefaultYes {
		return featureVoteUp
	}
	return featureVoteDown
}

// buildFeatureInfo constructs the response map for one amendment table entry.
// `enabledSet` (nil ⇒ fall back to registry defaults) gives the on-ledger enabled
// status; `tbl` (nil ⇒ registry defaults)
// provides operator veto/upvote overrides; when admin and the amendment is not yet
// enabled, `lastVote` supplies the vote tallies. Field emission mirrors rippled
// AmendmentTableImpl::injectJson + doFeature: `name` when the table holds one,
// `enabled`/`supported` always; `vetoed` and `count`/`validations`/`threshold`
// only for an admin viewing a not-yet-enabled amendment.
func buildFeatureInfo(id [32]byte, enabledSet map[[32]byte]bool, tbl *amendment.Table, lastVote *amendment.LastVote, isAdmin bool) map[string]any {
	known := knownFeature(id)
	supported := known != nil

	var enabled bool
	if enabledSet != nil {
		enabled = enabledSet[id]
	} else {
		enabled = supported && known.Vote == amendment.VoteDefaultYes
	}

	info := map[string]any{
		"enabled":   enabled,
		"supported": supported,
	}
	if name := featureName(id, tbl); name != "" {
		info["name"] = name
	}

	// `vetoed` is admin-only and only for amendments not yet enabled.
	if !enabled && isAdmin {
		switch featureVote(id, tbl) {
		case featureVoteObsolete:
			info["vetoed"] = "Obsolete"
		case featureVoteDown:
			info["vetoed"] = true
		default:
			info["vetoed"] = false
		}
	}

	// Admin-only vote tallies for amendments not yet enabled.
	if isAdmin && !enabled && lastVote != nil {
		info["count"] = lastVote.Votes[id]
		info["validations"] = lastVote.TrustedValidations
		if lastVote.Threshold > 0 {
			info["threshold"] = lastVote.Threshold
		}
	}

	return info
}
