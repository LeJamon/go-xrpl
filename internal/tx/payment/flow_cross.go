package payment

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// FlowCrossResult contains the result of an offer crossing operation
type FlowCrossResult struct {
	// TakerGot is the amount the taker received (what they wanted to buy)
	TakerGot EitherAmount

	// TakerPaid is the GROSS amount the taker paid (debited from their balance, including transfer fees)
	// Use this to check if the taker fully consumed their offer (GROSS >= originalTakerGets)
	TakerPaid EitherAmount

	// TakerPaidNet is the NET amount delivered to matching offers (after transfer fees)
	// Use this to calculate the remaining offer amount (remaining = original - NET)
	TakerPaidNet EitherAmount

	// Sandbox contains the state changes from the crossing
	Sandbox *PaymentSandbox

	// RemovableOffers contains offer keys that should be removed
	RemovableOffers map[[32]byte]bool

	// Result is the transaction result code
	Result tx.Result
}

// FlowCross executes offer crossing for an OfferCreate transaction.
// This is equivalent to rippled's flowCross() in CreateOffer.cpp.
//
// Rippled's flowCross() calls the full flow() payment engine with src=dst=taker,
// which builds proper strands (DirectSteps + BookSteps + XRPEndpointSteps).
// For IOU-to-IOU crossing, it also adds an XRP bridge path.
//
// From the taker's (offer creator's) perspective:
//   - TakerGets = what the taker is SELLING (paying out)
//   - TakerPays = what the taker is BUYING (receiving)
//
// Parameters:
//   - view: The ledger view for reading/writing state
//   - takerAccount: The account creating the offer
//   - takerGets: Amount the taker is selling (what they'll pay)
//   - takerPays: Amount the taker wants to buy (what they'll receive)
//   - txHash: Current transaction hash for metadata
//   - ledgerSeq: Current ledger sequence
//   - passive: If true, only cross offers with STRICTLY better quality
//
// Returns: FlowCrossResult with the amounts traded and state changes
func FlowCross(
	view tx.LedgerView,
	takerAccount [20]byte,
	takerGets, takerPays tx.Amount,
	txHash [32]byte,
	ledgerSeq uint32,
	passive bool,
	sell bool,
	parentCloseTime uint32,
	reserveBase, reserveIncrement uint64,
	fixReducedOffersV1 bool,
	fixReducedOffersV2 bool,
	fixRmSmallIncreasedQOffers bool,
	flowSortStrands bool,
	fixAMMv1_1 bool,
	fixAMMv1_2 bool,
	fixAMMOverflowOffer bool,
	fix1781 bool,
	domainID ...*[32]byte,
) FlowCrossResult {
	// Create sandbox for tracking changes
	sandbox := NewPaymentSandbox(view)
	sandbox.SetTransactionContext(txHash, ledgerSeq)

	// Convert deliver amount (what taker wants to receive)
	// Reference: rippled CreateOffer.cpp - deliver = takerAmount.out = takerPays
	deliver := ToEitherAmount(takerPays)

	// === Calculate sendMax: the maximum the taker can actually spend ===
	// Reference: rippled CreateOffer.cpp flowCross() lines 329-367
	//
	// 1. Start with takerGets (the offer amount)
	// 2. MULTIPLY by transfer rate to get gross spend needed
	// 3. Limit to available balance
	//
	// sendMax = min(takerGets * transferRate, balance)
	inStartBalance := tx.AccountFunds(view, takerAccount, takerGets, true, reserveBase, reserveIncrement)

	sendMax := ToEitherAmount(takerGets)

	if !takerGets.IsNative() {
		issuerID, err := state.DecodeAccountID(takerGets.Issuer)
		if err == nil && takerAccount != issuerID {
			transferRate := GetTransferRate(view, issuerID)
			if transferRate != QualityOne {
				// Reference: rippled CreateOffer.cpp line 348-352:
				//   sendMax = multiplyRound(takerAmount.in, gatewayXferRate, takerAmount.in.issue(), true)
				// multiplyRound calls mulRound(amount, as_amount(rate), issue, roundUp)
				// as_amount(Rate) = STAmount(noIssue(), rate.value, -9, false)
				// This uses STAmount multiply (muldiv/10^14) NOT IOUAmount::mulRatio
				rateAmt := rateAsAmount(transferRate)
				result := state.MulRound(sendMax.IOU, rateAmt, takerGets.Currency, takerGets.Issuer, true)
				sendMax = NewIOUEitherAmount(result)
			}
		}
	}

	// Calculate quality limit for offer crossing BEFORE capping sendMax to balance
	// AND before modifying deliver for tfSell.
	// Reference: rippled CreateOffer.cpp:
	//   Line 342-353: sendMax = takerAmount.in (+ transfer rate)
	//   Line 358: Quality threshold{takerAmount.out, sendMax};  // ORIGINAL amounts
	//   Line 362-364: if passive, ++threshold
	//   Line 367-368: if (sendMax > inStartBalance) sendMax = inStartBalance  // cap AFTER
	//   Line 382-401: if (tfSell) deliver = MAX  // AFTER threshold
	takerQuality := QualityFromAmounts(sendMax, deliver)

	// For passive offers, increment the quality threshold so we only cross
	// against offers with STRICTLY better quality (not equal)
	// Reference: rippled CreateOffer.cpp lines 362-364
	if passive {
		takerQuality = takerQuality.Increment()
	}

	// Now cap sendMax to available balance (AFTER threshold calculation)
	// Reference: rippled CreateOffer.cpp lines 367-368
	// Reference: rippled CreateOffer.cpp line 367-368
	availableBalance := ToEitherAmount(inStartBalance)
	if sendMax.Compare(availableBalance) > 0 {
		sendMax = availableBalance
	}

	// With tfSell, override deliver to MAX AFTER threshold and sendMax cap.
	// The taker wants to sell ALL their input even if they receive more than originally asked.
	// Reference: rippled CreateOffer.cpp lines 382-401 (after threshold at line 358, after cap at 367)
	// Note: In FlowCross, takerPays = the deliver currency (what gets delivered to the taker),
	// so we check takerPays.IsNative() to determine the deliver type (XRP vs IOU).
	if sell {
		if takerPays.IsNative() {
			// Reference: rippled STAmount::cMaxNative = 9000000000000000000
			deliver = NewXRPEitherAmount(9000000000000000000)
		} else {
			// Reference: rippled uses cMaxValue/2=4999999999999999, cMaxOffset=80
			deliver = NewIOUEitherAmount(tx.NewIssuedAmount(
				4999999999999999, 80,
				takerPays.Currency, takerPays.Issuer))
		}
	}

	// === Build proper strands using ToStrands ===
	// Reference: rippled flowCross() calls flow() with src=dst=taker, addDefaultPath=true
	// This builds complete strands with DirectSteps for IOU trust line transfers
	// and XRPEndpointSteps for XRP transfers, surrounding the BookStep.
	//
	// For IOU-IOU crossing, also add an XRP bridge path.
	// Reference: rippled CreateOffer.cpp lines 374-380
	var paths [][]PathStep
	if !takerPays.IsNative() && !takerGets.IsNative() {
		// XRP bridge path for IOU-to-IOU crossing
		paths = [][]PathStep{{
			{Currency: "XRP", Type: int(PathTypeCurrency)},
		}}
	}

	strands, strandResult := ToStrands(
		sandbox,
		takerAccount, // src (taker)
		takerAccount, // dst (taker - payment to self)
		takerPays,    // dstAmt (what we want to receive / deliver to self)
		&takerGets,   // srcAmt (what we're paying, for issue info)
		paths,        // explicit paths (XRP bridge for IOU-IOU)
		true,         // addDefaultPath
		true,         // offerCrossing - skip trust line checks, create lines on demand
		fix1781,      // fix1781 - gate XRP endpoint loop detection
	)

	if strandResult != tx.TesSUCCESS || len(strands) == 0 {
		// No valid strands - no crossing possible
		return FlowCrossResult{
			TakerGot:        zeroCrossAmount(takerPays),
			TakerPaid:       zeroCrossAmount(takerGets),
			TakerPaidNet:    zeroCrossAmount(takerGets),
			Sandbox:         sandbox,
			RemovableOffers: nil,
			Result:          tx.TecPATH_DRY,
		}
	}

	// Create AMMContext for offer crossing.
	// Reference: rippled Flow.cpp line 85: AMMContext ammContext(src, false);
	ammCtx := NewAMMContext(takerAccount, false)

	// Initialize AMM liquidity on BookSteps.
	// Reference: rippled BookStep constructor reads AMM SLE and creates AMMLiquidity.
	configureAMMOnBookSteps(sandbox, strands, ammCtx, parentCloseTime,
		fixAMMv1_1, fixAMMv1_2, fixAMMOverflowOffer)

	// Set multiPath after strands are built
	ammCtx.SetMultiPath(len(strands) > 1)

	// Post-process strands for offer crossing:
	// 1. Set quality limits on BookSteps (per-offer quality filtering)
	// 2. Enable offer crossing mode on DirectSteps (ignores trust line limits/quality,
	//    allows trust line creation during crossing)
	// Reference: rippled uses DirectIOfferCrossingStep instead of DirectIPaymentStep
	configureStrandsForOfferCrossing(strands, &takerQuality, parentCloseTime, fixReducedOffersV1, fixReducedOffersV2, fixRmSmallIncreasedQOffers)

	// For domain offers, set the domain on book steps so crossing uses the domain book directory.
	// Reference: rippled CreateOffer.cpp flowCross() passes domainID to flow()
	if len(domainID) > 0 && domainID[0] != nil {
		setDomainOnBookSteps(strands, domainID[0])
	}

	// Execute the flow
	// Reference: rippled flowCross passes partialPayment=!(txFlags & tfFillOrKill)
	// For now, always allow partial (FoK is handled by caller)
	result := Flow(sandbox, strands, deliver, true, &takerQuality, &sendMax, ammCtx, flowSortStrands)

	// Apply the flow sandbox changes to our root sandbox.
	// Reference: rippled CreateOffer.cpp line 711: psbFlow.apply(sb).
	// On parent-mismatch invariant violations rippled's outer try/catch
	// returns tecINTERNAL, so we mirror that result code here.
	if result.Sandbox != nil {
		if err := result.Sandbox.Apply(sandbox); err != nil {
			return FlowCrossResult{
				TakerGot:        ZeroXRPEitherAmount(),
				TakerPaid:       ZeroXRPEitherAmount(),
				TakerPaidNet:    ZeroXRPEitherAmount(),
				Sandbox:         sandbox,
				RemovableOffers: nil,
				Result:          tx.TecINTERNAL,
			}
		}
	}

	// Calculate GROSS and NET amounts for the taker's payment
	// result.In from Flow is the GROSS amount consumed from the taker
	// (what was debited from their balance, including transfer fees)
	// NET = divideRound(GROSS, rate, issue, true)
	// Reference: rippled CreateOffer.cpp line 458-463:
	//   nonGatewayAmountIn = divideRound(result.actualAmountIn, gatewayXferRate,
	//       takerAmount.in.issue(), true)
	takerPaidGross := result.In
	takerPaidNet := result.In

	if !takerGets.IsNative() {
		issuerID, err := state.DecodeAccountID(takerGets.Issuer)
		if err == nil && takerAccount != issuerID {
			transferRate := GetTransferRate(view, issuerID)
			if transferRate != QualityOne && transferRate > 0 {
				rateAmt := rateAsAmount(transferRate)
				netResult := state.DivRound(result.In.IOU, rateAmt, takerGets.Currency, takerGets.Issuer, true)
				takerPaidNet = NewIOUEitherAmount(netResult)
			}
		}
	}

	return FlowCrossResult{
		TakerGot:        result.Out,
		TakerPaid:       takerPaidGross,
		TakerPaidNet:    takerPaidNet,
		Sandbox:         sandbox,
		RemovableOffers: result.RemovableOffers,
		Result:          result.Result,
	}
}

