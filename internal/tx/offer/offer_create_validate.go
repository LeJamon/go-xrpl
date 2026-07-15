package offer

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// Validate runs the rules-free structural preflight checks of OfferCreate, in
// rippled's preflight order. The flags mask (amendment-conditional, GetFlagsMask)
// and the sfDomainID amendment gate (CheckExtraFeatures) are enforced by the
// engine before this body, so no rules-dependent check remains here.
func (o *OfferCreate) Validate() error {
	if err := o.BaseTx.Validate(); err != nil {
		return err
	}

	// The invalid-flags mask (GetFlagsMask) is enforced by the engine at the
	// preflight0 position, before this body — mirroring rippled preflight0.
	flags := o.GetFlags()

	// tfHybrid requires DomainID (rules-independent; rippled's first body check).
	if (flags&tfHybrid != 0) && o.DomainID == nil {
		return ter.Errorf(ter.TemINVALID_FLAG, "tfHybrid requires DomainID")
	}

	// IoC and FoK are mutually exclusive.
	bImmediateOrCancel := (flags & OfferCreateFlagImmediateOrCancel) != 0
	bFillOrKill := (flags & OfferCreateFlagFillOrKill) != 0
	if bImmediateOrCancel && bFillOrKill {
		return ter.Errorf(ter.TemINVALID_FLAG, "cannot set both ImmediateOrCancel and FillOrKill")
	}

	// Reference: lines 82-88
	if o.Expiration != nil && *o.Expiration == 0 {
		return ter.Errorf(ter.TemBAD_EXPIRATION, "expiration cannot be zero")
	}

	// Reference: lines 90-95
	if o.OfferSequence != nil && *o.OfferSequence == 0 {
		return ter.Errorf(ter.TemBAD_SEQUENCE, "OfferSequence cannot be zero")
	}

	// Validate amounts
	saTakerPays := o.TakerPays
	saTakerGets := o.TakerGets

	// Check required amounts are present (unset Amount has no type info)
	if !saTakerPays.IsNative() && !saTakerPays.IsMPT() && saTakerPays.Currency == "" {
		return ter.Errorf(ter.TemBAD_OFFER, "TakerPays is required")
	}
	if !saTakerGets.IsNative() && !saTakerGets.IsMPT() && saTakerGets.Currency == "" {
		return ter.Errorf(ter.TemBAD_OFFER, "TakerGets is required")
	}

	// Reference: lines 97-101
	if !isLegalNetAmount(saTakerPays) || !isLegalNetAmount(saTakerGets) {
		return ter.Errorf(ter.TemBAD_AMOUNT, "invalid amount")
	}

	// Cannot exchange XRP for XRP
	// Reference: lines 103-107
	if saTakerPays.IsNative() && saTakerGets.IsNative() {
		return ter.Errorf(ter.TemBAD_OFFER, "cannot exchange XRP for XRP")
	}

	// Amounts must be positive
	// Reference: lines 108-112
	if isAmountZeroOrNegative(saTakerPays) || isAmountZeroOrNegative(saTakerGets) {
		return ter.Errorf(ter.TemBAD_OFFER, "amounts must be positive")
	}

	// Check for redundant offer (same currency and issuer)
	// Reference: lines 120-124
	if sameOfferAsset(saTakerPays, saTakerGets) {
		return ter.Errorf(ter.TemREDUNDANT, "cannot create offer with same currency and issuer on both sides")
	}
	if badOfferMPTAsset(saTakerPays) || badOfferMPTAsset(saTakerGets) {
		return ter.Errorf(ter.TemBAD_CURRENCY, "MPT issuance ID has a zero issuer")
	}

	// Check for bad currency (XRP as non-native currency code)
	// Reference: lines 126-130
	if !saTakerPays.IsNative() && !saTakerPays.IsMPT() && saTakerPays.Currency == badCurrency() {
		return ter.Errorf(ter.TemBAD_CURRENCY, "cannot use XRP as non-native currency code")
	}
	if !saTakerGets.IsNative() && !saTakerGets.IsMPT() && saTakerGets.Currency == badCurrency() {
		return ter.Errorf(ter.TemBAD_CURRENCY, "cannot use XRP as non-native currency code")
	}

	// Reference: lines 132-137
	if !saTakerPays.IsMPT() && saTakerPays.IsNative() != (saTakerPays.Issuer == "") {
		return ter.Errorf(ter.TemBAD_ISSUER, "issuer mismatch for TakerPays")
	}
	if !saTakerGets.IsMPT() && saTakerGets.IsNative() != (saTakerGets.Issuer == "") {
		return ter.Errorf(ter.TemBAD_ISSUER, "issuer mismatch for TakerGets")
	}

	return nil
}

func sameOfferAsset(a, b tx.Amount) bool {
	if a.IsMPT() || b.IsMPT() {
		return a.IsMPT() && b.IsMPT() && a.MPTIssuanceID() == b.MPTIssuanceID()
	}
	if a.IsNative() || b.IsNative() {
		return a.IsNative() && b.IsNative()
	}
	return a.Currency == b.Currency && a.Issuer == b.Issuer
}

