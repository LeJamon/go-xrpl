package offer

import (
	"strings"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/permissioneddomain"
	"github.com/LeJamon/goXRPLd/keylet"
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

// parsePreflightError converts a preflight error message to the appropriate TER code.
// Reference: rippled uses specific TER codes for different validation failures.
func parsePreflightError(err error) tx.Result {
	if err == nil {
		return tx.TesSUCCESS
	}
	msg := err.Error()

	// Map error message prefixes to result codes
	prefixes := []struct {
		prefix string
		result tx.Result
	}{
		{"temDISABLED", tx.TemDISABLED},
		{"temINVALID_FLAG", tx.TemINVALID_FLAG},
		{"temBAD_EXPIRATION", tx.TemBAD_EXPIRATION},
		{"temBAD_SEQUENCE", tx.TemBAD_SEQUENCE},
		{"temBAD_AMOUNT", tx.TemBAD_AMOUNT},
		{"temBAD_OFFER", tx.TemBAD_OFFER},
		{"temREDUNDANT", tx.TemREDUNDANT},
		{"temBAD_CURRENCY", tx.TemBAD_CURRENCY},
		{"temBAD_ISSUER", tx.TemBAD_ISSUER},
	}

	for _, p := range prefixes {
		if strings.HasPrefix(msg, p.prefix) {
			return p.result
		}
	}

	return tx.TemMALFORMED
}

// badCurrency returns the "bad" currency code - using XRP as a non-native currency code
// Reference: rippled protocol/Issue.h badCurrency()
func badCurrency() string {
	return "XRP"
}

// Preflight performs all validation on the OfferCreate transaction.
// This matches rippled's preflight() which does ALL semantic validation.
// Reference: rippled CreateOffer.cpp preflight() lines 46-140
func (o *OfferCreate) Preflight(rules *amendment.Rules) error {
	// Only rules-dependent checks remain here. Rules-independent validation
	// is done in Validate() which runs before hash computation and fee deduction.

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
// Reference: rippled CreateOffer.cpp preclaim() lines 142-225
func (o *OfferCreate) Preclaim(ctx *tx.ApplyContext) tx.Result {
	rules := ctx.Rules()

	saTakerPays := o.TakerPays
	saTakerGets := o.TakerGets

	uPaysIssuerID := saTakerPays.Issuer
	uGetsIssuerID := saTakerGets.Issuer

	// Reference: lines 165-170
	if uPaysIssuerID != "" {
		if tx.IsGlobalFrozen(ctx.View, uPaysIssuerID) {
			return tx.TecFROZEN
		}
	}
	if uGetsIssuerID != "" {
		if tx.IsGlobalFrozen(ctx.View, uGetsIssuerID) {
			return tx.TecFROZEN
		}
	}

	// Check account has funds for the offer (at least partially funded)
	// Reference: rippled CreateOffer.cpp preclaim() lines 172-178
	// rippled checks accountFunds <= 0, NOT funds < takerGets.
	// Partially-funded offers are allowed; only completely unfunded offers are rejected.
	funds := tx.AccountFunds(ctx.View, ctx.AccountID, saTakerGets, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
	if funds.Signum() <= 0 {
		return tx.TecUNFUNDED_OFFER
	}

	// Check cancel sequence is valid (must be less than current account sequence)
	// Reference: lines 182-187
	if o.OfferSequence != nil {
		if ctx.Account.Sequence <= *o.OfferSequence {
			return tx.TemBAD_SEQUENCE
		}
	}

	// Reference: lines 189-200
	if hasExpired(ctx, o.Expiration) {
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
		result := checkAcceptAsset(ctx, paysIssuerID, saTakerPays.Currency, rules)
		if result != tx.TesSUCCESS {
			return result
		}
	}

	// Check domain membership if DomainID is specified
	// Reference: lines 217-222
	if o.DomainID != nil {
		if !accountInDomain(ctx.View, ctx.AccountID, *o.DomainID, ctx.Config.ParentCloseTime) {
			return tx.TecNO_PERMISSION
		}
	}

	return tx.TesSUCCESS
}

// hasExpired checks if an offer has expired.
// Reference: rippled app/tx/impl/Transactor.cpp hasExpired()
func hasExpired(ctx *tx.ApplyContext, expiration *uint32) bool {
	if expiration == nil {
		return false
	}
	return *expiration <= ctx.Config.ParentCloseTime
}

// checkAcceptAsset validates that an account can receive an asset.
// Reference: rippled CreateOffer.cpp checkAcceptAsset() lines 227-312
func checkAcceptAsset(ctx *tx.ApplyContext, issuerID [20]byte, currency string, rules *amendment.Rules) tx.Result {
	// Read issuer account
	issuerKey := keylet.Account(issuerID)
	issuerData, err := ctx.View.Read(issuerKey)
	if err != nil || issuerData == nil {
		return tx.TecNO_ISSUER
	}

	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TecNO_ISSUER
	}

	// If account is the issuer, always allowed
	// Reference: lines 254-256
	if rules.DepositPreauthEnabled() && ctx.AccountID == issuerID {
		return tx.TesSUCCESS
	}

	// Reference: lines 258-282
	if (issuerAccount.Flags & state.LsfRequireAuth) != 0 {
		trustLineKey := keylet.Line(ctx.AccountID, issuerID, currency)
		trustLineData, err := ctx.View.Read(trustLineKey)
		if err != nil || trustLineData == nil {
			return tx.TecNO_LINE
		}

		rs, err := state.ParseRippleState(trustLineData)
		if err != nil {
			return tx.TecNO_LINE
		}

		// Check authorization based on canonical ordering
		canonicalGT := state.CompareAccountIDsForLine(ctx.AccountID, issuerID) > 0
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
	if ctx.AccountID == issuerID {
		return tx.TesSUCCESS
	}

	// Reference: lines 293-309
	trustLineKey := keylet.Line(ctx.AccountID, issuerID, currency)
	trustLineData, err := ctx.View.Read(trustLineKey)
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
