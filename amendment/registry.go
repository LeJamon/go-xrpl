// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package amendment

import (
	"bytes"
	"fmt"
	"sort"
)

// The registry maps below are populated entirely by package variable
// initialization (the FeatureXxx declarations call register* during var init,
// which runs single-threaded before any goroutine sees this package). After
// init(), the maps are read-only — no synchronisation is required around
// FeatureByID / FeatureByName.
var (
	features       = make(map[[32]byte]*Feature)
	featuresByName = make(map[string]*Feature)
	// orderedFeatures is sorted by ID and frozen at init() so voting and
	// amendment-set hashing are deterministic across runs.
	orderedFeatures []*Feature
)

// Active features (newest first, matching rippled order). Each FeatureXxx is
// computed at package var init time and immediately registered in the maps
// above — there is no separate init() write-back, so cross-package callers
// observing FeatureXxx never see a zero ID.
var (
	FeatureSponsor                       = registerFeature("Sponsor", SupportedYes, VoteDefaultNo)
	FeatureBatchV1_1                     = registerFeature("BatchV1_1", SupportedYes, VoteDefaultNo)
	FeatureFixCleanup3_2_0               = registerFix("fixCleanup3_2_0", SupportedYes, VoteDefaultNo)
	FeatureFixCleanup3_1_3               = registerFix("fixCleanup3_1_3", SupportedYes, VoteDefaultYes)
	FeatureMPTokensV2                    = registerFeature("MPTokensV2", SupportedNo, VoteDefaultNo)
	FeatureConfidentialTransfer          = registerFeature("ConfidentialTransfer", SupportedNo, VoteDefaultNo)
	FeatureFixDirectoryLimit             = registerFix("fixDirectoryLimit", SupportedYes, VoteDefaultNo)
	FeatureFixIncludeKeyletFields        = registerFix("fixIncludeKeyletFields", SupportedYes, VoteDefaultNo)
	FeatureDynamicMPT                    = registerFeature("DynamicMPT", SupportedYes, VoteDefaultNo)
	FeatureFixTokenEscrowV1              = registerFix("fixTokenEscrowV1", SupportedYes, VoteDefaultNo)
	FeatureFixPriceOracleOrder           = registerFix("fixPriceOracleOrder", SupportedYes, VoteDefaultNo)
	FeatureFixMPTDeliveredAmount         = registerFix("fixMPTDeliveredAmount", SupportedYes, VoteDefaultNo)
	FeatureFixAMMClawbackRounding        = registerFix("fixAMMClawbackRounding", SupportedYes, VoteDefaultNo)
	FeatureTokenEscrow                   = registerFeature("TokenEscrow", SupportedYes, VoteDefaultNo)
	FeatureFixEnforceNFTokenTrustlineV2  = registerFix("fixEnforceNFTokenTrustlineV2", SupportedYes, VoteDefaultNo)
	FeatureFixAMMv1_3                    = registerFix("fixAMMv1_3", SupportedYes, VoteDefaultNo)
	FeaturePermissionedDEX               = registerFeature("PermissionedDEX", SupportedYes, VoteDefaultNo)
	FeatureLendingProtocol               = registerFeature("LendingProtocol", SupportedYes, VoteDefaultNo)
	FeatureSingleAssetVault              = registerFeature("SingleAssetVault", SupportedYes, VoteDefaultNo)
	FeaturePermissionDelegationV1_1      = registerFeature("PermissionDelegationV1_1", SupportedNo, VoteDefaultNo)
	FeatureFixPayChanCancelAfter         = registerFix("fixPayChanCancelAfter", SupportedYes, VoteDefaultNo)
	FeatureFixInvalidTxFlags             = registerFix("fixInvalidTxFlags", SupportedYes, VoteDefaultNo)
	FeatureFixFrozenLPTokenTransfer      = registerFix("fixFrozenLPTokenTransfer", SupportedYes, VoteDefaultNo)
	FeatureDeepFreeze                    = registerFeature("DeepFreeze", SupportedYes, VoteDefaultNo)
	FeaturePermissionedDomains           = registerFeature("PermissionedDomains", SupportedYes, VoteDefaultNo)
	FeatureDynamicNFT                    = registerFeature("DynamicNFT", SupportedYes, VoteDefaultNo)
	FeatureCredentials                   = registerFeature("Credentials", SupportedYes, VoteDefaultNo)
	FeatureAMMClawback                   = registerFeature("AMMClawback", SupportedYes, VoteDefaultNo)
	FeatureFixAMMv1_2                    = registerFix("fixAMMv1_2", SupportedYes, VoteDefaultNo)
	FeatureMPTokensV1                    = registerFeature("MPTokensV1", SupportedYes, VoteDefaultNo)
	FeatureInvariantsV1_1                = registerFeature("InvariantsV1_1", SupportedNo, VoteDefaultNo)
	FeatureFixNFTokenPageLinks           = registerFix("fixNFTokenPageLinks", SupportedYes, VoteDefaultNo)
	FeatureFixInnerObjTemplate2          = registerFix("fixInnerObjTemplate2", SupportedYes, VoteDefaultNo)
	FeatureFixEnforceNFTokenTrustline    = registerFix("fixEnforceNFTokenTrustline", SupportedYes, VoteDefaultNo)
	FeatureFixReducedOffersV2            = registerFix("fixReducedOffersV2", SupportedYes, VoteDefaultNo)
	FeatureNFTokenMintOffer              = registerFeature("NFTokenMintOffer", SupportedYes, VoteDefaultNo)
	FeatureFixAMMv1_1                    = registerFix("fixAMMv1_1", SupportedYes, VoteDefaultNo)
	FeatureFixPreviousTxnID              = registerFix("fixPreviousTxnID", SupportedYes, VoteDefaultNo)
	FeatureFixXChainRewardRounding       = registerFix("fixXChainRewardRounding", SupportedYes, VoteDefaultNo)
	FeatureFixEmptyDID                   = registerFix("fixEmptyDID", SupportedYes, VoteDefaultNo)
	FeaturePriceOracle                   = registerFeature("PriceOracle", SupportedYes, VoteDefaultNo)
	FeatureFixAMMOverflowOffer           = registerFix("fixAMMOverflowOffer", SupportedYes, VoteDefaultYes)
	FeatureFixInnerObjTemplate           = registerFix("fixInnerObjTemplate", SupportedYes, VoteDefaultNo)
	FeatureFixNFTokenReserve             = registerFix("fixNFTokenReserve", SupportedYes, VoteDefaultNo)
	FeatureFixFillOrKill                 = registerFix("fixFillOrKill", SupportedYes, VoteDefaultNo)
	FeatureDID                           = registerFeature("DID", SupportedYes, VoteDefaultNo)
	FeatureFixDisallowIncomingV1         = registerFix("fixDisallowIncomingV1", SupportedYes, VoteDefaultNo)
	FeatureXChainBridge                  = registerFeature("XChainBridge", SupportedNo, VoteDefaultNo)
	FeatureAMM                           = registerFeature("AMM", SupportedYes, VoteDefaultNo)
	FeatureClawback                      = registerFeature("Clawback", SupportedYes, VoteDefaultNo)
	FeatureFixUniversalNumber            = registerFix("fixUniversalNumber", SupportedYes, VoteDefaultNo)
	FeatureXRPFees                       = registerFeature("XRPFees", SupportedYes, VoteDefaultNo)
	FeatureFixRemoveNFTokenAutoTrustLine = registerFix("fixRemoveNFTokenAutoTrustLine", SupportedYes, VoteDefaultYes)
)

