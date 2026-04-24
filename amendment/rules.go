// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package amendment

// Rules provides a read-only view of which amendments are enabled for
// transaction processing and validation. It is typically loaded from
// the Amendments entry in a specific ledger.
type Rules struct {
	// enabled is the set of enabled amendment IDs
	enabled map[[32]byte]bool
}

// NewRules creates a new Rules instance with the given enabled amendments.
func NewRules(enabledIDs [][32]byte) *Rules {
	r := &Rules{
		enabled: make(map[[32]byte]bool, len(enabledIDs)),
	}
	for _, id := range enabledIDs {
		r.enabled[id] = true
	}
	return r
}

// NewRulesFromTable creates a Rules instance from an AmendmentTable.
func NewRulesFromTable(table *AmendmentTable) *Rules {
	return NewRules(table.GetEnabled())
}

// Enabled returns true if the amendment with the given ID is enabled.
// This is the primary method used during transaction processing.
func (r *Rules) Enabled(featureID [32]byte) bool {
	return r.enabled[featureID]
}

// EnabledCount returns the number of enabled amendments.
func (r *Rules) EnabledCount() int {
	return len(r.enabled)
}

// GetEnabled returns a slice of all enabled amendment IDs.
func (r *Rules) GetEnabled() [][32]byte {
	result := make([][32]byte, 0, len(r.enabled))
	for id := range r.enabled {
		result = append(result, id)
	}
	return result
}

// GenesisRules returns Rules with all amendments that should be enabled
// at genesis (VoteDefaultYes amendments).
func GenesisRules() *Rules {
	enabledIDs := make([][32]byte, 0)
	for _, f := range AllFeatures() {
		// Enable all default-yes features and retired features at genesis
		if (f.Vote == VoteDefaultYes || f.Retired) && f.Supported == SupportedYes {
			enabledIDs = append(enabledIDs, f.ID)
		}
	}
	return NewRules(enabledIDs)
}

// EmptyRules returns Rules with no amendments enabled.
// This is useful for testing or for very old ledger states.
func EmptyRules() *Rules {
	return NewRules(nil)
}

// AllSupportedRules returns Rules with all supported amendments enabled.
// This is useful for testing.
func AllSupportedRules() *Rules {
	enabledIDs := make([][32]byte, 0)
	for _, f := range AllFeatures() {
		if f.Supported == SupportedYes {
			enabledIDs = append(enabledIDs, f.ID)
		}
	}
	return NewRules(enabledIDs)
}

// Preset represents a predefined set of amendments for specific purposes.
type Preset int

const (
	// PresetEmpty has no amendments enabled.
	PresetEmpty Preset = iota
	// PresetGenesis has all genesis amendments enabled.
	PresetGenesis
	// PresetAllSupported has all supported amendments enabled.
	PresetAllSupported
)

// RulesForPreset returns Rules for the given preset.
func RulesForPreset(preset Preset) *Rules {
	switch preset {
	case PresetEmpty:
		return EmptyRules()
	case PresetGenesis:
		return GenesisRules()
	case PresetAllSupported:
		return AllSupportedRules()
	default:
		return EmptyRules()
	}
}

// RulesBuilder allows building custom Rules instances.
type RulesBuilder struct {
	enabled map[[32]byte]bool
}

// NewRulesBuilder creates a new RulesBuilder.
func NewRulesBuilder() *RulesBuilder {
	return &RulesBuilder{
		enabled: make(map[[32]byte]bool),
	}
}

// Enable adds an amendment to the enabled set.
func (b *RulesBuilder) Enable(featureID [32]byte) *RulesBuilder {
	b.enabled[featureID] = true
	return b
}

// EnableByName adds an amendment by name to the enabled set.
func (b *RulesBuilder) EnableByName(name string) *RulesBuilder {
	f := GetFeatureByName(name)
	if f != nil {
		b.enabled[f.ID] = true
	}
	return b
}

// Disable removes an amendment from the enabled set.
func (b *RulesBuilder) Disable(featureID [32]byte) *RulesBuilder {
	delete(b.enabled, featureID)
	return b
}

// DisableByName removes an amendment by name from the enabled set.
func (b *RulesBuilder) DisableByName(name string) *RulesBuilder {
	f := GetFeatureByName(name)
	if f != nil {
		delete(b.enabled, f.ID)
	}
	return b
}

// FromPreset initializes the builder from a preset.
func (b *RulesBuilder) FromPreset(preset Preset) *RulesBuilder {
	rules := RulesForPreset(preset)
	for id := range rules.enabled {
		b.enabled[id] = true
	}
	return b
}

// Build creates the Rules instance.
func (b *RulesBuilder) Build() *Rules {
	enabledIDs := make([][32]byte, 0, len(b.enabled))
	for id := range b.enabled {
		enabledIDs = append(enabledIDs, id)
	}
	return NewRules(enabledIDs)
}

