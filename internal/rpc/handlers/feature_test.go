package handlers

import (
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
)

func TestBuildFeatureInfo_TableVetoOverride(t *testing.T) {
	f := amendment.GetFeatureByName("DID") // supported, DefaultNo
	if f == nil {
		t.Fatal("DID feature must exist")
	}
	tbl := amendment.NewAmendmentTable()

	// Default (no table): DefaultNo + supported → vetoed true.
	info := buildFeatureInfo(f, map[[32]byte]bool{}, nil, nil, false)
	if info["vetoed"] != true {
		t.Fatalf("default vetoed = %v, want true", info["vetoed"])
	}

	// Operator upvote → vetoed false.
	tbl.UpVote(f.ID)
	info = buildFeatureInfo(f, map[[32]byte]bool{}, tbl, nil, false)
	if info["vetoed"] != false {
		t.Fatalf("upvoted vetoed = %v, want false", info["vetoed"])
	}

	// Operator veto → vetoed true.
	tbl.Veto(f.ID)
	info = buildFeatureInfo(f, map[[32]byte]bool{}, tbl, nil, false)
	if info["vetoed"] != true {
		t.Fatalf("vetoed = %v, want true", info["vetoed"])
	}
}

func TestBuildFeatureInfo_AdminCounts(t *testing.T) {
	f := amendment.GetFeatureByName("DID")
	tbl := amendment.NewAmendmentTable()
	tbl.SetLastVote(&amendment.LastVote{
		TrustedValidations: 10,
		Threshold:          8,
		Votes:              map[[32]byte]int{f.ID: 5},
	})
	lastVote := tbl.LastVote()

	// Non-admin: no tallies.
	info := buildFeatureInfo(f, map[[32]byte]bool{}, tbl, lastVote, false)
	if _, ok := info["count"]; ok {
		t.Fatal("non-admin must not see count")
	}

	// Admin, not enabled: tallies present.
	info = buildFeatureInfo(f, map[[32]byte]bool{}, tbl, lastVote, true)
	if info["count"] != 5 || info["validations"] != 10 || info["threshold"] != 8 {
		t.Fatalf("admin tallies wrong: %+v", info)
	}

	// Admin but enabled: no tallies (rippled only reports for not-enabled).
	info = buildFeatureInfo(f, map[[32]byte]bool{f.ID: true}, tbl, lastVote, true)
	if _, ok := info["count"]; ok {
		t.Fatal("enabled amendment must not report tallies")
	}
}

func TestFeatureVetoed_Obsolete(t *testing.T) {
	// Find an obsolete amendment in the registry, if any.
	var obsolete *amendment.Feature
	for _, f := range amendment.AllFeatures() {
		if f.Vote == amendment.VoteObsolete {
			obsolete = f
			break
		}
	}
	if obsolete == nil {
		t.Skip("no obsolete amendment registered")
	}
	if got := featureVetoed(obsolete, nil, obsolete.Supported == amendment.SupportedYes); got != "Obsolete" {
		t.Fatalf("obsolete vetoed = %v, want \"Obsolete\"", got)
	}
}
