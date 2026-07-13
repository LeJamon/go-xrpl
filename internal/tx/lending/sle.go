package lending

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
)

// The lending ledger objects carry their NUMBER fields (DebtTotal, CoverAvailable,
// PrincipalOutstanding, ...) as the canonical decimal/scientific string the binary
// codec round-trips; "" means the field is absent (soeDEFAULT zero). This mirrors
// the vault package's local Number seam.

// lendNum parses a NUMBER field string into a large-scale XRPLNumber (the scale a
// lending transaction context installs). "" / "0" decode to zero.
func lendNum(s string) lmath.N {
	if s == "" || s == "0" {
		return lmath.Zero()
	}
	num := &types.Number{}
	b, err := num.FromJSON(s)
	if err != nil {
		return lmath.Zero()
	}
	mantissa := int64(binary.BigEndian.Uint64(b[:8]))
	exp := int32(binary.BigEndian.Uint32(b[8:12]))
	return lmath.Num(mantissa, int(exp))
}

// numStr renders a large-scale XRPLNumber into the NUMBER-field convention: "" for
// zero, else a scientific string the codec re-normalizes to the identical value.
func numStr(n lmath.N) string {
	if n.IsZero() {
		return ""
	}
	return fmt.Sprintf("%de%d", n.Mantissa(), n.Exponent())
}

// loanBrokerData is the parsed form of an ltLOAN_BROKER ledger entry.
type loanBrokerData struct {
	Sequence             uint32
	OwnerNode            uint64
	VaultNode            uint64
	VaultID              [32]byte
	Account              [20]byte // pseudo-account
	Owner                [20]byte
	LoanSequence         uint32
	Data                 string // hex Blob
	ManagementFeeRate    uint16
	OwnerCount           uint32
	DebtTotal            string // NUMBER
	DebtMaximum          string // NUMBER
	CoverAvailable       string // NUMBER
	CoverRateMinimum     uint32
	CoverRateLiquidation uint32
	Flags                uint32
	PreviousTxnID        [32]byte
	PreviousTxnLgrSeq    uint32
}

// serializeLoanBroker encodes a LoanBroker entry to canonical binary.
func serializeLoanBroker(b *loanBrokerData) ([]byte, error) {
	ownerAddr, err := state.EncodeAccountID(b.Owner)
	if err != nil {
		return nil, fmt.Errorf("encode owner: %w", err)
	}
	pseudoAddr, err := state.EncodeAccountID(b.Account)
	if err != nil {
		return nil, fmt.Errorf("encode account: %w", err)
	}
	entry := &ledgerfields.LoanBroker{}
	entry.SetFlags(b.Flags)
	entry.SetSequence(b.Sequence)
	entry.SetOwnerNode(fmt.Sprintf("%X", b.OwnerNode))
	entry.SetVaultNode(fmt.Sprintf("%X", b.VaultNode))
	entry.SetVaultID(strings.ToUpper(hex.EncodeToString(b.VaultID[:])))
	entry.SetAccount(pseudoAddr)
	entry.SetOwner(ownerAddr)
	entry.SetLoanSequence(b.LoanSequence)
	entry.SetData(strings.ToUpper(b.Data))
	entry.SetManagementFeeRate(int(b.ManagementFeeRate))
	entry.SetOwnerCount(b.OwnerCount)
	entry.SetDebtTotal(wireNum(b.DebtTotal))
	entry.SetDebtMaximum(wireNum(b.DebtMaximum))
	entry.SetCoverAvailable(wireNum(b.CoverAvailable))
	entry.SetCoverRateMinimum(b.CoverRateMinimum)
	entry.SetCoverRateLiquidation(b.CoverRateLiquidation)
	var zeroHash [32]byte
	if b.PreviousTxnID != zeroHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(b.PreviousTxnID[:])))
		entry.SetPreviousTxnLgrSeq(b.PreviousTxnLgrSeq)
	}
	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode loan broker: %w", err)
	}
	return data, nil
}