// Obsolete features (supported but no longer voted on).
var (
	FeatureFixNFTokenNegOffer  = registerFix("fixNFTokenNegOffer", SupportedYes, VoteObsolete)
	FeatureFixNFTokenDirV1     = registerFix("fixNFTokenDirV1", SupportedYes, VoteObsolete)
	FeatureNonFungibleTokensV1 = registerFeature("NonFungibleTokensV1", SupportedYes, VoteObsolete)
)

// Retired features (active for 2+ years, pre-amendment code removed). Order
// mirrors rippled's features.macro retired section (fixes then features, each
// alphabetical). Retired amendments stay registered and supported so ledgers
// listing them remain valid, but are never voted on and their gated behaviour
// is the unconditional baseline.
var (
	FeatureFix1201                     = registerRetired("fix1201")
	FeatureFix1368                     = registerRetired("fix1368")
	FeatureFix1373                     = registerRetired("fix1373")
	FeatureFix1512                     = registerRetired("fix1512")
	FeatureFix1513                     = registerRetired("fix1513")
	FeatureFix1515                     = registerRetired("fix1515")
	FeatureFix1523                     = registerRetired("fix1523")
	FeatureFix1528                     = registerRetired("fix1528")
	FeatureFix1543                     = registerRetired("fix1543")
	FeatureFix1571                     = registerRetired("fix1571")
	FeatureFix1578                     = registerRetired("fix1578")
	FeatureFix1623                     = registerRetired("fix1623")
	FeatureFix1781                     = registerRetired("fix1781")
	FeatureFixAmendmentMajorityCalc    = registerRetired("fixAmendmentMajorityCalc")
	FeatureFixCheckThreading           = registerRetired("fixCheckThreading")
	FeatureFixMasterKeyAsRegularKey    = registerRetired("fixMasterKeyAsRegularKey")
	FeatureFixNonFungibleTokensV1_2    = registerRetired("fixNonFungibleTokensV1_2")
	FeatureFixNFTokenRemint            = registerRetired("fixNFTokenRemint")
	FeatureFixPayChanRecipientOwnerDir = registerRetired("fixPayChanRecipientOwnerDir")
	FeatureFixQualityUpperBound        = registerRetired("fixQualityUpperBound")
	FeatureFixReducedOffersV1          = registerRetired("fixReducedOffersV1")
	FeatureFixRmSmallIncreasedQOffers  = registerRetired("fixRmSmallIncreasedQOffers")
	FeatureFixSTAmountCanonicalize     = registerRetired("fixSTAmountCanonicalize")
	FeatureFixTakerDryOfferRemoval     = registerRetired("fixTakerDryOfferRemoval")
	FeatureFixTrustLinesToSelf         = registerRetired("fixTrustLinesToSelf")
	FeatureChecks                      = registerRetired("Checks")
	FeatureCheckCashMakesTrustLine     = registerRetired("CheckCashMakesTrustLine")
	FeatureCryptoConditions            = registerRetired("CryptoConditions")
	FeatureCryptoConditionsSuite       = registerRetired("CryptoConditionsSuite")
	FeatureDeletableAccounts           = registerRetired("DeletableAccounts")
	FeatureDepositAuth                 = registerRetired("DepositAuth")
	FeatureDepositPreauth              = registerRetired("DepositPreauth")
	FeatureDisallowIncoming            = registerRetired("DisallowIncoming")
	FeatureEscrow                      = registerRetired("Escrow")
	FeatureEnforceInvariants           = registerRetired("EnforceInvariants")
	FeatureExpandedSignerList          = registerRetired("ExpandedSignerList")
	FeatureFeeEscalation               = registerRetired("FeeEscalation")
	FeatureFlow                        = registerRetired("Flow")
	FeatureFlowCross                   = registerRetired("FlowCross")
	FeatureFlowSortStrands             = registerRetired("FlowSortStrands")
	FeatureHardenedValidations         = registerRetired("HardenedValidations")
	FeatureImmediateOfferKilled        = registerRetired("ImmediateOfferKilled")
	FeatureMultiSign                   = registerRetired("MultiSign")
	FeatureMultiSignReserve            = registerRetired("MultiSignReserve")
	FeatureNegativeUNL                 = registerRetired("NegativeUNL")
	FeatureNonFungibleTokensV1_1       = registerRetired("NonFungibleTokensV1_1")
	FeaturePayChan                     = registerRetired("PayChan")
	FeatureRequireFullyCanonicalSig    = registerRetired("RequireFullyCanonicalSig")
	FeatureSortedDirectories           = registerRetired("SortedDirectories")
	FeatureTicketBatch                 = registerRetired("TicketBatch")
	FeatureTickSize                    = registerRetired("TickSize")
	FeatureTrustSetAuth                = registerRetired("TrustSetAuth")
)

