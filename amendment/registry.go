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
// GetFeature / GetFeatureByName / FeatureCount.
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
	FeatureFixIncludeKeyletFields        = registerFix("fixIncludeKeyletFields", SupportedYes, VoteDefaultNo)
	FeatureFixDirectoryLimit             = registerFix("fixDirectoryLimit", SupportedYes, VoteDefaultNo)
	FeatureFixPriceOracleOrder           = registerFix("fixPriceOracleOrder", SupportedNo, VoteDefaultNo)
	FeatureFixMPTDeliveredAmount         = registerFix("fixMPTDeliveredAmount", SupportedNo, VoteDefaultNo)
	FeatureFixAMMClawbackRounding        = registerFix("fixAMMClawbackRounding", SupportedNo, VoteDefaultNo)
	FeatureTokenEscrow                   = registerFeature("TokenEscrow", SupportedYes, VoteDefaultNo)
	FeatureFixEnforceNFTokenTrustlineV2  = registerFix("fixEnforceNFTokenTrustlineV2", SupportedYes, VoteDefaultNo)
	FeatureFixAMMv1_3                    = registerFix("fixAMMv1_3", SupportedYes, VoteDefaultNo)
	FeaturePermissionedDEX               = registerFeature("PermissionedDEX", SupportedYes, VoteDefaultNo)
	FeatureBatch                         = registerFeature("Batch", SupportedYes, VoteDefaultNo)
	FeatureSingleAssetVault              = registerFeature("SingleAssetVault", SupportedNo, VoteDefaultNo)
	FeaturePermissionDelegation          = registerFeature("PermissionDelegation", SupportedNo, VoteDefaultNo)
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
	FeatureFixReducedOffersV1            = registerFix("fixReducedOffersV1", SupportedYes, VoteDefaultNo)
	FeatureFixNFTokenRemint              = registerFix("fixNFTokenRemint", SupportedYes, VoteDefaultNo)
	FeatureFixNonFungibleTokensV1_2      = registerFix("fixNonFungibleTokensV1_2", SupportedYes, VoteDefaultNo)
	FeatureFixUniversalNumber            = registerFix("fixUniversalNumber", SupportedYes, VoteDefaultNo)
	FeatureXRPFees                       = registerFeature("XRPFees", SupportedYes, VoteDefaultNo)
	FeatureDisallowIncoming              = registerFeature("DisallowIncoming", SupportedYes, VoteDefaultNo)
	FeatureImmediateOfferKilled          = registerFeature("ImmediateOfferKilled", SupportedYes, VoteDefaultNo)
	FeatureFixRemoveNFTokenAutoTrustLine = registerFix("fixRemoveNFTokenAutoTrustLine", SupportedYes, VoteDefaultYes)
	FeatureFixTrustLinesToSelf           = registerFix("fixTrustLinesToSelf", SupportedYes, VoteDefaultNo)
	FeatureNonFungibleTokensV1_1         = registerFeature("NonFungibleTokensV1_1", SupportedYes, VoteDefaultNo)
	FeatureExpandedSignerList            = registerFeature("ExpandedSignerList", SupportedYes, VoteDefaultNo)
	FeatureCheckCashMakesTrustLine       = registerFeature("CheckCashMakesTrustLine", SupportedYes, VoteDefaultNo)
	FeatureFixRmSmallIncreasedQOffers    = registerFix("fixRmSmallIncreasedQOffers", SupportedYes, VoteDefaultYes)
	FeatureFixSTAmountCanonicalize       = registerFix("fixSTAmountCanonicalize", SupportedYes, VoteDefaultYes)
	FeatureFlowSortStrands               = registerFeature("FlowSortStrands", SupportedYes, VoteDefaultYes)
	FeatureTicketBatch                   = registerFeature("TicketBatch", SupportedYes, VoteDefaultYes)
	FeatureNegativeUNL                   = registerFeature("NegativeUNL", SupportedYes, VoteDefaultYes)
	FeatureFixAmendmentMajorityCalc      = registerFix("fixAmendmentMajorityCalc", SupportedYes, VoteDefaultYes)
	FeatureHardenedValidations           = registerFeature("HardenedValidations", SupportedYes, VoteDefaultYes)
	FeatureFix1781                       = registerFix("fix1781", SupportedYes, VoteDefaultYes)
	FeatureRequireFullyCanonicalSig      = registerFeature("RequireFullyCanonicalSig", SupportedYes, VoteDefaultYes)
	FeatureFixQualityUpperBound          = registerFix("fixQualityUpperBound", SupportedYes, VoteDefaultYes)
	FeatureDeletableAccounts             = registerFeature("DeletableAccounts", SupportedYes, VoteDefaultYes)
	FeatureFixPayChanRecipientOwnerDir   = registerFix("fixPayChanRecipientOwnerDir", SupportedYes, VoteDefaultYes)
	FeatureFixCheckThreading             = registerFix("fixCheckThreading", SupportedYes, VoteDefaultYes)
	FeatureFixMasterKeyAsRegularKey      = registerFix("fixMasterKeyAsRegularKey", SupportedYes, VoteDefaultYes)
	FeatureFixTakerDryOfferRemoval       = registerFix("fixTakerDryOfferRemoval", SupportedYes, VoteDefaultYes)
	FeatureMultiSignReserve              = registerFeature("MultiSignReserve", SupportedYes, VoteDefaultYes)
	FeatureFix1578                       = registerFix("fix1578", SupportedYes, VoteDefaultYes)
	FeatureFix1515                       = registerFix("fix1515", SupportedYes, VoteDefaultYes)
	FeatureDepositPreauth                = registerFeature("DepositPreauth", SupportedYes, VoteDefaultYes)
	FeatureFix1623                       = registerFix("fix1623", SupportedYes, VoteDefaultYes)
	FeatureFix1543                       = registerFix("fix1543", SupportedYes, VoteDefaultYes)
	FeatureFix1571                       = registerFix("fix1571", SupportedYes, VoteDefaultYes)
	FeatureChecks                        = registerFeature("Checks", SupportedYes, VoteDefaultYes)
	FeatureDepositAuth                   = registerFeature("DepositAuth", SupportedYes, VoteDefaultYes)
	FeatureFix1513                       = registerFix("fix1513", SupportedYes, VoteDefaultYes)
	FeatureFlow                          = registerFeature("Flow", SupportedYes, VoteDefaultYes)
)