// parseLoanBroker decodes a LoanBroker entry via the ledgerfields decoder.
func parseLoanBroker(data []byte) (*loanBrokerData, error) {
	lb := &ledgerfields.LoanBroker{}
	if err := lb.Decode(data); err != nil {
		return nil, err
	}
	b := &loanBrokerData{
		Sequence:             lb.Sequence,
		LoanSequence:         lb.LoanSequence,
		Data:                 lb.Data,
		ManagementFeeRate:    uint16(lb.ManagementFeeRate),
		OwnerCount:           lb.OwnerCount,
		DebtTotal:            normNum(lb.DebtTotal),
		DebtMaximum:          normNum(lb.DebtMaximum),
		CoverAvailable:       normNum(lb.CoverAvailable),
		CoverRateMinimum:     lb.CoverRateMinimum,
		CoverRateLiquidation: lb.CoverRateLiquidation,
		Flags:                lb.Flags,
		PreviousTxnLgrSeq:    lb.PreviousTxnLgrSeq,
	}
	if id, err := state.DecodeAccountID(lb.Account); err == nil {
		b.Account = id
	}
	if id, err := state.DecodeAccountID(lb.Owner); err == nil {
		b.Owner = id
	}
	b.VaultID = hash256(lb.VaultID)
	b.PreviousTxnID = hash256(lb.PreviousTxnID)
	b.OwnerNode = hexU64(lb.OwnerNode)
	b.VaultNode = hexU64(lb.VaultNode)
	return b, nil
}

// loanData is the parsed form of an ltLOAN ledger entry.
type loanData struct {
	OwnerNode                uint64
	LoanBrokerNode           uint64
	LoanBrokerID             [32]byte
	LoanSequence             uint32
	Borrower                 [20]byte
	LoanOriginationFee       string // NUMBER
	LoanServiceFee           string // NUMBER
	LatePaymentFee           string // NUMBER
	ClosePaymentFee          string // NUMBER
	OverpaymentFee           uint32
	InterestRate             uint32
	LateInterestRate         uint32
	CloseInterestRate        uint32
	OverpaymentInterestRate  uint32
	StartDate                uint32
	PaymentInterval          uint32
	GracePeriod              uint32
	PreviousPaymentDueDate   uint32
	NextPaymentDueDate       uint32
	PaymentRemaining         uint32
	PeriodicPayment          string // NUMBER (soeREQUIRED)
	PrincipalOutstanding     string // NUMBER
	TotalValueOutstanding    string // NUMBER
	ManagementFeeOutstanding string // NUMBER
	LoanScale                int32
	Flags                    uint32
	PreviousTxnID            [32]byte
	PreviousTxnLgrSeq        uint32
}

