package payment

import (
	"encoding/hex"
	"math/big"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/ledger/entry"
)

// applyMPTPayment applies an MPT direct payment.
// Reference: rippled Payment.cpp doApply() mptDirect path + View.cpp rippleSendMPT/rippleCreditMPT
func (p *Payment) applyMPTPayment(ctx *tx.ApplyContext) tx.Result {
	// Parse MPTokenIssuanceID
	issuanceIDBytes, err := hex.DecodeString(p.MPTokenIssuanceID)
	if err != nil || len(issuanceIDBytes) != 24 {
		return tx.TecOBJECT_NOT_FOUND
	}
	var mptID [24]byte
	copy(mptID[:], issuanceIDBytes)

	// Look up the issuance
	issuanceKey := keylet.MPTIssuance(mptID)
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		return tx.TecOBJECT_NOT_FOUND
	}
	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	issuerID := issuance.Issuer

	// Decode destination
	destAccountID, err := state.DecodeAccountID(p.Destination)
	if err != nil {
		return tx.TemDST_NEEDED
	}

	// Check destination exists
	destKey := keylet.Account(destAccountID)
	destData, err := ctx.View.Read(destKey)
	if err != nil || destData == nil {
		return tx.TecNO_DST
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check destination tag requirement
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// requireAuth: check sender is authorized
	// Reference: rippled Payment.cpp:518-520
	if issuance.Flags&entry.LsfMPTRequireAuth != 0 && ctx.AccountID != issuerID {
		senderTokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)
		senderTokenRaw, err := ctx.View.Read(senderTokenKey)
		if err != nil || senderTokenRaw == nil {
			return tx.TecNO_AUTH
		}
		senderToken, err := state.ParseMPToken(senderTokenRaw)
		if err != nil {
			return tx.TefINTERNAL
		}
		if senderToken.Flags&entry.LsfMPTAuthorized == 0 {
			return tx.TecNO_AUTH
		}
	}

	// requireAuth: check destination is authorized
	// Reference: rippled Payment.cpp:522-524
	if issuance.Flags&entry.LsfMPTRequireAuth != 0 && destAccountID != issuerID {
		destTokenKey := keylet.MPToken(issuanceKey.Key, destAccountID)
		destTokenRaw, err := ctx.View.Read(destTokenKey)
		if err != nil || destTokenRaw == nil {
			return tx.TecNO_AUTH
		}
		destToken, err := state.ParseMPToken(destTokenRaw)
		if err != nil {
			return tx.TefINTERNAL
		}
		if destToken.Flags&entry.LsfMPTAuthorized == 0 {
			return tx.TecNO_AUTH
		}
	}

	// Verify deposit preauth
	// Reference: rippled Payment.cpp:531-539
	if result := p.verifyDepositPreauth(ctx, ctx.AccountID, destAccountID, destAccount); result != tx.TesSUCCESS {
		return result
	}

	// Extract the payment amount as uint64
	dstAmount := mptAmountToUint64(p.Amount)
	if dstAmount == 0 {
		return tx.TemBAD_AMOUNT
	}

	senderIsIssuer := ctx.AccountID == issuerID
	destIsIssuer := destAccountID == issuerID

	// canTransfer: holder-to-holder requires CanTransfer flag
	// Reference: rippled Payment.cpp:526-529
	if !senderIsIssuer && !destIsIssuer {
		if issuance.Flags&entry.LsfMPTCanTransfer == 0 {
			return tx.TecNO_AUTH
		}
	}

	// Compute transfer rate for holder-to-holder transfers
	// Reference: rippled Payment.cpp:546-557, View.cpp transferRate()
	// rate is in QUALITY_ONE format: 1_000_000_000 = 1.0
	rate := uint64(qualityOne)
	if !senderIsIssuer && !destIsIssuer {
		// Check frozen (globally or individually locked)
		if issuance.Flags&entry.LsfMPTLocked != 0 {
			return tx.TecLOCKED
		}
		// Check individual locks on sender and destination
		senderTokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)
		senderTokenRaw, _ := ctx.View.Read(senderTokenKey)
		if senderTokenRaw != nil {
			senderToken, _ := state.ParseMPToken(senderTokenRaw)
			if senderToken != nil && senderToken.Flags&entry.LsfMPTLocked != 0 {
				return tx.TecLOCKED
			}
		}
		destTokenKey := keylet.MPToken(issuanceKey.Key, destAccountID)
		destTokenRaw, _ := ctx.View.Read(destTokenKey)
		if destTokenRaw != nil {
			destToken, _ := state.ParseMPToken(destTokenRaw)
			if destToken != nil && destToken.Flags&entry.LsfMPTLocked != 0 {
				return tx.TecLOCKED
			}
		}

		// Transfer fee: rate = 1_000_000_000 + 10_000 * TransferFee
		if issuance.TransferFee > 0 {
			rate = qualityOne + 10_000*uint64(issuance.TransferFee)
		}
	}

	// maxSourceAmount: SendMax if present, otherwise dstAmount
	// Reference: rippled Payment.cpp:384-398 getMaxSourceAmount()
	maxSourceAmount := dstAmount
	if p.SendMax != nil {
		maxSourceAmount = mptAmountToUint64(*p.SendMax)
	}

	// Amount to deliver and required source amount factoring in transfer rate
	// Reference: rippled Payment.cpp:560-580
	amountDeliver := dstAmount
	requiredMaxSourceAmount := mptMultiply(dstAmount, rate)

	// Partial payment: if required exceeds maxSource, adjust amountDeliver
	isPartialPayment := p.GetFlags()&PaymentFlagPartialPayment != 0
	if isPartialPayment && requiredMaxSourceAmount > maxSourceAmount {
		requiredMaxSourceAmount = maxSourceAmount
		amountDeliver = mptDivide(maxSourceAmount, rate)
	}

	// Check: source insufficient
	if requiredMaxSourceAmount > maxSourceAmount {
		return tx.TecPATH_PARTIAL
	}

	// Check: DeliverMin not met
	if p.DeliverMin != nil {
		deliverMin := mptAmountToUint64(*p.DeliverMin)
		if deliverMin > 0 && amountDeliver < deliverMin {
			return tx.TecPATH_PARTIAL
		}
	}

	// Execute the actual transfer
	// Reference: rippled Payment.cpp:582-595
	var res tx.Result
	if senderIsIssuer || destIsIssuer {
		// Direct transfer (issuer involved, no transfer fee)
		res = p.mptDirectTransfer(ctx, issuance, issuanceKey, amountDeliver, senderIsIssuer, destIsIssuer, destAccountID)
	} else {
		// Transit through issuer (holder-to-holder, with transfer fee)
		res = p.mptTransitTransfer(ctx, issuance, issuanceKey, amountDeliver, rate, destAccountID)
	}

	// Map error codes per rippled Payment.cpp:593-594
	if res == tx.TecINSUFFICIENT_FUNDS || res == tx.TecPATH_DRY {
		res = tx.TecPATH_PARTIAL
	}

	return res
}

