// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package amendment

import (
	"bytes"
	"encoding/hex"
	"slices"
	"testing"
)

func TestFeatureID(t *testing.T) {
	// FeatureID should return consistent results
	id1 := FeatureID("Flow")
	id2 := FeatureID("Flow")

	if id1 != id2 {
		t.Error("FeatureID not consistent")
	}

	// Different names should give different IDs
	id3 := FeatureID("Checks")
	if id1 == id3 {
		t.Error("Different names gave same ID")
	}
}

func TestFeatureRegistry(t *testing.T) {
	count := len(AllFeatures())
	if count < 80 {
		t.Errorf("Expected at least 80 features, got %d", count)
	}

	flow := FeatureByName("Flow")
	if flow == nil {
		t.Fatal("Flow feature not found")
	}
	if flow.Name != "Flow" {
		t.Errorf("Expected name 'Flow', got '%s'", flow.Name)
	}
	if flow.Supported != SupportedYes {
		t.Error("Flow should be supported")
	}
	// Flow was retired in the rippled 3.2.0 wave: still supported, but voted
	// Obsolete and permanently enabled.
	if !flow.Retired {
		t.Error("Flow should be retired")
	}
	if flow.Vote != VoteObsolete {
		t.Error("Flow should vote Obsolete after retirement")
	}

	amm := FeatureByName("AMM")
	if amm == nil {
		t.Fatal("AMM feature not found")
	}
	if amm.Vote != VoteDefaultNo {
		t.Error("AMM should be VoteDefaultNo")
	}

	multiSign := FeatureByName("MultiSign")
	if multiSign == nil {
		t.Fatal("MultiSign feature not found")
	}
	if !multiSign.Retired {
		t.Error("MultiSign should be retired")
	}

	nftV1 := FeatureByName("NonFungibleTokensV1")
	if nftV1 == nil {
		t.Fatal("NonFungibleTokensV1 feature not found")
	}
	if nftV1.Vote != VoteObsolete {
		t.Error("NonFungibleTokensV1 should be obsolete")
	}
}

func TestFeatureIDMatches(t *testing.T) {
	// Verify the global IDs match the registered features
	flow := FeatureByName("Flow")
	if flow == nil {
		t.Fatal("Flow feature not found")
	}
	if flow.ID != FeatureFlow {
		t.Error("FeatureFlow ID mismatch")
	}

	amm := FeatureByName("AMM")
	if amm == nil {
		t.Fatal("AMM feature not found")
	}
	if amm.ID != FeatureAMM {
		t.Error("FeatureAMM ID mismatch")
	}

	sponsor := FeatureByName("Sponsor")
	if sponsor == nil {
		t.Fatal("Sponsor feature not found")
	}
	const sponsorID = "BE1F90581635DBCEBFC4678C4B54FEDDC1A17B50FD02CFE765A4132A342126AC"
	want, err := hex.DecodeString(sponsorID)
	if err != nil {
		t.Fatalf("decode Sponsor amendment ID: %v", err)
	}
	if got := sponsor.ID[:]; !bytes.Equal(got, want) {
		t.Errorf("Sponsor ID = %X, want %s", got, sponsorID)
	}
	if sponsor.Supported != SupportedNo || sponsor.Vote != VoteDefaultNo {
		t.Errorf("Sponsor support/vote = (%v, %v), want (SupportedNo, VoteDefaultNo)", sponsor.Supported, sponsor.Vote)
	}
	if AllSupportedRules().Enabled(FeatureSponsor) {
		t.Error("unsupported Sponsor amendment must not be enabled by the all-supported preset")
	}
}

func TestTable(t *testing.T) {
	table := NewTable()

	// Initially nothing should be enabled
	if table.IsEnabled(FeatureFlow) {
		t.Error("Flow should not be enabled initially")
	}

	// Enable Flow
	table.Enable(FeatureFlow)
	if !table.IsEnabled(FeatureFlow) {
		t.Error("Flow should be enabled after Enable()")
	}

	if !table.IsSupported(FeatureFlow) {
		t.Error("Flow should be supported")
	}

	if table.EnabledCount() != 1 {
		t.Errorf("Expected 1 enabled, got %d", table.EnabledCount())
	}

	// Disable
	table.Disable(FeatureFlow)
	if table.IsEnabled(FeatureFlow) {
		t.Error("Flow should be disabled after Disable()")
	}
}

func TestTableVoting(t *testing.T) {
	table := NewTable()

	// Veto an amendment
	table.Veto(FeatureAMM)
	if !table.IsVetoed(FeatureAMM) {
		t.Error("AMM should be vetoed")
	}

	// Unveto
	table.Unveto(FeatureAMM)
	if table.IsVetoed(FeatureAMM) {
		t.Error("AMM should not be vetoed after Unveto()")
	}

	// UpVote an amendment; this also clears any veto.
	table.UpVote(FeatureAMM)
	if !table.IsUpVoted(FeatureAMM) {
		t.Error("AMM should be upvoted")
	}
	if table.IsVetoed(FeatureAMM) {
		t.Error("UpVote should clear any veto")
	}
}