func init() {
	orderedFeatures = make([]*Feature, 0, len(features))
	for _, f := range features {
		orderedFeatures = append(orderedFeatures, f)
	}
	sort.Slice(orderedFeatures, func(i, j int) bool {
		return bytes.Compare(orderedFeatures[i].ID[:], orderedFeatures[j].ID[:]) < 0
	})
}

// registerFeature registers a feature and returns its ID. Called from
// package var initializers so the returned ID is available before init()
// and before any cross-package init() that imports this package.
// Panics on duplicate-id so a copy-paste of a feature name fails at
// process start rather than silently overwriting an existing entry.
func registerFeature(name string, supported Supported, vote VoteBehavior) [32]byte {
	return register(name, supported, vote, false)
}

// registerFix registers a fix (amendment that fixes a bug). Fix names are
// used as-is for ID derivation; the "fix" prefix is part of the name itself.
func registerFix(name string, supported Supported, vote VoteBehavior) [32]byte {
	return register(name, supported, vote, false)
}

// registerRetired registers a feature that has been active long enough that
// its pre-amendment code has been removed from rippled. Mirrors rippled's
// retireFeature (Supported::yes, VoteBehavior::Obsolete): retired features are
// still supported but voted Obsolete so Vote-based filters never re-propose
// them. Genesis still enables them via the Retired flag (see rules.go).
func registerRetired(name string) [32]byte {
	return register(name, SupportedYes, VoteObsolete, true)
}

