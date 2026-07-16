// Package lending implements the XLS-66 LendingProtocol transaction types
// (LoanBroker* and Loan*), porting rippled 3.1.0's transactor semantics onto the
// XRPLNumber amortization math in the lmath subpackage. A LoanBroker is a
// pseudo-account sitting on a SingleAssetVault; loans draw principal from the
// vault and repay it with amortized interest and fees.
package lending

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
)

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

// GetFlagsMask adopts the engine FlagsMasker seam. LoanBrokerSet defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (l *LoanBrokerSet) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (l *LoanBrokerSet) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.VaultID == "" {
		return ter.Errorf(ter.TemMALFORMED, "VaultID is required")
	}
	if l.Data != nil && !validDataLength(*l.Data, maxDataPayloadLength) {
		return ter.Errorf(ter.TemINVALID, "Data too long")
	}
	if !validRangeU16(l.ManagementFeeRate, protocol.MaxManagementFeeRate) {
		return ter.Errorf(ter.TemINVALID, "ManagementFeeRate out of range")
	}
	if !validRangeU32(l.CoverRateMinimum, protocol.MaxCoverRate) {
		return ter.Errorf(ter.TemINVALID, "CoverRateMinimum out of range")
	}
	if !validRangeU32(l.CoverRateLiquidation, protocol.MaxCoverRate) {
		return ter.Errorf(ter.TemINVALID, "CoverRateLiquidation out of range")
	}
	if !validNumberRange(l.DebtMaximum, 0, maxMPTokenAmount) {
		return ter.Errorf(ter.TemINVALID, "DebtMaximum out of range")
	}
	if l.LoanBrokerID != nil {
		if l.ManagementFeeRate != nil || l.CoverRateMinimum != nil || l.CoverRateLiquidation != nil {
			return ter.Errorf(ter.TemINVALID, "fixed fields cannot be changed on an existing LoanBroker")
		}
		if isZeroHashStr(*l.LoanBrokerID) {
			return ter.Errorf(ter.TemINVALID, "LoanBrokerID cannot be zero")
		}
	}
	if isZeroHashStr(l.VaultID) {
		return ter.Errorf(ter.TemINVALID, "VaultID cannot be zero")
	}
	minZero := l.CoverRateMinimum == nil || *l.CoverRateMinimum == 0
	liqZero := l.CoverRateLiquidation == nil || *l.CoverRateLiquidation == 0
	if minZero != liqZero {
		return ter.Errorf(ter.TemINVALID, "CoverRateMinimum and CoverRateLiquidation must both be zero or non-zero")
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

// GetFlagsMask adopts the engine FlagsMasker seam. LoanBrokerDelete defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (l *LoanBrokerDelete) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (l *LoanBrokerDelete) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanBrokerID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanBrokerID is required")
	}
	if isZeroHashStr(l.LoanBrokerID) {
		return ter.Errorf(ter.TemINVALID, "LoanBrokerID cannot be zero")
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

// GetFlagsMask adopts the engine FlagsMasker seam. LoanBrokerCoverDeposit defines
// no type-specific flags, so it uses the base universal mask, checked at preflight0.
func (l *LoanBrokerCoverDeposit) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (l *LoanBrokerCoverDeposit) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanBrokerID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanBrokerID is required")
	}
	if isZeroHashStr(l.LoanBrokerID) {
		return ter.Errorf(ter.TemINVALID, "LoanBrokerID cannot be zero")
	}
	if l.Amount.Signum() <= 0 {
		return ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
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

// GetFlagsMask adopts the engine FlagsMasker seam. LoanBrokerCoverWithdraw defines
// no type-specific flags, so it uses the base universal mask, checked at preflight0.
func (l *LoanBrokerCoverWithdraw) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (l *LoanBrokerCoverWithdraw) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanBrokerID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanBrokerID is required")
	}
	if isZeroHashStr(l.LoanBrokerID) {
		return ter.Errorf(ter.TemINVALID, "LoanBrokerID cannot be zero")
	}
	if l.Amount.Signum() <= 0 {
		return ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
	}
	if l.Destination != "" && isZeroAccount(l.Destination) {
		return ter.Errorf(ter.TemMALFORMED, "Destination cannot be zero")
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

// GetFlagsMask adopts the engine FlagsMasker seam. LoanBrokerCoverClawback defines
// no type-specific flags, so it uses the base universal mask, checked at preflight0.
func (l *LoanBrokerCoverClawback) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (l *LoanBrokerCoverClawback) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanBrokerID == nil && l.Amount == nil {
		return ter.Errorf(ter.TemINVALID, "LoanBrokerID or Amount is required")
	}
	if l.LoanBrokerID != nil && isZeroHashStr(*l.LoanBrokerID) {
		return ter.Errorf(ter.TemINVALID, "LoanBrokerID cannot be zero")
	}
	if l.Amount != nil {
		if l.Amount.IsNative() {
			return ter.Errorf(ter.TemBAD_AMOUNT, "cannot clawback native asset")
		}
		if l.Amount.Signum() < 0 {
			return ter.Errorf(ter.TemBAD_AMOUNT, "Amount cannot be negative")
		}
		if l.LoanBrokerID == nil {
			if l.Amount.IsMPT() {
				return ter.Errorf(ter.TemINVALID, "cannot derive LoanBroker from an MPT amount")
			}
			issuer := l.Amount.Issuer
			if issuer == "" || issuer == l.Account {
				return ter.Errorf(ter.TemINVALID, "invalid Amount issuer")
			}
		}
	}
	return nil
}