func TestTableDesired(t *testing.T) {
	table := NewTable()
	defaultYes := DefaultYesFeatures()[0].ID

	if !slices.Contains(table.Desired(), defaultYes) {
		t.Fatal("Desired omitted a default-yes amendment")
	}

	table.Veto(defaultYes)
	if slices.Contains(table.Desired(), defaultYes) {
		t.Fatal("Desired retained a vetoed amendment")
	}

	table.UpVote(FeatureAMM)
	if !slices.Contains(table.Desired(), FeatureAMM) {
		t.Fatal("Desired omitted an explicitly upvoted supported amendment")
	}
}

func TestRetiredFeaturesVoteObsolete(t *testing.T) {
	// Retired features must vote Obsolete so the consensus adaptor's vote policy
	// never re-proposes amendments whose pre-amendment code no longer exists.
	for _, f := range AllFeatures() {
		if !f.Retired {
			continue
		}
		if f.Vote != VoteObsolete {
			t.Errorf("retired feature %s should vote Obsolete, got %v", f.Name, f.Vote)
		}
		if !f.IsObsolete() {
			t.Errorf("retired feature %s IsObsolete() should be true", f.Name)
		}
	}
}

func TestTableClone(t *testing.T) {
	table := NewTable()
	table.Enable(FeatureFlow)
	table.Veto(FeatureAMM)

	clone := table.Clone()

	if !clone.IsEnabled(FeatureFlow) {
		t.Error("Clone should have Flow enabled")
	}
	if !clone.IsVetoed(FeatureAMM) {
		t.Error("Clone should have AMM vetoed")
	}

	// Modify original, clone should not change
	table.Disable(FeatureFlow)
	if !clone.IsEnabled(FeatureFlow) {
		t.Error("Clone should still have Flow enabled")
	}
}

func TestRules(t *testing.T) {
	enabledIDs := [][32]byte{FeatureFlow, FeatureChecks}
	rules := NewRules(enabledIDs)

	if !rules.Enabled(FeatureFlow) {
		t.Error("Flow should be enabled")
	}
	if !rules.Enabled(FeatureChecks) {
		t.Error("Checks should be enabled")
	}
	if rules.Enabled(FeatureAMM) {
		t.Error("AMM should not be enabled")
	}
	if rules.EnabledCount() != 2 {
		t.Errorf("Expected 2 enabled, got %d", rules.EnabledCount())
	}
}

func TestGenesisRules(t *testing.T) {
	rules := GenesisRules()

	// Genesis rules should include all VoteDefaultYes features
	if !rules.Enabled(FeatureFlow) {
		t.Error("Genesis rules should have Flow enabled")
	}
	if !rules.Enabled(FeatureChecks) {
		t.Error("Genesis rules should have Checks enabled")
	}
	if !rules.Enabled(FeatureDepositAuth) {
		t.Error("Genesis rules should have DepositAuth enabled")
	}

	// Should not include VoteDefaultNo features
	if rules.Enabled(FeatureAMM) {
		t.Error("Genesis rules should not have AMM enabled")
	}
}

func TestEmptyRules(t *testing.T) {
	rules := EmptyRules()

	if rules.EnabledCount() != 0 {
		t.Errorf("Empty rules should have 0 enabled, got %d", rules.EnabledCount())
	}
	if rules.Enabled(FeatureFlow) {
		t.Error("Empty rules should not have Flow enabled")
	}
}

func TestAllSupportedRules(t *testing.T) {
	rules := AllSupportedRules()

	// Should include all supported features
	if !rules.Enabled(FeatureFlow) {
		t.Error("AllSupported rules should have Flow enabled")
	}
	if !rules.Enabled(FeatureAMM) {
		t.Error("AllSupported rules should have AMM enabled")
	}

	// Count should match supported features
	supportedCount := len(SupportedFeatures())
	if rules.EnabledCount() != supportedCount {
		t.Errorf("Expected %d enabled, got %d", supportedCount, rules.EnabledCount())
	}
}

func TestRulesBuilder(t *testing.T) {
	rules := NewRulesBuilder().
		FromPreset(PresetGenesis).
		EnableByName("AMM").
		DisableByName("Flow").
		Build()

	if !rules.Enabled(FeatureAMM) {
		t.Error("Builder should have enabled AMM")
	}
	if rules.Enabled(FeatureFlow) {
		t.Error("Builder should have disabled Flow")
	}
}

func TestSupportedFeatures(t *testing.T) {
	supported := SupportedFeatures()

	// Should have many supported features
	if len(supported) < 70 {
		t.Errorf("Expected at least 70 supported features, got %d", len(supported))
	}

	// All returned features should be supported
	for _, f := range supported {
		if f.Supported != SupportedYes {
			t.Errorf("Feature %s should be supported", f.Name)
		}
	}
}

