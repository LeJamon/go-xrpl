package payment

import (
	"encoding/hex"
	"math"
	"math/big"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

// applyMPTPayment applies an MPT direct payment.
// Reference: rippled Payment.cpp doApply() mptDirect path + View.cpp rippleSendMPT/rippleCreditMPT
func (p *Payment) applyMPTPayment(ctx *tx.ApplyContext) ter.Result {
	mptIDHex := p.Amount.MPTIssuanceID()
	issuanceIDBytes, err := hex.DecodeString(mptIDHex)
	if err != nil || len(issuanceIDBytes) != 24 {
		return ter.TecOBJECT_NOT_FOUND
	}
	var mptID [24]byte
	copy(mptID[:], issuanceIDBytes)

	// Look up the issuance
	issuanceKey := keylet.MPTIssuance(mptID)
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		return ter.TecOBJECT_NOT_FOUND
	}
	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		return ter.TefINTERNAL
	}

	issuerID := issuance.Issuer

	// Decode destination
	destAccountID, err := state.DecodeAccountID(p.Destination)
	if err != nil {
		return ter.TemDST_NEEDED
	}

	// Check destination exists
	destKey := keylet.Account(destAccountID)
	destData, err := ctx.View.Read(destKey)
	if err != nil || destData == nil {
		return ter.TecNO_DST
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Check destination tag requirement
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
		return ter.TecDST_TAG_NEEDED
	}

	if res := mptutil.RequireAuthWithTypeAt(
		ctx.View, mptID, ctx.AccountID, mptutil.LegacyAuth, ctx.Config.ParentCloseTime,
	); res != ter.TesSUCCESS {
		return res
	}

	if res := mptutil.RequireAuthWithTypeAt(
		ctx.View, mptID, destAccountID, mptutil.LegacyAuth, ctx.Config.ParentCloseTime,
	); res != ter.TesSUCCESS {
		return res
	}

	senderIsIssuer := ctx.AccountID == issuerID
	destIsIssuer := destAccountID == issuerID

	if result := mptutil.CanTransfer(ctx.View, mptID, ctx.AccountID, destAccountID); result != ter.TesSUCCESS {
		return result
	}

	// Verify deposit preauth
	// Reference: rippled Payment.cpp:531-539
	if result := credential.VerifyDepositPreauth(ctx, p.CredentialIDs, ctx.AccountID, destAccountID, destAccount); result != ter.TesSUCCESS {
		return result
	}

	// Extract the payment amount as uint64
	dstAmount := mptAmountToUint64(p.Amount)
	if dstAmount == 0 {
		return ter.TemBAD_AMOUNT
	}

	// Compute transfer rate for holder-to-holder transfers
	// Reference: rippled Payment.cpp:546-557, View.cpp transferRate()
	// rate is in QUALITY_ONE format: 1_000_000_000 = 1.0
	rate := uint64(mptRateOne)
	if !senderIsIssuer && !destIsIssuer {
		if mptutil.IsFrozen(ctx.View, mptID, ctx.AccountID) ||
			mptutil.IsFrozen(ctx.View, mptID, destAccountID) {
			return ter.TecLOCKED
		}

		// Transfer fee: rate = 1_000_000_000 + 10_000 * TransferFee
		if issuance.TransferFee > 0 {
			rate = mptRateOne + 10_000*uint64(issuance.TransferFee)
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
	requiredMaxSourceAmount := mptMultiply(dstAmount, rate, ctx.NumberContext())

	// Partial payment: if required exceeds maxSource, adjust amountDeliver
	isPartialPayment := p.GetFlags()&PaymentFlagPartialPayment != 0
	if isPartialPayment && requiredMaxSourceAmount > maxSourceAmount {
		requiredMaxSourceAmount = maxSourceAmount
		amountDeliver = mptDivide(maxSourceAmount, rate)
	}

	// Check: source insufficient
	if requiredMaxSourceAmount > maxSourceAmount {
		return ter.TecPATH_PARTIAL
	}

	// Check: DeliverMin not met
	if p.DeliverMin != nil {
		deliverMin := mptAmountToUint64(*p.DeliverMin)
		if deliverMin > 0 && amountDeliver < deliverMin {
			return ter.TecPATH_PARTIAL
		}
	}

	// Execute the actual transfer
	// Reference: rippled Payment.cpp:582-595
	var res ter.Result
	if senderIsIssuer || destIsIssuer {
		// Direct transfer (issuer involved, no transfer fee)
		res = p.mptDirectTransfer(ctx, issuance, issuanceKey, amountDeliver, senderIsIssuer, destIsIssuer, destAccountID)
	} else {
		// Transit through issuer (holder-to-holder, with transfer fee)
		res = p.mptTransitTransfer(ctx, issuance, issuanceKey, amountDeliver, rate, destAccountID)
	}

	if res == ter.TesSUCCESS {
		// Record the actual delivered amount when it differs from the requested
		// Amount (partial payment or transfer fee), gated on fixMPTDeliveredAmount.
		// Reference: rippled Payment.cpp:616-621
		if ctx.Rules().Enabled(amendment.FeatureFixMPTDeliveredAmount) && amountDeliver != dstAmount {
			deliveredAmt := state.NewMPTAmountWithIssuanceID(int64(amountDeliver), p.Amount.Issuer, mptIDHex)
			ctx.Metadata.DeliveredAmount = &deliveredAmt
		}
	} else if res == ter.TecINSUFFICIENT_FUNDS || res == ter.TecPATH_DRY {
		// Map error codes per rippled Payment.cpp:623-624
		res = ter.TecPATH_PARTIAL
	}

	return res
}

// mptDirectTransfer handles MPT payment where one party is the issuer.
// No transfer fee applies. Handles MaximumAmount enforcement.
func (p *Payment) mptDirectTransfer(ctx *tx.ApplyContext, issuance *state.MPTokenIssuanceData,
	issuanceKey keylet.Keylet, amount uint64, senderIsIssuer, destIsIssuer bool, destAccountID [20]byte) ter.Result {
	// If sender is issuer: check MaximumAmount
	// Reference: rippled View.cpp rippleSendMPT() lines 2044-2055
	if senderIsIssuer {
		maxAmount := maxMPTokenAmount
		if issuance.MaximumAmount != nil {
			maxAmount = *issuance.MaximumAmount
		}
		if amount > maxAmount || issuance.OutstandingAmount > maxAmount-amount {
			return ter.TecPATH_DRY
		}
	}

	// rippleCreditMPT: sender side
	if senderIsIssuer {
		issuance.OutstandingAmount += amount
	} else {
		senderTokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)
		senderTokenRaw, err := ctx.View.Read(senderTokenKey)
		if err != nil || senderTokenRaw == nil {
			return ter.TecNO_AUTH
		}
		senderToken, err := state.ParseMPToken(senderTokenRaw)
		if err != nil {
			return ter.TefINTERNAL
		}
		if senderToken.MPTAmount < amount {
			return ter.TecINSUFFICIENT_FUNDS
		}
		senderToken.MPTAmount -= amount
		updatedSenderToken, err := state.SerializeMPToken(senderToken)
		if err != nil {
			return ter.TefINTERNAL
		}
		if err := ctx.View.Update(senderTokenKey, updatedSenderToken); err != nil {
			return ter.TefINTERNAL
		}
	}

	// rippleCreditMPT: receiver side
	if destIsIssuer {
		if issuance.OutstandingAmount < amount {
			return ter.TefINTERNAL
		}
		issuance.OutstandingAmount -= amount
	} else {
		destTokenKey := keylet.MPToken(issuanceKey.Key, destAccountID)
		destTokenRaw, err := ctx.View.Read(destTokenKey)
		if err != nil || destTokenRaw == nil {
			return ter.TecNO_AUTH
		}
		destToken, err := state.ParseMPToken(destTokenRaw)
		if err != nil {
			return ter.TefINTERNAL
		}
		destToken.MPTAmount += amount
		updatedDestToken, err := state.SerializeMPToken(destToken)
		if err != nil {
			return ter.TefINTERNAL
		}
		if err := ctx.View.Update(destTokenKey, updatedDestToken); err != nil {
			return ter.TefINTERNAL
		}
	}

	// Update issuance
	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedIssuance); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// mptTransitTransfer handles holder-to-holder MPT payment via transit through issuer.
