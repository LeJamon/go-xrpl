package offer

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/permissioneddomain"
	"github.com/LeJamon/go-xrpl/keylet"
)

// Validate performs rules-independent validation on the OfferCreate transaction.
// This is called by the engine's preflight step BEFORE hash computation and fee deduction.
// All checks here must NOT depend on amendment rules (rules-dependent checks go in Preflight).
// Reference: rippled CreateOffer.cpp preflight() - rules-independent subset
func (o *OfferCreate) Validate() error {
	if err := o.BaseTx.Validate(); err != nil {
		return err
	}

	// Reference: rippled CreateOffer.cpp preflight() lines 61-65
	flags := o.GetFlags()
	if flags&tfOfferCreateMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags set")
	}

	// IoC and FoK are mutually exclusive
	// Reference: lines 73-80
	bImmediateOrCancel := (flags & OfferCreateFlagImmediateOrCancel) != 0
	bFillOrKill := (flags & OfferCreateFlagFillOrKill) != 0
	if bImmediateOrCancel && bFillOrKill {
		return tx.Errorf(tx.TemINVALID_FLAG, "cannot set both ImmediateOrCancel and FillOrKill")
	}

	// tfHybrid requires DomainID (rules-independent check)
	// Reference: lines 70-71
	if (flags&tfHybrid != 0) && o.DomainID == nil {
		return tx.Errorf(tx.TemINVALID_FLAG, "tfHybrid requires DomainID")
	}

	// Reference: lines 82-88
	if o.Expiration != nil && *o.Expiration == 0 {
		return tx.Errorf(tx.TemBAD_EXPIRATION, "expiration cannot be zero")
	}

	// Reference: lines 90-95
	if o.OfferSequence != nil && *o.OfferSequence == 0 {
		return tx.Errorf(tx.TemBAD_SEQUENCE, "OfferSequence cannot be zero")
	}

	// Validate amounts
	saTakerPays := o.TakerPays
	saTakerGets := o.TakerGets

	// Check required amounts are present (unset Amount has no type info)
	if !saTakerPays.IsNative() && saTakerPays.Currency == "" {
		return tx.Errorf(tx.TemBAD_OFFER, "TakerPays is required")
	}
	if !saTakerGets.IsNative() && saTakerGets.Currency == "" {
		return tx.Errorf(tx.TemBAD_OFFER, "TakerGets is required")
	}

	// Reference: lines 97-101
	if !isLegalNetAmount(saTakerPays) || !isLegalNetAmount(saTakerGets) {
		return tx.Errorf(tx.TemBAD_AMOUNT, "invalid amount")
	}

	// Cannot exchange XRP for XRP
	// Reference: lines 103-107
	if saTakerPays.IsNative() && saTakerGets.IsNative() {
		return tx.Errorf(tx.TemBAD_OFFER, "cannot exchange XRP for XRP")
	}

	// Amounts must be positive
	// Reference: lines 108-112
	if isAmountZeroOrNegative(saTakerPays) || isAmountZeroOrNegative(saTakerGets) {
		return tx.Errorf(tx.TemBAD_OFFER, "amounts must be positive")
	}

	uPaysCurrency := saTakerPays.Currency
	uPaysIssuerID := saTakerPays.Issuer
	uGetsCurrency := saTakerGets.Currency
	uGetsIssuerID := saTakerGets.Issuer

	// Check for redundant offer (same currency and issuer)
	// Reference: lines 120-124
	if uPaysCurrency == uGetsCurrency && uPaysIssuerID == uGetsIssuerID {
		return tx.Errorf(tx.TemREDUNDANT, "cannot create offer with same currency and issuer on both sides")
	}

	// Check for bad currency (XRP as non-native currency code)
	// Reference: lines 126-130
	if !saTakerPays.IsNative() && uPaysCurrency == badCurrency() {
		return tx.Errorf(tx.TemBAD_CURRENCY, "cannot use XRP as non-native currency code")
	}
	if !saTakerGets.IsNative() && uGetsCurrency == badCurrency() {
		return tx.Errorf(tx.TemBAD_CURRENCY, "cannot use XRP as non-native currency code")
	}

	// Reference: lines 132-137
	if saTakerPays.IsNative() != (uPaysIssuerID == "") {
		return tx.Errorf(tx.TemBAD_ISSUER, "issuer mismatch for TakerPays")
	}
	if saTakerGets.IsNative() != (uGetsIssuerID == "") {
		return tx.Errorf(tx.TemBAD_ISSUER, "issuer mismatch for TakerGets")
	}

	return nil
}

// badCurrency returns the "bad" currency code - using XRP as a non-native currency code
// Reference: rippled protocol/Issue.h badCurrency()
func badCurrency() string {
	return "XRP"
}

// PreflightRules performs the amendment-rules-dependent preflight checks for
// OfferCreate. The rules-independent structural validation lives in Validate().
// The engine runs this right after Validate(), so these tem* rejections happen
// before fee deduction, matching rippled's preflight().
// Reference: rippled CreateOffer.cpp preflight() lines 49-51, 67-68
func (o *OfferCreate) PreflightRules(rules *amendment.Rules) error {
	// Check if DomainID field is present without PermissionedDEX amendment
	// Reference: rippled CreateOffer.cpp preflight() lines 49-51
	if o.DomainID != nil && !rules.PermissionedDEXEnabled() {
		return tx.Errorf(tx.TemDISABLED, "DomainID requires PermissionedDEX amendment")
	}

	// Reference: lines 67-68
	flags := o.GetFlags()
	if !rules.PermissionedDEXEnabled() && (flags&tfHybrid != 0) {
		return tx.Errorf(tx.TemINVALID_FLAG, "tfHybrid requires PermissionedDEX amendment")
	}

	return nil
}