func badOfferMPTAsset(amount tx.Amount) bool {
	if !amount.IsMPT() {
		return false
	}
	id, err := mptutil.DecodeID(amount.MPTIssuanceID())
	return err != nil || mptutil.Issuer(id) == ([20]byte{})
}

// badCurrency returns the "bad" currency code - using XRP as a non-native currency code
// Reference: rippled protocol/Issue.h badCurrency()
func badCurrency() string {
	return "XRP"
}

// GetFlagsMask returns the invalid-flags mask enforced by the engine at the
// preflight0 position. It is amendment-conditional, mirroring rippled
// CreateOffer::getFlagsMask: tfHybrid is only a valid flag once PermissionedDEX
// is enabled, so with the amendment off it is added to the invalid mask.
func (o *OfferCreate) GetFlagsMask(rules *amendment.Rules) uint32 {
	if rules.PermissionedDEXEnabled() {
		return tfOfferCreateMask
	}
	return tfOfferCreateMask | tfHybrid
}

// CheckExtraFeatures runs the amendment gate rippled evaluates in
// checkExtraFeatures — before preflight0's flags mask and the common checks — so
// an sfDomainID under a disabled PermissionedDEX surfaces temDISABLED ahead of
// every other tx-specific tem code.
func (o *OfferCreate) CheckExtraFeatures(rules *amendment.Rules) error {
	if o.DomainID != nil && !rules.PermissionedDEXEnabled() {
		return ter.Errorf(ter.TemDISABLED, "DomainID requires PermissionedDEX amendment")
	}
	// MPT-denominated offers require MPTokensV2 (rippled checkExtraFeatures).
	if !rules.MPTokensV2Enabled() && (o.TakerPays.IsMPT() || o.TakerGets.IsMPT()) {
		return ter.Errorf(ter.TemDISABLED, "MPT amounts require MPTokensV2 amendment")
	}
	return nil
}

// PreflightRules carries the amendment-gated tem* checks rippled evaluates in
// OfferCreate::preflight after the flags mask.
func (o *OfferCreate) PreflightRules(rules *amendment.Rules) error {
	// A zero DomainID is invalid: keylet::permissionedDomain uses the DomainID
	// as the ledger key, so a zero DomainID can never name a domain entry.
	if o.DomainID != nil && rules.FixCleanup3_2_0Enabled() && *o.DomainID == ([32]byte{}) {
		return ter.Errorf(ter.TemMALFORMED, "DomainID cannot be zero")
	}
	return nil
}

// Preclaim validates the transaction against ledger state before application.
// Runs through the engine's Preclaimer dispatch, before fee deduction.
// Reference: rippled CreateOffer.cpp preclaim() lines 142-225
func (o *OfferCreate) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(o.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	account, readErr := tx.ReadAccountRoot(view, accountID)
	if readErr != nil {
		return ter.TefINTERNAL
	}
	if account == nil {
		return ter.TerNO_ACCOUNT
	}

	saTakerPays := o.TakerPays
	saTakerGets := o.TakerGets

	uPaysIssuerID := saTakerPays.Issuer
	uGetsIssuerID := saTakerGets.Issuer

	// Reference: lines 165-170
	if saTakerPays.IsMPT() {
		id, decodeErr := mptutil.DecodeID(saTakerPays.MPTIssuanceID())
		if decodeErr != nil {
			return ter.TefINTERNAL
		}
		if mptutil.IsGlobalFrozen(view, id) {
			return ter.TecLOCKED
		}
	} else if uPaysIssuerID != "" {
		if tx.IsGlobalFrozen(view, uPaysIssuerID) {
			return ter.TecFROZEN
		}
	}
	if saTakerGets.IsMPT() {
		id, decodeErr := mptutil.DecodeID(saTakerGets.MPTIssuanceID())
		if decodeErr != nil {
			return ter.TefINTERNAL
		}
		if mptutil.IsGlobalFrozen(view, id) {
			return ter.TecLOCKED
		}
	} else if uGetsIssuerID != "" {
		if tx.IsGlobalFrozen(view, uGetsIssuerID) {
			return ter.TecFROZEN
		}
	}

	// Check account has funds for the offer (at least partially funded)
	// Reference: rippled CreateOffer.cpp preclaim() lines 172-178
	// rippled checks accountFunds <= 0, NOT funds < takerGets.
	// Partially-funded offers are allowed; only completely unfunded offers are rejected.
	if saTakerGets.IsMPT() {
		id, decodeErr := mptutil.DecodeID(saTakerGets.MPTIssuanceID())
		if decodeErr != nil {
			return ter.TefINTERNAL
		}
		if accountID != mptutil.Issuer(id) {
			funds, result := mptutil.Funds(view, id, accountID, true)
			if result == ter.TefINTERNAL {
				return result
			}
			if funds <= 0 {
				return ter.TecUNFUNDED_OFFER
			}
		}
	} else {
		funds := tx.AccountFunds(view, accountID, saTakerGets, true, config.ReserveBase, config.ReserveIncrement)
		if funds.Signum() <= 0 {
			return ter.TecUNFUNDED_OFFER
		}
	}

	// Check cancel sequence is valid. rippled compares the *pre-transaction*
	// account sequence (CreateOffer.cpp:182-186). This Preclaim runs in the
	// engine pipeline before doApply consumes the sequence, so account (read
	// here from the view) still holds the stored pre-transaction sequence.
	if o.OfferSequence != nil {
		if account.Sequence <= *o.OfferSequence {
			return ter.TemBAD_SEQUENCE
		}
	}

	// Reference: lines 189-200
	if tx.HasExpired(o.Expiration, config.ParentCloseTime) {
		return ter.TecEXPIRED
	}

	// Check we can accept what the taker will pay us (for non-native)
	// Reference: lines 203-213
	if !saTakerPays.IsNative() {
		var result ter.Result
		if saTakerPays.IsMPT() {
			result = checkAcceptMPT(view, accountID, saTakerPays, config.ParentCloseTime)
		} else {
			paysIssuerID, err := state.DecodeAccountID(uPaysIssuerID)
			if err != nil {
				return ter.TecNO_ISSUER
			}
			result = checkAcceptAsset(view, accountID, paysIssuerID, saTakerPays.Currency)
		}
		if result != ter.TesSUCCESS {
			return result
		}
	}

	// Check domain membership if DomainID is specified
	// Reference: lines 217-222
	if o.DomainID != nil {
		if !accountInDomain(view, accountID, *o.DomainID, config.ParentCloseTime) {
			return ter.TecNO_PERMISSION
		}
	}

	for _, amount := range []tx.Amount{saTakerPays, saTakerGets} {
		if amount.IsMPT() {
			id, decodeErr := mptutil.DecodeID(amount.MPTIssuanceID())
			if decodeErr != nil {
				return ter.TefINTERNAL
			}
			if result := mptutil.CanTrade(view, id); result != ter.TesSUCCESS {
				return result
			}
		}
	}

	return ter.TesSUCCESS
}

