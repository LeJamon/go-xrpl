package payment

import (
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	tx "github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
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

	balance := s.xrpLiquid(sb)

	var result int64
	if s.isLast {
		// Destination: accept full requested amount
		result = out.XRP
	} else {
		// Source: limited by available balance
		if balance < out.XRP {
			result = balance
		} else {
			result = out.XRP
		}
	}

	// Execute the transfer.
	// When isLast=true (destination endpoint), accountSend may fail if the requested
	// amount exceeds the serializable range (e.g., sell-flag deliver = 9e18 drops).
	// In rippled, STAmount doesn't validate during arithmetic so this succeeds.
	// Our binary codec validates amounts <= MaxDrops (1e17).
	// For destination in Rev: the sandbox will be reset when the limiting step is found
	// (BookStep limits to actual order book depth), so the credit doesn't need to persist.
	// If accountSend fails, we still cache the result and return — the forward pass
	// will execute the actual credit with reasonable amounts.
	// Reference: rippled XRPEndpointStep::revImp calls accountSend regardless of isLast_.
	err := s.accountSend(sb, result)
	if err != nil && !s.isLast {
		// Source endpoint failure is real — can't debit from account
		return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
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

	balance := s.xrpLiquid(sb)

	var result int64
	if s.isLast {
		// Destination: accept full input
		result = in.XRP
	} else {
		// Source: limited by available balance
		if balance < in.XRP {
			result = balance
		} else {
			result = in.XRP
		}
	}

	// Execute the transfer
	err := s.accountSend(sb, result)
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
	q := qualityFromFloat64(1.0)
	return &q, s.DebtDirection(v, StrandDirectionForward)
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

	if in.XRP != *s.cache {
		// Input doesn't match cached value - this is a warning but not failure
	}

	return true, in
}

// xrpLiquid returns the available XRP balance for the account (balance - reserve)
func (s *XRPEndpointStep) xrpLiquid(sb *PaymentSandbox) int64 {
	accountKey := keylet.Account(s.account)
	data, err := sb.Read(accountKey)
	if err != nil || data == nil {
		return 0
	}

	accountRoot, err := sle.ParseAccountRoot(data)
	if err != nil {
		return 0
	}

	// Get reserve based on owner count
	// Use ownerCountHook to get adjusted owner count
	ownerCount := sb.OwnerCountHook(s.account, accountRoot.OwnerCount)

	// Apply reserveReduction (for offer crossing when trust line doesn't exist yet).
	// This is rippled's confineOwnerCount: adjusted = ownerCount + reserveReduction,
	// clamped to 0 on underflow.
	// Reference: rippled View.cpp confineOwnerCount() + xrpLiquid()
	adjustedOwnerCount := int32(ownerCount) + s.reserveReduction
	if adjustedOwnerCount < 0 {
		adjustedOwnerCount = 0
	}

	// Read reserve values from ledger's FeeSettings
	// Reference: rippled View.cpp xrpLiquid() reads reserves from fees keylet
	baseReserve, ownerReserve := GetLedgerReserves(sb)

	reserve := uint64(baseReserve) + uint64(adjustedOwnerCount)*uint64(ownerReserve)

	// Available = balance - reserve
	if accountRoot.Balance < reserve {
		return 0
	}
	available := int64(accountRoot.Balance - reserve)

	return available
}

// accountSend transfers XRP between accounts in the sandbox
func (s *XRPEndpointStep) accountSend(sb *PaymentSandbox, drops int64) error {
	if drops <= 0 {
		return nil // Nothing to send
	}

	var sender, receiver [20]byte
	var xrpAccount [20]byte // Zero = XRP pseudo-account

	if s.isLast {
		sender = xrpAccount
		receiver = s.account
	} else {
		sender = s.account
		receiver = xrpAccount
	}

	// If sender is not XRP pseudo-account, deduct from sender
	if sender != xrpAccount {
		senderKey := keylet.Account(sender)
		data, err := sb.Read(senderKey)
		if err != nil || data == nil {
			return err
		}

		senderRoot, err := sle.ParseAccountRoot(data)
		if err != nil {
			return err
		}

		senderRoot.Balance -= uint64(drops)

		// Serialize and update
		newData, err := sle.SerializeAccountRoot(senderRoot)
		if err != nil {
			return err
		}
		sb.Update(senderKey, newData)
	}

	// If receiver is not XRP pseudo-account, credit to receiver
	if receiver != xrpAccount {
		receiverKey := keylet.Account(receiver)
		data, err := sb.Read(receiverKey)
		if err != nil || data == nil {
			return err
		}

		receiverRoot, err := sle.ParseAccountRoot(data)
		if err != nil {
			return err
		}

		receiverRoot.Balance += uint64(drops)

		// Serialize and update
		newData, err := sle.SerializeAccountRoot(receiverRoot)
		if err != nil {
			return err
		}
		sb.Update(receiverKey, newData)
	}

	return nil
}

// Check validates the XRPEndpointStep before use
func (s *XRPEndpointStep) Check(sb *PaymentSandbox) tx.Result {
	// Check account exists
	accountKey := keylet.Account(s.account)
	exists, err := sb.Exists(accountKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	if !exists {
		return tx.TerNO_ACCOUNT
	}

	return tx.TesSUCCESS
}
