package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func TestBuildFeatureInfo_TableVetoOverride(t *testing.T) {
	f := amendment.FeatureByName("DID") // supported, DefaultNo
	if f == nil {
		t.Fatal("DID feature must exist")
	}
	tbl := amendment.NewTable()

	// vetoed is admin-only and only for not-yet-enabled amendments
	// (rippled injectJson: `if (!fs.enabled && isAdmin)`).
	// Admin, not enabled, no table override: DefaultNo + supported → vetoed true.
	info := buildFeatureInfo(f.ID, map[[32]byte]bool{}, nil, nil, true)
	if info["vetoed"] != true {
		t.Fatalf("default vetoed = %v, want true", info["vetoed"])
	}

	// Operator upvote → vetoed false.
	tbl.UpVote(f.ID)
	info = buildFeatureInfo(f.ID, map[[32]byte]bool{}, tbl, nil, true)
	if info["vetoed"] != false {
		t.Fatalf("upvoted vetoed = %v, want false", info["vetoed"])
	}

	// Operator veto → vetoed true.
	tbl.Veto(f.ID)
	info = buildFeatureInfo(f.ID, map[[32]byte]bool{}, tbl, nil, true)
	if info["vetoed"] != true {
		t.Fatalf("vetoed = %v, want true", info["vetoed"])
	}

	// Non-admin must never see vetoed.
	info = buildFeatureInfo(f.ID, map[[32]byte]bool{}, tbl, nil, false)
	if _, ok := info["vetoed"]; ok {
		t.Fatal("non-admin must not see vetoed")
	}

	// Admin but already enabled must not see vetoed.
	info = buildFeatureInfo(f.ID, map[[32]byte]bool{f.ID: true}, tbl, nil, true)
	if _, ok := info["vetoed"]; ok {
		t.Fatal("enabled amendment must not report vetoed")
	}
}

func TestBuildFeatureInfo_AdminCounts(t *testing.T) {
	f := amendment.FeatureByName("DID")
	tbl := amendment.NewTable()
	tbl.SetLastVote(&amendment.LastVote{
		TrustedValidations: 10,
		Threshold:          8,
		Votes:              map[[32]byte]int{f.ID: 5},
	})
	lastVote := tbl.LastVote()

	// Non-admin: no tallies.
	info := buildFeatureInfo(f.ID, map[[32]byte]bool{}, tbl, lastVote, false)
	if _, ok := info["count"]; ok {
		t.Fatal("non-admin must not see count")
	}

	// Admin, not enabled: tallies present.
	info = buildFeatureInfo(f.ID, map[[32]byte]bool{}, tbl, lastVote, true)
	if info["count"] != 5 || info["validations"] != 10 || info["threshold"] != 8 {
		t.Fatalf("admin tallies wrong: %+v", info)
	}

	// Admin but enabled: no tallies (rippled only reports for not-enabled).
	info = buildFeatureInfo(f.ID, map[[32]byte]bool{f.ID: true}, tbl, lastVote, true)
	if _, ok := info["count"]; ok {
		t.Fatal("enabled amendment must not report tallies")
	}
}

func TestFeatureVote_Obsolete(t *testing.T) {
	retired := amendment.FeatureByName("fixAMMOverflowOffer")
	if retired == nil || retired.Vote != amendment.VoteObsolete {
		t.Fatal("fixAMMOverflowOffer must be registered as obsolete")
	}
	tbl := amendment.NewTable()
	tbl.UpVote(retired.ID)
	if got := featureVote(retired.ID, tbl); got != featureVoteObsolete {
		t.Fatalf("obsolete vote = %v, want obsolete even when upvoted", got)
	}
	info := buildFeatureInfo(retired.ID, map[[32]byte]bool{}, tbl, nil, true)
	if info["vetoed"] != "Obsolete" {
		t.Fatalf("obsolete vetoed = %v, want \"Obsolete\"", info["vetoed"])
	}
}

