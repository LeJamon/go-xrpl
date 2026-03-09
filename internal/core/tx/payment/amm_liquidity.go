package payment

// AMMLiquidity provides AMM offers to BookStep.
// Offers are generated in two ways:
// - Multi-path: Fibonacci sequence sizing with limited iterations
// - Single-path: sized to match competing CLOB quality, or max offer
//
// Reference: rippled/src/xrpld/app/paths/AMMLiquidity.h and detail/AMMLiquidity.cpp

import (
	"github.com/LeJamon/goXRPLd/keylet"
	tx "github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

// AMMLiquidity generates synthetic AMM offers for BookStep consumption.
type AMMLiquidity struct {
	ammContext     *AMMContext
	ammAccountID   [20]byte
	tradingFee     uint16
	issueIn        Issue
	issueOut       Issue
	initialPoolIn  tx.Amount
	initialPoolOut tx.Amount

	// Amendment flags
	fixAMMv1_1          bool
	fixAMMv1_2          bool
	fixAMMOverflowOffer bool
}

// Fibonacci sequence for multi-path offer scaling.
// Reference: rippled AMMLiquidity.cpp generateFibSeqOffer()
var ammFibSequence = [AMMMaxIterations]uint32{
	1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 144, 233, 377, 610, 987,
	1597, 2584, 4181, 6765, 10946, 17711, 28657, 46368, 75025, 121393,
	196418, 317811, 514229, 832040, 1346269,
}

// InitialFibSeqPct = 5 / 20000 = 0.00025
// Reference: rippled AMMLiquidity.h: Number(5) / 20000
var ammInitialFibSeqPct = sle.NewIssuedAmountFromValue(25e13, -18, "", "") // 0.00025

// NewAMMLiquidity creates a new AMMLiquidity for an AMM pool.
func NewAMMLiquidity(
	view *PaymentSandbox,
	ammAccountID [20]byte,
	tradingFee uint16,
	issueIn, issueOut Issue,
	ammContext *AMMContext,
	fixAMMv1_1, fixAMMv1_2, fixAMMOverflowOffer bool,
) *AMMLiquidity {
	liq := &AMMLiquidity{
		ammContext:          ammContext,
		ammAccountID:        ammAccountID,
		tradingFee:          tradingFee,
		issueIn:             issueIn,
		issueOut:            issueOut,
		fixAMMv1_1:          fixAMMv1_1,
		fixAMMv1_2:          fixAMMv1_2,
		fixAMMOverflowOffer: fixAMMOverflowOffer,
	}
	// Fetch initial balances
	liq.initialPoolIn, liq.initialPoolOut = liq.fetchBalances(view)
	return liq
}

// fetchBalances reads current AMM pool balances from the ledger view.
func (l *AMMLiquidity) fetchBalances(view *PaymentSandbox) (tx.Amount, tx.Amount) {
	assetIn := ammAccountHoldsFromPayment(view, l.ammAccountID, l.issueIn)
	assetOut := ammAccountHoldsFromPayment(view, l.ammAccountID, l.issueOut)
	return assetIn, assetOut
}

// GetOffer generates an AMM offer. Returns nil if CLOB quality is better.
// Reference: rippled AMMLiquidity.cpp getOffer()
func (l *AMMLiquidity) GetOffer(view *PaymentSandbox, clobQuality *Quality) *AMMOffer {
	// Can't generate more offers if multi-path iteration limit reached
	if l.ammContext.MaxItersReached() {
		return nil
	}

	poolIn, poolOut := l.fetchBalances(view)

	// Frozen accounts
	if poolIn.IsZero() || poolOut.IsZero() {
		return nil
	}

	// Check if AMM's Spot Price Quality (SPQ) is worse than CLOB quality
	if clobQuality != nil {
		spotPriceQ := QualityFromAmounts(toEitherAmt(poolIn), toEitherAmt(poolOut))
		if spotPriceQ.WorseThan(*clobQuality) || spotPriceQ.Value == clobQuality.Value {
			return nil
		}
		if WithinRelativeDistance(spotPriceQ, *clobQuality, 1e-7) {
			return nil
		}
	}

	offer := l.generateOffer(view, poolIn, poolOut, clobQuality)
	if offer == nil {
		return nil
	}
	// Validate offer amounts are positive
	if offer.amountIn.Signum() <= 0 || offer.amountOut.Signum() <= 0 {
		return nil
	}
	return offer
}

// generateOffer creates the actual AMM offer based on path configuration.
func (l *AMMLiquidity) generateOffer(view *PaymentSandbox, poolIn, poolOut tx.Amount, clobQuality *Quality) *AMMOffer {
	if l.ammContext.MultiPath() {
		// Multi-path: Fibonacci sequence sizing
		offerIn, offerOut, ok := l.generateFibSeqOffer(poolIn, poolOut)
		if !ok {
			return nil
		}
		if clobQuality != nil {
			offerQ := QualityFromAmounts(toEitherAmt(offerIn), toEitherAmt(offerOut))
			if offerQ.WorseThan(*clobQuality) {
				return nil
			}
		}
		return NewAMMOffer(l, offerIn, offerOut, poolIn, poolOut)
	}

	if clobQuality == nil {
		// No CLOB to compare: return max offer
		return l.maxOffer(poolIn, poolOut)
	}

	// Single-path with CLOB quality: match spot price to CLOB quality
	offerIn, offerOut, ok := ChangeSpotPriceQuality(
		poolIn, poolOut, *clobQuality, l.tradingFee,
		l.fixAMMv1_1, l.issueOut.IsXRP(),
	)
	if ok {
		return NewAMMOffer(l, offerIn, offerOut, poolIn, poolOut)
	}

	// fixAMMv1_2 fallback: try maxOffer if quality beats CLOB
	if l.fixAMMv1_2 {
		maxOff := l.maxOffer(poolIn, poolOut)
		if maxOff != nil {
			maxQ := QualityFromAmounts(toEitherAmt(maxOff.amountIn), toEitherAmt(maxOff.amountOut))
			if maxQ.BetterThan(*clobQuality) {
				return maxOff
			}
		}
	}

	return nil
}

// generateFibSeqOffer generates an offer sized by Fibonacci sequence.
// Reference: rippled AMMLiquidity.cpp generateFibSeqOffer()
func (l *AMMLiquidity) generateFibSeqOffer(poolIn, poolOut tx.Amount) (tx.Amount, tx.Amount, bool) {
	// Initial offer: InitialFibSeqPct * initialPoolIn
	// Use toNumber to handle XRP*IOU multiplication, then convert back
	nInitPoolIn := toNumber(l.initialPoolIn)
	curIn := fromNumber(nInitPoolIn.Mul(ammInitialFibSeqPct, true), l.initialPoolIn) // round up
	curOut := SwapAssetIn(l.initialPoolIn, l.initialPoolOut, curIn, l.tradingFee, l.fixAMMv1_1)

	if l.ammContext.CurIters() == 0 {
		return curIn, curOut, true
	}

	// Scale by Fibonacci number
	idx := l.ammContext.CurIters() - 1
	if int(idx) >= len(ammFibSequence) {
		return tx.Amount{}, tx.Amount{}, false
	}
	fibNum := sle.NewIssuedAmountFromValue(int64(ammFibSequence[idx]), 0, "", "")
	// curOut may be IOU or XRP — use toNumber for multiplication, convert back
	nCurOut := toNumber(curOut)
	curOut = fromNumber(nCurOut.Mul(fibNum, false), poolOut) // round down

	// Check overflow: if curOut >= poolOut, can't generate offer
	if curOut.Compare(poolOut) >= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	curIn = SwapAssetOut(poolIn, poolOut, curOut, l.tradingFee, l.fixAMMv1_1)
	return curIn, curOut, true
}

// maxOffer generates the maximum possible AMM offer.
// Reference: rippled AMMLiquidity.cpp maxOffer()
func (l *AMMLiquidity) maxOffer(poolIn, poolOut tx.Amount) *AMMOffer {
	if !l.fixAMMOverflowOffer {
		// Pre-fix: takerPays = max, takerGets = swapIn(max)
		// Quality uses pool balances (spot price), NOT the max amounts.
		// Reference: rippled AMMLiquidity.cpp maxOffer() line 128-133
		maxIn := maxAmountLike(poolIn)
		maxOut := SwapAssetIn(poolIn, poolOut, maxIn, l.tradingFee, l.fixAMMv1_1)
		return NewAMMOfferWithBalanceQuality(l, maxIn, maxOut, poolIn, poolOut)
	}

	// Post-fix: takerGets = 99% * poolOut, takerPays = swapOut(takerGets)
	// Quality uses pool balances (spot price).
	// Reference: rippled AMMLiquidity.cpp maxOffer() line 140-144
	pct := sle.NewIssuedAmountFromValue(99e13, -15, "", "") // 0.99
	nPoolOut := toNumber(poolOut)
	maxOut := fromNumber(nPoolOut.Mul(pct, false), poolOut) // round down

	if maxOut.IsZero() || maxOut.Compare(poolOut) >= 0 {
		return nil
	}

	maxIn := SwapAssetOut(poolIn, poolOut, maxOut, l.tradingFee, l.fixAMMv1_1)
	return NewAMMOfferWithBalanceQuality(l, maxIn, maxOut, poolIn, poolOut)
}

// ammAccountHoldsFromPayment reads the AMM account's balance for a given issue
// from the PaymentSandbox. This mirrors rippled's ammAccountHolds().
// Returns zero if the trust line is frozen (global or individual freeze).
// Reference: rippled AMMUtils.cpp ammAccountHolds() lines 210-234
func ammAccountHoldsFromPayment(view *PaymentSandbox, ammAccountID [20]byte, issue Issue) tx.Amount {
	if issue.IsXRP() {
		// Read XRP balance from account root (stored as uint64 drops)
		accountData, err := view.Read(keylet.Account(ammAccountID))
		if err != nil || accountData == nil {
			return sle.NewXRPAmountFromInt(0)
		}
		acct, err := sle.ParseAccountRoot(accountData)
		if err != nil {
			return sle.NewXRPAmountFromInt(0)
		}
		return sle.NewXRPAmountFromInt(int64(acct.Balance))
	}

	zeroIOU := sle.NewIssuedAmountFromValue(0, -100, issue.Currency, sle.EncodeAccountIDSafe(issue.Issuer))

	// Read IOU balance from trust line
	trustKey := keylet.Line(ammAccountID, issue.Issuer, issue.Currency)
	data, err := view.Read(trustKey)
	if err != nil || data == nil {
		return zeroIOU
	}

	trustLine, err := sle.ParseRippleState(data)
	if err != nil {
		return zeroIOU
	}

	// Check if the trust line is frozen.
	// Reference: rippled AMMUtils.cpp ammAccountHolds() line 226:
	//   !isFrozen(view, ammAccountID, issue.currency, issue.account)
	// isFrozen checks global freeze on issuer AND individual freeze on trust line.
	if isTrustLineFrozenForAMM(view, ammAccountID, issue.Issuer, trustLine) {
		return zeroIOU
	}

	bal := trustLineBalanceFor(trustLine, ammAccountID)
	issuerStr := sle.EncodeAccountIDSafe(issue.Issuer)
	return sle.NewIssuedAmountFromValue(bal.Mantissa(), bal.Exponent(), issue.Currency, issuerStr)
}

// isTrustLineFrozenForAMM checks if a trust line is frozen for the AMM account.
// Checks both global freeze on the issuer and individual freeze on the trust line.
// Reference: rippled View.cpp isFrozen(view, account, currency, issuer)
func isTrustLineFrozenForAMM(view *PaymentSandbox, ammAccountID, issuerID [20]byte, trustLine *sle.RippleState) bool {
	// Check global freeze on the issuer
	issuerData, err := view.Read(keylet.Account(issuerID))
	if err == nil && issuerData != nil {
		issuerAcct, err := sle.ParseAccountRoot(issuerData)
		if err == nil && (issuerAcct.Flags&sle.LsfGlobalFreeze) != 0 {
			return true
		}
	}

	// Check individual freeze on the trust line.
	// The issuer's freeze flag is on their side of the trust line.
	// Reference: rippled View.cpp isFrozen():
	//   (issuer > account) ? lsfHighFreeze : lsfLowFreeze
	issuerIsHigh := sle.CompareAccountIDsForLine(issuerID, ammAccountID) > 0
	if issuerIsHigh {
		return (trustLine.Flags & sle.LsfHighFreeze) != 0
	}
	return (trustLine.Flags & sle.LsfLowFreeze) != 0
}

// trustLineBalanceFor extracts the balance from a trust line for a given account.
// If account is the low account, balance is used directly.
// If account is the high account, balance is negated.
func trustLineBalanceFor(tl *sle.RippleState, account [20]byte) tx.Amount {
	lowID, _ := sle.DecodeAccountID(tl.LowLimit.Issuer)
	if lowID == account {
		return tl.Balance
	}
	return tl.Balance.Negate()
}
