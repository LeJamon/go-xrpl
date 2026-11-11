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
		Delegation:        tx.NotDelegable,
		Consequences:      tx.NormalConsequences,
		DisallowedInBatch: true,
		Privileges:        tx.PrivCreatePseudoAcct | tx.PrivMayAuthorizeMPT,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanBrokerDelete,
		New: func() tx.Transaction {
			return &LoanBrokerDelete{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerDelete, "")}
		},
		Delegation:        tx.NotDelegable,
		Consequences:      tx.NormalConsequences,
		DisallowedInBatch: true,
		Privileges:        tx.PrivMustDeleteAcct | tx.PrivMayAuthorizeMPT,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanBrokerCoverDeposit,
		New: func() tx.Transaction {
			return &LoanBrokerCoverDeposit{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerCoverDeposit, "")}
		},
		Delegation:        tx.NotDelegable,
		Consequences:      tx.NormalConsequences,
		DisallowedInBatch: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanBrokerCoverWithdraw,
		New: func() tx.Transaction {
			return &LoanBrokerCoverWithdraw{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerCoverWithdraw, "")}
		},
		Delegation:        tx.NotDelegable,
		Consequences:      tx.NormalConsequences,
		DisallowedInBatch: true,
		Privileges:        tx.PrivMayAuthorizeMPT,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanBrokerCoverClawback,
		New: func() tx.Transaction {
			return &LoanBrokerCoverClawback{BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerCoverClawback, "")}
		},
		Delegation:        tx.NotDelegable,
		Consequences:      tx.NormalConsequences,
		DisallowedInBatch: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanSet,
		New: func() tx.Transaction {
			return &LoanSet{BaseTx: *tx.NewBaseTx(tx.TypeLoanSet, "")}
		},
		Delegation:        tx.NotDelegable,
		Consequences:      tx.NormalConsequences,
		DisallowedInBatch: true,
		Privileges:        tx.PrivMayAuthorizeMPT | tx.PrivMustModifyVault,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanDelete,
		New: func() tx.Transaction {
			return &LoanDelete{BaseTx: *tx.NewBaseTx(tx.TypeLoanDelete, "")}
		},
		Delegation:        tx.NotDelegable,
		Consequences:      tx.NormalConsequences,
		DisallowedInBatch: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanManage,
		New: func() tx.Transaction {
			return &LoanManage{BaseTx: *tx.NewBaseTx(tx.TypeLoanManage, "")}
		},
		Delegation:        tx.NotDelegable,
		Consequences:      tx.NormalConsequences,
		DisallowedInBatch: true,
		Privileges:        tx.PrivMayModifyVault,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeLoanPay,
		New: func() tx.Transaction {
			return &LoanPay{BaseTx: *tx.NewBaseTx(tx.TypeLoanPay, "")}
		},
		Delegation:        tx.NotDelegable,
		Consequences:      tx.NormalConsequences,
		DisallowedInBatch: true,
		Privileges:        tx.PrivMayAuthorizeMPT | tx.PrivMustModifyVault,
	})
}
