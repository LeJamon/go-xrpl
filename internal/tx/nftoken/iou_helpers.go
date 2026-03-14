package nftoken

import (
	"encoding/binary"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// isOfferAmountNegative checks the raw binary SLE data of an NFTokenOffer
// to determine if its Amount field represents a negative value.
// In XRPL binary encoding for XRP: bit 63 = 0 (native), bit 62 = sign (1=positive, 0=negative).
// For IOU: bit 63 = 1, bit 62 = sign.
// The NFTokenOfferData struct uses uint64 for Amount and loses the sign, so
// this function re-parses the raw binary to recover it.
// Reference: rippled NFTokenAcceptOffer.cpp pay() line 404: if (amount < beast::zero) return tecINTERNAL;
func isOfferAmountNegative(rawSLEData []byte) bool {
	// FieldTypeAmount = 6, Amount fieldCode = 1 → header byte = 0x61
	// Walk the binary SLE to find the Amount field
	offset := 0
	for offset < len(rawSLEData) {
		if offset+1 > len(rawSLEData) {
			break
		}

		header := rawSLEData[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		if typeCode == 0 {
			if offset >= len(rawSLEData) {
				break
			}
			typeCode = rawSLEData[offset]
			offset++
		}

		if fieldCode == 0 {
			if offset >= len(rawSLEData) {
				break
			}
			fieldCode = rawSLEData[offset]
			offset++
		}

		switch typeCode {
		case 1: // UInt16
			if offset+2 > len(rawSLEData) {
				return false
			}
			offset += 2

		case 2: // UInt32
			if offset+4 > len(rawSLEData) {
				return false
			}
			offset += 4

		case 3: // UInt64
			if offset+8 > len(rawSLEData) {
				return false
			}
			offset += 8

		case 5: // Hash256
			if offset+32 > len(rawSLEData) {
				return false
			}
			offset += 32

		case 6: // Amount
			if offset+8 > len(rawSLEData) {
				return false
			}
			if fieldCode == 1 { // Amount field (sfAmount)
				rawAmount := binary.BigEndian.Uint64(rawSLEData[offset : offset+8])
				isNative := (rawAmount & 0x8000000000000000) == 0
				isPositive := (rawAmount & 0x4000000000000000) != 0

				if isNative {
					// XRP: negative if bit 62 is 0 AND value is not zero
					value := rawAmount & 0x3FFFFFFFFFFFFFFF
					if !isPositive && value != 0 {
						return true
					}
				} else {
					// IOU: negative if bit 62 is 0
					// The mantissa/exponent are in the lower bits
					if !isPositive {
						return true
					}
				}
				return false
			}
			// Skip this amount field
			if rawSLEData[offset]&0x80 == 0 {
				offset += 8 // XRP
			} else {
				offset += 48 // IOU
			}

		case 8: // AccountID
			if offset >= len(rawSLEData) {
				return false
			}
			length := int(rawSLEData[offset])
			offset++
			if offset+length > len(rawSLEData) {
				return false
			}
			offset += length

		case 0x0E: // Array end marker / Object end
			// End of object or array
			continue

		case 0x0F: // End marker
			return false

		default:
			// Unknown type - bail
			return false
		}
	}
	return false
}

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

// offerIOUToAmount converts an NFTokenOfferData's IOU amount to a tx.Amount.
// If the offer has no IOU amount, returns an XRP amount from the offer's Amount field.
func offerIOUToAmount(offer *state.NFTokenOfferData) tx.Amount {
	if offer.AmountIOU == nil {
		return tx.NewXRPAmount(int64(offer.Amount))
	}
	issuerAddr, err := addresscodec.EncodeAccountIDToClassicAddress(offer.AmountIOU.Issuer[:])
	if err != nil {
		return tx.NewXRPAmount(0)
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
		return rippleCreditIOU(view, from, to, amount)
	}

	// Third party: sender → issuer (with transfer rate) and issuer → receiver
	// Get transfer rate from issuer
	transferRate := getTransferRate(view, issuerID)
	if transferRate != 0 && transferRate != qualityOne {
		// Charge sender the amount * transferRate / QUALITY_ONE
		senderAmount := amount.MulRatio(transferRate, qualityOne, true)
		// Credit receiver the original amount
		if r := rippleCreditIOU(view, issuerID, to, amount); r != tx.TesSUCCESS {
			return r
		}
		// Debit sender the increased amount
		return rippleCreditIOU(view, from, issuerID, senderAmount)
	}

	// No transfer rate — direct credit/debit
	if r := rippleCreditIOU(view, issuerID, to, amount); r != tx.TesSUCCESS {
		return r
	}
	return rippleCreditIOU(view, from, issuerID, amount)
}

// qualityOne is the base transfer rate (1x = no fee)
const qualityOne uint32 = 1_000_000_000

// getTransferRate reads the transfer rate from an issuer's account.
// Returns 0 if no rate is set, or the rate as uint32 (QUALITY_ONE = 1e9 = no fee).
func getTransferRate(view tx.LedgerView, issuerID [20]byte) uint32 {
	acctKey := keylet.Account(issuerID)
	acctData, err := view.Read(acctKey)
	if err != nil || acctData == nil {
		return 0
	}
	acct, err := state.ParseAccountRoot(acctData)
	if err != nil {
		return 0
	}
	return acct.TransferRate
}

// rippleCreditIOU modifies the trust line balance between two accounts.
// If the trust line does not exist, it is auto-created (matching rippled's rippleCredit).
// Reference: rippled Ledger/View.cpp rippleCredit
func rippleCreditIOU(view tx.LedgerView, sender, receiver [20]byte, amount tx.Amount) tx.Result {
	if amount.IsZero() {
		return tx.TesSUCCESS
	}

	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Determine the two accounts for the trust line
	var acct1, acct2 [20]byte
	if sender == issuerID {
		acct1 = issuerID
		acct2 = receiver
	} else {
		acct1 = sender
		acct2 = issuerID
	}

	trustLineKey := keylet.Line(acct1, acct2, amount.Currency)
	trustLineData, err := view.Read(trustLineKey)

	if err != nil || trustLineData == nil {
		// Trust line doesn't exist — auto-create it
		// Reference: rippled rippleCredit creates trust lines on the fly
		return createTrustLineWithBalance(view, sender, receiver, amount, trustLineKey)
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Determine account ordering: low account < high account
	senderIsLow := state.CompareAccountIDsForLine(sender, receiver) < 0

	// Balance convention: positive = low account owes high account
	// When sender is low: sending means decreasing the balance (low account pays)
	// When sender is high: sending means increasing the balance (high account pays)
	if senderIsLow {
		// Sender is low — subtract from balance (low pays)
		newBalance, err := rs.Balance.Sub(amount)
		if err != nil {
			return tx.TefINTERNAL
		}
		rs.Balance = newBalance
	} else {
		// Sender is high — add to balance (high pays, from high's perspective)
		newBalance, err := rs.Balance.Add(amount)
		if err != nil {
			return tx.TefINTERNAL
		}
		rs.Balance = newBalance
	}

	// Serialize and update
	updated, err := state.SerializeRippleState(rs)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := view.Update(trustLineKey, updated); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
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

// createTrustLineWithBalance creates a new trust line between sender and receiver
// with the initial balance set from the transfer amount.
// Only the RECEIVER gets a reserve flag set and OwnerCount incremented.
// NoRipple flags are set based on each account's DefaultRipple setting.
// Reference: rippled Ledger/View.cpp rippleCredit → trustCreate
func createTrustLineWithBalance(view tx.LedgerView, sender, receiver [20]byte, amount tx.Amount, trustLineKey keylet.Keylet) tx.Result {
	senderIsHigh := state.CompareAccountIDsForLine(sender, receiver) > 0

	// Determine low/high accounts
	var lowAccountID, highAccountID [20]byte
	if senderIsHigh {
		lowAccountID = receiver
		highAccountID = sender
	} else {
		lowAccountID = sender
		highAccountID = receiver
	}

	lowAccountStr, err := state.EncodeAccountID(lowAccountID)
	if err != nil {
		return tx.TefINTERNAL
	}
	highAccountStr, err := state.EncodeAccountID(highAccountID)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Set balance based on sender's position
	// Convention: positive balance = LOW account holds tokens
	// When sender is HIGH (paying LOW): LOW receives → balance = +amount
	// When sender is LOW (paying HIGH): HIGH receives → balance = -amount
	var balance tx.Amount
	if senderIsHigh {
		// Sender is HIGH, receiver is LOW → LOW gets tokens → positive balance
		balance = state.NewIssuedAmountFromValue(amount.IOU().Mantissa(), amount.IOU().Exponent(), amount.Currency, state.AccountOneAddress)
	} else {
		// Sender is LOW, receiver is HIGH → HIGH gets tokens → negative balance
		negated := amount.Negate()
		balance = state.NewIssuedAmountFromValue(negated.IOU().Mantissa(), negated.IOU().Exponent(), amount.Currency, state.AccountOneAddress)
	}

	// Determine receiver's position and set reserve flag accordingly.
	// Reference: rippled trustCreate — uFlags = bSetHigh ? lsfHighReserve : lsfLowReserve
	// In rippled, the "set" side is the receiver. Only the receiver gets a reserve.
	var flags uint32
	receiverIsHigh := !senderIsHigh
	if receiverIsHigh {
		flags |= state.LsfHighReserve
	} else {
		flags |= state.LsfLowReserve
	}

	// Set NoRipple flags based on DefaultRipple settings.
	// Reference: rippled trustCreate lines 1415-1432
	// If an account does NOT have DefaultRipple, set NoRipple on that side.
	receiverAcctData, err := view.Read(keylet.Account(receiver))
	if err != nil || receiverAcctData == nil {
		return tx.TefINTERNAL
	}
	receiverAcct, err := state.ParseAccountRoot(receiverAcctData)
	if err != nil {
		return tx.TefINTERNAL
	}
	receiverNoRipple := (receiverAcct.Flags & state.LsfDefaultRipple) == 0
	if receiverNoRipple {
		if receiverIsHigh {
			flags |= state.LsfHighNoRipple
		} else {
			flags |= state.LsfLowNoRipple
		}
	}

	senderAcctData, err := view.Read(keylet.Account(sender))
	if err != nil || senderAcctData == nil {
		return tx.TefINTERNAL
	}
	senderAcct, err := state.ParseAccountRoot(senderAcctData)
	if err != nil {
		return tx.TefINTERNAL
	}
	senderNoRipple := (senderAcct.Flags & state.LsfDefaultRipple) == 0
	if senderNoRipple {
		if senderIsHigh {
			flags |= state.LsfHighNoRipple
		} else {
			flags |= state.LsfLowNoRipple
		}
	}

	rs := &state.RippleState{
		Balance:   balance,
		LowLimit:  tx.NewIssuedAmount(0, -100, amount.Currency, lowAccountStr),
		HighLimit: tx.NewIssuedAmount(0, -100, amount.Currency, highAccountStr),
		Flags:     flags,
	}

	// Insert into LOW account's owner directory
	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDirResult, err := state.DirInsert(view, lowDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return tx.TefINTERNAL
	}
	rs.LowNode = lowDirResult.Page

	// Insert into HIGH account's owner directory
	highDirKey := keylet.OwnerDir(highAccountID)
	highDirResult, err := state.DirInsert(view, highDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return tx.TefINTERNAL
	}
	rs.HighNode = highDirResult.Page

	// Serialize and insert the trust line
	trustLineData, err := state.SerializeRippleState(rs)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := view.Insert(trustLineKey, trustLineData); err != nil {
		return tx.TefINTERNAL
	}

	// Only increment OwnerCount for the RECEIVER (matching rippled's trustCreate).
	// The sender (IOU issuer) doesn't get a reserve for auto-created trust lines.
	adjustOwnerCountViaView(view, receiver, 1)

	return tx.TesSUCCESS
}

// checkIssuerTrustLineForAccept checks that the NFT issuer has a trust line for the
// IOU currency. Used by NFTokenAcceptOffer doApply path — gated on fixEnforceNFTokenTrustline.
// Reference: rippled NFTokenAcceptOffer.cpp doApply lines 373-377
func checkIssuerTrustLineForAccept(ctx *tx.ApplyContext, nftIssuerID [20]byte, amount tx.Amount, nftFlags uint16) tx.Result {
	if !ctx.Rules().Enabled(amendment.FeatureFixEnforceNFTokenTrustline) {
		return tx.TesSUCCESS
	}
	if nftFlags&nftFlagTrustLine != 0 {
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
