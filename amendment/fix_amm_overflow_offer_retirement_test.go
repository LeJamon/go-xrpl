package amendment

import (
	"encoding/hex"
	"slices"
	"testing"
)

func TestFixAMMOverflowOfferRetirement(t *testing.T) {
	const (
		name  = "fixAMMOverflowOffer"
		idHex = "12523DF04B553A0B1AD74F42DDB741DE8DC06A03FC089A0EF197E2A87F1D8107"
	)

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

	if feature.ID != wantID || FeatureFixAMMOverflowOffer != wantID {
		t.Fatalf("%s ID = %X, exported ID = %X, want %s", name, feature.ID, FeatureFixAMMOverflowOffer, idHex)
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
	if slices.Contains(NewTable().Desired(), wantID) {
		t.Fatalf("retired %s must not be proposed by a fresh amendment table", name)
	}
}
