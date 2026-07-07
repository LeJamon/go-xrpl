// Package lending implements the pre-activation surface for the XLS-66
// LendingProtocol feature (LoanBroker* and Loan* transaction types).
//
// The transactors are registered so a LendingProtocol-aware node parses these
// transactions (matching a rippled 3.0.0 node) and rejects them with
// temDISABLED at preflight while FeatureLendingProtocol is off, rather than
// failing with temUNKNOWN or a parse error. The full lending semantics —
// LoanBroker/Loan ledger objects, fee/interest math, delinquency handling —
// are 3.1.0 work tracked separately, so Apply is intentionally a hard-error
// stub guarding against the amendment being enabled before that lands.
package lending

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func requiredLending() [][32]byte { return [][32]byte{amendment.FeatureLendingProtocol} }

// LoanBrokerSet creates or updates a Loan Broker.
type LoanBrokerSet struct {
	tx.BaseTx

	VaultID              string  `json:"VaultID" xrpl:"VaultID"`
	LoanBrokerID         *string `json:"LoanBrokerID,omitempty" xrpl:"LoanBrokerID,omitempty"`
	Data                 *string `json:"Data,omitempty" xrpl:"Data,omitempty"`
	ManagementFeeRate    *uint16 `json:"ManagementFeeRate,omitempty" xrpl:"ManagementFeeRate,omitempty"`
	DebtMaximum          *string `json:"DebtMaximum,omitempty" xrpl:"DebtMaximum,omitempty"`
	CoverRateMinimum     *uint32 `json:"CoverRateMinimum,omitempty" xrpl:"CoverRateMinimum,omitempty"`
	CoverRateLiquidation *uint32 `json:"CoverRateLiquidation,omitempty" xrpl:"CoverRateLiquidation,omitempty"`
}

// NewLoanBrokerSet creates a LoanBrokerSet transaction.
func NewLoanBrokerSet(account, vaultID string) *LoanBrokerSet {
	return &LoanBrokerSet{
		BaseTx:  *tx.NewBaseTx(tx.TypeLoanBrokerSet, account),
		VaultID: vaultID,
	}
}

func (l *LoanBrokerSet) TxType() tx.Type { return tx.TypeLoanBrokerSet }

func (l *LoanBrokerSet) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.VaultID == "" {
		return ter.Errorf(ter.TemMALFORMED, "VaultID is required")
	}
	return nil
}

func (l *LoanBrokerSet) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanBrokerSet) RequiredAmendments() [][32]byte   { return requiredLending() }

// LoanBrokerDelete deletes a Loan Broker.
type LoanBrokerDelete struct {
	tx.BaseTx

	LoanBrokerID string `json:"LoanBrokerID" xrpl:"LoanBrokerID"`
}

// NewLoanBrokerDelete creates a LoanBrokerDelete transaction.
func NewLoanBrokerDelete(account, loanBrokerID string) *LoanBrokerDelete {
	return &LoanBrokerDelete{
		BaseTx:       *tx.NewBaseTx(tx.TypeLoanBrokerDelete, account),
		LoanBrokerID: loanBrokerID,
	}
}

func (l *LoanBrokerDelete) TxType() tx.Type { return tx.TypeLoanBrokerDelete }

func (l *LoanBrokerDelete) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanBrokerID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanBrokerID is required")
	}
	return nil
}

func (l *LoanBrokerDelete) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanBrokerDelete) RequiredAmendments() [][32]byte   { return requiredLending() }

// LoanBrokerCoverDeposit deposits First Loss Capital into a Loan Broker.
type LoanBrokerCoverDeposit struct {
	tx.BaseTx

	LoanBrokerID string    `json:"LoanBrokerID" xrpl:"LoanBrokerID"`
	Amount       tx.Amount `json:"Amount" xrpl:"Amount,amount"`
}

// NewLoanBrokerCoverDeposit creates a LoanBrokerCoverDeposit transaction.
func NewLoanBrokerCoverDeposit(account, loanBrokerID string, amount tx.Amount) *LoanBrokerCoverDeposit {
	return &LoanBrokerCoverDeposit{
		BaseTx:       *tx.NewBaseTx(tx.TypeLoanBrokerCoverDeposit, account),
		LoanBrokerID: loanBrokerID,
		Amount:       amount,
	}
}

func (l *LoanBrokerCoverDeposit) TxType() tx.Type { return tx.TypeLoanBrokerCoverDeposit }

func (l *LoanBrokerCoverDeposit) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanBrokerID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanBrokerID is required")
	}
	if l.Amount.IsZero() {
		return ter.Errorf(ter.TemBAD_AMOUNT, "Amount is required")
	}
	return nil
}

func (l *LoanBrokerCoverDeposit) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanBrokerCoverDeposit) RequiredAmendments() [][32]byte   { return requiredLending() }