// Preclaim validates the transaction against ledger state before application.
// Runs through the engine's Preclaimer dispatch, before fee deduction.
// Reference: rippled CreateOffer.cpp preclaim() lines 142-225
func (o *OfferCreate) Preclaim(view tx.LedgerView, config tx.EngineConfig) tx.Result {
	rules := config.GetRules()

	accountID, err := state.DecodeAccountID(o.Account)
	if err != nil {
		return tx.TemBAD_SRC_ACCOUNT
	}
	account, readErr := tx.ReadAccountRoot(view, accountID)
	if readErr != nil {
		return tx.TefINTERNAL
	}
	if account == nil {
		return tx.TerNO_ACCOUNT
	}

	saTakerPays := o.TakerPays
	saTakerGets := o.TakerGets

	uPaysIssuerID := saTakerPays.Issuer
	uGetsIssuerID := saTakerGets.Issuer

	// Reference: lines 165-170
	if uPaysIssuerID != "" {
		if tx.IsGlobalFrozen(view, uPaysIssuerID) {
			return tx.TecFROZEN
		}
	}
	if uGetsIssuerID != "" {
		if tx.IsGlobalFrozen(view, uGetsIssuerID) {
			return tx.TecFROZEN
		}
	}

	// Check account has funds for the offer (at least partially funded)
	// Reference: rippled CreateOffer.cpp preclaim() lines 172-178
	// rippled checks accountFunds <= 0, NOT funds < takerGets.
	// Partially-funded offers are allowed; only completely unfunded offers are rejected.
	funds := tx.AccountFunds(view, accountID, saTakerGets, true, config.ReserveBase, config.ReserveIncrement)
	if funds.Signum() <= 0 {
		return tx.TecUNFUNDED_OFFER
	}

	// Check cancel sequence is valid. rippled compares the *pre-transaction*
	// account sequence (CreateOffer.cpp:182-186). This Preclaim runs in the
	// engine pipeline before doApply consumes the sequence, so account (read
	// here from the view) still holds the stored pre-transaction sequence.
	if o.OfferSequence != nil {
		if account.Sequence <= *o.OfferSequence {
			return tx.TemBAD_SEQUENCE
		}
	}

	// Reference: lines 189-200
	if tx.HasExpired(o.Expiration, config.ParentCloseTime) {
		if rules.DepositPreauthEnabled() {
			return tx.TecEXPIRED
		}
		return tx.TesSUCCESS
	}

	// Check we can accept what the taker will pay us (for non-native)
	// Reference: lines 203-213
	if !saTakerPays.IsNative() {
		paysIssuerID, err := state.DecodeAccountID(uPaysIssuerID)
		if err != nil {
			return tx.TecNO_ISSUER
		}
		result := checkAcceptAsset(view, accountID, paysIssuerID, saTakerPays.Currency, rules)
		if result != tx.TesSUCCESS {
			return result
		}
	}

	// Check domain membership if DomainID is specified
	// Reference: lines 217-222
	if o.DomainID != nil {
		if !accountInDomain(view, accountID, *o.DomainID, config.ParentCloseTime) {
			return tx.TecNO_PERMISSION
		}
	}

	return tx.TesSUCCESS
}

// checkAcceptAsset validates that an account can receive an asset.
// Reference: rippled CreateOffer.cpp checkAcceptAsset() lines 227-312
func checkAcceptAsset(view tx.LedgerView, accountID, issuerID [20]byte, currency string, rules *amendment.Rules) tx.Result {
	// Read issuer account
	issuerAccount, err := tx.ReadAccountRoot(view, issuerID)
	if err != nil || issuerAccount == nil {
		return tx.TecNO_ISSUER
	}

	// If account is the issuer, always allowed
	// Reference: lines 254-256
	if rules.DepositPreauthEnabled() && accountID == issuerID {
		return tx.TesSUCCESS
	}

	// Reference: lines 258-282
	if (issuerAccount.Flags & state.LsfRequireAuth) != 0 {
		trustLineKey := keylet.Line(accountID, issuerID, currency)
		trustLineData, err := view.Read(trustLineKey)
		if err != nil || trustLineData == nil {
			return tx.TecNO_LINE
		}

		rs, err := state.ParseRippleState(trustLineData)
		if err != nil {
			return tx.TecNO_LINE
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
			return tx.TecNO_AUTH
		}
	}

	// If account is issuer, always allowed (redundant check but matches rippled)
	// Reference: lines 288-291
	if accountID == issuerID {
		return tx.TesSUCCESS
	}

	// Reference: lines 293-309
	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		// No trustline = OK (will be created if needed)
		return tx.TesSUCCESS
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TesSUCCESS
	}

	deepFrozen := (rs.Flags & (state.LsfLowDeepFreeze | state.LsfHighDeepFreeze)) != 0
	if deepFrozen {
		return tx.TecFROZEN
	}

	return tx.TesSUCCESS
}

// accountInDomain checks if an account is a member of a permissioned domain.
// Reference: rippled app/misc/PermissionedDEXHelpers.cpp accountInDomain()
func accountInDomain(view tx.LedgerView, accountID [20]byte, domainID [32]byte, parentCloseTime uint32) bool {
	return permissioneddomain.AccountInDomain(view, accountID, domainID, parentCloseTime)
}