// mptDirectTransfer handles MPT payment where one party is the issuer.
// No transfer fee applies. Handles MaximumAmount enforcement.
func (p *Payment) mptDirectTransfer(ctx *tx.ApplyContext, issuance *state.MPTokenIssuanceData,
	issuanceKey keylet.Keylet, amount uint64, senderIsIssuer, destIsIssuer bool, destAccountID [20]byte) tx.Result {
	// If sender is issuer: check MaximumAmount
	// Reference: rippled View.cpp rippleSendMPT() lines 2044-2055
	if senderIsIssuer {
		maxAmount := uint64(maxMPTokenAmount)
		if issuance.MaximumAmount != nil {
			maxAmount = *issuance.MaximumAmount
		}
		if amount > maxAmount || issuance.OutstandingAmount > maxAmount-amount {
			return tx.TecPATH_DRY
		}
	}

	// rippleCreditMPT: sender side
	if senderIsIssuer {
		issuance.OutstandingAmount += amount
	} else {
		senderTokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)
		senderTokenRaw, err := ctx.View.Read(senderTokenKey)
		if err != nil || senderTokenRaw == nil {
			return tx.TecNO_AUTH
		}
		senderToken, err := state.ParseMPToken(senderTokenRaw)
		if err != nil {
			return tx.TefINTERNAL
		}
		if senderToken.MPTAmount < amount {
			return tx.TecINSUFFICIENT_FUNDS
		}
		senderToken.MPTAmount -= amount
		updatedSenderToken, err := state.SerializeMPToken(senderToken)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(senderTokenKey, updatedSenderToken); err != nil {
			return tx.TefINTERNAL
		}
	}

	// rippleCreditMPT: receiver side
	if destIsIssuer {
		if issuance.OutstandingAmount < amount {
			return tx.TefINTERNAL
		}
		issuance.OutstandingAmount -= amount
	} else {
		destTokenKey := keylet.MPToken(issuanceKey.Key, destAccountID)
		destTokenRaw, err := ctx.View.Read(destTokenKey)
		if err != nil || destTokenRaw == nil {
			return tx.TecNO_AUTH
		}
		destToken, err := state.ParseMPToken(destTokenRaw)
		if err != nil {
			return tx.TefINTERNAL
		}
		destToken.MPTAmount += amount
		updatedDestToken, err := state.SerializeMPToken(destToken)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(destTokenKey, updatedDestToken); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Update issuance
	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedIssuance); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// mptTransitTransfer handles holder-to-holder MPT payment via transit through issuer.