// LoanBrokerCoverWithdraw withdraws First Loss Capital from a Loan Broker.
type LoanBrokerCoverWithdraw struct {
	tx.BaseTx

	LoanBrokerID   string    `json:"LoanBrokerID" xrpl:"LoanBrokerID"`
	Amount         tx.Amount `json:"Amount" xrpl:"Amount,amount"`
	Destination    string    `json:"Destination,omitempty" xrpl:"Destination,omitempty"`
	DestinationTag *uint32   `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`
}

// NewLoanBrokerCoverWithdraw creates a LoanBrokerCoverWithdraw transaction.
func NewLoanBrokerCoverWithdraw(account, loanBrokerID string, amount tx.Amount) *LoanBrokerCoverWithdraw {
	return &LoanBrokerCoverWithdraw{
		BaseTx:       *tx.NewBaseTx(tx.TypeLoanBrokerCoverWithdraw, account),
		LoanBrokerID: loanBrokerID,
		Amount:       amount,
	}
}

func (l *LoanBrokerCoverWithdraw) TxType() tx.Type { return tx.TypeLoanBrokerCoverWithdraw }

func (l *LoanBrokerCoverWithdraw) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanBrokerID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanBrokerID is required")
	}
	if l.Amount.IsZero() {
		return ter.Errorf(ter.TemBAD_AMOUNT, "Amount is required")
	}
	return nil
}

func (l *LoanBrokerCoverWithdraw) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanBrokerCoverWithdraw) RequiredAmendments() [][32]byte   { return requiredLending() }

// LoanBrokerCoverClawback claws back First Loss Capital from a Loan Broker.
type LoanBrokerCoverClawback struct {
	tx.BaseTx

	LoanBrokerID *string    `json:"LoanBrokerID,omitempty" xrpl:"LoanBrokerID,omitempty"`
	Amount       *tx.Amount `json:"Amount,omitempty" xrpl:"Amount,omitempty,amount"`
}

// NewLoanBrokerCoverClawback creates a LoanBrokerCoverClawback transaction.
func NewLoanBrokerCoverClawback(account string) *LoanBrokerCoverClawback {
	return &LoanBrokerCoverClawback{
		BaseTx: *tx.NewBaseTx(tx.TypeLoanBrokerCoverClawback, account),
	}
}

func (l *LoanBrokerCoverClawback) TxType() tx.Type { return tx.TypeLoanBrokerCoverClawback }

func (l *LoanBrokerCoverClawback) Validate() error { return l.BaseTx.Validate() }

func (l *LoanBrokerCoverClawback) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanBrokerCoverClawback) RequiredAmendments() [][32]byte   { return requiredLending() }

// LoanSet creates a Loan.
type LoanSet struct {
	tx.BaseTx

	LoanBrokerID            string         `json:"LoanBrokerID" xrpl:"LoanBrokerID"`
	Data                    *string        `json:"Data,omitempty" xrpl:"Data,omitempty"`
	Counterparty            string         `json:"Counterparty,omitempty" xrpl:"Counterparty,omitempty"`
	CounterpartySignature   map[string]any `json:"CounterpartySignature,omitempty" xrpl:"CounterpartySignature,omitempty"`
	LoanOriginationFee      *string        `json:"LoanOriginationFee,omitempty" xrpl:"LoanOriginationFee,omitempty"`
	LoanServiceFee          *string        `json:"LoanServiceFee,omitempty" xrpl:"LoanServiceFee,omitempty"`
	LatePaymentFee          *string        `json:"LatePaymentFee,omitempty" xrpl:"LatePaymentFee,omitempty"`
	ClosePaymentFee         *string        `json:"ClosePaymentFee,omitempty" xrpl:"ClosePaymentFee,omitempty"`
	OverpaymentFee          *uint32        `json:"OverpaymentFee,omitempty" xrpl:"OverpaymentFee,omitempty"`
	InterestRate            *uint32        `json:"InterestRate,omitempty" xrpl:"InterestRate,omitempty"`
	LateInterestRate        *uint32        `json:"LateInterestRate,omitempty" xrpl:"LateInterestRate,omitempty"`
	CloseInterestRate       *uint32        `json:"CloseInterestRate,omitempty" xrpl:"CloseInterestRate,omitempty"`
	OverpaymentInterestRate *uint32        `json:"OverpaymentInterestRate,omitempty" xrpl:"OverpaymentInterestRate,omitempty"`
	PrincipalRequested      string         `json:"PrincipalRequested" xrpl:"PrincipalRequested"`
	PaymentTotal            *uint32        `json:"PaymentTotal,omitempty" xrpl:"PaymentTotal,omitempty"`
	PaymentInterval         *uint32        `json:"PaymentInterval,omitempty" xrpl:"PaymentInterval,omitempty"`
	GracePeriod             *uint32        `json:"GracePeriod,omitempty" xrpl:"GracePeriod,omitempty"`
}

