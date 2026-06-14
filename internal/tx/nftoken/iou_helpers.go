package nftoken

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
)

// checkNFTTrustlineAuthorized checks if an account is authorized for an IOU currency.
// Returns tesSUCCESS if authorized, or tecNO_LINE/tecNO_AUTH if not.
// Reference: rippled NFTokenUtils.cpp checkTrustlineAuthorized
func checkNFTTrustlineAuthorized(view tx.LedgerView, accountID [20]byte, currency string, issuerID [20]byte) tx.Result {
	// Issuer is always authorized for their own currency
	if accountID == issuerID {
		return tx.TesSUCCESS
	}

	// Read issuer account to check RequireAuth flag
	issuerKey := keylet.Account(issuerID)
	issuerData, err := view.Read(issuerKey)
	if err != nil || issuerData == nil {
		return tx.TecNO_ISSUER
	}
	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// If issuer doesn't require auth, any account can hold this currency
	if issuerAccount.Flags&state.LsfRequireAuth == 0 {
		return tx.TesSUCCESS
	}

	// Issuer requires auth — check if the trust line exists and is authorized
	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return tx.TecNO_LINE
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check authorization flag based on account ordering
	// Reference: rippled — if (id > issue.account) check lsfLowAuth else lsfHighAuth
	// When id > issuer: issuer is the LOW account → check LsfLowAuth (issuer's auth flag)
	// When id < issuer: issuer is the HIGH account → check LsfHighAuth (issuer's auth flag)
	if state.CompareAccountIDsForLine(accountID, issuerID) > 0 {
		if rs.Flags&state.LsfLowAuth == 0 {
			return tx.TecNO_AUTH
		}
	} else {
		if rs.Flags&state.LsfHighAuth == 0 {
			return tx.TecNO_AUTH
		}
	}

	return tx.TesSUCCESS
}

// checkNFTTrustlineDeepFrozen checks if the trust line between account and
// the asset issuer is deep-frozen. Returns tecFROZEN if either side has set
// deep freeze. Gated behind featureDeepFreeze.
// Reference: rippled NFTokenUtils.cpp nft::checkTrustlineDeepFrozen()
func checkNFTTrustlineDeepFrozen(view tx.LedgerView, accountID [20]byte, currency string, issuerID [20]byte, rules *amendment.Rules) tx.Result {
	if rules == nil || !rules.DeepFreezeEnabled() {
		return tx.TesSUCCESS
	}

	issuerKey := keylet.Account(issuerID)
	issuerData, err := view.Read(issuerKey)
	if err != nil || issuerData == nil {
		return tx.TecNO_ISSUER
	}

	// An account can not create a trustline to itself
	if accountID == issuerID {
		return tx.TesSUCCESS
	}

	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		// No trust line — not frozen
		return tx.TesSUCCESS
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Either side having deep freeze set blocks the operation
	if (rs.Flags & (state.LsfLowDeepFreeze | state.LsfHighDeepFreeze)) != 0 {
		return tx.TecFROZEN
	}

	return tx.TesSUCCESS
}

// offerIOUToAmount converts an NFTokenOfferData's IOU amount to a tx.Amount.
// If the offer has no IOU amount, returns an XRP amount from the offer's Amount field.
func offerIOUToAmount(offer *state.NFTokenOfferData) (tx.Amount, error) {
	if offer.AmountIOU == nil {
		return tx.NewXRPAmount(int64(offer.Amount)), nil
	}
	issuerAddr, err := addresscodec.EncodeAccountIDToClassicAddress(offer.AmountIOU.Issuer[:])
	if err != nil {
		return tx.Amount{}, fmt.Errorf("failed to encode NFTokenOffer IOU issuer: %w", err)
	}
	return state.NewIssuedAmountFromDecimalString(offer.AmountIOU.Value, offer.AmountIOU.Currency, issuerAddr)
}

// accountSendIOU transfers IOU between accounts via trust lines.
// Handles three cases:
//  1. from == IOU issuer: issuer creates tokens → credit receiver
//  2. to == IOU issuer: holder redeems tokens → debit sender
//  3. third party: two trust line modifications with optional transfer rate
//
// Reference: rippled View.cpp accountSend → rippleSendIOU → rippleCreditIOU
func accountSendIOU(view tx.LedgerView, from, to [20]byte, amount tx.Amount) tx.Result {
	if amount.IsZero() || from == to {
		return tx.TesSUCCESS
	}

	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	if from == issuerID || to == issuerID {
		// Direct: issuer is one side — no transfer fee
		return tx.RippleCredit(view, from, to, amount)
	}

	// Third party: sender → issuer (with transfer rate) and issuer → receiver
	transferRate := payment.GetTransferRate(view, issuerID)
	if transferRate != payment.QualityOne {
		// Charge the sender amount * transferRate, rounded to nearest. rippled's
		// rippleSendIOU uses multiply() (round-to-nearest), not the round-up
		// multiplyRound(), so MulRatio(..., roundUp=true) would diverge by 1 ulp.
		rateAmount := state.NewIssuedAmountFromValue(int64(transferRate), -9, amount.Currency, amount.Issuer)
		senderAmount := amount.Mul(rateAmount, false)
		// Credit receiver the original amount
		if r := tx.RippleCredit(view, issuerID, to, amount); r != tx.TesSUCCESS {
			return r
		}
		// Debit sender the increased amount
		return tx.RippleCredit(view, from, issuerID, senderAmount)
	}

	// No transfer rate — direct credit/debit
	if r := tx.RippleCredit(view, issuerID, to, amount); r != tx.TesSUCCESS {
		return r
	}
	return tx.RippleCredit(view, from, issuerID, amount)
}