// FlowEnabled returns true if the Flow amendment is enabled.
func (r *Rules) FlowEnabled() bool {
	return r.Enabled(FeatureFlow)
}

// ChecksEnabled returns true if the Checks amendment is enabled.
func (r *Rules) ChecksEnabled() bool {
	return r.Enabled(FeatureChecks)
}

// DepositAuthEnabled returns true if the DepositAuth amendment is enabled.
func (r *Rules) DepositAuthEnabled() bool {
	return r.Enabled(FeatureDepositAuth)
}

// DepositPreauthEnabled returns true if the DepositPreauth amendment is enabled.
func (r *Rules) DepositPreauthEnabled() bool {
	return r.Enabled(FeatureDepositPreauth)
}

// AMMEnabled returns true if the AMM amendment is enabled.
func (r *Rules) AMMEnabled() bool {
	return r.Enabled(FeatureAMM)
}

// NFTsEnabled returns true if the NonFungibleTokensV1_1 amendment is enabled.
func (r *Rules) NFTsEnabled() bool {
	return r.Enabled(FeatureNonFungibleTokensV1_1)
}

// ClawbackEnabled returns true if the Clawback amendment is enabled.
func (r *Rules) ClawbackEnabled() bool {
	return r.Enabled(FeatureClawback)
}

// XChainBridgeEnabled returns true if the XChainBridge amendment is enabled.
func (r *Rules) XChainBridgeEnabled() bool {
	return r.Enabled(FeatureXChainBridge)
}

// DIDEnabled returns true if the DID amendment is enabled.
func (r *Rules) DIDEnabled() bool {
	return r.Enabled(FeatureDID)
}

// PriceOracleEnabled returns true if the PriceOracle amendment is enabled.
func (r *Rules) PriceOracleEnabled() bool {
	return r.Enabled(FeaturePriceOracle)
}

// TicketBatchEnabled returns true if the TicketBatch amendment is enabled.
func (r *Rules) TicketBatchEnabled() bool {
	return r.Enabled(FeatureTicketBatch)
}

// ExpandedSignerListEnabled returns true if the ExpandedSignerList amendment is enabled.
func (r *Rules) ExpandedSignerListEnabled() bool {
	return r.Enabled(FeatureExpandedSignerList)
}

// DeletableAccountsEnabled returns true if the DeletableAccounts amendment is enabled.
func (r *Rules) DeletableAccountsEnabled() bool {
	return r.Enabled(FeatureDeletableAccounts)
}

// XRPFeesEnabled returns true if the XRPFees amendment is enabled.
func (r *Rules) XRPFeesEnabled() bool {
	return r.Enabled(FeatureXRPFees)
}

// DisallowIncomingEnabled returns true if the DisallowIncoming amendment is enabled.
func (r *Rules) DisallowIncomingEnabled() bool {
	return r.Enabled(FeatureDisallowIncoming)
}

// RequireFullyCanonicalSigEnabled returns true if the RequireFullyCanonicalSig amendment is enabled.
func (r *Rules) RequireFullyCanonicalSigEnabled() bool {
	return r.Enabled(FeatureRequireFullyCanonicalSig)
}

// CredentialsEnabled returns true if the Credentials amendment is enabled.
func (r *Rules) CredentialsEnabled() bool {
	return r.Enabled(FeatureCredentials)
}

// MPTokensV1Enabled returns true if the MPTokensV1 amendment is enabled.
func (r *Rules) MPTokensV1Enabled() bool {
	return r.Enabled(FeatureMPTokensV1)
}

// DeepFreezeEnabled returns true if the DeepFreeze amendment is enabled.
func (r *Rules) DeepFreezeEnabled() bool {
	return r.Enabled(FeatureDeepFreeze)
}

// BatchEnabled returns true if the Batch amendment is enabled.
func (r *Rules) BatchEnabled() bool {
	return r.Enabled(FeatureBatch)
}

// PermissionedDEXEnabled returns true if the PermissionedDEX amendment is enabled.
func (r *Rules) PermissionedDEXEnabled() bool {
	return r.Enabled(FeaturePermissionedDEX)
}

// TokenEscrowEnabled returns true if the TokenEscrow amendment is enabled.
func (r *Rules) TokenEscrowEnabled() bool {
	return r.Enabled(FeatureTokenEscrow)
}

// SortedDirectoriesEnabled returns true if the SortedDirectories amendment is enabled.
// This is a retired amendment that changed owner directory creation to use sfOwner
// instead of book fields (TakerPaysCurrency, TakerPaysIssuer, etc.).
// Before this amendment, both owner and book directories used book fields.
func (r *Rules) SortedDirectoriesEnabled() bool {
	return r.Enabled(FeatureSortedDirectories)
}
