package lending

import (
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// The lending ledger objects carry their NUMBER fields (DebtTotal, CoverAvailable,
// PrincipalOutstanding, ...) as the canonical decimal/scientific string the binary
// codec round-trips; "" means the field is absent (soeDEFAULT zero). This mirrors
// the vault package's local Number seam.

// lendNum parses a NUMBER field string into a large-scale XRPLNumber (the scale a
// lending transaction context installs). "" / "0" decode to zero.
func lendNum(s string) lmath.N {
	return lendNumForRules(s, nil)
}

func lendNumForRules(s string, rules *amendment.Rules) lmath.N {
	scale := lendingNumberScale(rules)
	if s == "" || s == "0" {
		return lmath.NumScaled(0, 0, scale)
	}
	number, err := state.ParseXRPLNumber(s, scale, state.RoundToNearest)
	if err != nil {
		return lmath.NumScaled(0, 0, scale)
	}
	return number
}

func lendingNumberScale(rules *amendment.Rules) state.MantissaScale {
	return tx.NumberContextForRules(rules).Scale()
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

func serializeLoanBroker(b *loanBrokerData) ([]byte, error) {
	return serializeLoanBrokerForRules(b, nil)
}

func serializeLoanBrokerForRules(b *loanBrokerData, rules *amendment.Rules) ([]byte, error) {
	entry := &ledgerfields.LoanBroker{}
	entry.SetFlags(b.Flags)
	entry.SetSequence(b.Sequence)
	entry.SetOwnerNodeValue(b.OwnerNode)
	entry.SetVaultNodeValue(b.VaultNode)
	entry.SetVaultIDValue(b.VaultID)
	if err := entry.SetAccountValue(b.Account); err != nil {
		return nil, fmt.Errorf("encode account: %w", err)
	}
	if err := entry.SetOwnerValue(b.Owner); err != nil {
		return nil, fmt.Errorf("encode owner: %w", err)
	}
	entry.SetLoanSequence(b.LoanSequence)
	entry.SetData(strings.ToUpper(b.Data))
	entry.SetManagementFeeRate(b.ManagementFeeRate)
	entry.SetOwnerCount(b.OwnerCount)
	numbers, err := lendingWireNumbers(lendingNumberScale(rules), b.DebtTotal, b.DebtMaximum, b.CoverAvailable)
	if err != nil {
		return nil, fmt.Errorf("encode loan broker Number: %w", err)
	}
	if err := entry.SetDebtTotalValue(ledgerfields.NumberValue(numbers[0])); err != nil {
		return nil, fmt.Errorf("encode DebtTotal: %w", err)
	}
	if err := entry.SetDebtMaximumValue(ledgerfields.NumberValue(numbers[1])); err != nil {
		return nil, fmt.Errorf("encode DebtMaximum: %w", err)
	}
	if err := entry.SetCoverAvailableValue(ledgerfields.NumberValue(numbers[2])); err != nil {
		return nil, fmt.Errorf("encode CoverAvailable: %w", err)
	}
	entry.SetCoverRateMinimum(b.CoverRateMinimum)
	entry.SetCoverRateLiquidation(b.CoverRateLiquidation)
	var zeroHash [32]byte
	if b.PreviousTxnID != zeroHash {
		entry.SetPreviousTxnIDValue(b.PreviousTxnID)
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
	account, err := lb.GetAccount()
	if err != nil {
		return nil, err
	}
	owner, err := lb.GetOwner()
	if err != nil {
		return nil, err
	}
	vaultID, err := lb.GetVaultID()
	if err != nil {
		return nil, err
	}
	previousTxnID, err := lb.GetPreviousTxnID()
	if err != nil {
		return nil, err
	}
	ownerNode, err := lb.GetOwnerNode()
	if err != nil {
		return nil, err
	}
	vaultNode, err := lb.GetVaultNode()
	if err != nil {
		return nil, err
	}
	sequence, err := lb.GetSequence()
	if err != nil {
		return nil, err
	}
	loanSequence, err := lb.GetLoanSequence()
	if err != nil {
		return nil, err
	}
	managementFeeRate, err := lb.GetManagementFeeRate()
	if err != nil {
		return nil, err
	}
	ownerCount, err := lb.GetOwnerCount()
	if err != nil {
		return nil, err
	}
	coverRateMinimum, err := lb.GetCoverRateMinimum()
	if err != nil {
		return nil, err
	}
	coverRateLiquidation, err := lb.GetCoverRateLiquidation()
	if err != nil {
		return nil, err
	}
	flags, err := lb.GetFlags()
	if err != nil {
		return nil, err
	}
	previousTxnLgrSeq, err := lb.GetPreviousTxnLgrSeq()
	if err != nil {
		return nil, err
	}
	debtTotal, err := readNumber(lb.HasDebtTotal, lb.GetDebtTotal)
	if err != nil {
		return nil, err
	}
	debtMaximum, err := readNumber(lb.HasDebtMaximum, lb.GetDebtMaximum)
	if err != nil {
		return nil, err
	}
	coverAvailable, err := readNumber(lb.HasCoverAvailable, lb.GetCoverAvailable)
	if err != nil {
		return nil, err
	}
	b := &loanBrokerData{
		Sequence:             sequence,
		LoanSequence:         loanSequence,
		Data:                 lb.Data,
		ManagementFeeRate:    managementFeeRate,
		OwnerCount:           ownerCount,
		DebtTotal:            debtTotal,
		DebtMaximum:          debtMaximum,
		CoverAvailable:       coverAvailable,
		CoverRateMinimum:     coverRateMinimum,
		CoverRateLiquidation: coverRateLiquidation,
		Flags:                flags,
		PreviousTxnID:        previousTxnID,
		PreviousTxnLgrSeq:    previousTxnLgrSeq,
		Account:              account,
		Owner:                owner,
		VaultID:              vaultID,
		OwnerNode:            ownerNode,
		VaultNode:            vaultNode,
	}
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
	return serializeLoanForRules(l, nil)
}

func serializeLoanForRules(l *loanData, rules *amendment.Rules) ([]byte, error) {
	entry := &ledgerfields.Loan{}
	entry.SetFlags(l.Flags)
	entry.SetOwnerNodeValue(l.OwnerNode)
	entry.SetLoanBrokerNodeValue(l.LoanBrokerNode)
	entry.SetLoanBrokerIDValue(l.LoanBrokerID)
	entry.SetLoanSequence(l.LoanSequence)
	if err := entry.SetBorrowerValue(l.Borrower); err != nil {
		return nil, fmt.Errorf("encode borrower: %w", err)
	}
	numbers, err := lendingWireNumbers(
		lendingNumberScale(rules),
		l.LoanOriginationFee,
		l.LoanServiceFee,
		l.LatePaymentFee,
		l.ClosePaymentFee,
		l.PeriodicPayment,
		l.PrincipalOutstanding,
		l.TotalValueOutstanding,
		l.ManagementFeeOutstanding,
	)
	if err != nil {
		return nil, fmt.Errorf("encode loan Number: %w", err)
	}
	if err := entry.SetLoanOriginationFeeValue(ledgerfields.NumberValue(numbers[0])); err != nil {
		return nil, fmt.Errorf("encode LoanOriginationFee: %w", err)
	}
	if err := entry.SetLoanServiceFeeValue(ledgerfields.NumberValue(numbers[1])); err != nil {
		return nil, fmt.Errorf("encode LoanServiceFee: %w", err)
	}
	if err := entry.SetLatePaymentFeeValue(ledgerfields.NumberValue(numbers[2])); err != nil {
		return nil, fmt.Errorf("encode LatePaymentFee: %w", err)
	}
	if err := entry.SetClosePaymentFeeValue(ledgerfields.NumberValue(numbers[3])); err != nil {
		return nil, fmt.Errorf("encode ClosePaymentFee: %w", err)
	}
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
	if err := entry.SetPeriodicPaymentValue(ledgerfields.NumberValue(numbers[4])); err != nil {
		return nil, fmt.Errorf("encode PeriodicPayment: %w", err)
	}
	if err := entry.SetPrincipalOutstandingValue(ledgerfields.NumberValue(numbers[5])); err != nil {
		return nil, fmt.Errorf("encode PrincipalOutstanding: %w", err)
	}
	if err := entry.SetTotalValueOutstandingValue(ledgerfields.NumberValue(numbers[6])); err != nil {
		return nil, fmt.Errorf("encode TotalValueOutstanding: %w", err)
	}
	if err := entry.SetManagementFeeOutstandingValue(ledgerfields.NumberValue(numbers[7])); err != nil {
		return nil, fmt.Errorf("encode ManagementFeeOutstanding: %w", err)
	}
	entry.SetLoanScale(l.LoanScale)
	var zeroHash [32]byte
	if l.PreviousTxnID != zeroHash {
		entry.SetPreviousTxnIDValue(l.PreviousTxnID)
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
	borrower, err := ll.GetBorrower()
	if err != nil {
		return nil, err
	}
	loanBrokerID, err := ll.GetLoanBrokerID()
	if err != nil {
		return nil, err
	}
	previousTxnID, err := ll.GetPreviousTxnID()
	if err != nil {
		return nil, err
	}
	ownerNode, err := ll.GetOwnerNode()
	if err != nil {
		return nil, err
	}
	loanBrokerNode, err := ll.GetLoanBrokerNode()
	if err != nil {
		return nil, err
	}
	loanSequence, err := ll.GetLoanSequence()
	if err != nil {
		return nil, err
	}
	overpaymentFee, err := ll.GetOverpaymentFee()
	if err != nil {
		return nil, err
	}
	interestRate, err := ll.GetInterestRate()
	if err != nil {
		return nil, err
	}
	lateInterestRate, err := ll.GetLateInterestRate()
	if err != nil {
		return nil, err
	}
	closeInterestRate, err := ll.GetCloseInterestRate()
	if err != nil {
		return nil, err
	}
	overpaymentInterestRate, err := ll.GetOverpaymentInterestRate()
	if err != nil {
		return nil, err
	}
	startDate, err := ll.GetStartDate()
	if err != nil {
		return nil, err
	}
	paymentInterval, err := ll.GetPaymentInterval()
	if err != nil {
		return nil, err
	}
	gracePeriod, err := ll.GetGracePeriod()
	if err != nil {
		return nil, err
	}
	previousPaymentDueDate, err := ll.GetPreviousPaymentDueDate()
	if err != nil {
		return nil, err
	}
	nextPaymentDueDate, err := ll.GetNextPaymentDueDate()
	if err != nil {
		return nil, err
	}
	paymentRemaining, err := ll.GetPaymentRemaining()
	if err != nil {
		return nil, err
	}
	flags, err := ll.GetFlags()
	if err != nil {
		return nil, err
	}
	previousTxnLgrSeq, err := ll.GetPreviousTxnLgrSeq()
	if err != nil {
		return nil, err
	}
	numbers := make([]string, 8)
	getters := []struct {
		has  func() bool
		get  func() (ledgerfields.NumberValue, error)
		dest *string
	}{
		{ll.HasLoanOriginationFee, ll.GetLoanOriginationFee, &numbers[0]},
		{ll.HasLoanServiceFee, ll.GetLoanServiceFee, &numbers[1]},
		{ll.HasLatePaymentFee, ll.GetLatePaymentFee, &numbers[2]},
		{ll.HasClosePaymentFee, ll.GetClosePaymentFee, &numbers[3]},
		{ll.HasPeriodicPayment, ll.GetPeriodicPayment, &numbers[4]},
		{ll.HasPrincipalOutstanding, ll.GetPrincipalOutstanding, &numbers[5]},
		{ll.HasTotalValueOutstanding, ll.GetTotalValueOutstanding, &numbers[6]},
		{ll.HasManagementFeeOutstanding, ll.GetManagementFeeOutstanding, &numbers[7]},
	}
	for _, getter := range getters {
		value, err := readNumber(getter.has, getter.get)
		if err != nil {
			return nil, err
		}
		*getter.dest = value
	}
	l := &loanData{
		LoanSequence:             loanSequence,
		LoanOriginationFee:       numbers[0],
		LoanServiceFee:           numbers[1],
		LatePaymentFee:           numbers[2],
		ClosePaymentFee:          numbers[3],
		OverpaymentFee:           overpaymentFee,
		InterestRate:             interestRate,
		LateInterestRate:         lateInterestRate,
		CloseInterestRate:        closeInterestRate,
		OverpaymentInterestRate:  overpaymentInterestRate,
		StartDate:                startDate,
		PaymentInterval:          paymentInterval,
		GracePeriod:              gracePeriod,
		PreviousPaymentDueDate:   previousPaymentDueDate,
		NextPaymentDueDate:       nextPaymentDueDate,
		PaymentRemaining:         paymentRemaining,
		PeriodicPayment:          numbers[4],
		PrincipalOutstanding:     numbers[5],
		TotalValueOutstanding:    numbers[6],
		ManagementFeeOutstanding: numbers[7],
		LoanScale:                int32(ll.LoanScale),
		Flags:                    flags,
		PreviousTxnID:            previousTxnID,
		PreviousTxnLgrSeq:        previousTxnLgrSeq,
		Borrower:                 borrower,
		LoanBrokerID:             loanBrokerID,
		OwnerNode:                ownerNode,
		LoanBrokerNode:           loanBrokerNode,
	}
	return l, nil
}

// --- small encoding helpers ---

func normNum(v ledgerfields.NumberValue) string {
	s := string(v)
	if s == "" || s == "0" {
		return ""
	}
	return s
}

func readNumber(has func() bool, get func() (ledgerfields.NumberValue, error)) (string, error) {
	if !has() {
		return "", nil
	}
	value, err := get()
	if err != nil {
		return "", err
	}
	return normNum(value), nil
}

func lendingWireNumbers(scale state.MantissaScale, values ...string) ([]string, error) {
	numbers := make([]string, len(values))
	for i, value := range values {
		if value == "" || value == "0" {
			numbers[i] = "0"
			continue
		}
		number, err := state.ParseXRPLNumber(value, scale, state.RoundToNearest)
		if err != nil {
			return nil, err
		}
		numbers[i] = fmt.Sprintf("%de%d", number.Mantissa(), number.Exponent())
	}
	return numbers, nil
}