func register(name string, supported Supported, vote VoteBehavior, retired bool) [32]byte {
	id := FeatureID(name)
	if existing, dup := features[id]; dup {
		panic(fmt.Sprintf("amendment: duplicate feature registration: %q collides with %q (id %x)", name, existing.Name, id))
	}
	f := &Feature{
		Name:      name,
		ID:        id,
		Supported: supported,
		Vote:      vote,
		Retired:   retired,
	}
	features[id] = f
	featuresByName[name] = f
	return id
}

// FeatureByID returns a copy of the feature with the given ID, or nil if not
// found. The returned pointer is independent of the process-wide registry, so
// mutating it cannot corrupt global amendment/voting state.
func FeatureByID(id [32]byte) *Feature {
	f, ok := features[id]
	if !ok {
		return nil
	}
	cp := *f
	return &cp
}

// FeatureByName returns a copy of the feature with the given name, or nil if
// not found. The returned pointer is independent of the process-wide registry.
func FeatureByName(name string) *Feature {
	f, ok := featuresByName[name]
	if !ok {
		return nil
	}
	cp := *f
	return &cp
}

// AllFeatures returns copies of all registered features in ID-sorted order.
// The slice and its elements are independent of the registry.
func AllFeatures() []*Feature {
	out := make([]*Feature, len(orderedFeatures))
	for i, f := range orderedFeatures {
		cp := *f
		out[i] = &cp
	}
	return out
}

// SupportedFeatures returns copies of the supported features in ID-sorted order.
func SupportedFeatures() []*Feature {
	result := make([]*Feature, 0, len(orderedFeatures))
	for _, f := range orderedFeatures {
		if f.Supported == SupportedYes {
			cp := *f
			result = append(result, &cp)
		}
	}
	return result
}

// DefaultYesFeatures returns copies of the features that default to a yes vote,
// in ID-sorted order.
func DefaultYesFeatures() []*Feature {
	result := make([]*Feature, 0, len(orderedFeatures))
	for _, f := range orderedFeatures {
		if f.Vote == VoteDefaultYes && !f.Retired {
			cp := *f
			result = append(result, &cp)
		}
	}
	return result
}