// rippled's amendment table is seeded with the supported amendments only; an
// unsupported or subsumed one is listed once the ledger, a majority or an
// operator vote names it, without a name unless the operator upvoted it.
func TestFeatureListedMatchesRippledTable(t *testing.T) {
	unsupported := amendment.FeatureByName("MPTokensV2")
	subsumed := amendment.FeatureByName("fixNFTokenDirV1")
	supported := amendment.FeatureByName("DID")
	if unsupported == nil || unsupported.Supported != amendment.SupportedNo || subsumed == nil || subsumed.Supported != amendment.SupportedNo || supported == nil {
		t.Fatal("test amendments are not registered as expected")
	}
	unknown := [32]byte{0xAB}

	for _, id := range [][32]byte{unsupported.ID, subsumed.ID, unknown} {
		if featureListed(id, map[[32]byte]bool{}, nil, amendment.NewTable()) {
			t.Fatalf("%X listed without a ledger, majority or vote reference", id)
		}
	}
	if !featureListed(supported.ID, nil, nil, nil) {
		t.Fatal("supported amendment must always be listed")
	}

	for name, listed := range map[string]func(id [32]byte) bool{
		"enabled":  func(id [32]byte) bool { return featureListed(id, map[[32]byte]bool{id: true}, nil, nil) },
		"majority": func(id [32]byte) bool { return featureListed(id, nil, map[[32]byte]uint32{id: 1}, nil) },
		"entry": func(id [32]byte) bool {
			tbl := amendment.NewTable()
			tbl.AddEntry(id, "")
			return featureListed(id, nil, nil, tbl)
		},
	} {
		for _, id := range [][32]byte{unsupported.ID, subsumed.ID, unknown} {
			if !listed(id) {
				t.Errorf("%s: %X must be listed", name, id)
			}
		}
	}

	info := buildFeatureInfo(unsupported.ID, map[[32]byte]bool{unsupported.ID: true}, nil, nil, true)
	if _, ok := info["name"]; ok || info["supported"] != false || info["enabled"] != true {
		t.Fatalf("enabled unsupported entry = %v", info)
	}
	info = buildFeatureInfo(unknown, map[[32]byte]bool{}, nil, nil, true)
	if _, ok := info["name"]; ok || info["supported"] != false || info["vetoed"] != true {
		t.Fatalf("unknown entry = %v", info)
	}

	// A config upvote names the amendment; a vote that only names its ID does not.
	tbl := amendment.NewTable()
	tbl.UpVote(unsupported.ID)
	tbl.AddEntry(unsupported.ID, unsupported.Name)
	info = buildFeatureInfo(unsupported.ID, map[[32]byte]bool{}, tbl, nil, true)
	if info["name"] != "MPTokensV2" || info["supported"] != false || info["vetoed"] != false {
		t.Fatalf("config-upvoted unsupported entry = %v", info)
	}
	tbl = amendment.NewTable()
	tbl.AddEntry(unsupported.ID, "")
	tbl.UpVote(unsupported.ID)
	info = buildFeatureInfo(unsupported.ID, map[[32]byte]bool{}, tbl, nil, true)
	if _, ok := info["name"]; ok || info["vetoed"] != false {
		t.Fatalf("vetoed-then-unvetoed unsupported entry = %v", info)
	}
}

// voteRecordingController applies votes to its table like the ledger service
// and counts the persisted ones.
type voteRecordingController struct {
	types.LedgerService
	table     *amendment.Table
	persisted int
}

func (c *voteRecordingController) Table() *amendment.Table { return c.table }
func (c *voteRecordingController) SetAmendmentVote(_ context.Context, id [32]byte, vetoed bool) error {
	c.persisted++
	if vetoed {
		c.table.Veto(id)
	} else {
		c.table.UpVote(id)
	}
	return nil
}
func (*voteRecordingController) GetClosedLedgerView() (types.LedgerStateView, error) {
	return nil, nil
}