// payIOU wraps accountSendIOU with post-hoc balance validation.
// With fixNonFungibleTokensV1_2, after the payment is processed, it checks that
// neither party's balance went negative (which would indicate insufficient funds
// to cover the IOU transfer rate).
// Reference: rippled NFTokenAcceptOffer.cpp pay()
func payIOU(ctx *tx.ApplyContext, from, to [20]byte, amount tx.Amount) tx.Result {
	if amount.IsZero() {
		return tx.TesSUCCESS
	}

	result := accountSendIOU(ctx.View, from, to, amount)

	if !ctx.Rules().Enabled(amendment.FeatureFixNonFungibleTokensV1_2) {
		return result
	}
	if result != tx.TesSUCCESS {
		return result
	}

	// Post-hoc check: ensure neither party went negative after accounting for transfer rate
	if accountIOUBalanceSignum(ctx.View, from, amount) < 0 {
		return tx.TecINSUFFICIENT_FUNDS
	}
	if accountIOUBalanceSignum(ctx.View, to, amount) < 0 {
		return tx.TecINSUFFICIENT_FUNDS
	}

	return tx.TesSUCCESS
}

// accountIOUBalanceSignum returns the signum of an account's IOU balance.
// Unlike tx.AccountFunds, this returns -1 if the balance is negative (doesn't clamp to 0).
// Used for post-hoc checks after IOU transfers.
// Returns: -1 (negative/owes), 0 (zero), 1 (positive/has funds)
// For the IOU issuer, always returns 1 (issuer has unlimited).
func accountIOUBalanceSignum(view tx.LedgerView, accountID [20]byte, amount tx.Amount) int {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return 0
	}

	// Issuer always has positive balance in their own currency
	if accountID == issuerID {
		return 1
	}

	trustLineKey := keylet.Line(accountID, issuerID, amount.Currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return 0
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return 0
	}

	accountIsLow := state.CompareAccountIDsForLine(accountID, issuerID) < 0
	balance := rs.Balance
	if !accountIsLow {
		balance = balance.Negate()
	}

	return balance.Signum()
}

// accountHoldsIOU returns the IOU balance without the issuer exception.
// This matches rippled's accountHolds behavior: the issuer is NOT treated as
// having unlimited funds (unlike AccountFunds).
// Used for pre-fixNonFungibleTokensV1_2 fund checks.
func accountHoldsIOU(view tx.LedgerView, accountID [20]byte, amount tx.Amount) tx.Amount {
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	// NO issuer exception here (unlike AccountFunds)

	trustLineKey := keylet.Line(accountID, issuerID, amount.Currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	accountIsLow := state.CompareAccountIDsForLine(accountID, issuerID) < 0
	balance := rs.Balance
	if !accountIsLow {
		balance = balance.Negate()
	}

	if balance.Signum() <= 0 {
		return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	return state.NewIssuedAmountFromValue(balance.IOU().Mantissa(), balance.IOU().Exponent(), amount.Currency, amount.Issuer)
}

// checkIssuerTrustLineForAccept checks that the NFT issuer has a trust line for the
// IOU currency. Used by NFTokenAcceptOffer doApply path — gated on fixEnforceNFTokenTrustline.
// Reference: rippled NFTokenAcceptOffer.cpp doApply lines 373-377
func checkIssuerTrustLineForAccept(ctx *tx.ApplyContext, nftIssuerID [20]byte, amount tx.Amount, nftFlags uint16) tx.Result {
	if !ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustline) {
		return tx.TesSUCCESS
	}
	if nftFlags&NFTokenFlagTrustLine != 0 {
		return tx.TesSUCCESS
	}

	iouIssuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	// NFT issuer == IOU issuer: issuer doesn't need trust line for own currency
	if nftIssuerID == iouIssuerID {
		return tx.TesSUCCESS
	}

	trustLineKey := keylet.Line(nftIssuerID, iouIssuerID, amount.Currency)
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return tx.TecNO_LINE
	}

	return tx.TesSUCCESS
}
