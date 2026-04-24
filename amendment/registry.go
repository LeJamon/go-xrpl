// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package amendment

import (
	"sync"
)

var (
	registryMu     sync.RWMutex
	features       = make(map[[32]byte]*Feature)
	featuresByName = make(map[string]*Feature)
)

var (
	// Active features (newest first, matching rippled order)
	FeatureFixDirectoryLimit             [32]byte
	FeatureFixPriceOracleOrder           [32]byte
	FeatureFixMPTDeliveredAmount         [32]byte
	FeatureFixAMMClawbackRounding        [32]byte
	FeatureTokenEscrow                   [32]byte
	FeatureFixEnforceNFTokenTrustlineV2  [32]byte
	FeatureFixAMMv1_3                    [32]byte
	FeaturePermissionedDEX               [32]byte
	FeatureBatch                         [32]byte
	FeatureSingleAssetVault              [32]byte
	FeaturePermissionDelegation          [32]byte
	FeatureFixPayChanCancelAfter         [32]byte
	FeatureFixInvalidTxFlags             [32]byte
	FeatureFixFrozenLPTokenTransfer      [32]byte
	FeatureDeepFreeze                    [32]byte
	FeaturePermissionedDomains           [32]byte
	FeatureDynamicNFT                    [32]byte
	FeatureCredentials                   [32]byte
	FeatureAMMClawback                   [32]byte
	FeatureFixAMMv1_2                    [32]byte
	FeatureMPTokensV1                    [32]byte
	FeatureInvariantsV1_1                [32]byte
	FeatureFixNFTokenPageLinks           [32]byte
	FeatureFixInnerObjTemplate2          [32]byte
	FeatureFixEnforceNFTokenTrustline    [32]byte
	FeatureFixReducedOffersV2            [32]byte
	FeatureNFTokenMintOffer              [32]byte
	FeatureFixAMMv1_1                    [32]byte
	FeatureFixPreviousTxnID              [32]byte
	FeatureFixXChainRewardRounding       [32]byte
	FeatureFixEmptyDID                   [32]byte
	FeaturePriceOracle                   [32]byte
	FeatureFixAMMOverflowOffer           [32]byte
	FeatureFixInnerObjTemplate           [32]byte
	FeatureFixNFTokenReserve             [32]byte
	FeatureFixFillOrKill                 [32]byte
	FeatureDID                           [32]byte
	FeatureFixDisallowIncomingV1         [32]byte
	FeatureXChainBridge                  [32]byte
	FeatureAMM                           [32]byte
	FeatureClawback                      [32]byte
	FeatureFixReducedOffersV1            [32]byte
	FeatureFixNFTokenRemint              [32]byte
	FeatureFixNonFungibleTokensV1_2      [32]byte
	FeatureFixUniversalNumber            [32]byte
	FeatureXRPFees                       [32]byte
	FeatureDisallowIncoming              [32]byte
	FeatureImmediateOfferKilled          [32]byte
	FeatureFixRemoveNFTokenAutoTrustLine [32]byte
	FeatureFixTrustLinesToSelf           [32]byte
	FeatureNonFungibleTokensV1_1         [32]byte
	FeatureExpandedSignerList            [32]byte
	FeatureCheckCashMakesTrustLine       [32]byte
	FeatureFixRmSmallIncreasedQOffers    [32]byte
	FeatureFixSTAmountCanonicalize       [32]byte
	FeatureFlowSortStrands               [32]byte
	FeatureTicketBatch                   [32]byte
	FeatureNegativeUNL                   [32]byte
	FeatureFixAmendmentMajorityCalc      [32]byte
	FeatureHardenedValidations           [32]byte
	FeatureFix1781                       [32]byte
	FeatureRequireFullyCanonicalSig      [32]byte
	FeatureFixQualityUpperBound          [32]byte
	FeatureDeletableAccounts             [32]byte
	FeatureFixPayChanRecipientOwnerDir   [32]byte
	FeatureFixCheckThreading             [32]byte
	FeatureFixMasterKeyAsRegularKey      [32]byte
	FeatureFixTakerDryOfferRemoval       [32]byte
	FeatureMultiSignReserve              [32]byte
	FeatureFix1578                       [32]byte
	FeatureFix1515                       [32]byte
	FeatureDepositPreauth                [32]byte
	FeatureFix1623                       [32]byte
	FeatureFix1543                       [32]byte
	FeatureFix1571                       [32]byte
	FeatureChecks                        [32]byte
	FeatureDepositAuth                   [32]byte
	FeatureFix1513                       [32]byte
	FeatureFlow                          [32]byte

	// Obsolete features (supported but no longer voted on)
	FeatureFixNFTokenNegOffer    [32]byte
	FeatureFixNFTokenDirV1       [32]byte
	FeatureNonFungibleTokensV1   [32]byte
	FeatureCryptoConditionsSuite [32]byte

	// Retired features (active for 2+ years, pre-amendment code removed)
	FeatureMultiSign         [32]byte
	FeatureTrustSetAuth      [32]byte
	FeatureFeeEscalation     [32]byte
	FeaturePayChan           [32]byte
	FeatureCryptoConditions  [32]byte
	FeatureTickSize          [32]byte
	FeatureFix1368           [32]byte
	FeatureEscrow            [32]byte
	FeatureFix1373           [32]byte
	FeatureEnforceInvariants [32]byte
	FeatureSortedDirectories [32]byte
	FeatureFix1201           [32]byte
	FeatureFix1512           [32]byte
	FeatureFix1523           [32]byte
	FeatureFix1528           [32]byte
	FeatureFlowCross         [32]byte
)