func TestDefaultYesFeatures(t *testing.T) {
	defaultYes := DefaultYesFeatures()

	// After the 3.2.0 retirement wave, only a handful of active fixes still
	// default to a yes vote (fixCleanup3_1_3, fixAMMOverflowOffer,
	// fixRemoveNFTokenAutoTrustLine).
	if len(defaultYes) < 3 {
		t.Errorf("Expected at least 3 default yes features, got %d", len(defaultYes))
	}

	// All returned features should be default yes and not retired
	for _, f := range defaultYes {
		if f.Vote != VoteDefaultYes {
			t.Errorf("Feature %s should be VoteDefaultYes", f.Name)
		}
		if f.Retired {
			t.Errorf("Feature %s should not be retired", f.Name)
		}
	}
}

func TestFeatureHelperMethods(t *testing.T) {
	flow := FeatureByName("Flow")
	if flow == nil {
		t.Fatal("Flow feature not found")
	}

	if !flow.IsSupported() {
		t.Error("Flow.IsSupported() should return true")
	}
	// Retired: no longer default-yes, now votes Obsolete.
	if flow.IsDefaultYes() {
		t.Error("Flow.IsDefaultYes() should return false after retirement")
	}
	if !flow.IsObsolete() {
		t.Error("Flow.IsObsolete() should return true after retirement")
	}
	if flow.String() != "Flow" {
		t.Errorf("Flow.String() should return 'Flow', got '%s'", flow.String())
	}

	nftV1 := FeatureByName("NonFungibleTokensV1")
	if nftV1 == nil {
		t.Fatal("NonFungibleTokensV1 feature not found")
	}
	if !nftV1.IsObsolete() {
		t.Error("NonFungibleTokensV1.IsObsolete() should return true")
	}
}

func TestHasUnsupportedEnabled(t *testing.T) {
	table := NewTable()

	// Initially no unsupported enabled
	if table.HasUnsupportedEnabled() {
		t.Error("Should not have unsupported enabled initially")
	}

	// Enable a supported amendment
	table.Enable(FeatureFlow)
	if table.HasUnsupportedEnabled() {
		t.Error("Should not have unsupported enabled with only Flow")
	}

	// Enable an unknown amendment (simulating a future amendment)
	var unknownID [32]byte
	unknownID[0] = 0xFF
	table.Enable(unknownID)
	if !table.HasUnsupportedEnabled() {
		t.Error("Should have unsupported enabled with unknown ID")
	}

	unsupported := table.UnsupportedEnabledIDs()
	if len(unsupported) != 1 {
		t.Errorf("Expected 1 unsupported, got %d", len(unsupported))
	}
}

// TestFeatureIDsAreUnique ensures all registered features have unique IDs
func TestFeatureIDsAreUnique(t *testing.T) {
	features := AllFeatures()
	seen := make(map[[32]byte]string)

	for _, f := range features {
		if existing, ok := seen[f.ID]; ok {
			t.Errorf("Duplicate ID: %s and %s have same ID %s",
				existing, f.Name, hex.EncodeToString(f.ID[:]))
		}
		seen[f.ID] = f.Name
	}
}

// TestAllExpectedFeaturesExist checks that key features are registered
func TestAllExpectedFeaturesExist(t *testing.T) {
	expectedFeatures := []string{
		"Flow",
		"Checks",
		"DepositAuth",
		"AMM",
		"Clawback",
		"XChainBridge",
		"DID",
		"PriceOracle",
		"NonFungibleTokensV1_1",
		"TicketBatch",
		"XRPFees",
		"DisallowIncoming",
		"DeletableAccounts",
		"DepositPreauth",
		"MultiSignReserve",
		"HardenedValidations",
		"RequireFullyCanonicalSig",
		"NegativeUNL",
		"FlowSortStrands",
		"ExpandedSignerList",
		"CheckCashMakesTrustLine",
		"ImmediateOfferKilled",
		"NFTokenMintOffer",
		"Credentials",
		"AMMClawback",
		"MPTokensV1",
		"MPTokensV2",
		"Sponsor",
		"DeepFreeze",
		"DynamicNFT",
		"PermissionedDomains",
		"Batch",
		"PermissionedDEX",
		"TokenEscrow",
		"fixTokenEscrowV1",
		"fixCleanup3_2_0",
		// Retired
		"MultiSign",
		"TrustSetAuth",
		"FeeEscalation",
		"PayChan",
		"Escrow",
		"EnforceInvariants",
		"FlowCross",
		// Obsolete
		"NonFungibleTokensV1",
		"CryptoConditionsSuite",
	}

	for _, name := range expectedFeatures {
		f := FeatureByName(name)
		if f == nil {
			t.Errorf("Expected feature '%s' not found", name)
		}
	}
}