// Obsolete features (supported but no longer voted on).
var (
	FeatureFixNFTokenNegOffer    = registerFix("fixNFTokenNegOffer", SupportedYes, VoteObsolete)
	FeatureFixNFTokenDirV1       = registerFix("fixNFTokenDirV1", SupportedYes, VoteObsolete)
	FeatureNonFungibleTokensV1   = registerFeature("NonFungibleTokensV1", SupportedYes, VoteObsolete)
	FeatureCryptoConditionsSuite = registerFeature("CryptoConditionsSuite", SupportedYes, VoteObsolete)
)

// Retired features (active for 2+ years, pre-amendment code removed).
var (
	FeatureMultiSign         = registerRetired("MultiSign")
	FeatureTrustSetAuth      = registerRetired("TrustSetAuth")
	FeatureFeeEscalation     = registerRetired("FeeEscalation")
	FeaturePayChan           = registerRetired("PayChan")
	FeatureCryptoConditions  = registerRetired("CryptoConditions")
	FeatureTickSize          = registerRetired("TickSize")
	FeatureFix1368           = registerRetired("fix1368")
	FeatureEscrow            = registerRetired("Escrow")
	FeatureFix1373           = registerRetired("fix1373")
	FeatureEnforceInvariants = registerRetired("EnforceInvariants")
	FeatureSortedDirectories = registerRetired("SortedDirectories")
	FeatureFix1201           = registerRetired("fix1201")
	FeatureFix1512           = registerRetired("fix1512")
	FeatureFix1523           = registerRetired("fix1523")
	FeatureFix1528           = registerRetired("fix1528")
	FeatureFlowCross         = registerRetired("FlowCross")
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

// GetFeature returns the feature with the given ID, or nil if not found.
func GetFeature(id [32]byte) *Feature {
	return features[id]
}

// GetFeatureByName returns the feature with the given name, or nil if not found.
func GetFeatureByName(name string) *Feature {
	return featuresByName[name]
}

// AllFeatures returns all registered features in ID-sorted order. The
// returned slice is fresh; callers may mutate it.
func AllFeatures() []*Feature {
	out := make([]*Feature, len(orderedFeatures))
	copy(out, orderedFeatures)
	return out
}

// SupportedFeatures returns supported features in ID-sorted order.
func SupportedFeatures() []*Feature {
	result := make([]*Feature, 0, len(orderedFeatures))
	for _, f := range orderedFeatures {
		if f.Supported == SupportedYes {
			result = append(result, f)
		}
	}
	return result
}

// DefaultYesFeatures returns features that default to a yes vote, in
// ID-sorted order.
func DefaultYesFeatures() []*Feature {
	result := make([]*Feature, 0, len(orderedFeatures))
	for _, f := range orderedFeatures {
		if f.Vote == VoteDefaultYes && !f.Retired {
			result = append(result, f)
		}
	}
	return result
}

// FeatureCount returns the total number of registered features.
func FeatureCount() int {
	return len(features)
}
