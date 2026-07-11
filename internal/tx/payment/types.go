package payment

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// DebtDirection indicates whether a step is issuing or redeeming currency
type DebtDirection int

const (
	// DebtDirectionIssues means the step is creating new debt (issuing)
	DebtDirectionIssues DebtDirection = iota
	// DebtDirectionRedeems means the step is reducing existing debt (redeeming)
	DebtDirectionRedeems
)

func Redeems(dir DebtDirection) bool {
	return dir == DebtDirectionRedeems
}

func Issues(dir DebtDirection) bool {
	return dir == DebtDirectionIssues
}

// StrandDirection indicates the direction of flow through a strand
type StrandDirection int

const (
	// StrandDirectionForward means executing from source to destination
	StrandDirectionForward StrandDirection = iota
	// StrandDirectionReverse means calculating from destination back to source
	StrandDirectionReverse
)

// Issue identifies an XRP, issued-currency, or MPT asset.
type Issue struct {
	Currency string
	Issuer   [20]byte
	MPTID    [24]byte
	IsMPT    bool
}

func (i Issue) IsXRP() bool {
	return !i.IsMPT && (i.Currency == "XRP" || i.Currency == "")
}

// IsConsistent reports whether the issue's currency and issuer agree on XRP-ness:
// XRP must carry the zero issuer and a non-XRP currency must carry a non-zero
// issuer. A bare-currency path element that resolves to a zero issuer yields an
// inconsistent (uncrossable) book. Mirrors rippled isConsistent(Issue).
func (i Issue) IsConsistent() bool {
	if i.IsMPT {
		return i.MPTID != [24]byte{} && i.Issuer != [20]byte{} &&
			i.Issuer == mptIssuer(i.MPTID)
	}
	return i.IsXRP() == (i.Issuer == [20]byte{})
}

func NewMPTIssue(id [24]byte) Issue {
	return Issue{MPTID: id, Issuer: mptIssuer(id), IsMPT: true}
}

func (i Issue) Equal(other Issue) bool {
	if i.IsMPT || other.IsMPT {
		return i.IsMPT && other.IsMPT && i.MPTID == other.MPTID
	}
	if i.IsXRP() && other.IsXRP() {
		return true
	}
	return i.Currency == other.Currency && i.Issuer == other.Issuer
}

// Book represents an order book (input/output issue pair)
type Book struct {
	In       Issue
	Out      Issue
	DomainID *[32]byte // nil for open market, non-nil for permissioned domain book
}

// Strand is a sequence of Steps forming a complete payment path
type Strand []Step

// Step is a single unit of payment flow in a strand.
// Steps transform amounts from one currency/account to another.
type Step interface {
	Rev(sb *PaymentSandbox, afView *PaymentSandbox, ofrsToRm map[[32]byte]bool, out EitherAmount) (EitherAmount, EitherAmount)
	Fwd(sb *PaymentSandbox, afView *PaymentSandbox, ofrsToRm map[[32]byte]bool, in EitherAmount) (EitherAmount, EitherAmount)
	CachedIn() *EitherAmount
	CachedOut() *EitherAmount
	DebtDirection(sb *PaymentSandbox, dir StrandDirection) DebtDirection
	QualityUpperBound(v *PaymentSandbox, prevStepDir DebtDirection) (*Quality, DebtDirection)
	// GetQualityFunc returns the QualityFunction for this step.
	// Used in one-path optimization where the quality function is non-constant
	// (has AMM) and there is a limitQuality. The QualityFunction allows
	// calculation of required path output given requested limitQuality.
	// The default implementation creates a CLOB-like QF from QualityUpperBound.
	// Reference: rippled Steps.h Step::getQualityFunc()
	GetQualityFunc(v *PaymentSandbox, prevStepDir DebtDirection) (*QualityFunction, DebtDirection)
	IsZero(amt EitherAmount) bool
	EqualIn(a, b EitherAmount) bool
	EqualOut(a, b EitherAmount) bool
	Inactive() bool
	OffersUsed() uint32
	DirectStepAccts() *[2][20]byte
	BookStepBook() *Book
	LineQualityIn(v *PaymentSandbox) uint32
	ValidFwd(sb *PaymentSandbox, afView *PaymentSandbox, in EitherAmount) (bool, EitherAmount)
}

// StrandResult captures the outcome of executing a single strand
type StrandResult struct {
	Success    bool
	In         EitherAmount
	Out        EitherAmount
	Sandbox    *PaymentSandbox
	OffsToRm   map[[32]byte]bool
	OffersUsed uint32
	Inactive   bool
}

// FlowResult captures the overall result of payment flow execution
type FlowResult struct {
	In              EitherAmount
	Out             EitherAmount
	Sandbox         *PaymentSandbox
	RemovableOffers map[[32]byte]bool
	Result          ter.Result
}

func GetIssue(amt tx.Amount) Issue {
	if amt.IsNative() {
		return Issue{Currency: "XRP"}
	}
	if amt.IsMPT() {
		if id, ok := decodeMPTID(amt.MPTIssuanceID()); ok {
			return NewMPTIssue(id)
		}
		return Issue{IsMPT: true}
	}

	var issuerBytes [20]byte
	if issuerID, err := state.DecodeAccountID(amt.Issuer); err == nil {
		issuerBytes = issuerID
	}

	return Issue{
		Currency: amt.Currency,
		Issuer:   issuerBytes,
	}
}

func decodeMPTID(value string) ([24]byte, bool) {
	var id [24]byte
	b, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(b) != len(id) {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

func mptIssuer(id [24]byte) [20]byte {
	var issuer [20]byte
	copy(issuer[:], id[4:])
	return issuer
}

func newMPTAmount(value int64, id [24]byte) tx.Amount {
	return state.NewMPTAmountWithIssuanceID(
		value,
		state.EncodeAccountIDSafe(mptIssuer(id)),
		strings.ToUpper(hex.EncodeToString(id[:])),
	)
}