func (l *LoanBrokerCoverClawback) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanBrokerCoverClawback) RequiredAmendments() [][32]byte   { return requiredLending() }

// LoanSet creates a Loan.
type LoanSet struct {
	tx.BaseTx

	LoanBrokerID            string  `json:"LoanBrokerID" xrpl:"LoanBrokerID"`
	Data                    *string `json:"Data,omitempty" xrpl:"Data,omitempty"`
	Counterparty            string  `json:"Counterparty,omitempty" xrpl:"Counterparty,omitempty"`
	LoanOriginationFee      *string `json:"LoanOriginationFee,omitempty" xrpl:"LoanOriginationFee,omitempty"`
	LoanServiceFee          *string `json:"LoanServiceFee,omitempty" xrpl:"LoanServiceFee,omitempty"`
	LatePaymentFee          *string `json:"LatePaymentFee,omitempty" xrpl:"LatePaymentFee,omitempty"`
	ClosePaymentFee         *string `json:"ClosePaymentFee,omitempty" xrpl:"ClosePaymentFee,omitempty"`
	OverpaymentFee          *uint32 `json:"OverpaymentFee,omitempty" xrpl:"OverpaymentFee,omitempty"`
	InterestRate            *uint32 `json:"InterestRate,omitempty" xrpl:"InterestRate,omitempty"`
	LateInterestRate        *uint32 `json:"LateInterestRate,omitempty" xrpl:"LateInterestRate,omitempty"`
	CloseInterestRate       *uint32 `json:"CloseInterestRate,omitempty" xrpl:"CloseInterestRate,omitempty"`
	OverpaymentInterestRate *uint32 `json:"OverpaymentInterestRate,omitempty" xrpl:"OverpaymentInterestRate,omitempty"`
	PrincipalRequested      string  `json:"PrincipalRequested" xrpl:"PrincipalRequested"`
	PaymentTotal            *uint32 `json:"PaymentTotal,omitempty" xrpl:"PaymentTotal,omitempty"`
	PaymentInterval         *uint32 `json:"PaymentInterval,omitempty" xrpl:"PaymentInterval,omitempty"`
	GracePeriod             *uint32 `json:"GracePeriod,omitempty" xrpl:"GracePeriod,omitempty"`
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

// GetFlagsMask adopts the engine FlagsMasker seam with the LoanSet invalid-flags
// mask (rippled LoanSet::getFlagsMask = tfLoanSetMask), checked at preflight0.
func (l *LoanSet) GetFlagsMask(rules *amendment.Rules) uint32 {
	return TfLoanSetMask
}

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
	// A LoanSet must carry a CounterpartySignature, except as a batch inner tx.
	hasCounterSig := l.GetCommon().CounterpartySignature != nil
	if l.GetFlags()&tx.TfInnerBatchTxn == 0 && !hasCounterSig {
		return ter.Errorf(ter.TemBAD_SIGNER, "LoanSet requires a CounterpartySignature")
	}
	if l.Data != nil && !validDataLength(*l.Data, maxDataPayloadLength) {
		return ter.Errorf(ter.TemINVALID, "Data too long")
	}
	if !validNumberMinimum(l.LoanServiceFee, 0) || !validNumberMinimum(l.LatePaymentFee, 0) || !validNumberMinimum(l.ClosePaymentFee, 0) {
		return ter.Errorf(ter.TemINVALID, "fee cannot be negative")
	}
	principal := lendNum(l.PrincipalRequested)
	if principal.Signum() <= 0 {
		return ter.Errorf(ter.TemINVALID, "PrincipalRequested must be positive")
	}
	if l.LoanOriginationFee != nil {
		fee := lendNum(*l.LoanOriginationFee)
		if fee.Signum() < 0 || fee.Cmp(principal) > 0 {
			return ter.Errorf(ter.TemINVALID, "LoanOriginationFee out of range")
		}
	}
	if !validRangeU32(l.InterestRate, protocol.MaxInterestRate) ||
		!validRangeU32(l.OverpaymentFee, protocol.MaxOverpaymentFee) ||
		!validRangeU32(l.LateInterestRate, protocol.MaxLateInterestRate) ||
		!validRangeU32(l.CloseInterestRate, protocol.MaxCloseInterestRate) ||
		!validRangeU32(l.OverpaymentInterestRate, protocol.MaxOverpaymentInterestRate) {
		return ter.Errorf(ter.TemINVALID, "rate out of range")
	}
	if l.PaymentTotal != nil && *l.PaymentTotal == 0 {
		return ter.Errorf(ter.TemINVALID, "PaymentTotal must be positive")
	}
	if l.PaymentInterval != nil && *l.PaymentInterval < minPaymentInterval {
		return ter.Errorf(ter.TemINVALID, "PaymentInterval too small")
	}
	if l.GracePeriod != nil {
		maxGrace := minPaymentInterval
		if l.PaymentInterval != nil {
			maxGrace = *l.PaymentInterval
		}
		if *l.GracePeriod < defaultGracePeriod || *l.GracePeriod > maxGrace {
			return ter.Errorf(ter.TemINVALID, "GracePeriod out of range")
		}
	}
	if isZeroHashStr(l.LoanBrokerID) {
		return ter.Errorf(ter.TemINVALID, "LoanBrokerID cannot be zero")
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

// GetFlagsMask adopts the engine FlagsMasker seam. LoanDelete defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (l *LoanDelete) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (l *LoanDelete) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanID is required")
	}
	if isZeroHashStr(l.LoanID) {
		return ter.Errorf(ter.TemINVALID, "LoanID cannot be zero")
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

// GetFlagsMask adopts the engine FlagsMasker seam with the LoanManage invalid-flags
// mask (rippled LoanManage::getFlagsMask = tfLoanManageMask), checked at preflight0.
// The at-most-one-flag exclusivity check stays in Validate.
func (l *LoanManage) GetFlagsMask(rules *amendment.Rules) uint32 {
	return TfLoanManageMask
}

func (l *LoanManage) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanID is required")
	}
	if isZeroHashStr(l.LoanID) {
		return ter.Errorf(ter.TemINVALID, "LoanID cannot be zero")
	}
	if f := l.GetFlags() & (TfLoanDefault | TfLoanImpair | TfLoanUnimpair); f&(f-1) != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "at most one LoanManage flag may be set")
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

// GetFlagsMask adopts the engine FlagsMasker seam with the LoanPay invalid-flags
// mask (rippled LoanPay::getFlagsMask = tfLoanPayMask), checked at preflight0. The
// mutually-exclusive flag check stays in Validate.
func (l *LoanPay) GetFlagsMask(rules *amendment.Rules) uint32 {
	return TfLoanPayMask
}

func (l *LoanPay) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}
	if l.LoanID == "" {
		return ter.Errorf(ter.TemMALFORMED, "LoanID is required")
	}
	if isZeroHashStr(l.LoanID) {
		return ter.Errorf(ter.TemINVALID, "LoanID cannot be zero")
	}
	if l.Amount.Signum() <= 0 {
		return ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
	}
	if f := l.GetFlags() & (TfLoanOverpayment | TfLoanFullPayment | TfLoanLatePayment); f&(f-1) != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "LoanPay flags are mutually exclusive")
	}
	return nil
}

func (l *LoanPay) Flatten() (map[string]any, error) { return tx.ReflectFlatten(l) }
func (l *LoanPay) RequiredAmendments() [][32]byte   { return requiredLending() }