func checkAcceptMPT(view tx.LedgerView, accountID [20]byte, amount tx.Amount, parentCloseTime uint32) ter.Result {
	id, err := mptutil.DecodeID(amount.MPTIssuanceID())
	if err != nil {
		return ter.TefINTERNAL
	}
	issuer := mptutil.Issuer(id)
	issuerAccount, readErr := tx.ReadAccountRoot(view, issuer)
	if readErr != nil || issuerAccount == nil {
		return ter.TecNO_ISSUER
	}
	if accountID == issuer {
		return ter.TesSUCCESS
	}
	if result := mptutil.RequireAuthAt(view, id, accountID, false, parentCloseTime); result != ter.TesSUCCESS {
		return result
	}
	if mptutil.IsFrozen(view, id, accountID) {
		return ter.TecLOCKED
	}
	return ter.TesSUCCESS
}

// checkAcceptAsset validates that an account can receive an asset.
// Reference: rippled CreateOffer.cpp checkAcceptAsset() lines 227-312
func checkAcceptAsset(view tx.LedgerView, accountID, issuerID [20]byte, currency string) ter.Result {
	// Read issuer account
	issuerAccount, err := tx.ReadAccountRoot(view, issuerID)
	if err != nil || issuerAccount == nil {
		return ter.TecNO_ISSUER
	}

	// An issuer can always accept its own issuance, and no self-trustline exists
	// to be frozen. This early return precedes the RequireAuth check.
	// Reference: lines 254-256
	if accountID == issuerID {
		return ter.TesSUCCESS
	}

	// Reference: lines 258-282
	if (issuerAccount.Flags & state.LsfRequireAuth) != 0 {
		trustLineKey := keylet.Line(accountID, issuerID, currency)
		trustLineData, err := view.Read(trustLineKey)
		if err != nil || trustLineData == nil {
			return ter.TecNO_LINE
		}

		rs, err := state.ParseRippleState(trustLineData)
		if err != nil {
			return ter.TecNO_LINE
		}

		// Check authorization based on canonical ordering
		canonicalGT := state.CompareAccountIDs(accountID, issuerID) > 0
		var isAuthorized bool
		if canonicalGT {
			isAuthorized = (rs.Flags & state.LsfLowAuth) != 0
		} else {
			isAuthorized = (rs.Flags & state.LsfHighAuth) != 0
		}

		if !isAuthorized {
			return ter.TecNO_AUTH
		}
	}

	// Reference: lines 293-309
	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		// No trustline = OK (will be created if needed)
		return ter.TesSUCCESS
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return ter.TesSUCCESS
	}

	deepFrozen := (rs.Flags & (state.LsfLowDeepFreeze | state.LsfHighDeepFreeze)) != 0
	if deepFrozen {
		return ter.TecFROZEN
	}

	return ter.TesSUCCESS
}

// accountInDomain checks if an account is a member of a permissioned domain.
// Reference: rippled app/misc/PermissionedDEXHelpers.cpp accountInDomain()
func accountInDomain(view tx.LedgerView, accountID [20]byte, domainID [32]byte, parentCloseTime uint32) bool {
	return permissioneddomain.AccountInDomain(view, accountID, domainID, parentCloseTime)
}
