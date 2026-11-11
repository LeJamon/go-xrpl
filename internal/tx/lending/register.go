package lending

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all LendingProtocol (LoanBroker/Loan) transaction types
// with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanBrokerSet,
		New: func() tx.Transaction {
			return &LoanBrokerSet{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerSet, "")}
		},
		Delegation: tx.NotDelegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanBrokerDelete,
		New: func() tx.Transaction {
			return &LoanBrokerDelete{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerDelete, "")}
		},
		Delegation: tx.NotDelegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanBrokerCoverDeposit,
		New: func() tx.Transaction {
			return &LoanBrokerCoverDeposit{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerCoverDeposit, "")}
		},
		Delegation: tx.NotDelegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanBrokerCoverWithdraw,
		New: func() tx.Transaction {
			return &LoanBrokerCoverWithdraw{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerCoverWithdraw, "")}
		},
		Delegation: tx.NotDelegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanBrokerCoverClawback,
		New: func() tx.Transaction {
			return &LoanBrokerCoverClawback{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerCoverClawback, "")}
		},
		Delegation: tx.NotDelegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanSet,
		New: func() tx.Transaction {
			return &LoanSet{BaseTx: *tx.NewBaseTx(tx.TypeLoanSet, "")}
		},
		Delegation: tx.NotDelegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanDelete,
		New: func() tx.Transaction {
			return &LoanDelete{BaseTx: *tx.NewBaseTx(tx.TypeLoanDelete, "")}
		},
		Delegation: tx.NotDelegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanManage,
		New: func() tx.Transaction {
			return &LoanManage{BaseTx: *tx.NewBaseTx(tx.TypeLoanManage, "")}
		},
		Delegation: tx.NotDelegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanPay,
		New: func() tx.Transaction {
			return &LoanPay{BaseTx: *tx.NewBaseTx(tx.TypeLoanPay, "")}
		},
		Delegation: tx.NotDelegable,
	})
}
