package lending

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// Transaction flags for the LendingProtocol transaction types, matching
// rippled TxFlags.h at tag 3.0.0. LoanSet and LoanPay share tfLoanOverpayment
// (0x10000); LoanManage uses a distinct delinquency-flag set.
const (
	// LoanSet: the loan supports overpayments.
	// LoanPay: any excess in this payment may be used toward principal.
	TfLoanOverpayment uint32 = 0x00010000

	// LoanPay: the payment is an early full payment of the loan.
	TfLoanFullPayment uint32 = 0x00020000

	// LoanPay: the payment is late.
	TfLoanLatePayment uint32 = 0x00040000

	// LoanManage delinquency flags.
	TfLoanDefault  uint32 = 0x00010000
	TfLoanImpair   uint32 = 0x00020000
	TfLoanUnimpair uint32 = 0x00040000
)

// Flag masks reject any bit outside the permitted set (temINVALID_FLAG).
const (
	TfLoanSetMask    uint32 = ^(tx.TfUniversal | TfLoanOverpayment)
	TfLoanPayMask    uint32 = ^(tx.TfUniversal | TfLoanOverpayment | TfLoanFullPayment | TfLoanLatePayment)
	TfLoanManageMask uint32 = ^(tx.TfUniversal | TfLoanDefault | TfLoanImpair | TfLoanUnimpair)
)

// Loan ledger-object (lsf) flags (rippled LedgerFormats.h).
const (
	LsfLoanDefault     = entry.LsfLoanDefault
	LsfLoanImpaired    = entry.LsfLoanImpaired
	LsfLoanOverpayment = entry.LsfLoanOverpayment
)
