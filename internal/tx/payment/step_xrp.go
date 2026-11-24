package payment

import (
	"errors"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// XRPEndpointStep handles XRP as source (first step) or destination (last step).
// For XRP, input equals output (1:1 ratio, no conversion).
//
// When used as source (isLast=false):
//   - Limited by account's available XRP balance after reserve
//
// When used as destination (isLast=true):
//   - Accepts any amount (no limit from this step)
//
// Based on rippled's XRPEndpointStep implementation.
type XRPEndpointStep struct {
	// account is the XRP account (source or destination)
	account [20]byte

	// isLast indicates if this is the destination endpoint (true) or source (false)
	isLast bool

	// reserveReduction adjusts the owner count when computing available XRP.
	// For offer crossing, this is -1 when the trust line for the delivered
	// currency doesn't exist yet. The trust line will be created by the
	// crossing flow, so we anticipate its reserve impact.
	// Reference: rippled XRPEndpointOfferCrossingStep::computeReserveReduction
	reserveReduction int32

	// cache holds the result from the last Rev() call
	cache *int64
}

// NewXRPEndpointStep creates a new XRPEndpointStep for payments.
// Parameters:
//   - account: The account that sends/receives XRP
//   - isLast: true if destination (last step), false if source (first step)
func NewXRPEndpointStep(account [20]byte, isLast bool) *XRPEndpointStep {
	return &XRPEndpointStep{
		account:          account,
		isLast:           isLast,
		reserveReduction: 0,
		cache:            nil,
	}
}

// NewXRPEndpointStepForOfferCrossing creates a new XRPEndpointStep for offer crossing.
// It computes the reserve reduction: if this is the first step (source) and the
// trust line for the delivered currency doesn't exist yet, reserveReduction is -1.
// This allows the taker to use XRP that would otherwise be locked as reserve for
// the trust line that offer crossing will create.
//
// Reference: rippled XRPEndpointOfferCrossingStep::computeReserveReduction
func NewXRPEndpointStepForOfferCrossing(account [20]byte, isLast bool, isFirst bool, deliver Issue, view *PaymentSandbox) *XRPEndpointStep {
	reduction := int32(0)
	if isFirst && !deliver.IsXRP() {
		// Check if trust line for the delivered issue already exists
		lineKey := keylet.Line(account, deliver.Issuer, deliver.Currency)
		data, err := view.Read(lineKey)
		if err != nil || data == nil {
			// Trust line doesn't exist — reduce reserve by 1 owner count
			reduction = -1
		}
	}
	return &XRPEndpointStep{
		account:          account,
		isLast:           isLast,
		reserveReduction: reduction,
		cache:            nil,
	}
}

// Rev calculates the input needed to produce the requested output.
// For XRP endpoints, input == output.
// If source, limited by available balance.
func (s *XRPEndpointStep) Rev(
	sb *PaymentSandbox,
	afView *PaymentSandbox,
	ofrsToRm map[[32]byte]bool,
	out EitherAmount,
) (EitherAmount, EitherAmount) {
	if !out.IsNative {
		// Should never happen - XRP endpoint only handles XRP
		return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
	}

	// The account is read once for the liquidity check and the transfer;
	// nothing writes it in between.
	account, readErr := tx.ReadAccountRoot(sb, s.account)
	balance := s.liquidOf(sb, account)

	var result int64
	if s.isLast {
		// Destination: accept full requested amount
		result = out.XRP
	} else {
		// Source: limited by available balance
		result = min(balance, out.XRP)
	}

	// Execute the transfer. rippled's revImp credits regardless of isLast_ and
	// returns {0,0} on any non-tesSUCCESS accountSend, so a genuine failure must
	// dry the strand in both directions.
	//
	// The one accommodation is the destination endpoint when the credit pushes
	// its balance above the serializable range (e.g. a receive-max / sell-flag
	// deliver): rippled's STAmount tolerates such transient over-range values
	// during the reverse pass, but our binary codec does not. That credit never
	// has to persist — the limiting BookStep resets this sandbox and the forward
	// pass re-credits a bounded amount — so we treat that specific error as the
	// expected codec artifact (accountSend reports it via errXRPBalanceOutOfRange)
	// and cache the result as rippled would, while any other (genuine) credit
	// failure still dries the strand.
	err := s.accountSend(sb, result, account, readErr)
	if err != nil {
		if !s.isLast || !errors.Is(err, errXRPBalanceOutOfRange) {
			return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
		}
		sb.CreditHook([20]byte{}, s.account, tx.NewXRPAmount(result), tx.NewXRPAmount(-int64(account.Balance)))
	}

	s.cache = &result
	return NewXRPEitherAmount(result), NewXRPEitherAmount(result)
}

// Fwd executes the step with the given input and returns actual in/out.
// For XRP endpoints, input == output.
func (s *XRPEndpointStep) Fwd(
	sb *PaymentSandbox,
	afView *PaymentSandbox,
	ofrsToRm map[[32]byte]bool,
	in EitherAmount,
) (EitherAmount, EitherAmount) {
	if !in.IsNative {
		return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
	}

	account, readErr := tx.ReadAccountRoot(sb, s.account)
	balance := s.liquidOf(sb, account)

	var result int64
	if s.isLast {
		// Destination: accept full input
		result = in.XRP
	} else {
		// Source: limited by available balance
		result = min(balance, in.XRP)
	}

	// Execute the transfer
	err := s.accountSend(sb, result, account, readErr)
	if err != nil {
		return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
	}

	s.cache = &result
	return NewXRPEitherAmount(result), NewXRPEitherAmount(result)
}

// CachedIn returns the input amount from the last Rev() call
func (s *XRPEndpointStep) CachedIn() *EitherAmount {
	if s.cache == nil {
		return nil
	}
	result := NewXRPEitherAmount(*s.cache)
	return &result
}

// CachedOut returns the output amount from the last Rev() call
func (s *XRPEndpointStep) CachedOut() *EitherAmount {
	// For XRP endpoint, cached in == cached out
	return s.CachedIn()
}

// DebtDirection returns the debt direction for this step.
// XRP endpoints always "issue" from the ledger's perspective.
func (s *XRPEndpointStep) DebtDirection(sb *PaymentSandbox, dir StrandDirection) DebtDirection {
	return DebtDirectionIssues
}

// QualityUpperBound returns the worst-case quality for this step.
// XRP endpoint has identity quality (1:1 rate).
// Reference: rippled XRPEndpointStep::qualityUpperBound → QUALITY_ONE
func (s *XRPEndpointStep) QualityUpperBound(v *PaymentSandbox, prevStepDir DebtDirection) (*Quality, DebtDirection) {
	q := qualityOne
	return &q, s.DebtDirection(v, StrandDirectionForward)
}

// GetQualityFunc returns the QualityFunction for this step.
// XRP endpoint uses the default implementation: CLOB-like QF from QualityUpperBound.
// Reference: rippled Steps.h Step::getQualityFunc() default implementation
func (s *XRPEndpointStep) GetQualityFunc(v *PaymentSandbox, prevStepDir DebtDirection) (*QualityFunction, DebtDirection) {
	q, dir := s.QualityUpperBound(v, prevStepDir)
	if q == nil {
		return nil, dir
	}
	return newCLOBLikeQualityFunction(numberMath{ctx: v.NumberContext()}, *q), dir
}

// IsZero returns true if the amount is zero
func (s *XRPEndpointStep) IsZero(amt EitherAmount) bool {
	if !amt.IsNative {
		return true // Non-XRP is effectively zero for XRP step
	}
	return amt.XRP == 0
}

// EqualIn returns true if the input portions are equal
func (s *XRPEndpointStep) EqualIn(a, b EitherAmount) bool {
	if a.IsNative != b.IsNative {
		return false
	}
	if a.IsNative {
		return a.XRP == b.XRP
	}
	return a.IOU.Compare(b.IOU) == 0
}

// EqualOut returns true if the output portions are equal
func (s *XRPEndpointStep) EqualOut(a, b EitherAmount) bool {
	return s.EqualIn(a, b)
}

// Inactive returns false - XRP endpoints don't become inactive
func (s *XRPEndpointStep) Inactive() bool {
	return false
}

// OffersUsed returns 0 - XRP endpoints don't use offers
func (s *XRPEndpointStep) OffersUsed() uint32 {
	return 0
}

// DirectStepAccts returns the (src, dst) accounts for this step
func (s *XRPEndpointStep) DirectStepAccts() *[2][20]byte {
	var xrpAccount [20]byte // Zero account represents XRP
	var result [2][20]byte
	if s.isLast {
		result[0] = xrpAccount // src is XRP pseudo-account
		result[1] = s.account  // dst is the actual account
	} else {
		result[0] = s.account  // src is the actual account
		result[1] = xrpAccount // dst is XRP pseudo-account
	}
	return &result
}

// BookStepBook returns nil - this is not a book step
func (s *XRPEndpointStep) BookStepBook() *Book {
	return nil
}

// LineQualityIn returns QualityOne for XRP (no quality adjustment)
func (s *XRPEndpointStep) LineQualityIn(v *PaymentSandbox) uint32 {
	return QualityOne
}

// ValidFwd validates that the step can correctly execute in forward direction
func (s *XRPEndpointStep) ValidFwd(sb *PaymentSandbox, afView *PaymentSandbox, in EitherAmount) (bool, EitherAmount) {
	if s.cache == nil {
		return false, ZeroXRPEitherAmount()
	}

	if !in.IsNative {
		return false, ZeroXRPEitherAmount()
	}

	balance := s.xrpLiquid(sb)

	if !s.isLast && balance < in.XRP {
		// Source has insufficient balance
		return false, NewXRPEitherAmount(balance)
	}

	// A mismatch between in.XRP and the cached value is a warning in rippled but
	// not a failure; the strand still validates with the supplied input.

	return true, in
}

// xrpLiquid returns the available XRP balance for the account (balance - reserve)
func (s *XRPEndpointStep) xrpLiquid(sb *PaymentSandbox) int64 {
	accountRoot, _ := tx.ReadAccountRoot(sb, s.account)
	return s.liquidOf(sb, accountRoot)
}

// liquidOf is xrpLiquid for the step account's AccountRoot as already read
// from sb, nil when absent or unreadable. accountRoot is not modified.
func (s *XRPEndpointStep) liquidOf(sb *PaymentSandbox, accountRoot *state.AccountRoot) int64 {
	if accountRoot == nil {
		return 0
	}

	counts := sb.OwnerCountsHook(s.account, tx.OwnerCounts{
		Owner: accountRoot.OwnerCount, Sponsored: accountRoot.SponsoredOwnerCount,
		Sponsoring: accountRoot.SponsoringOwnerCount,
	})
	ownerCount := counts.Owner

	// Apply reserveReduction (for offer crossing when trust line doesn't exist yet).
	// This is rippled's confineOwnerCount: adjusted = ownerCount + reserveReduction,
	// clamped to 0 on underflow.
	// Reference: rippled View.cpp confineOwnerCount() + xrpLiquid()
	adjustedOwnerCount := tx.ConfineOwnerCount(ownerCount, int(s.reserveReduction))

	// Pseudo-accounts have no reserve requirement.
	// Reference: rippled View.cpp xrpLiquid() lines 631-633.
	var reserve uint64
	if !accountRoot.IsPseudoAccount() {
		baseReserve, ownerReserve := GetLedgerReserves(sb)
		accountWithHook := *accountRoot
		accountWithHook.OwnerCount = counts.Owner
		accountWithHook.SponsoredOwnerCount = counts.Sponsored
		accountWithHook.SponsoringOwnerCount = counts.Sponsoring
		reserve, _ = tx.AccountReserveForView(sb, tx.EngineConfig{
			ReserveBase: uint64(baseReserve), ReserveIncrement: uint64(ownerReserve),
		}, &accountWithHook, adjustedOwnerCount)
	}

	// Apply the deferred-credit balance hook to the full balance. XRP credited to
	// this account earlier in the same payment must not be counted as spendable
	// liquidity, otherwise the same drops could be moved twice through the strand.
	// Reference: rippled View.cpp xrpLiquid() line 637:
	//   balance = view.balanceHook(id, xrpAccount(), fullBalance)
	fullBalance := tx.NewXRPAmount(int64(accountRoot.Balance))
	balance := sb.BalanceHook(s.account, [20]byte{}, fullBalance).Drops()
	if balance < 0 {
		balance = 0
	}

	if uint64(balance) < reserve {
		return 0
	}
	return balance - int64(reserve)
}

// accountSend transfers XRP between accounts in the sandbox. account is the
// step account's AccountRoot as read from sb, nil when absent or when the read
// failed with readErr; accountSend takes ownership of it.
func (s *XRPEndpointStep) accountSend(sb *PaymentSandbox, amount int64, account *state.AccountRoot, readErr error) error {
	if amount <= 0 || s.account == ([20]byte{}) {
		return nil
	}
	if account == nil {
		return readErr
	}

	var sender, receiver [20]byte
	preCreditBalance := int64(account.Balance)
	if s.isLast {
		receiver = s.account
		preCreditBalance = -preCreditBalance
		if account.Balance+uint64(amount) > uint64(drops.MaxDrops) {
			return errXRPBalanceOutOfRange
		}
		account.Balance += uint64(amount)
	} else {
		sender = s.account
		if account.Balance < uint64(amount) {
			sb.markFundsFailure()
			return errInsufficientFunds
		}
		account.Balance -= uint64(amount)
	}

	data, err := state.SerializeAccountRoot(account)
	if err != nil {
		return err
	}
	if err := sb.Update(keylet.Account(s.account), data); err != nil {
		return err
	}
	sb.CreditHook(sender, receiver, tx.NewXRPAmount(amount), tx.NewXRPAmount(preCreditBalance))
	return nil
}

// Check validates the XRPEndpointStep before use
func (s *XRPEndpointStep) Check(sb *PaymentSandbox) ter.Result {
	var xrpAccount [20]byte
	if s.account == xrpAccount {
		return ter.TemBAD_PATH
	}

	// Check account exists
	accountKey := keylet.Account(s.account)
	exists, err := sb.Exists(accountKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if !exists {
		return ter.TerNO_ACCOUNT
	}

	src, dst := s.account, xrpAccount
	if s.isLast {
		src, dst = xrpAccount, s.account
	}
	dstAccount, _ := tx.ReadAccountRoot(sb, dst)
	if result := checkFreeze(sb, src, dst, "XRP", dstAccount); result != ter.TesSUCCESS {
		return result
	}

	return ter.TesSUCCESS
}