func init() {
	// Register all features matching rippled's features.macro
	registerFix("fixDirectoryLimit", SupportedYes, VoteDefaultNo, &FeatureFixDirectoryLimit)
	registerFix("fixPriceOracleOrder", SupportedNo, VoteDefaultNo, &FeatureFixPriceOracleOrder)
	registerFix("fixMPTDeliveredAmount", SupportedNo, VoteDefaultNo, &FeatureFixMPTDeliveredAmount)
	registerFix("fixAMMClawbackRounding", SupportedNo, VoteDefaultNo, &FeatureFixAMMClawbackRounding)
	registerFeature("TokenEscrow", SupportedYes, VoteDefaultNo, &FeatureTokenEscrow)
	registerFix("fixEnforceNFTokenTrustlineV2", SupportedYes, VoteDefaultNo, &FeatureFixEnforceNFTokenTrustlineV2)
	registerFix("fixAMMv1_3", SupportedYes, VoteDefaultNo, &FeatureFixAMMv1_3)
	registerFeature("PermissionedDEX", SupportedYes, VoteDefaultNo, &FeaturePermissionedDEX)
	registerFeature("Batch", SupportedYes, VoteDefaultNo, &FeatureBatch)
	registerFeature("SingleAssetVault", SupportedNo, VoteDefaultNo, &FeatureSingleAssetVault)
	registerFeature("PermissionDelegation", SupportedNo, VoteDefaultNo, &FeaturePermissionDelegation)
	registerFix("fixPayChanCancelAfter", SupportedYes, VoteDefaultNo, &FeatureFixPayChanCancelAfter)
	registerFix("fixInvalidTxFlags", SupportedYes, VoteDefaultNo, &FeatureFixInvalidTxFlags)
	registerFix("fixFrozenLPTokenTransfer", SupportedYes, VoteDefaultNo, &FeatureFixFrozenLPTokenTransfer)
	registerFeature("DeepFreeze", SupportedYes, VoteDefaultNo, &FeatureDeepFreeze)
	registerFeature("PermissionedDomains", SupportedYes, VoteDefaultNo, &FeaturePermissionedDomains)
	registerFeature("DynamicNFT", SupportedYes, VoteDefaultNo, &FeatureDynamicNFT)
	registerFeature("Credentials", SupportedYes, VoteDefaultNo, &FeatureCredentials)
	registerFeature("AMMClawback", SupportedYes, VoteDefaultNo, &FeatureAMMClawback)
	registerFix("fixAMMv1_2", SupportedYes, VoteDefaultNo, &FeatureFixAMMv1_2)
	registerFeature("MPTokensV1", SupportedYes, VoteDefaultNo, &FeatureMPTokensV1)
	registerFeature("InvariantsV1_1", SupportedNo, VoteDefaultNo, &FeatureInvariantsV1_1)
	registerFix("fixNFTokenPageLinks", SupportedYes, VoteDefaultNo, &FeatureFixNFTokenPageLinks)
	registerFix("fixInnerObjTemplate2", SupportedYes, VoteDefaultNo, &FeatureFixInnerObjTemplate2)
	registerFix("fixEnforceNFTokenTrustline", SupportedYes, VoteDefaultNo, &FeatureFixEnforceNFTokenTrustline)
	registerFix("fixReducedOffersV2", SupportedYes, VoteDefaultNo, &FeatureFixReducedOffersV2)
	registerFeature("NFTokenMintOffer", SupportedYes, VoteDefaultNo, &FeatureNFTokenMintOffer)
	registerFix("fixAMMv1_1", SupportedYes, VoteDefaultNo, &FeatureFixAMMv1_1)
	registerFix("fixPreviousTxnID", SupportedYes, VoteDefaultNo, &FeatureFixPreviousTxnID)
	registerFix("fixXChainRewardRounding", SupportedYes, VoteDefaultNo, &FeatureFixXChainRewardRounding)
	registerFix("fixEmptyDID", SupportedYes, VoteDefaultNo, &FeatureFixEmptyDID)
	registerFeature("PriceOracle", SupportedYes, VoteDefaultNo, &FeaturePriceOracle)
	registerFix("fixAMMOverflowOffer", SupportedYes, VoteDefaultYes, &FeatureFixAMMOverflowOffer)
	registerFix("fixInnerObjTemplate", SupportedYes, VoteDefaultNo, &FeatureFixInnerObjTemplate)
	registerFix("fixNFTokenReserve", SupportedYes, VoteDefaultNo, &FeatureFixNFTokenReserve)
	registerFix("fixFillOrKill", SupportedYes, VoteDefaultNo, &FeatureFixFillOrKill)
	registerFeature("DID", SupportedYes, VoteDefaultNo, &FeatureDID)
	registerFix("fixDisallowIncomingV1", SupportedYes, VoteDefaultNo, &FeatureFixDisallowIncomingV1)
	registerFeature("XChainBridge", SupportedYes, VoteDefaultNo, &FeatureXChainBridge)
	registerFeature("AMM", SupportedYes, VoteDefaultNo, &FeatureAMM)
	registerFeature("Clawback", SupportedYes, VoteDefaultNo, &FeatureClawback)
	registerFix("fixReducedOffersV1", SupportedYes, VoteDefaultNo, &FeatureFixReducedOffersV1)
	registerFix("fixNFTokenRemint", SupportedYes, VoteDefaultNo, &FeatureFixNFTokenRemint)
	registerFix("fixNonFungibleTokensV1_2", SupportedYes, VoteDefaultNo, &FeatureFixNonFungibleTokensV1_2)
	registerFix("fixUniversalNumber", SupportedYes, VoteDefaultNo, &FeatureFixUniversalNumber)
	registerFeature("XRPFees", SupportedYes, VoteDefaultNo, &FeatureXRPFees)
	registerFeature("DisallowIncoming", SupportedYes, VoteDefaultNo, &FeatureDisallowIncoming)
	registerFeature("ImmediateOfferKilled", SupportedYes, VoteDefaultNo, &FeatureImmediateOfferKilled)
	registerFix("fixRemoveNFTokenAutoTrustLine", SupportedYes, VoteDefaultYes, &FeatureFixRemoveNFTokenAutoTrustLine)
	registerFix("fixTrustLinesToSelf", SupportedYes, VoteDefaultNo, &FeatureFixTrustLinesToSelf)
	registerFeature("NonFungibleTokensV1_1", SupportedYes, VoteDefaultNo, &FeatureNonFungibleTokensV1_1)
	registerFeature("ExpandedSignerList", SupportedYes, VoteDefaultNo, &FeatureExpandedSignerList)
	registerFeature("CheckCashMakesTrustLine", SupportedYes, VoteDefaultNo, &FeatureCheckCashMakesTrustLine)
	registerFix("fixRmSmallIncreasedQOffers", SupportedYes, VoteDefaultYes, &FeatureFixRmSmallIncreasedQOffers)
	registerFix("fixSTAmountCanonicalize", SupportedYes, VoteDefaultYes, &FeatureFixSTAmountCanonicalize)
	registerFeature("FlowSortStrands", SupportedYes, VoteDefaultYes, &FeatureFlowSortStrands)
	registerFeature("TicketBatch", SupportedYes, VoteDefaultYes, &FeatureTicketBatch)
	registerFeature("NegativeUNL", SupportedYes, VoteDefaultYes, &FeatureNegativeUNL)
	registerFix("fixAmendmentMajorityCalc", SupportedYes, VoteDefaultYes, &FeatureFixAmendmentMajorityCalc)
	registerFeature("HardenedValidations", SupportedYes, VoteDefaultYes, &FeatureHardenedValidations)
	registerFix("fix1781", SupportedYes, VoteDefaultYes, &FeatureFix1781)
	registerFeature("RequireFullyCanonicalSig", SupportedYes, VoteDefaultYes, &FeatureRequireFullyCanonicalSig)
	registerFix("fixQualityUpperBound", SupportedYes, VoteDefaultYes, &FeatureFixQualityUpperBound)
	registerFeature("DeletableAccounts", SupportedYes, VoteDefaultYes, &FeatureDeletableAccounts)
	registerFix("fixPayChanRecipientOwnerDir", SupportedYes, VoteDefaultYes, &FeatureFixPayChanRecipientOwnerDir)
	registerFix("fixCheckThreading", SupportedYes, VoteDefaultYes, &FeatureFixCheckThreading)
	registerFix("fixMasterKeyAsRegularKey", SupportedYes, VoteDefaultYes, &FeatureFixMasterKeyAsRegularKey)
	registerFix("fixTakerDryOfferRemoval", SupportedYes, VoteDefaultYes, &FeatureFixTakerDryOfferRemoval)
	registerFeature("MultiSignReserve", SupportedYes, VoteDefaultYes, &FeatureMultiSignReserve)
	registerFix("fix1578", SupportedYes, VoteDefaultYes, &FeatureFix1578)
	registerFix("fix1515", SupportedYes, VoteDefaultYes, &FeatureFix1515)
	registerFeature("DepositPreauth", SupportedYes, VoteDefaultYes, &FeatureDepositPreauth)
	registerFix("fix1623", SupportedYes, VoteDefaultYes, &FeatureFix1623)
	registerFix("fix1543", SupportedYes, VoteDefaultYes, &FeatureFix1543)
	registerFix("fix1571", SupportedYes, VoteDefaultYes, &FeatureFix1571)
	registerFeature("Checks", SupportedYes, VoteDefaultYes, &FeatureChecks)
	registerFeature("DepositAuth", SupportedYes, VoteDefaultYes, &FeatureDepositAuth)
	registerFix("fix1513", SupportedYes, VoteDefaultYes, &FeatureFix1513)
	registerFeature("Flow", SupportedYes, VoteDefaultYes, &FeatureFlow)

	// Obsolete features (supported but no longer voted on)
	registerFix("fixNFTokenNegOffer", SupportedYes, VoteObsolete, &FeatureFixNFTokenNegOffer)
	registerFix("fixNFTokenDirV1", SupportedYes, VoteObsolete, &FeatureFixNFTokenDirV1)
	registerFeature("NonFungibleTokensV1", SupportedYes, VoteObsolete, &FeatureNonFungibleTokensV1)
	registerFeature("CryptoConditionsSuite", SupportedYes, VoteObsolete, &FeatureCryptoConditionsSuite)

	// Retired features (active for 2+ years, pre-amendment code removed)
	registerRetired("MultiSign", &FeatureMultiSign)
	registerRetired("TrustSetAuth", &FeatureTrustSetAuth)
	registerRetired("FeeEscalation", &FeatureFeeEscalation)
	registerRetired("PayChan", &FeaturePayChan)
	registerRetired("CryptoConditions", &FeatureCryptoConditions)
	registerRetired("TickSize", &FeatureTickSize)
	registerRetired("fix1368", &FeatureFix1368)
	registerRetired("Escrow", &FeatureEscrow)
	registerRetired("fix1373", &FeatureFix1373)
	registerRetired("EnforceInvariants", &FeatureEnforceInvariants)
	registerRetired("SortedDirectories", &FeatureSortedDirectories)
	registerRetired("fix1201", &FeatureFix1201)
	registerRetired("fix1512", &FeatureFix1512)
	registerRetired("fix1523", &FeatureFix1523)
	registerRetired("fix1528", &FeatureFix1528)
	registerRetired("FlowCross", &FeatureFlowCross)
}