// serializeLoan encodes a Loan entry to canonical binary.
func serializeLoan(l *loanData) ([]byte, error) {
	borrowerAddr, err := state.EncodeAccountID(l.Borrower)
	if err != nil {
		return nil, fmt.Errorf("encode borrower: %w", err)
	}
	entry := &ledgerfields.Loan{}
	entry.SetFlags(l.Flags)
	entry.SetOwnerNode(fmt.Sprintf("%X", l.OwnerNode))
	entry.SetLoanBrokerNode(fmt.Sprintf("%X", l.LoanBrokerNode))
	entry.SetLoanBrokerID(strings.ToUpper(hex.EncodeToString(l.LoanBrokerID[:])))
	entry.SetLoanSequence(l.LoanSequence)
	entry.SetBorrower(borrowerAddr)
	entry.SetLoanOriginationFee(wireNum(l.LoanOriginationFee))
	entry.SetLoanServiceFee(wireNum(l.LoanServiceFee))
	entry.SetLatePaymentFee(wireNum(l.LatePaymentFee))
	entry.SetClosePaymentFee(wireNum(l.ClosePaymentFee))
	entry.SetOverpaymentFee(l.OverpaymentFee)
	entry.SetInterestRate(l.InterestRate)
	entry.SetLateInterestRate(l.LateInterestRate)
	entry.SetCloseInterestRate(l.CloseInterestRate)
	entry.SetOverpaymentInterestRate(l.OverpaymentInterestRate)
	entry.SetStartDate(l.StartDate)
	entry.SetPaymentInterval(l.PaymentInterval)
	entry.SetGracePeriod(l.GracePeriod)
	entry.SetPreviousPaymentDueDate(l.PreviousPaymentDueDate)
	entry.SetNextPaymentDueDate(l.NextPaymentDueDate)
	entry.SetPaymentRemaining(l.PaymentRemaining)
	entry.SetPeriodicPayment(wireNum(l.PeriodicPayment))
	entry.SetPrincipalOutstanding(wireNum(l.PrincipalOutstanding))
	entry.SetTotalValueOutstanding(wireNum(l.TotalValueOutstanding))
	entry.SetManagementFeeOutstanding(wireNum(l.ManagementFeeOutstanding))
	entry.SetLoanScale(int(l.LoanScale))
	var zeroHash [32]byte
	if l.PreviousTxnID != zeroHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(l.PreviousTxnID[:])))
		entry.SetPreviousTxnLgrSeq(l.PreviousTxnLgrSeq)
	}
	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode loan: %w", err)
	}
	return data, nil
}

// parseLoan decodes a Loan entry via the ledgerfields decoder.
func parseLoan(data []byte) (*loanData, error) {
	ll := &ledgerfields.Loan{}
	if err := ll.Decode(data); err != nil {
		return nil, err
	}
	l := &loanData{
		LoanSequence:             ll.LoanSequence,
		LoanOriginationFee:       normNum(ll.LoanOriginationFee),
		LoanServiceFee:           normNum(ll.LoanServiceFee),
		LatePaymentFee:           normNum(ll.LatePaymentFee),
		ClosePaymentFee:          normNum(ll.ClosePaymentFee),
		OverpaymentFee:           ll.OverpaymentFee,
		InterestRate:             ll.InterestRate,
		LateInterestRate:         ll.LateInterestRate,
		CloseInterestRate:        ll.CloseInterestRate,
		OverpaymentInterestRate:  ll.OverpaymentInterestRate,
		StartDate:                ll.StartDate,
		PaymentInterval:          ll.PaymentInterval,
		GracePeriod:              ll.GracePeriod,
		PreviousPaymentDueDate:   ll.PreviousPaymentDueDate,
		NextPaymentDueDate:       ll.NextPaymentDueDate,
		PaymentRemaining:         ll.PaymentRemaining,
		PeriodicPayment:          normNum(ll.PeriodicPayment),
		PrincipalOutstanding:     normNum(ll.PrincipalOutstanding),
		TotalValueOutstanding:    normNum(ll.TotalValueOutstanding),
		ManagementFeeOutstanding: normNum(ll.ManagementFeeOutstanding),
		LoanScale:                int32(ll.LoanScale),
		Flags:                    ll.Flags,
		PreviousTxnLgrSeq:        ll.PreviousTxnLgrSeq,
	}
	if id, err := state.DecodeAccountID(ll.Borrower); err == nil {
		l.Borrower = id
	}
	l.LoanBrokerID = hash256(ll.LoanBrokerID)
	l.PreviousTxnID = hash256(ll.PreviousTxnID)
	l.OwnerNode = hexU64(ll.OwnerNode)
	l.LoanBrokerNode = hexU64(ll.LoanBrokerNode)
	return l, nil
}

// --- small encoding helpers ---

func normNum(v any) string {
	s, ok := v.(string)
	if !ok || s == "" || s == "0" {
		return ""
	}
	return s
}

func wireNum(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func hash256(s string) [32]byte {
	var h [32]byte
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		copy(h[:], b)
	}
	return h
}

func hexU64(s string) uint64 {
	n, _ := strconv.ParseUint(s, 16, 64)
	return n
}
