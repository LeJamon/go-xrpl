package payment

import (
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	tx "github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

// DirectStepI handles IOU transfers between two accounts via trust lines.
// This step does not use the order book - it directly transfers IOUs along
// existing trust lines between accounts.
//
// The step considers:
// - Trust line balance and limits
// - QualityIn/QualityOut settings on trust lines
// - Transfer fees when the source is issuing and previous step redeems
//
// In offer crossing mode (offerCrossing=true), this step behaves differently:
// - quality() always returns QUALITY_ONE (ignores trust line quality fields)
// - When isLast, maxFlow returns the full desired amount (ignores trust line limits)
// - rippleCredit creates trust lines automatically when they don't exist
// Reference: rippled DirectIOfferCrossingStep vs DirectIPaymentStep
//
// Based on rippled's DirectStepI implementation.
type DirectStepI struct {
	// src is the source account sending IOUs
	src [20]byte

	// dst is the destination account receiving IOUs
	dst [20]byte

	// currency is the currency code being transferred
	currency string

	// prevStep is the previous step in the strand (for transfer fee calculation)
	prevStep Step

	// isFirst indicates if this is the first step in the strand
	isFirst bool

	// isLast indicates if this is the last step in the strand
	isLast bool

	// offerCrossing indicates this step is used for offer crossing, not payment.
	// Reference: rippled DirectIOfferCrossingStep
	offerCrossing bool

	// cache holds the results from the last Rev() call
	cache *directCache
}

// directCache holds cached values from the reverse pass
type directCache struct {
	in         tx.Amount
	srcToDst   tx.Amount // Amount transferred from src to dst
	out        tx.Amount
	srcDebtDir DebtDirection
}

// NewDirectStepI creates a new DirectStepI for IOU transfers
func NewDirectStepI(src, dst [20]byte, currency string, prevStep Step, isFirst, isLast bool) *DirectStepI {
	return &DirectStepI{
		src:      src,
		dst:      dst,
		currency: currency,
		prevStep: prevStep,
		isFirst:  isFirst,
		isLast:   isLast,
		cache:    nil,
	}
}

// Rev calculates the input needed to produce the requested output
func (s *DirectStepI) Rev(
	sb *PaymentSandbox,
	afView *PaymentSandbox,
	ofrsToRm map[[32]byte]bool,
	out EitherAmount,
) (EitherAmount, EitherAmount) {
	s.cache = nil

	if out.IsNative {
		// Should never happen - DirectStepI only handles IOU
		return ZeroIOUEitherAmount(s.currency, ""), ZeroIOUEitherAmount(s.currency, "")
	}

	// Get maximum flow and debt direction
	var maxSrcToDst tx.Amount
	var srcDebtDir DebtDirection

	if s.offerCrossing && s.isLast {
		// Offer crossing last step: ignore trust line limits.
		// The issuer can issue any amount; trust line created by rippleCredit if needed.
		// Reference: rippled DirectIOfferCrossingStep::maxFlow() lines 384-403
		maxSrcToDst = out.IOU
		srcDebtDir = DebtDirectionIssues
	} else {
		maxSrcToDst, srcDebtDir = s.maxPaymentFlow(sb)
	}

	// Get qualities
	srcQOut, dstQIn := s.qualities(sb, srcDebtDir, StrandDirectionReverse)

	// Determine issuer for srcToDst
	var issuer string
	if Redeems(srcDebtDir) {
		issuer = sle.EncodeAccountIDSafe(s.dst)
	} else {
		issuer = sle.EncodeAccountIDSafe(s.src)
	}

	zeroAmt := tx.NewIssuedAmount(0, -100, s.currency, issuer)
	if maxSrcToDst.Compare(zeroAmt) <= 0 {
		// Dry - no liquidity
		s.cache = &directCache{
			in:         zeroAmt,
			srcToDst:   zeroAmt,
			out:        zeroAmt,
			srcDebtDir: srcDebtDir,
		}
		return ZeroIOUEitherAmount(s.currency, issuer), ZeroIOUEitherAmount(s.currency, issuer)
	}

	// Calculate srcToDst = out / dstQIn (round up)
	srcToDst := mulRatioAmount(out.IOU, QualityOne, dstQIn, true)


	if srcToDst.Compare(maxSrcToDst) <= 0 {
		// Non-limiting case
		in := mulRatioAmount(srcToDst, srcQOut, QualityOne, true)
		s.cache = &directCache{
			in:         in,
			srcToDst:   srcToDst,
			out:        out.IOU,
			srcDebtDir: srcDebtDir,
		}


		// Execute the credit
		_ = s.rippleCredit(sb, srcToDst, issuer)

		return NewIOUEitherAmount(in), out
	}

	// Limiting case
	in := mulRatioAmount(maxSrcToDst, srcQOut, QualityOne, true)
	actualOut := mulRatioAmount(maxSrcToDst, dstQIn, QualityOne, false)

	s.cache = &directCache{
		in:         in,
		srcToDst:   maxSrcToDst,
		out:        actualOut,
		srcDebtDir: srcDebtDir,
	}

	// Execute the credit
	_ = s.rippleCredit(sb, maxSrcToDst, issuer)

	return NewIOUEitherAmount(in), NewIOUEitherAmount(actualOut)
}

// Fwd executes the step with the given input
func (s *DirectStepI) Fwd(
	sb *PaymentSandbox,
	afView *PaymentSandbox,
	ofrsToRm map[[32]byte]bool,
	in EitherAmount,
) (EitherAmount, EitherAmount) {
	if s.cache == nil {
		return ZeroIOUEitherAmount(s.currency, ""), ZeroIOUEitherAmount(s.currency, "")
	}

	if in.IsNative {
		return ZeroIOUEitherAmount(s.currency, ""), ZeroIOUEitherAmount(s.currency, "")
	}

	// Get maximum flow and debt direction
	var maxSrcToDst tx.Amount
	var srcDebtDir DebtDirection

	if s.offerCrossing && s.isLast {
		// Offer crossing last step: ignore trust line limits.
		// Use cached srcToDst from reverse pass (not in.IOU).
		// Reference: rippled DirectIOfferCrossingStep::maxFlow() lines 384-403
		maxSrcToDst = s.cache.srcToDst
		srcDebtDir = DebtDirectionIssues
	} else {
		maxSrcToDst, srcDebtDir = s.maxPaymentFlow(sb)
	}

	// Get qualities
	srcQOut, dstQIn := s.qualities(sb, srcDebtDir, StrandDirectionForward)

	// Determine issuer
	var issuer string
	if Redeems(srcDebtDir) {
		issuer = sle.EncodeAccountIDSafe(s.dst)
	} else {
		issuer = sle.EncodeAccountIDSafe(s.src)
	}

	zeroAmt := tx.NewIssuedAmount(0, -100, s.currency, issuer)
	if maxSrcToDst.Compare(zeroAmt) <= 0 {
		// Dry
		s.cache = &directCache{
			in:         zeroAmt,
			srcToDst:   zeroAmt,
			out:        zeroAmt,
			srcDebtDir: srcDebtDir,
		}
		return ZeroIOUEitherAmount(s.currency, issuer), ZeroIOUEitherAmount(s.currency, issuer)
	}

	// Calculate srcToDst = in / srcQOut (round down)
	srcToDst := mulRatioAmount(in.IOU, QualityOne, srcQOut, false)


	if srcToDst.Compare(maxSrcToDst) <= 0 {
		// Non-limiting case
		out := mulRatioAmount(srcToDst, dstQIn, QualityOne, false)
		s.setCacheLimiting(in.IOU, srcToDst, out, srcDebtDir)


		// Execute the credit
		s.rippleCredit(sb, s.cache.srcToDst, issuer)

		return NewIOUEitherAmount(s.cache.in), NewIOUEitherAmount(s.cache.out)
	}

	// Limiting case
	actualIn := mulRatioAmount(maxSrcToDst, srcQOut, QualityOne, true)
	out := mulRatioAmount(maxSrcToDst, dstQIn, QualityOne, false)
	s.setCacheLimiting(actualIn, maxSrcToDst, out, srcDebtDir)

	// Execute the credit
	s.rippleCredit(sb, s.cache.srcToDst, issuer)

	return NewIOUEitherAmount(s.cache.in), NewIOUEitherAmount(s.cache.out)
}

// setCacheLimiting updates the cache, keeping minimum values to prevent
// the forward pass from delivering more than the reverse pass
func (s *DirectStepI) setCacheLimiting(fwdIn, fwdSrcToDst, fwdOut tx.Amount, srcDebtDir DebtDirection) {
	if s.cache == nil {
		s.cache = &directCache{
			in:         fwdIn,
			srcToDst:   fwdSrcToDst,
			out:        fwdOut,
			srcDebtDir: srcDebtDir,
		}
		return
	}

	s.cache.in = fwdIn
	if fwdSrcToDst.Compare(s.cache.srcToDst) < 0 {
		s.cache.srcToDst = fwdSrcToDst
	}
	if fwdOut.Compare(s.cache.out) < 0 {
		s.cache.out = fwdOut
	}
	s.cache.srcDebtDir = srcDebtDir
}

// CachedIn returns the input from the last Rev() call
func (s *DirectStepI) CachedIn() *EitherAmount {
	if s.cache == nil {
		return nil
	}
	result := NewIOUEitherAmount(s.cache.in)
	return &result
}

// CachedOut returns the output from the last Rev() call
func (s *DirectStepI) CachedOut() *EitherAmount {
	if s.cache == nil {
		return nil
	}
	result := NewIOUEitherAmount(s.cache.out)
	return &result
}

// DebtDirection returns whether this step redeems or issues
func (s *DirectStepI) DebtDirection(sb *PaymentSandbox, dir StrandDirection) DebtDirection {
	if dir == StrandDirectionForward && s.cache != nil {
		return s.cache.srcDebtDir
	}

	// Check src's balance toward dst
	srcOwed := s.accountHolds(sb)
	zeroAmt := tx.NewIssuedAmount(0, -100, s.currency, "")
	if srcOwed.Compare(zeroAmt) > 0 {
		return DebtDirectionRedeems
	}
	return DebtDirectionIssues
}

// QualityUpperBound returns the worst-case quality for this step
func (s *DirectStepI) QualityUpperBound(v *PaymentSandbox, prevStepDir DebtDirection) (*Quality, DebtDirection) {
	// Offer crossing: quality is always 1.0 (identity rate)
	// Reference: rippled DirectIOfferCrossingStep::quality() → Quality{STAmount::uRateOne}
	if s.offerCrossing {
		q := qualityFromFloat64(1.0)
		return &q, DebtDirectionIssues
	}

	srcDebtDir := s.DebtDirection(v, StrandDirectionForward)
	srcQOut, dstQIn := s.qualities(v, srcDebtDir, StrandDirectionForward)

	// Quality = srcQOut / dstQIn using precise STAmount division
	// Reference: rippled getRate(STAmount(iss, dstQIn), STAmount(iss, srcQOut))
	// getRate(out, in) = divide(in, out) = srcQOut / dstQIn
	srcQOutAmt := NewIOUEitherAmount(sle.NewIssuedAmountFromValue(int64(srcQOut), 0, "", ""))
	dstQInAmt := NewIOUEitherAmount(sle.NewIssuedAmountFromValue(int64(dstQIn), 0, "", ""))
	q := QualityFromAmounts(srcQOutAmt, dstQInAmt)

	return &q, srcDebtDir
}

// IsZero returns true if the amount is zero
func (s *DirectStepI) IsZero(amt EitherAmount) bool {
	if amt.IsNative {
		return true // Native is effectively zero for IOU step
	}
	return amt.IOU.IsZero()
}

// EqualIn returns true if input portions are equal
func (s *DirectStepI) EqualIn(a, b EitherAmount) bool {
	if a.IsNative != b.IsNative {
		return false
	}
	if a.IsNative {
		return a.XRP == b.XRP
	}
	return a.IOU.Compare(b.IOU) == 0
}

// EqualOut returns true if output portions are equal
func (s *DirectStepI) EqualOut(a, b EitherAmount) bool {
	return s.EqualIn(a, b)
}

// Inactive returns false - DirectStepI doesn't become inactive
func (s *DirectStepI) Inactive() bool {
	return false
}

// OffersUsed returns 0 - DirectStepI doesn't use offers
func (s *DirectStepI) OffersUsed() uint32 {
	return 0
}

// DirectStepAccts returns (src, dst) accounts
func (s *DirectStepI) DirectStepAccts() *[2][20]byte {
	return &[2][20]byte{s.src, s.dst}
}

// BookStepBook returns nil - this is not a book step
func (s *DirectStepI) BookStepBook() *Book {
	return nil
}

// LineQualityIn returns the QualityIn for the dst's trust line
func (s *DirectStepI) LineQualityIn(v *PaymentSandbox) uint32 {
	return s.quality(v, true) // true = QualityDirection::in
}

// ValidFwd validates that the step can correctly execute in forward
func (s *DirectStepI) ValidFwd(sb *PaymentSandbox, afView *PaymentSandbox, in EitherAmount) (bool, EitherAmount) {
	if s.cache == nil {
		return false, ZeroIOUEitherAmount(s.currency, "")
	}

	if in.IsNative {
		return false, ZeroIOUEitherAmount(s.currency, "")
	}

	maxSrcToDst, _ := s.maxPaymentFlow(sb)

	if maxSrcToDst.Compare(s.cache.srcToDst) < 0 {
		// Exceeded max
		return false, NewIOUEitherAmount(s.cache.out)
	}

	return true, NewIOUEitherAmount(s.cache.out)
}

// maxPaymentFlow returns the maximum amount that can flow and the debt direction
func (s *DirectStepI) maxPaymentFlow(sb *PaymentSandbox) (tx.Amount, DebtDirection) {
	srcOwed := s.accountHolds(sb)
	zeroAmt := tx.NewIssuedAmount(0, -100, s.currency, "")

	if srcOwed.Compare(zeroAmt) > 0 {
		// Source holds IOUs from dst - they can redeem up to their balance
		return srcOwed, DebtDirectionRedeems
	}

	// Source has issued IOUs to dst (srcOwed is negative or zero)
	// They can issue up to (credit limit - what they've already issued)
	creditLimit := s.creditLimit(sb)
	// srcOwed is negative, so creditLimit + srcOwed = creditLimit - |srcOwed|
	maxIssue, _ := creditLimit.Add(srcOwed)
	return maxIssue, DebtDirectionIssues
}

// qualities returns srcQOut and dstQIn based on debt direction
func (s *DirectStepI) qualities(sb *PaymentSandbox, srcDebtDir DebtDirection, dir StrandDirection) (uint32, uint32) {
	if Redeems(srcDebtDir) {
		return s.qualitiesSrcRedeems(sb)
	}
	return s.qualitiesSrcIssues(sb, dir)
}

// qualitiesSrcRedeems returns qualities when source redeems
func (s *DirectStepI) qualitiesSrcRedeems(sb *PaymentSandbox) (uint32, uint32) {
	if s.prevStep == nil {
		return QualityOne, QualityOne
	}

	prevStepQIn := s.prevStep.LineQualityIn(sb)
	srcQOut := s.quality(sb, false) // QualityDirection::out

	if prevStepQIn > srcQOut {
		srcQOut = prevStepQIn
	}
	return srcQOut, QualityOne
}

// qualitiesSrcIssues returns qualities when source issues
func (s *DirectStepI) qualitiesSrcIssues(sb *PaymentSandbox, dir StrandDirection) (uint32, uint32) {
	// Charge transfer rate when issuing and previous step redeems
	var srcQOut uint32 = QualityOne

	if s.prevStep != nil {
		prevDebtDir := s.prevStep.DebtDirection(sb, dir)
		if Redeems(prevDebtDir) {
			// Get transfer rate from src account
			srcQOut = s.transferRate(sb)
		}
	}

	dstQIn := s.quality(sb, true) // QualityDirection::in

	// If this is the last step, cap dstQIn at QUALITY_ONE
	if s.isLast && dstQIn > QualityOne {
		dstQIn = QualityOne
	}

	return srcQOut, dstQIn
}

// quality returns the quality setting from the trust line
// isIn: true for QualityIn (dst's perspective), false for QualityOut (src's perspective)
func (s *DirectStepI) quality(sb *PaymentSandbox, isIn bool) uint32 {
	// Offer crossing ignores trust line Quality fields.
	// Reference: rippled DirectIOfferCrossingStep::quality() lines 370-375
	if s.offerCrossing {
		return QualityOne
	}

	if s.src == s.dst {
		return QualityOne
	}

	// Read trust line
	trustLineKey := keylet.Line(s.dst, s.src, s.currency)
	data, err := sb.Read(trustLineKey)
	if err != nil || data == nil {
		return QualityOne
	}

	rs, err := sle.ParseRippleState(data)
	if err != nil {
		return QualityOne
	}

	if isIn {
		// QualityIn for dst
		if sle.CompareAccountIDs(s.dst, s.src) < 0 {
			if rs.LowQualityIn != 0 {
				return rs.LowQualityIn
			}
		} else {
			if rs.HighQualityIn != 0 {
				return rs.HighQualityIn
			}
		}
	} else {
		// QualityOut for src
		if sle.CompareAccountIDs(s.src, s.dst) < 0 {
			if rs.LowQualityOut != 0 {
				return rs.LowQualityOut
			}
		} else {
			if rs.HighQualityOut != 0 {
				return rs.HighQualityOut
			}
		}
	}

	return QualityOne
}

// accountHolds returns the balance src holds of dst's IOUs
func (s *DirectStepI) accountHolds(sb *PaymentSandbox) tx.Amount {
	trustLineKey := keylet.Line(s.src, s.dst, s.currency)
	data, err := sb.Read(trustLineKey)
	if err != nil || data == nil {
		return tx.NewIssuedAmount(0, -100, s.currency, "")
	}

	rs, err := sle.ParseRippleState(data)
	if err != nil {
		return tx.NewIssuedAmount(0, -100, s.currency, "")
	}

	// Balance is from low account's perspective (rippled convention):
	// Positive balance = LOW is OWED by HIGH (LOW has credit/can redeem from HIGH)
	// Negative balance = LOW OWES HIGH (LOW has debt to HIGH)
	balance := rs.Balance

	srcIsLow := sle.CompareAccountIDs(s.src, s.dst) < 0

	if srcIsLow {
		// src is low account
		return balance
	}
	// src is high account
	return balance.Negate()
}

// creditLimit returns the credit limit dst has extended to src
func (s *DirectStepI) creditLimit(sb *PaymentSandbox) tx.Amount {
	trustLineKey := keylet.Line(s.src, s.dst, s.currency)
	data, err := sb.Read(trustLineKey)
	if err != nil || data == nil {
		return tx.NewIssuedAmount(0, -100, s.currency, "")
	}

	rs, err := sle.ParseRippleState(data)
	if err != nil {
		return tx.NewIssuedAmount(0, -100, s.currency, "")
	}

	// Return dst's limit (what dst allows src to owe)
	if sle.CompareAccountIDs(s.dst, s.src) < 0 {
		// dst is low account
		return rs.LowLimit
	}
	// dst is high account
	return rs.HighLimit
}

// transferRate returns the transfer rate for src account
func (s *DirectStepI) transferRate(sb *PaymentSandbox) uint32 {
	accountKey := keylet.Account(s.src)
	data, err := sb.Read(accountKey)
	if err != nil || data == nil {
		return QualityOne
	}

	account, err := sle.ParseAccountRoot(data)
	if err != nil {
		return QualityOne
	}

	if account.TransferRate == 0 {
		return QualityOne
	}
	return account.TransferRate
}

// rippleCredit transfers IOUs from src to dst in the sandbox.
// If no trust line exists, creates one automatically.
// After updating, checks if the trust line should be deleted (zero balance, auto-created).
// Reference: rippled View.cpp rippleCreditIOU() lines 1635-1748
func (s *DirectStepI) rippleCredit(sb *PaymentSandbox, amount tx.Amount, issuer string) error {
	if amount.IsZero() {
		return nil
	}

	trustLineKey := keylet.Line(s.src, s.dst, s.currency)
	data, err := sb.Read(trustLineKey)
	if err != nil {
		return err
	}

	if data == nil {
		// Trust line doesn't exist - create one.
		// This happens during offer crossing when the taker receives IOUs
		// from an issuer without a pre-existing trust line.
		// Reference: rippled rippleCredit() → trustCreate() lines 1756-1782
		return s.trustCreate(sb, amount)
	}

	rs, err := sle.ParseRippleState(data)
	if err != nil {
		return err
	}

	// Compute sender's balance BEFORE update (from sender's perspective)
	// Reference: rippled rippleCreditIOU() line 1672-1673: if bSenderHigh, negate
	srcIsLow := sle.CompareAccountIDs(s.src, s.dst) < 0
	var saBefore tx.Amount
	if srcIsLow {
		saBefore = rs.Balance
	} else {
		saBefore = rs.Balance.Negate()
	}

	// CreditHook before balance update (matches rippled line 1675)
	sb.CreditHook(s.src, s.dst, amount, saBefore)

	// Update balance
	// Balance is from low account's perspective:
	// When src transfers to dst:
	// - If src is LOW: balance DECREASES (LOW pays HIGH)
	// - If src is HIGH: balance INCREASES (HIGH pays LOW)
	if srcIsLow {
		rs.Balance, _ = rs.Balance.Sub(amount)
	} else {
		rs.Balance, _ = rs.Balance.Add(amount)
	}

	// Compute sender's balance AFTER update
	var saBalance tx.Amount
	if srcIsLow {
		saBalance = rs.Balance
	} else {
		saBalance = rs.Balance.Negate()
	}

	// Check trust line deletion conditions
	// Reference: rippled rippleCreditIOU() lines 1688-1745
	bDelete := false
	uFlags := rs.Flags

	if saBefore.Signum() > 0 && saBalance.Signum() <= 0 {
		// Sender's balance went from positive to zero/negative
		var senderReserve, senderNoRipple, senderFreeze uint32
		var senderLimit tx.Amount
		var senderQualityIn, senderQualityOut uint32

		if srcIsLow {
			senderReserve = sle.LsfLowReserve
			senderNoRipple = sle.LsfLowNoRipple
			senderFreeze = sle.LsfLowFreeze
			senderLimit = rs.LowLimit
			senderQualityIn = rs.LowQualityIn
			senderQualityOut = rs.LowQualityOut
		} else {
			senderReserve = sle.LsfHighReserve
			senderNoRipple = sle.LsfHighNoRipple
			senderFreeze = sle.LsfHighFreeze
			senderLimit = rs.HighLimit
			senderQualityIn = rs.HighQualityIn
			senderQualityOut = rs.HighQualityOut
		}

		// Read sender's DefaultRipple flag
		senderDefaultRipple := false
		senderKey := keylet.Account(s.src)
		senderData, sErr := sb.Read(senderKey)
		if sErr == nil && senderData != nil {
			senderAcct, pErr := sle.ParseAccountRoot(senderData)
			if pErr == nil {
				senderDefaultRipple = (senderAcct.Flags & sle.LsfDefaultRipple) != 0
			}
		}

		hasNoRipple := (uFlags & senderNoRipple) != 0
		noRippleMatchesDefault := hasNoRipple != senderDefaultRipple

		if (uFlags&senderReserve) != 0 &&
			noRippleMatchesDefault &&
			(uFlags&senderFreeze) == 0 &&
			senderLimit.Signum() == 0 &&
			senderQualityIn == 0 &&
			senderQualityOut == 0 {

			// Clear sender's reserve flag and decrement OwnerCount
			// Reference: rippled lines 1716-1722
			rs.Flags &= ^senderReserve
			s.adjustOwnerCount(sb, s.src, -1)

			// Check final deletion condition
			// Reference: rippled lines 1725-1726
			var receiverReserve uint32
			if srcIsLow {
				receiverReserve = sle.LsfHighReserve
			} else {
				receiverReserve = sle.LsfLowReserve
			}
			bDelete = saBalance.Signum() == 0 && (uFlags&receiverReserve) == 0
		}
	}

	// Update PreviousTxnID and PreviousTxnLgrSeq
	txHash, ledgerSeq := sb.GetTransactionContext()
	if txHash != [32]byte{} {
		rs.PreviousTxnID = txHash
		rs.PreviousTxnLgrSeq = ledgerSeq
	}

	// Serialize — want to reflect balance even if deleting (for metadata)
	// Reference: rippled line 1734
	newData, err := sle.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	if bDelete {
		// Update first (for metadata), then delete
		sb.Update(trustLineKey, newData)

		// Determine low/high accounts for trustDelete
		var lowAccount, highAccount [20]byte
		if srcIsLow {
			lowAccount = s.src
			highAccount = s.dst
		} else {
			lowAccount = s.dst
			highAccount = s.src
		}
		return trustDeleteLine(sb, trustLineKey, rs, lowAccount, highAccount)
	}

	sb.Update(trustLineKey, newData)

	return nil
}

// trustDeleteLine removes a trust line from the ledger, including directory removal.
// Reference: rippled View.cpp trustDelete() lines 1534-1571
func trustDeleteLine(sb *PaymentSandbox, lineKey keylet.Keylet, rs *sle.RippleState, lowAccount, highAccount [20]byte) error {
	// Remove from low account's owner directory
	lowDirKey := keylet.OwnerDir(lowAccount)
	lowResult, err := sle.DirRemove(sb, lowDirKey, rs.LowNode, lineKey.Key, false)
	if err != nil {
		return err
	}
	if lowResult != nil {
		applyDirRemoveResultGeneric(sb, lowResult)
	}

	// Remove from high account's owner directory
	highDirKey := keylet.OwnerDir(highAccount)
	highResult, err := sle.DirRemove(sb, highDirKey, rs.HighNode, lineKey.Key, false)
	if err != nil {
		return err
	}
	if highResult != nil {
		applyDirRemoveResultGeneric(sb, highResult)
	}

	// Erase the trust line
	return sb.Erase(lineKey)
}

// applyDirRemoveResultGeneric applies directory removal changes to the sandbox.
// This is a standalone version of BookStep.applyDirRemoveResult.
func applyDirRemoveResultGeneric(sb *PaymentSandbox, result *sle.DirRemoveResult) {
	for _, mod := range result.ModifiedNodes {
		isBookDir := mod.NewState.TakerPaysCurrency != [20]byte{} || mod.NewState.TakerGetsCurrency != [20]byte{}
		data, err := sle.SerializeDirectoryNode(mod.NewState, isBookDir)
		if err != nil {
			continue
		}
		sb.Update(keylet.Keylet{Key: mod.Key}, data)
	}

	for _, del := range result.DeletedNodes {
		sb.Erase(keylet.Keylet{Key: del.Key})
	}
}

// trustCreate creates a new trust line between src and dst with the given balance.
// This is called by rippleCredit when no trust line exists (e.g., during offer crossing).
// Reference: rippled View.cpp trustCreate() lines 1329-1445
func (s *DirectStepI) trustCreate(sb *PaymentSandbox, amount tx.Amount) error {
	// Determine low and high accounts
	srcIsLow := sle.CompareAccountIDs(s.src, s.dst) < 0
	var lowAccountID, highAccountID [20]byte
	if srcIsLow {
		lowAccountID = s.src
		highAccountID = s.dst
	} else {
		lowAccountID = s.dst
		highAccountID = s.src
	}

	lowAccountStr := sle.EncodeAccountIDSafe(lowAccountID)
	highAccountStr := sle.EncodeAccountIDSafe(highAccountID)

	// Calculate the initial balance from low account's perspective
	// When src sends to dst:
	// - If src is LOW: LOW pays HIGH → balance decreases (negative)
	// - If src is HIGH: HIGH pays LOW → balance increases (positive)
	var balance tx.Amount
	if srcIsLow {
		balance = amount.Negate()
	} else {
		balance = amount
	}

	// Get transaction context
	txHash, ledgerSeq := sb.GetTransactionContext()

	// Check receiver account's DefaultRipple flag for NoRipple setting
	var noRipple bool
	dstAccountKey := keylet.Account(s.dst)
	dstAccountData, err := sb.Read(dstAccountKey)
	if err == nil && dstAccountData != nil {
		dstAccount, parseErr := sle.ParseAccountRoot(dstAccountData)
		if parseErr == nil {
			// NoRipple is the default unless DefaultRipple is set
			const lsfDefaultRipple = 0x00800000
			noRipple = (dstAccount.Flags & lsfDefaultRipple) == 0
		}
	}

	// Build the trust line flags
	var flags uint32
	// Set reserve flag for the receiver (dst) side
	if srcIsLow {
		// dst is HIGH
		if noRipple {
			flags |= sle.LsfHighNoRipple
		}
		flags |= sle.LsfHighReserve
	} else {
		// dst is LOW
		if noRipple {
			flags |= sle.LsfLowNoRipple
		}
		flags |= sle.LsfLowReserve
	}

	// Create the RippleState
	rs := &sle.RippleState{
		Balance:           tx.NewIssuedAmount(balance.IOU().Mantissa(), balance.IOU().Exponent(), s.currency, sle.AccountOneAddress),
		LowLimit:          tx.NewIssuedAmount(0, -100, s.currency, lowAccountStr),
		HighLimit:         tx.NewIssuedAmount(0, -100, s.currency, highAccountStr),
		Flags:             flags,
		LowNode:           0,
		HighNode:          0,
		PreviousTxnID:     txHash,
		PreviousTxnLgrSeq: ledgerSeq,
	}

	trustLineKey := keylet.Line(s.src, s.dst, s.currency)

	// Insert into LOW account's owner directory
	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDirResult, err := sle.DirInsert(sb, lowDirKey, trustLineKey.Key, func(dir *sle.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return err
	}

	// Insert into HIGH account's owner directory
	highDirKey := keylet.OwnerDir(highAccountID)
	highDirResult, err := sle.DirInsert(sb, highDirKey, trustLineKey.Key, func(dir *sle.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return err
	}

	// Set directory node hints
	rs.LowNode = lowDirResult.Page
	rs.HighNode = highDirResult.Page

	// Serialize and insert
	trustLineData, err := sle.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	if err := sb.Insert(trustLineKey, trustLineData); err != nil {
		return err
	}

	// Increment receiver's OwnerCount
	// Reference: rippled trustCreate() adjustOwnerCount for receiver
	return s.adjustOwnerCount(sb, s.dst, 1)
}

// adjustOwnerCount modifies an account's OwnerCount by delta.
func (s *DirectStepI) adjustOwnerCount(sb *PaymentSandbox, account [20]byte, delta int32) error {
	accountKey := keylet.Account(account)
	data, err := sb.Read(accountKey)
	if err != nil || data == nil {
		return err
	}

	acct, err := sle.ParseAccountRoot(data)
	if err != nil {
		return err
	}

	if delta > 0 {
		acct.OwnerCount += uint32(delta)
	} else if uint32(-delta) <= acct.OwnerCount {
		acct.OwnerCount -= uint32(-delta)
	}

	newData, err := sle.SerializeAccountRoot(acct)
	if err != nil {
		return err
	}

	sb.Update(accountKey, newData)
	return nil
}

// DirectStepSrcAcct returns the source account for NoRipple checking
// Reference: rippled Steps.h directStepSrcAcct()
func (s *DirectStepI) DirectStepSrcAcct() *[20]byte {
	return &s.src
}

// Check validates the DirectStepI before use
func (s *DirectStepI) Check(sb *PaymentSandbox) tx.Result {
	// Offer crossing: no trust line checks needed.
	// Trust lines are created on demand during crossing via rippleCredit.
	// Reference: rippled DirectIOfferCrossingStep::check() lines 462-470
	if s.offerCrossing {
		return tx.TesSUCCESS
	}

	// Check trust line exists
	trustLineKey := keylet.Line(s.src, s.dst, s.currency)
	exists, err := sb.Exists(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	if !exists {
		return tx.TerNO_LINE
	}

	// Check freeze status — skip for pure issue/redeem (single-step strand)
	// Reference: rippled DirectStep.cpp:906-912
	//   if (!(ctx.isLast && ctx.isFirst))
	//       checkFreeze(ctx.view, src_, dst_, currency_);
	if !(s.isFirst && s.isLast) {
		if result := checkFreeze(sb, s.src, s.dst, s.currency); result != tx.TesSUCCESS {
			return result
		}
	}

	// Check authorization
	// Reference: rippled DirectStep.cpp checkAuth()
	if result := checkAuth(sb, s.src, s.dst, s.currency); result != tx.TesSUCCESS {
		return result
	}

	return tx.TesSUCCESS
}

// checkAuth checks if the trust line is properly authorized when RequireAuth is set.
// Reference: rippled DirectStep.cpp check() - auth section (lines 420-430)
// Only checks if the SOURCE requires auth and has authorized the trust line.
// The auth flag checked is on the source's own side:
//   (src > dst) ? lsfHighAuth : lsfLowAuth
// Auth is only checked when the balance is zero.
func checkAuth(view *PaymentSandbox, src, dst [20]byte, currency string) tx.Result {
	// Read source account to check RequireAuth
	srcKey := keylet.Account(src)
	srcData, err := view.Read(srcKey)
	if err != nil || srcData == nil {
		return tx.TesSUCCESS
	}

	srcAccount, err := sle.ParseAccountRoot(srcData)
	if err != nil {
		return tx.TesSUCCESS
	}

	// Only check auth if source has RequireAuth
	if (srcAccount.Flags & sle.LsfRequireAuth) == 0 {
		return tx.TesSUCCESS
	}

	// Get the trust line
	trustLineKey := keylet.Line(src, dst, currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return tx.TerNO_LINE
	}

	rs, err := sle.ParseRippleState(data)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check the source's own auth flag
	// Reference: rippled DirectStep.cpp L420:
	//   auto const authField = (src_ > dst_) ? lsfHighAuth : lsfLowAuth;
	srcIsHigh := sle.CompareAccountIDs(src, dst) > 0
	var authFlag uint32
	if srcIsHigh {
		authFlag = sle.LsfHighAuth
	} else {
		authFlag = sle.LsfLowAuth
	}

	// Only block if auth flag is NOT set AND balance is zero
	// Reference: rippled DirectStep.cpp L422-430
	if (rs.Flags&authFlag) == 0 && rs.Balance.Signum() == 0 {
		return tx.TerNO_AUTH
	}

	return tx.TesSUCCESS
}

// checkFreeze checks if a trust line is frozen.
// Reference: rippled StepChecks.h checkFreeze()
// Returns terNO_LINE if the trust line is frozen, tesSUCCESS otherwise.
func checkFreeze(view *PaymentSandbox, src, dst [20]byte, currency string) tx.Result {
	// Get the trust line
	trustLineKey := keylet.Line(src, dst, currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return tx.TerNO_LINE
	}

	rs, err := sle.ParseRippleState(data)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Determine which account is low/high
	srcIsLow := sle.CompareAccountIDs(src, dst) < 0

	// Check individual freeze
	// If src is low, check if high (dst) has frozen the line
	// If src is high, check if low (dst) has frozen the line
	if srcIsLow {
		// dst is high, check if high has frozen
		if (rs.Flags & sle.LsfHighFreeze) != 0 {
			return tx.TerNO_LINE
		}
	} else {
		// dst is low, check if low has frozen
		if (rs.Flags & sle.LsfLowFreeze) != 0 {
			return tx.TerNO_LINE
		}
	}

	// Check global freeze on destination account only (not source)
	// Reference: rippled StepChecks.h:43-49 — only checks dst global freeze
	dstKey := keylet.Account(dst)
	dstData, _ := view.Read(dstKey)

	if dstData != nil {
		dstAccount, err := sle.ParseAccountRoot(dstData)
		if err == nil && (dstAccount.Flags&sle.LsfGlobalFreeze) != 0 {
			return tx.TerNO_LINE
		}
	}

	return tx.TesSUCCESS
}

// CheckWithPrevStep validates the DirectStepI with NoRipple checking against previous step.
// Reference: rippled DirectStep.cpp make_DirectStepI() lines 918-923
func (s *DirectStepI) CheckWithPrevStep(sb *PaymentSandbox, prevStep Step) tx.Result {
	// First do basic check
	if result := s.Check(sb); result != tx.TesSUCCESS {
		return result
	}

	// If there's a previous DirectStep, check NoRipple constraint
	// Reference: rippled DirectStep.cpp:918-923
	if prevStep != nil {
		if prevDirectStep, ok := prevStep.(*DirectStepI); ok {
			prevSrc := prevDirectStep.src
			result := checkNoRipple(sb, prevSrc, s.src, s.dst, s.currency)
			if result != tx.TesSUCCESS {
				return result
			}
		}
	}

	return tx.TesSUCCESS
}

// checkNoRipple checks if the middle account (cur) has NoRipple set on both sides.
// Reference: rippled StepChecks.h checkNoRipple()
func checkNoRipple(view *PaymentSandbox, prev, cur, next [20]byte, currency string) tx.Result {
	// Fetch the ripple lines into and out of this node
	sleInKey := keylet.Line(prev, cur, currency)
	sleOutKey := keylet.Line(cur, next, currency)

	sleInData, err := view.Read(sleInKey)
	if err != nil || sleInData == nil {
		return tx.TerNO_LINE
	}

	sleOutData, err := view.Read(sleOutKey)
	if err != nil || sleOutData == nil {
		return tx.TerNO_LINE
	}

	sleIn, err := sle.ParseRippleState(sleInData)
	if err != nil {
		return tx.TefINTERNAL
	}

	sleOut, err := sle.ParseRippleState(sleOutData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check NoRipple flags
	// Reference: rippled StepChecks.h:105-106
	// The flag to check depends on account ordering in the trust line
	curIsHighIn := sle.CompareAccountIDs(cur, prev) > 0
	curIsHighOut := sle.CompareAccountIDs(cur, next) > 0

	var noRippleIn, noRippleOut bool
	if curIsHighIn {
		noRippleIn = (sleIn.Flags & sle.LsfHighNoRipple) != 0
	} else {
		noRippleIn = (sleIn.Flags & sle.LsfLowNoRipple) != 0
	}

	if curIsHighOut {
		noRippleOut = (sleOut.Flags & sle.LsfHighNoRipple) != 0
	} else {
		noRippleOut = (sleOut.Flags & sle.LsfLowNoRipple) != 0
	}

	// If BOTH sides have NoRipple set, return terNO_RIPPLE
	if noRippleIn && noRippleOut {
		return tx.TerNO_RIPPLE
	}

	return tx.TesSUCCESS
}

// mulRatioAmount multiplies an Amount by num/den
func mulRatioAmount(amt tx.Amount, num, den uint32, roundUp bool) tx.Amount {
	return amt.MulRatio(num, den, roundUp)
}