// registerFeature registers a feature with the given parameters.
func registerFeature(name string, supported Supported, vote VoteBehavior, idPtr *[32]byte) {
	id := FeatureID(name)
	*idPtr = id

	f := &Feature{
		Name:      name,
		ID:        id,
		Supported: supported,
		Vote:      vote,
		Retired:   false,
	}

	registryMu.Lock()
	features[id] = f
	featuresByName[name] = f
	registryMu.Unlock()
}

// registerFix registers a fix (amendment that fixes a bug) with the given parameters.
// Fix names are prefixed with "fix" in the ledger.
func registerFix(name string, supported Supported, vote VoteBehavior, idPtr *[32]byte) {
	// Fixes use the name as-is for the ID calculation
	registerFeature(name, supported, vote, idPtr)
}

// registerRetired registers a retired feature.
func registerRetired(name string, idPtr *[32]byte) {
	id := FeatureID(name)
	*idPtr = id

	f := &Feature{
		Name:      name,
		ID:        id,
		Supported: SupportedYes,
		Vote:      VoteDefaultYes,
		Retired:   true,
	}

	registryMu.Lock()
	features[id] = f
	featuresByName[name] = f
	registryMu.Unlock()
}

// GetFeature returns the feature with the given ID, or nil if not found.
func GetFeature(id [32]byte) *Feature {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return features[id]
}

// GetFeatureByName returns the feature with the given name, or nil if not found.
func GetFeatureByName(name string) *Feature {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return featuresByName[name]
}

// AllFeatures returns a slice of all registered features.
func AllFeatures() []*Feature {
	registryMu.RLock()
	defer registryMu.RUnlock()

	result := make([]*Feature, 0, len(features))
	for _, f := range features {
		result = append(result, f)
	}
	return result
}

// SupportedFeatures returns a slice of all supported features.
func SupportedFeatures() []*Feature {
	registryMu.RLock()
	defer registryMu.RUnlock()

	result := make([]*Feature, 0)
	for _, f := range features {
		if f.Supported == SupportedYes {
			result = append(result, f)
		}
	}
	return result
}

// DefaultYesFeatures returns a slice of all features that should be voted yes by default.
func DefaultYesFeatures() []*Feature {
	registryMu.RLock()
	defer registryMu.RUnlock()

	result := make([]*Feature, 0)
	for _, f := range features {
		if f.Vote == VoteDefaultYes && !f.Retired {
			result = append(result, f)
		}
	}
	return result
}

// FeatureCount returns the total number of registered features.
func FeatureCount() int {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return len(features)
}