// configureStrandsForOfferCrossing sets up strands for offer crossing by:
//  1. Setting quality limits on BookSteps in DIRECT strands (single BookStep)
//  2. Enabling offer crossing mode on DirectSteps (ignores trust line limits/quality,
//     allows trust line creation during crossing)
//
// Quality limits are NOT set on individual BookSteps in BRIDGED strands (2 BookSteps)
// because the taker's quality is computed from the overall offer (e.g., EUR/USD), but
// individual legs have different dimensions (EUR/XRP and XRP/USD). The combined quality
// of the bridge is what matters, not individual leg quality.
//
// Reference: rippled uses DirectIOfferCrossingStep + quality threshold on BookStep
func configureStrandsForOfferCrossing(strands []Strand, qualityLimit *Quality, parentCloseTime uint32, fixReducedOffersV1 bool, fixReducedOffersV2 bool, fixRmSmallIncreasedQOffers bool) {
	for _, strand := range strands {
		// Count BookSteps in the strand
		bookStepCount := 0
		for _, step := range strand {
			if _, ok := step.(*BookStep); ok {
				bookStepCount++
			}
		}

		for _, step := range strand {
			if bookStep, ok := step.(*BookStep); ok {
				// Only set per-BookStep quality limits for direct strands (1 BookStep).
				// Bridge strands (2 BookSteps) have incompatible quality dimensions.
				if bookStepCount == 1 {
					bookStep.qualityLimit = qualityLimit
				}
				bookStep.parentCloseTime = parentCloseTime
				bookStep.fixReducedOffersV1 = fixReducedOffersV1
				bookStep.fixReducedOffersV2 = fixReducedOffersV2
				bookStep.fixRmSmallIncreasedQOffers = fixRmSmallIncreasedQOffers
				// In offer crossing, the offer owner pays the transfer fee (not the path sender).
				// This makes DebtDirection() return Issues instead of Redeems, preventing the
				// following DirectStepI from incorrectly deducting the transfer rate from the
				// recipient's amount.
				// Reference: rippled BookOfferCrossingStep: ownerPaysTransferFee_ = true
				bookStep.ownerPaysTransferFee = true
			}
			if directStep, ok := step.(*DirectStepI); ok {
				directStep.offerCrossing = true
			}
		}
	}
}