// The vetoed parameter follows rippled AmendmentTableImpl::veto / unVeto: a
// veto only changes and persists an up vote, but adds the amendment to the
// table; an unveto only changes and persists a down vote of a listed one.
func TestFeatureVetoFollowsRippledTable(t *testing.T) {
	controller := &voteRecordingController{table: amendment.NewTable()}
	call := func(role types.Role, params string) (map[string]any, *rpcerrors.RpcError) {
		t.Helper()
		result, rpcErr := (&FeatureMethod{}).Handle(&types.RpcContext{
			Context:  context.Background(),
			Role:     role,
			Services: types.NewTestServiceGraph(&types.ServiceContainer{Ledger: controller}),
		}, json.RawMessage(params))
		if rpcErr != nil {
			return nil, rpcErr
		}
		return result.(map[string]any), nil
	}
	unknown := strings.Repeat("CD", 32)

	_, rpcErr := call(types.RoleGuest, `{"feature":"DID","vetoed":false}`)
	require.NotNil(t, rpcErr)
	require.Equal(t, rpcerrors.RpcNO_PERMISSION, rpcErr.Code)

	_, rpcErr = call(types.RoleAdmin, `{"feature":"`+unknown+`","vetoed":false}`)
	require.NotNil(t, rpcErr)
	require.Equal(t, rpcerrors.RpcBAD_FEATURE, rpcErr.Code)
	require.Zero(t, controller.persisted)

	resp, rpcErr := call(types.RoleAdmin, `{"feature":"`+unknown+`","vetoed":true}`)
	require.Nil(t, rpcErr)
	require.Equal(t, map[string]any{"enabled": false, "supported": false, "vetoed": true}, resp[unknown])
	require.Zero(t, controller.persisted, "a down vote is not changed or persisted")
	resp, rpcErr = call(types.RoleAdmin, `{}`)
	require.Nil(t, rpcErr)
	require.Contains(t, resp["features"], unknown, "the vetoed amendment stays listed")

	_, rpcErr = call(types.RoleAdmin, `{"feature":"DID","vetoed":true}`)
	require.Nil(t, rpcErr)
	require.Zero(t, controller.persisted, "DID already defaults to a down vote")

	did := featureHex(amendment.FeatureDID)
	resp, rpcErr = call(types.RoleAdmin, `{"feature":"DID","vetoed":false}`)
	require.Nil(t, rpcErr)
	require.Equal(t, false, resp[did].(map[string]any)["vetoed"])
	require.Equal(t, 1, controller.persisted)

	resp, rpcErr = call(types.RoleAdmin, `{"feature":"DID","vetoed":true}`)
	require.Nil(t, rpcErr)
	require.Equal(t, true, resp[did].(map[string]any)["vetoed"])
	require.Equal(t, 2, controller.persisted)

	resp, rpcErr = call(types.RoleAdmin, `{"feature":"fixAMMOverflowOffer","vetoed":true}`)
	require.Nil(t, rpcErr)
	require.Equal(t, "Obsolete", resp[featureHex(amendment.FeatureFixAMMOverflowOffer)].(map[string]any)["vetoed"])
	require.Equal(t, 2, controller.persisted, "an obsolete vote never changes")

	// Vetoing then unvetoing an unsupported amendment by ID gives it no name.
	mpt := featureHex(amendment.FeatureMPTokensV2)
	_, rpcErr = call(types.RoleAdmin, `{"feature":"`+mpt+`","vetoed":true}`)
	require.Nil(t, rpcErr)
	resp, rpcErr = call(types.RoleAdmin, `{"feature":"`+mpt+`","vetoed":false}`)
	require.Nil(t, rpcErr)
	require.Equal(t, map[string]any{"enabled": false, "supported": false, "vetoed": false}, resp[mpt])
	require.Equal(t, 3, controller.persisted)
}