// Transfer fee is applied: sender pays amountDeliver * rate / QUALITY_ONE.
// Reference: rippled View.cpp rippleSendMPT() lines 2068-2085
func (p *Payment) mptTransitTransfer(ctx *tx.ApplyContext, issuance *state.MPTokenIssuanceData,
	issuanceKey keylet.Keylet, amountDeliver, rate uint64, destAccountID [20]byte) ter.Result {
	// Actual amount sender pays (includes transfer fee)
	saActual := mptMultiply(amountDeliver, rate, ctx.NumberContext())

	// Step 1: Credit receiver (issuer → receiver via rippleCreditMPT)
	// Outstanding increases by amountDeliver
	issuance.OutstandingAmount += amountDeliver

	destTokenKey := keylet.MPToken(issuanceKey.Key, destAccountID)
	destTokenRaw, err := ctx.View.Read(destTokenKey)
	if err != nil || destTokenRaw == nil {
		return ter.TecNO_AUTH
	}
	destToken, err := state.ParseMPToken(destTokenRaw)
	if err != nil {
		return ter.TefINTERNAL
	}
	destToken.MPTAmount += amountDeliver

	// Step 2: Debit sender (sender → issuer via rippleCreditMPT)
	// Outstanding decreases by saActual
	senderTokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)
	senderTokenRaw, err := ctx.View.Read(senderTokenKey)
	if err != nil || senderTokenRaw == nil {
		return ter.TecNO_AUTH
	}
	senderToken, err := state.ParseMPToken(senderTokenRaw)
	if err != nil {
		return ter.TefINTERNAL
	}
	if senderToken.MPTAmount < saActual {
		return ter.TecINSUFFICIENT_FUNDS
	}
	senderToken.MPTAmount -= saActual
	issuance.OutstandingAmount -= saActual

	// Net OutstandingAmount change: amountDeliver - saActual (negative, fee burned)

	// Serialize and update all modified entries
	updatedSenderToken, err := state.SerializeMPToken(senderToken)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(senderTokenKey, updatedSenderToken); err != nil {
		return ter.TefINTERNAL
	}

	updatedDestToken, err := state.SerializeMPToken(destToken)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(destTokenKey, updatedDestToken); err != nil {
		return ter.TefINTERNAL
	}

	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedIssuance); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