// Transfer fee is applied: sender pays amountDeliver * rate / QUALITY_ONE.
// Reference: rippled View.cpp rippleSendMPT() lines 2068-2085
func (p *Payment) mptTransitTransfer(ctx *tx.ApplyContext, issuance *state.MPTokenIssuanceData,
	issuanceKey keylet.Keylet, amountDeliver, rate uint64, destAccountID [20]byte) tx.Result {
	// Actual amount sender pays (includes transfer fee)
	saActual := mptMultiply(amountDeliver, rate)

	// Step 1: Credit receiver (issuer → receiver via rippleCreditMPT)
	// Outstanding increases by amountDeliver
	issuance.OutstandingAmount += amountDeliver

	destTokenKey := keylet.MPToken(issuanceKey.Key, destAccountID)
	destTokenRaw, err := ctx.View.Read(destTokenKey)
	if err != nil || destTokenRaw == nil {
		return tx.TecNO_AUTH
	}
	destToken, err := state.ParseMPToken(destTokenRaw)
	if err != nil {
		return tx.TefINTERNAL
	}
	destToken.MPTAmount += amountDeliver

	// Step 2: Debit sender (sender → issuer via rippleCreditMPT)
	// Outstanding decreases by saActual
	senderTokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)
	senderTokenRaw, err := ctx.View.Read(senderTokenKey)
	if err != nil || senderTokenRaw == nil {
		return tx.TecNO_AUTH
	}
	senderToken, err := state.ParseMPToken(senderTokenRaw)
	if err != nil {
		return tx.TefINTERNAL
	}
	if senderToken.MPTAmount < saActual {
		return tx.TecINSUFFICIENT_FUNDS
	}
	senderToken.MPTAmount -= saActual
	issuance.OutstandingAmount -= saActual

	// Net OutstandingAmount change: amountDeliver - saActual (negative, fee burned)

	// Serialize and update all modified entries
	updatedSenderToken, err := state.SerializeMPToken(senderToken)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(senderTokenKey, updatedSenderToken); err != nil {
		return tx.TefINTERNAL
	}

	updatedDestToken, err := state.SerializeMPToken(destToken)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(destTokenKey, updatedDestToken); err != nil {
		return tx.TefINTERNAL
	}

	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedIssuance); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

const (
	// qualityOne is the identity rate (1.0) in rippled's rate format
	qualityOne = 1_000_000_000
	// maxMPTokenAmount is the maximum MPT value (int64 max)
	maxMPTokenAmount = 0x7FFFFFFFFFFFFFFF
)

// mptMultiply multiplies amount by rate/QUALITY_ONE using big.Int to avoid overflow.
// Reference: rippled STAmount multiply() for MPT - "No rounding"
func mptMultiply(amount, rate uint64) uint64 {
	if rate == qualityOne {
		return amount
	}
	result := new(big.Int).Mul(
		new(big.Int).SetUint64(amount),
		new(big.Int).SetUint64(rate),
	)
	result.Div(result, new(big.Int).SetUint64(qualityOne))
	return result.Uint64()
}

// mptDivide divides amount by rate/QUALITY_ONE using big.Int to avoid overflow.
// Reference: rippled STAmount divide() for MPT - "No rounding"
func mptDivide(amount, rate uint64) uint64 {
	if rate == qualityOne {
		return amount
	}
	result := new(big.Int).Mul(
		new(big.Int).SetUint64(amount),
		new(big.Int).SetUint64(qualityOne),
	)
	result.Div(result, new(big.Int).SetUint64(rate))
	return result.Uint64()
}

// mptAmountToUint64 converts an Amount to a uint64 integer value.
// Prefers the raw MPT int64 value when available to avoid IOU normalization precision loss.
func mptAmountToUint64(a tx.Amount) uint64 {
	// Use raw MPT value if available (preserves precision for large values)
	if raw, ok := a.MPTRaw(); ok {
		if raw <= 0 {
			return 0
		}
		return uint64(raw)
	}
	// Fallback: reconstruct from IOU mantissa/exponent
	mantissa := a.Mantissa()
	if mantissa <= 0 {
		return 0
	}
	exp := a.Exponent()
	result := uint64(mantissa)
	for exp > 0 {
		result *= 10
		exp--
	}
	for exp < 0 {
		result /= 10
		exp++
	}
	return result
}
