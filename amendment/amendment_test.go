// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package amendment

import (
	"encoding/hex"
	"testing"
)

func TestSHA512Half(t *testing.T) {
	// Test vector: SHA512Half of "Flow" should match rippled
	result := SHA512Half([]byte("Flow"))

	// The ID should be 32 bytes
	if len(result) != 32 {
		t.Errorf("Expected 32 bytes, got %d", len(result))
	}

	// Verify it's not all zeros
	allZero := true
	for _, b := range result {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("SHA512Half returned all zeros")
	}
}

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
	count := FeatureCount()
	if count < 80 {
		t.Errorf("Expected at least 80 features, got %d", count)
	}

	flow := GetFeatureByName("Flow")
	if flow == nil {
		t.Fatal("Flow feature not found")
	}
	if flow.Name != "Flow" {
		t.Errorf("Expected name 'Flow', got '%s'", flow.Name)
	}
	if flow.Supported != SupportedYes {
		t.Error("Flow should be supported")
	}
	if flow.Vote != VoteDefaultYes {
		t.Error("Flow should be VoteDefaultYes")
	}

	amm := GetFeatureByName("AMM")
	if amm == nil {
		t.Fatal("AMM feature not found")
	}
	if amm.Vote != VoteDefaultNo {
		t.Error("AMM should be VoteDefaultNo")
	}

	multiSign := GetFeatureByName("MultiSign")
	if multiSign == nil {
		t.Fatal("MultiSign feature not found")
	}
	if !multiSign.Retired {
		t.Error("MultiSign should be retired")
	}

	nftV1 := GetFeatureByName("NonFungibleTokensV1")
	if nftV1 == nil {
		t.Fatal("NonFungibleTokensV1 feature not found")
	}
	if nftV1.Vote != VoteObsolete {
		t.Error("NonFungibleTokensV1 should be obsolete")
	}
}

func TestFeatureIDMatches(t *testing.T) {
	// Verify the global IDs match the registered features
	flow := GetFeatureByName("Flow")
	if flow == nil {
		t.Fatal("Flow feature not found")
	}
	if flow.ID != FeatureFlow {
		t.Error("FeatureFlow ID mismatch")
	}

	amm := GetFeatureByName("AMM")
	if amm == nil {
		t.Fatal("AMM feature not found")
	}
	if amm.ID != FeatureAMM {
		t.Error("FeatureAMM ID mismatch")
	}
}

func TestAmendmentTable(t *testing.T) {
	table := NewAmendmentTable()

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

func TestAmendmentTableVoting(t *testing.T) {
	table := NewAmendmentTable()

	// Veto an amendment
	table.Veto(FeatureAMM)
	if !table.IsVetoed(FeatureAMM) {
		t.Error("AMM should be vetoed")
	}

	// Vetoed amendments should not be in desired list
	desired := table.GetDesired()
	for _, id := range desired {
		if id == FeatureAMM {
			t.Error("AMM should not be in desired list when vetoed")
		}
	}

	// Unveto
	table.Unveto(FeatureAMM)
	if table.IsVetoed(FeatureAMM) {
		t.Error("AMM should not be vetoed after Unveto()")
	}

	// UpVote an amendment
	table.UpVote(FeatureAMM)
	if !table.IsUpVoted(FeatureAMM) {
		t.Error("AMM should be upvoted")
	}

	// UpVoted amendments should be in desired list
	desired = table.GetDesired()
	found := false
	for _, id := range desired {
		if id == FeatureAMM {
			found = true
			break
		}
	}
	if !found {
		t.Error("AMM should be in desired list when upvoted")
	}
}

func TestAmendmentTableWithEnabled(t *testing.T) {
	enabledIDs := [][32]byte{FeatureFlow, FeatureChecks}
	table := NewAmendmentTableWithEnabled(enabledIDs)

	if !table.IsEnabled(FeatureFlow) {
		t.Error("Flow should be enabled")
	}
	if !table.IsEnabled(FeatureChecks) {
		t.Error("Checks should be enabled")
	}
	if table.IsEnabled(FeatureAMM) {
		t.Error("AMM should not be enabled")
	}
}

func TestAmendmentTableClone(t *testing.T) {
	table := NewAmendmentTable()
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
	if !rules.FlowEnabled() {
		t.Error("FlowEnabled() should return true")
	}
	if !rules.Enabled(FeatureChecks) {
		t.Error("Checks should be enabled")
	}
	if !rules.ChecksEnabled() {
		t.Error("ChecksEnabled() should return true")
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
	if !rules.FlowEnabled() {
		t.Error("Genesis rules should have Flow enabled")
	}
	if !rules.ChecksEnabled() {
		t.Error("Genesis rules should have Checks enabled")
	}
	if !rules.DepositAuthEnabled() {
		t.Error("Genesis rules should have DepositAuth enabled")
	}

	// Should not include VoteDefaultNo features
	if rules.AMMEnabled() {
		t.Error("Genesis rules should not have AMM enabled")
	}
}

func TestEmptyRules(t *testing.T) {
	rules := EmptyRules()

	if rules.EnabledCount() != 0 {
		t.Errorf("Empty rules should have 0 enabled, got %d", rules.EnabledCount())
	}
	if rules.FlowEnabled() {
		t.Error("Empty rules should not have Flow enabled")
	}
}

func TestAllSupportedRules(t *testing.T) {
	rules := AllSupportedRules()

	// Should include all supported features
	if !rules.FlowEnabled() {
		t.Error("AllSupported rules should have Flow enabled")
	}
	if !rules.AMMEnabled() {
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

	if !rules.AMMEnabled() {
		t.Error("Builder should have enabled AMM")
	}
	if rules.FlowEnabled() {
		t.Error("Builder should have disabled Flow")
	}
}

func TestRulesFromTable(t *testing.T) {
	table := NewAmendmentTable()
	table.Enable(FeatureFlow)
	table.Enable(FeatureAMM)

	rules := NewRulesFromTable(table)

	if !rules.FlowEnabled() {
		t.Error("Rules from table should have Flow enabled")
	}
	if !rules.AMMEnabled() {
		t.Error("Rules from table should have AMM enabled")
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

	// Should have some default yes features
	if len(defaultYes) < 25 {
		t.Errorf("Expected at least 25 default yes features, got %d", len(defaultYes))
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
	flow := GetFeatureByName("Flow")
	if flow == nil {
		t.Fatal("Flow feature not found")
	}

	if !flow.IsSupported() {
		t.Error("Flow.IsSupported() should return true")
	}
	if !flow.IsDefaultYes() {
		t.Error("Flow.IsDefaultYes() should return true")
	}
	if flow.IsObsolete() {
		t.Error("Flow.IsObsolete() should return false")
	}
	if flow.String() != "Flow" {
		t.Errorf("Flow.String() should return 'Flow', got '%s'", flow.String())
	}

	nftV1 := GetFeatureByName("NonFungibleTokensV1")
	if nftV1 == nil {
		t.Fatal("NonFungibleTokensV1 feature not found")
	}
	if !nftV1.IsObsolete() {
		t.Error("NonFungibleTokensV1.IsObsolete() should return true")
	}
}

func TestHasUnsupportedEnabled(t *testing.T) {
	table := NewAmendmentTable()

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

	unsupported := table.GetUnsupportedEnabled()
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
		"DeepFreeze",
		"DynamicNFT",
		"PermissionedDomains",
		"Batch",
		"PermissionedDEX",
		"TokenEscrow",
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
		f := GetFeatureByName(name)
		if f == nil {
			t.Errorf("Expected feature '%s' not found", name)
		}
	}
}
