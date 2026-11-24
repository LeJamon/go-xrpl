package amendment

import (
	"encoding/hex"
	"slices"
	"testing"
)

func TestFixAMMOverflowOfferRetirement(t *testing.T) {
	testAMMFixRetirement(t, "fixAMMOverflowOffer", "12523DF04B553A0B1AD74F42DDB741DE8DC06A03FC089A0EF197E2A87F1D8107", FeatureFixAMMOverflowOffer)
}

func TestFixAMMv1_1Retirement(t *testing.T) {
	testAMMFixRetirement(t, "fixAMMv1_1", "35291ADD2D79EB6991343BDA0912269C817D0F094B02226C1C14AD2858962ED4", FeatureFixAMMv1_1)
}

func testAMMFixRetirement(t *testing.T, name, idHex string, exported [32]byte) {
	t.Helper()
	feature := FeatureByName(name)
	if feature == nil {
		t.Fatalf("%s is not registered", name)
	}
	decoded, err := hex.DecodeString(idHex)
	if err != nil {
		t.Fatalf("decode %s ID: %v", name, err)
	}
	var wantID [32]byte
	copy(wantID[:], decoded)

	if feature.ID != wantID || exported != wantID {
		t.Fatalf("%s ID = %X, exported ID = %X, want %s", name, feature.ID, exported, idHex)
	}
	if feature.Supported != SupportedYes || feature.Vote != VoteObsolete || !feature.Retired {
		t.Fatalf("%s status = supported:%v vote:%v retired:%t, want supported:yes vote:obsolete retired:true", name, feature.Supported, feature.Vote, feature.Retired)
	}
	if !feature.IsSupported() || !feature.IsObsolete() || feature.IsDefaultYes() {
		t.Fatalf("%s helper status is inconsistent with retirement: %+v", name, feature)
	}

	supported := false
	for _, candidate := range SupportedFeatures() {
		if candidate.ID == wantID {
			supported = true
			break
		}
	}
	if !supported {
		t.Fatalf("retired %s must remain advertised as supported", name)
	}
	if !slices.Contains(PermanentlyEnabledIDs(), wantID) {
		t.Fatalf("retired %s must be permanently enabled", name)
	}
	if !GenesisRules().Enabled(wantID) {
		t.Fatalf("retired %s must be enabled by genesis rules", name)
	}
	for _, candidate := range DefaultYesFeatures() {
		if candidate.ID == wantID {
			t.Fatalf("retired %s must be excluded from default-yes voting", name)
		}
	}
	table := NewTable()
	if slices.Contains(table.Desired(), wantID) {
		t.Fatalf("retired %s must not be proposed by a fresh amendment table", name)
	}

	table.UpVote(wantID)
	if slices.Contains(table.Desired(), wantID) {
		t.Fatalf("retired %s must not be proposed after an explicit upvote", name)
	}

	table.Veto(wantID)
	if slices.Contains(table.Desired(), wantID) {
		t.Fatalf("retired %s must not be proposed after a veto", name)
	}
}