// NewLoanSet creates a LoanSet transaction.
func NewLoanSet(account, loanBrokerID, principalRequested string) *LoanSet {
	return &LoanSet{
		BaseTx:             *tx.NewBaseTx(tx.TypeLoanSet, account),
		LoanBrokerID:       loanBrokerID,
		PrincipalRequested: principalRequested,
	}
}

func (l *LoanSet) TxType() tx.Type { return tx.TypeLoanSet }

func (l *LoanSet) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanBrokerID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanBrokerID is required")
	}
	if l.PrincipalRequested == "" {
		return ter.Errorf(ter.TemMALFORMED, "PrincipalRequested is required")
	}
	return nil
}

func (l *LoanSet) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanSet) RequiredAmendments() [][32]byte   { return requiredLending() }

// LoanDelete deletes a Loan.
type LoanDelete struct {
	tx.BaseTx

	LoanID string `json:"LoanID" xrpl:"LoanID"`
}

// NewLoanDelete creates a LoanDelete transaction.
func NewLoanDelete(account, loanID string) *LoanDelete {
	return &LoanDelete{
		BaseTx: *tx.NewBaseTx(tx.TypeLoanDelete, account),
		LoanID: loanID,
	}
}

func (l *LoanDelete) TxType() tx.Type { return tx.TypeLoanDelete }

func (l *LoanDelete) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanID is required")
	}
	return nil
}

func (l *LoanDelete) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanDelete) RequiredAmendments() [][32]byte   { return requiredLending() }

// LoanManage changes the delinquency status of a Loan.
type LoanManage struct {
	tx.BaseTx

	LoanID string `json:"LoanID" xrpl:"LoanID"`
}

// NewLoanManage creates a LoanManage transaction.
func NewLoanManage(account, loanID string) *LoanManage {
	return &LoanManage{
		BaseTx: *tx.NewBaseTx(tx.TypeLoanManage, account),
		LoanID: loanID,
	}
}

func (l *LoanManage) TxType() tx.Type { return tx.TypeLoanManage }

func (l *LoanManage) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanID is required")
	}
	return nil
}

func (l *LoanManage) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanManage) RequiredAmendments() [][32]byte   { return requiredLending() }

// LoanPay makes a payment on a Loan.
type LoanPay struct {
	tx.BaseTx

	LoanID string    `json:"LoanID" xrpl:"LoanID"`
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`
}

// NewLoanPay creates a LoanPay transaction.
func NewLoanPay(account, loanID string, amount tx.Amount) *LoanPay {
	return &LoanPay{
		BaseTx: *tx.NewBaseTx(tx.TypeLoanPay, account),
		LoanID: loanID,
		Amount: amount,
	}
}

func (l *LoanPay) TxType() tx.Type { return tx.TypeLoanPay }

func (l *LoanPay) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanID is required")
	}
	if l.Amount.IsZero() {
		return ter.Errorf(ter.TemBAD_AMOUNT, "Amount is required")
	}
	return nil
}

func (l *LoanPay) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanPay) RequiredAmendments() [][32]byte   { return requiredLending() }

// The Apply methods are intentionally unimplemented. LendingProtocol is
// SupportedNo, so the engine rejects these transactions at preflight with
// temDISABLED and Apply is unreachable. Each returns a hard error that mutates
// no state, guarding against the amendment being enabled before the real
// lending semantics (issue #1245) are implemented.

func (l *LoanBrokerSet) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("loan broker set apply: not implemented", "account", l.Account)
	return ter.TefINTERNAL
}

func (l *LoanBrokerDelete) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("loan broker delete apply: not implemented", "account", l.Account)
	return ter.TefINTERNAL
}

func (l *LoanBrokerCoverDeposit) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("loan broker cover deposit apply: not implemented", "account", l.Account)
	return ter.TefINTERNAL
}

func (l *LoanBrokerCoverWithdraw) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("loan broker cover withdraw apply: not implemented", "account", l.Account)
	return ter.TefINTERNAL
}

func (l *LoanBrokerCoverClawback) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("loan broker cover clawback apply: not implemented", "account", l.Account)
	return ter.TefINTERNAL
}

func (l *LoanSet) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("loan set apply: not implemented", "account", l.Account)
	return ter.TefINTERNAL
}

func (l *LoanDelete) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("loan delete apply: not implemented", "account", l.Account)
	return ter.TefINTERNAL
}

func (l *LoanManage) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("loan manage apply: not implemented", "account", l.Account)
	return ter.TefINTERNAL
}

func (l *LoanPay) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("loan pay apply: not implemented", "account", l.Account)
	return ter.TefINTERNAL
}