const (
	// mptRateOne is the identity transfer rate (1.0) in rippled's rate format.
	mptRateOne = 1_000_000_000
	// maxMPTokenAmount is the maximum MPT value (int64 max)
	maxMPTokenAmount = protocol.MaxMPTokenAmount
)

func mptMultiply(amount, rate uint64, numberContext state.NumberContext) uint64 {
	if rate == mptRateOne {
		return amount
	}
	if amount > math.MaxInt64 || rate == 0 || rate > math.MaxUint32 {
		panic("MPT amount out of range")
	}

	amountNumber := numberContext.Int(int64(amount))
	rateNumber := numberContext.Number(int64(rate), -9, state.RoundToNearest)
	result := amountNumber.MulRounded(rateNumber, state.RoundToNearest).
		ToInt64WithMode(state.RoundToNearest)
	if result < 0 {
		panic("MPT amount out of range")
	}
	return uint64(result)
}

func mptDivide(amount, rate uint64) uint64 {
	if rate == mptRateOne {
		return amount
	}
	if amount > math.MaxInt64 || rate == 0 || rate > math.MaxUint32 {
		panic("MPT amount out of range")
	}
	if amount == 0 {
		return 0
	}

	numMantissa, numExponent := normalizeMPTDivideOperand(amount, 0)
	denMantissa, denExponent := normalizeMPTDivideOperand(rate, -9)
	numerator := new(big.Int).Mul(
		new(big.Int).SetUint64(numMantissa),
		new(big.Int).SetUint64(100_000_000_000_000_000),
	)
	quotient := new(big.Int).Quo(numerator, new(big.Int).SetUint64(denMantissa))
	if !quotient.IsUint64() {
		panic("MPT amount out of range")
	}
	return mptFromUncheckedNumber(quotient.Uint64()+5, numExponent-denExponent-17)
}

func normalizeMPTDivideOperand(mantissa uint64, exponent int) (uint64, int) {
	for mantissa < uint64(state.MinMantissa) {
		mantissa *= 10
		exponent--
	}
	return mantissa, exponent
}

func mptFromUncheckedNumber(mantissa uint64, exponent int) uint64 {
	if mantissa == 0 || exponent <= -20 {
		return 0
	}
	if exponent > 18 {
		panic("MPT amount out of range")
	}
	if mantissa > math.MaxInt64 {
		mantissa /= 10
		exponent++
	}

	for exponent > 0 {
		if mantissa > math.MaxInt64/10 {
			panic("MPT amount out of range")
		}
		mantissa *= 10
		exponent--
	}
	if exponent < 0 {
		divisor := uint64(1)
		for ; exponent < 0; exponent++ {
			divisor *= 10
		}
		quotient, remainder := mantissa/divisor, mantissa%divisor
		half := divisor / 2
		if remainder > half || (remainder == half && quotient&1 != 0) {
			quotient++
		}
		mantissa = quotient
	}
	if mantissa > math.MaxInt64 {
		panic("MPT amount out of range")
	}
	return mantissa
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