// zeroCrossAmount returns a zero EitherAmount matching the type of the given amount
func zeroCrossAmount(amt tx.Amount) EitherAmount {
	if amt.IsNative() {
		return ZeroXRPEitherAmount()
	}
	return ZeroIOUEitherAmount(amt.Currency, amt.Issuer)
}

// GetTransferRate returns the transfer rate for an issuer account.
// Transfer rate is stored as a uint32 where QualityOne (1,000,000,000) means no fee.
// A value of 1,020,000,000 means a 2% transfer fee.
//
// Returns QualityOne if the issuer doesn't exist or has no transfer rate set.
func GetTransferRate(view tx.LedgerView, issuer [20]byte) uint32 {
	accountKey := keylet.Account(issuer)
	data, err := view.Read(accountKey)
	if err != nil || data == nil {
		return QualityOne
	}

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return QualityOne
	}

	if account.TransferRate == 0 {
		return QualityOne
	}
	return account.TransferRate
}

// GetTransferRateByAddress returns the transfer rate for an issuer given its address string.
// This is a convenience wrapper around GetTransferRate.
func GetTransferRateByAddress(view tx.LedgerView, issuerAddress string) uint32 {
	if issuerAddress == "" {
		return QualityOne
	}

	issuerID, err := state.DecodeAccountID(issuerAddress)
	if err != nil {
		return QualityOne
	}

	return GetTransferRate(view, issuerID)
}

