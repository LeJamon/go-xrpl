package lending

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all LendingProtocol (LoanBroker/Loan) transaction types
// with the tx registry.
func Register() {
	tx.Register(tx.TypeLoanBrokerSet, func() tx.Transaction {
		return &LoanBrokerSet{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerSet, "")}
	})
	tx.Register(tx.TypeLoanBrokerDelete, func() tx.Transaction {
		return &LoanBrokerDelete{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerDelete, "")}
	})
	tx.Register(tx.TypeLoanBrokerCoverDeposit, func() tx.Transaction {
		return &LoanBrokerCoverDeposit{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerCoverDeposit, "")}
	})
	tx.Register(tx.TypeLoanBrokerCoverWithdraw, func() tx.Transaction {
		return &LoanBrokerCoverWithdraw{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerCoverWithdraw, "")}
	})
	tx.Register(tx.TypeLoanBrokerCoverClawback, func() tx.Transaction {
		return &LoanBrokerCoverClawback{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerCoverClawback, "")}
	})
	tx.Register(tx.TypeLoanSet, func() tx.Transaction {
		return &LoanSet{BaseTx: *tx.NewBaseTx(tx.TypeLoanSet, "")}
	})
	tx.Register(tx.TypeLoanDelete, func() tx.Transaction {
		return &LoanDelete{BaseTx: *tx.NewBaseTx(tx.TypeLoanDelete, "")}
	})
	tx.Register(tx.TypeLoanManage, func() tx.Transaction {
		return &LoanManage{BaseTx: *tx.NewBaseTx(tx.TypeLoanManage, "")}
	})
	tx.Register(tx.TypeLoanPay, func() tx.Transaction {
		return &LoanPay{BaseTx: *tx.NewBaseTx(tx.TypeLoanPay, "")}
	})
}