// AccountFundsInSandbox returns account funds with BalanceHook applied.
// Matches rippled's accountFunds(psb, ...) in CreateOffer.cpp line 432.
// BalanceHook subtracts DeferredCredits so self-crossing round-trips report zero.
func AccountFundsInSandbox(sb *PaymentSandbox, accountID [20]byte, amount tx.Amount, fhZeroIfFrozen bool, reserveBase, reserveIncrement uint64) tx.Amount {
	rawBalance := tx.AccountFunds(sb, accountID, amount, fhZeroIfFrozen, reserveBase, reserveIncrement)

	if amount.IsNative() {
		return sb.BalanceHook(accountID, [20]byte{}, rawBalance)
	}

	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return rawBalance
	}
	return sb.BalanceHook(accountID, issuerID, rawBalance)
}

// rateAsAmount converts a uint32 transfer rate to an Amount, matching rippled's
// detail::as_amount(Rate) which creates STAmount(noIssue(), rate.value, -9, false).
// For example, Rate{1005000000} becomes an Amount with mantissa=1005000000, exponent=-9,
// which normalizes to mantissa=1005000000000000, exponent=-15 (representing 1.005).
// Reference: rippled Rate2.cpp detail::as_amount()
func rateAsAmount(rate uint32) tx.Amount {
	return state.NewIssuedAmountFromValue(int64(rate), -9, "", "")
}
