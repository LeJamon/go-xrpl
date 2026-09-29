package invariants

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

// XLS-66 lending invariants, ported from rippled InvariantCheck.cpp
// (ValidLoanBroker, ValidLoan).
const lsfLoanOverpaymentFlag = entry.LsfLoanOverpayment

type decodedLendingEntry struct {
	typ   entry.Type
	model entry.Entry
}

func decodeEntry(data []byte) (decodedLendingEntry, error) {
	typ, err := state.DecodeType(data)
	if err != nil {
		return decodedLendingEntry{}, err
	}
	model := entry.New(typ)
	if model == nil {
		return decodedLendingEntry{}, fmt.Errorf("no generated decoder for %s", typ)
	}
	if err := model.Decode(data); err != nil {
		return decodedLendingEntry{}, err
	}
	return decodedLendingEntry{typ: typ, model: model}, nil
}

// numFieldIsNegative reports whether a NUMBER field is present and negative (the
// codec renders negatives with a leading '-').
func numFieldIsNegative(fields decodedLendingEntry, key string) bool {
	v, ok := fields.number(key)
	return ok && strings.HasPrefix(v, "-")
}

func u32FieldPresent(fields decodedLendingEntry, key string) (uint32, bool) {
	return fields.uint32(key)
}

func u32Field(fields decodedLendingEntry, key string) uint32 {
	v, _ := u32FieldPresent(fields, key)
	return v
}

func i32Field(fields decodedLendingEntry, key string) int {
	v, _ := fields.int32(key)
	return int(v)
}

func generatedUint32(has func() bool, get func() (uint32, error)) (uint32, bool) {
	if !has() {
		return 0, false
	}
	value, err := get()
	return value, err == nil
}

func lendingViolation(name, msg string) *InvariantViolation {
	return &InvariantViolation{Name: name, Message: msg}
}

func (d decodedLendingEntry) number(key string) (string, bool) {
	get := func(present bool, value entry.NumberValue, err error) (string, bool) {
		if err != nil {
			return "", false
		}
		if !present {
			return "", true
		}
		return value, true
	}
	switch model := d.model.(type) {
	case *entry.Loan:
		switch key {
		case "LoanOriginationFee":
			value, err := model.GetLoanOriginationFee()
			return get(model.HasLoanOriginationFee(), value, err)
		case "LoanServiceFee":
			value, err := model.GetLoanServiceFee()
			return get(model.HasLoanServiceFee(), value, err)
		case "LatePaymentFee":
			value, err := model.GetLatePaymentFee()
			return get(model.HasLatePaymentFee(), value, err)
		case "ClosePaymentFee":
			value, err := model.GetClosePaymentFee()
			return get(model.HasClosePaymentFee(), value, err)
		case "PeriodicPayment":
			value, err := model.GetPeriodicPayment()
			return get(model.HasPeriodicPayment(), value, err)
		case "PrincipalOutstanding":
			value, err := model.GetPrincipalOutstanding()
			return get(model.HasPrincipalOutstanding(), value, err)
		case "TotalValueOutstanding":
			value, err := model.GetTotalValueOutstanding()
			return get(model.HasTotalValueOutstanding(), value, err)
		case "ManagementFeeOutstanding":
			value, err := model.GetManagementFeeOutstanding()
			return get(model.HasManagementFeeOutstanding(), value, err)
		}
	case *entry.LoanBroker:
		switch key {
		case "DebtTotal":
			value, err := model.GetDebtTotal()
			return get(model.HasDebtTotal(), value, err)
		case "DebtMaximum":
			value, err := model.GetDebtMaximum()
			return get(model.HasDebtMaximum(), value, err)
		case "CoverAvailable":
			value, err := model.GetCoverAvailable()
			return get(model.HasCoverAvailable(), value, err)
		}
	case *entry.Vault:
		switch key {
		case "AssetsTotal":
			value, err := model.GetAssetsTotal()
			return get(model.HasAssetsTotal(), value, err)
		case "AssetsAvailable":
			value, err := model.GetAssetsAvailable()
			return get(model.HasAssetsAvailable(), value, err)
		case "AssetsMaximum":
			value, err := model.GetAssetsMaximum()
			return get(model.HasAssetsMaximum(), value, err)
		case "LossUnrealized":
			value, err := model.GetLossUnrealized()
			return get(model.HasLossUnrealized(), value, err)
		}
	}
	return "", false
}

func (d decodedLendingEntry) uint32(key string) (uint32, bool) {
	switch model := d.model.(type) {
	case *entry.Loan:
		switch key {
		case "LoanSequence":
			return generatedUint32(model.HasLoanSequence, model.GetLoanSequence)
		case "OverpaymentFee":
			return generatedUint32(model.HasOverpaymentFee, model.GetOverpaymentFee)
		case "InterestRate":
			return generatedUint32(model.HasInterestRate, model.GetInterestRate)
		case "LateInterestRate":
			return generatedUint32(model.HasLateInterestRate, model.GetLateInterestRate)
		case "CloseInterestRate":
			return generatedUint32(model.HasCloseInterestRate, model.GetCloseInterestRate)
		case "OverpaymentInterestRate":
			return generatedUint32(model.HasOverpaymentInterestRate, model.GetOverpaymentInterestRate)
		case "StartDate":
			return generatedUint32(model.HasStartDate, model.GetStartDate)
		case "PaymentInterval":
			return generatedUint32(model.HasPaymentInterval, model.GetPaymentInterval)
		case "GracePeriod":
			return generatedUint32(model.HasGracePeriod, model.GetGracePeriod)
		case "PreviousPaymentDueDate":
			return generatedUint32(model.HasPreviousPaymentDueDate, model.GetPreviousPaymentDueDate)
		case "NextPaymentDueDate":
			return generatedUint32(model.HasNextPaymentDueDate, model.GetNextPaymentDueDate)
		case "PaymentRemaining":
			return generatedUint32(model.HasPaymentRemaining, model.GetPaymentRemaining)
		case "Flags":
			return generatedUint32(model.HasFlags, model.GetFlags)
		}
	case *entry.LoanBroker:
		switch key {
		case "Sequence":
			return generatedUint32(model.HasSequence, model.GetSequence)
		case "LoanSequence":
			return generatedUint32(model.HasLoanSequence, model.GetLoanSequence)
		case "OwnerCount":
			return generatedUint32(model.HasOwnerCount, model.GetOwnerCount)
		case "Flags":
			return generatedUint32(model.HasFlags, model.GetFlags)
		}
	case *entry.Vault:
		switch key {
		case "VaultKind":
			value, err := model.GetVaultKind()
			if !model.HasVaultKind() || err != nil {
				return 0, false
			}
			return uint32(value), true
		case "SubscriptionDate":
			return generatedUint32(model.HasSubscriptionDate, model.GetSubscriptionDate)
		case "RedemptionDate":
			return generatedUint32(model.HasRedemptionDate, model.GetRedemptionDate)
		case "Flags":
			return generatedUint32(model.HasFlags, model.GetFlags)
		}
	}
	return 0, false
}

func (d decodedLendingEntry) int32(key string) (int32, bool) {
	model, ok := d.model.(*entry.Loan)
	if !ok || key != "LoanScale" {
		return 0, false
	}
	value, err := model.GetLoanScale()
	return value, err == nil && model.HasLoanScale()
}

func (d decodedLendingEntry) hash32(key string) ([32]byte, bool) {
	switch model := d.model.(type) {
	case *entry.Loan:
		if key == "LoanBrokerID" && model.HasLoanBrokerID() {
			value, err := model.GetLoanBrokerID()
			return value, err == nil
		}
	case *entry.LoanBroker:
		if key == "VaultID" && model.HasVaultID() {
			value, err := model.GetVaultID()
			return value, err == nil
		}
	}
	return [32]byte{}, false
}

func (d decodedLendingEntry) account(key string) ([20]byte, bool) {
	if key != "Account" {
		return [20]byte{}, false
	}
	switch model := d.model.(type) {
	case *entry.LoanBroker:
		if model.HasAccount() {
			value, err := model.GetAccount()
			return value, err == nil
		}
	case *entry.Vault:
		if model.HasAccount() {
			value, err := model.GetAccount()
			return value, err == nil
		}
	}
	return [20]byte{}, false
}

func (d decodedLendingEntry) issue(key string) (entry.IssueValue, bool) {
	model, ok := d.model.(*entry.Vault)
	if !ok || key != "Asset" || !model.HasAsset() {
		return entry.IssueValue{}, false
	}
	value, err := model.GetAsset()
	return value, err == nil
}

func loanNumber(fields decodedLendingEntry, key string, numberContext state.NumberContext) (state.XRPLNumber, bool) {
	zero := numberContext.Int(0)
	s, ok := fields.number(key)
	if !ok {
		return zero, false
	}
	if s == "" || s == "0" {
		return zero, true
	}
	n, ok := vault.ParseLedgerNumberWithNumberContext(s, numberContext)
	return n, ok
}

func loanAllZero(fields decodedLendingEntry, numberContext state.NumberContext) (bool, bool) {
	for _, field := range []string{"TotalValueOutstanding", "PrincipalOutstanding", "ManagementFeeOutstanding"} {
		n, ok := loanNumber(fields, field, numberContext)
		if !ok {
			return false, false
		}
		if !n.IsZero() {
			return false, true
		}
	}
	return true, true
}

func loanFlag(fields decodedLendingEntry, flag uint32) bool {
	return u32Field(fields, "Flags")&flag != 0
}

func loanDueDate(fields decodedLendingEntry) (uint32, bool) {
	loan, ok := fields.model.(*entry.Loan)
	if !ok {
		return 0, false
	}
	value, err := loan.GetNextPaymentDueDate()
	return value, err == nil
}

func transactionType(txn Transaction) TxType {
	if txn == nil {
		return TxType(0)
	}
	return txn.TxType()
}

func checkValidLoanForTx(txn Transaction, result Result, entries []InvariantEntry, view ReadView, rules *amendment.Rules, numberContexts ...state.NumberContext) *InvariantViolation {
	if rules == nil {
		return nil
	}
	numberContext := tx.NumberContextForRules(rules)
	if len(numberContexts) > 0 {
		numberContext = numberContexts[0]
	}
	lpV11 := rules.Enabled(amendment.FeatureLendingProtocolV1_1)
	txType := transactionType(txn)
	var deletedLoans int

	for _, e := range entries {
		if e.EntryType != entry.TypeLoan {
			continue
		}
		if e.IsDelete || e.After == nil {
			if e.IsDelete {
				deletedLoans++
			}
			if lpV11 {
				continue
			}
		}

		afterData := e.After
		if afterData == nil {
			afterData = e.DeleteFinal
			if afterData == nil {
				afterData = e.Before
			}
		}
		if afterData == nil {
			continue
		}
		after, err := decodeEntry(afterData)
		if err != nil {
			return lendingViolation("ValidLoan", fmt.Sprintf("could not decode Loan: %v", err))
		}
		var before decodedLendingEntry
		beforePresent := false
		if e.Before != nil {
			before, err = decodeEntry(e.Before)
			if err != nil {
				return lendingViolation("ValidLoan", fmt.Sprintf("could not decode prior Loan: %v", err))
			}
			beforePresent = true
		}
		if !beforePresent && result == TesSUCCESS && view != nil {
			vaultData, found, violation := loanVaultForSchedule(view, after)
			if violation != nil {
				return violation
			}
			if found {
				if violation := checkLoanRedemptionSchedule(after, vaultData); violation != nil {
					return violation
				}
			}
		}

		paymentRemaining := u32Field(after, "PaymentRemaining")
		allZero, fieldsValid := loanAllZero(after, numberContext)
		if !fieldsValid {
			return lendingViolation("ValidLoan", "loan outstanding amount is malformed")
		}
		if paymentRemaining == 0 && !allZero {
			return lendingViolation("ValidLoan", "loan with zero payments remaining is not paid off")
		}
		if paymentRemaining != 0 && allZero {
			return lendingViolation("ValidLoan", "loan with payments remaining is fully paid off")
		}

		if !lpV11 && beforePresent && loanFlag(before, lsfLoanOverpaymentFlag) != loanFlag(after, lsfLoanOverpaymentFlag) {
			return lendingViolation("ValidLoan", "loan overpayment flag changed")
		}
		for _, field := range []string{"LoanServiceFee", "LatePaymentFee", "ClosePaymentFee", "PrincipalOutstanding", "TotalValueOutstanding", "ManagementFeeOutstanding"} {
			n, ok := loanNumber(after, field, numberContext)
			if !ok {
				return lendingViolation("ValidLoan", field+" is malformed")
			}
			if n.Signum() < 0 {
				return lendingViolation("ValidLoan", field+" is negative")
			}
		}
		periodicPayment, ok := loanNumber(after, "PeriodicPayment", numberContext)
		if !ok {
			return lendingViolation("ValidLoan", "PeriodicPayment is malformed")
		}
		if periodicPayment.Signum() <= 0 {
			return lendingViolation("ValidLoan", "PeriodicPayment is zero or negative")
		}

		if !lpV11 {
			continue
		}
		if !beforePresent && txType != protocol.TxTypeLoanSet {
			return lendingViolation("ValidLoan", "loan created by a transaction other than LoanSet")
		}
		if paymentRemaining == 0 {
			nextDue, ok := loanDueDate(after)
			if !ok {
				return lendingViolation("ValidLoan", "NextPaymentDueDate is malformed")
			}
			if nextDue != 0 {
				return lendingViolation("ValidLoan", "loan with zero payments must have zero next payment due date")
			}
		}
		if beforePresent {
			if loanFlag(before, entry.LsfLoanImpaired) != loanFlag(after, entry.LsfLoanImpaired) &&
				txType != protocol.TxTypeLoanManage && txType != protocol.TxTypeLoanPay {
				return lendingViolation("ValidLoan", "loan impaired flag changed outside LoanManage or LoanPay")
			}
			if loanFlag(before, entry.LsfLoanDefault) != loanFlag(after, entry.LsfLoanDefault) && txType != protocol.TxTypeLoanManage {
				return lendingViolation("ValidLoan", "loan default flag changed outside LoanManage")
			}
		}

		if view != nil {
			broker, vaultData, violation := loanBrokerVault(view, after)
			if violation != nil {
				return violation
			}
			vaultFields, decodeErr := decodeEntry(vaultData)
			if decodeErr != nil {
				return lendingViolation("ValidLoan", fmt.Sprintf("could not decode Vault: %v", decodeErr))
			}
			if violation := checkLoanInterest(after, vaultFields, numberContext); violation != nil {
				return violation
			}
			_ = broker
		}

		if result == TesSUCCESS && txType == protocol.TxTypeLoanPay && beforePresent && paymentRemaining != 0 {
			beforePrincipal, bok := loanNumber(before, "PrincipalOutstanding", numberContext)
			afterPrincipal, aok := loanNumber(after, "PrincipalOutstanding", numberContext)
			beforeRemaining := u32Field(before, "PaymentRemaining")
			if !bok || !aok {
				return lendingViolation("ValidLoan", "loan principal is malformed")
			}
			if afterPrincipal.Cmp(beforePrincipal) > 0 {
				return lendingViolation("ValidLoan", "loan pay must not increase PrincipalOutstanding on a non-full-repayment")
			}
			beforeTotal, tok := loanNumber(before, "TotalValueOutstanding", numberContext)
			afterTotal, aok := loanNumber(after, "TotalValueOutstanding", numberContext)
			if !tok || !aok {
				return lendingViolation("ValidLoan", "loan total value is malformed")
			}
			if afterTotal.Cmp(beforeTotal) > 0 {
				return lendingViolation("ValidLoan", "loan pay must not increase TotalValueOutstanding on a non-full-repayment")
			}
			if afterPrincipal.Cmp(beforePrincipal) == 0 && afterTotal.Cmp(beforeTotal) == 0 {
				return lendingViolation("ValidLoan", "loan pay must decrease PrincipalOutstanding or TotalValueOutstanding on a non-full-repayment")
			}
			if paymentRemaining >= beforeRemaining {
				return lendingViolation("ValidLoan", "loan pay must decrease PaymentRemaining on a non-full-repayment")
			}
			beforeDue, bok := loanDueDate(before)
			afterDue, aok := loanDueDate(after)
			interval := u32Field(after, "PaymentInterval")
			if !bok || !aok || interval == 0 || afterDue <= beforeDue || (afterDue-beforeDue)%interval != 0 {
				return lendingViolation("ValidLoan", "loan pay must advance NextPaymentDueDate by a positive multiple of PaymentInterval on a non-full-repayment")
			}
		}
	}

	if lpV11 && deletedLoans != 0 && txType != protocol.TxTypeLoanDelete {
		return lendingViolation("ValidLoan", "loan deleted by a transaction other than LoanDelete")
	}
	return nil
}

// Missing brokers and vaults do not impose a creation schedule constraint.
func loanVaultForSchedule(view ReadView, loan decodedLendingEntry) ([]byte, bool, *InvariantViolation) {
	brokerID, ok := loan.hash32("LoanBrokerID")
	if !ok {
		return nil, false, lendingViolation("ValidLoan", "loan broker ID is malformed")
	}
	var err error
	brokerData, err := view.Read(keylet.LoanBrokerByID(brokerID))
	if err != nil {
		return nil, false, lendingViolation("ValidLoan", fmt.Sprintf("could not read LoanBroker: %v", err))
	}
	if brokerData == nil {
		return nil, false, nil
	}
	broker, err := decodeEntry(brokerData)
	if err != nil {
		return nil, false, lendingViolation("ValidLoan", fmt.Sprintf("could not decode LoanBroker: %v", err))
	}
	vaultID, ok := broker.hash32("VaultID")
	if !ok {
		return nil, false, lendingViolation("ValidLoan", "loan broker vault ID is malformed")
	}
	vaultData, err := view.Read(keylet.VaultByID(vaultID))
	if err != nil {
		return nil, false, lendingViolation("ValidLoan", fmt.Sprintf("could not read Vault: %v", err))
	}
	if vaultData == nil {
		return nil, false, nil
	}
	return vaultData, true, nil
}

func loanBrokerVault(view ReadView, loan decodedLendingEntry) (decodedLendingEntry, []byte, *InvariantViolation) {
	brokerID, ok := loan.hash32("LoanBrokerID")
	if !ok {
		return decodedLendingEntry{}, nil, lendingViolation("ValidLoan", "loan broker ID is malformed")
	}
	var err error
	brokerData, err := view.Read(keylet.LoanBrokerByID(brokerID))
	if err != nil {
		return decodedLendingEntry{}, nil, lendingViolation("ValidLoan", fmt.Sprintf("could not read LoanBroker: %v", err))
	}
	if brokerData == nil {
		return decodedLendingEntry{}, nil, lendingViolation("ValidLoan", "loan broker does not exist")
	}
	broker, err := decodeEntry(brokerData)
	if err != nil {
		return decodedLendingEntry{}, nil, lendingViolation("ValidLoan", fmt.Sprintf("could not decode LoanBroker: %v", err))
	}
	vaultID, ok := broker.hash32("VaultID")
	if !ok {
		return decodedLendingEntry{}, nil, lendingViolation("ValidLoan", "loan broker vault ID is malformed")
	}
	vaultData, err := view.Read(keylet.VaultByID(vaultID))
	if err != nil {
		return decodedLendingEntry{}, nil, lendingViolation("ValidLoan", fmt.Sprintf("could not read Vault: %v", err))
	}
	if vaultData == nil {
		return decodedLendingEntry{}, nil, lendingViolation("ValidLoan", "loan broker vault does not exist")
	}
	return broker, vaultData, nil
}

func checkLoanRedemptionSchedule(loan decodedLendingEntry, vaultData []byte) *InvariantViolation {
	vaultFields, err := decodeEntry(vaultData)
	if err != nil {
		return lendingViolation("ValidLoan", fmt.Sprintf("could not decode Vault: %v", err))
	}
	vaultKind, present := u32FieldPresent(vaultFields, "VaultKind")
	if !present || uint8(vaultKind) != vault.VaultKindClosedEnded {
		return nil
	}
	start, present := u32FieldPresent(loan, "StartDate")
	if !present {
		return lendingViolation("ValidLoan", "closed-ended loan StartDate is malformed")
	}
	interval, present := u32FieldPresent(loan, "PaymentInterval")
	if !present {
		return lendingViolation("ValidLoan", "closed-ended loan PaymentInterval is malformed")
	}
	remaining := uint64(u32Field(loan, "PaymentRemaining"))
	redemption, present := u32FieldPresent(vaultFields, "RedemptionDate")
	if !present {
		return lendingViolation("ValidLoan", "closed-ended vault RedemptionDate is malformed")
	}
	if uint64(start)+uint64(interval)*remaining+vault.LoanRedemptionBuffer > uint64(redemption) {
		return lendingViolation("ValidLoan", "closed-ended loan final payment must precede RedemptionDate by at least the redemption buffer")
	}
	return nil
}

func checkLoanInterest(loan, vaultData decodedLendingEntry, numberContext state.NumberContext) *InvariantViolation {
	vaultAsset, ok := loanVaultAsset(vaultData, numberContext.Scale())
	if !ok {
		return lendingViolation("ValidLoan", "loan broker vault asset is malformed")
	}
	total, tok := loanNumber(loan, "TotalValueOutstanding", numberContext)
	principal, pok := loanNumber(loan, "PrincipalOutstanding", numberContext)
	management, mok := loanNumber(loan, "ManagementFeeOutstanding", numberContext)
	if !tok || !pok || !mok {
		return lendingViolation("ValidLoan", "loan outstanding amount is malformed")
	}
	interestDue := total.Sub(principal).Sub(management)
	if vaultAsset.integral() {
		if interestDue.Signum() < 0 {
			return lendingViolation("ValidLoan", "loan interest due is negative")
		}
		return nil
	}
	tolerance := state.NewXRPLNumberScaled(1, i32Field(loan, "LoanScale"), numberContext.Scale(), state.RoundToNearest)
	if interestDue.Cmp(tolerance.Negate()) < 0 {
		return lendingViolation("ValidLoan", "loan interest due is negative")
	}
	return nil
}

func loanVaultAsset(fields decodedLendingEntry, scale state.MantissaScale) (vvAsset, bool) {
	asset, ok := fields.issue("Asset")
	if !ok {
		return vvAsset{}, false
	}
	return vvAssetFromIssue(asset, scale)
}

func vvAssetFromIssue(issue entry.IssueValue, scale state.MantissaScale) (vvAsset, bool) {
	if issue.MPTIssuanceID != "" {
		decoded, err := hex.DecodeString(issue.MPTIssuanceID)
		if err != nil || len(decoded) != 24 {
			return vvAsset{}, false
		}
		asset := vvAsset{isMPT: true, numberScale: scale}
		copy(asset.mptID[:], decoded)
		return asset, true
	}
	if isNativeXRPCurrency(issue.Currency) && issue.Issuer == "" {
		return vvAsset{isXRP: true, numberScale: scale}, true
	}
	asset := vvAsset{currency: issue.Currency, numberScale: scale}
	issuer, err := state.DecodeAccountID(issue.Issuer)
	if err != nil {
		return vvAsset{}, false
	}
	asset.issuer = issuer
	return asset, true
}

func checkValidLoanBroker(entries []InvariantEntry, view ReadView, rules *amendment.Rules, numberContexts ...state.NumberContext) *InvariantViolation {
	return checkValidLoanBrokerForTx(nil, entries, view, rules, numberContexts...)
}

func checkValidLoanBrokerForTx(txn Transaction, entries []InvariantEntry, view ReadView, rules *amendment.Rules, numberContexts ...state.NumberContext) *InvariantViolation {
	if rules == nil {
		return nil
	}
	numberContext := tx.NumberContextForRules(rules)
	if len(numberContexts) > 0 {
		numberContext = numberContexts[0]
	}
	lpV11 := rules.Enabled(amendment.FeatureLendingProtocolV1_1)
	txType := transactionType(txn)

	type brokerState struct {
		before []byte
		after  []byte
		key    [32]byte
	}
	brokers := make(map[[32]byte]brokerState)
	var unkeyedBrokers []brokerState
	var deletedBrokers []brokerState
	var lines, mpts [][]byte
	addBroker := func(id [32]byte) {
		if _, ok := brokers[id]; !ok {
			brokers[id] = brokerState{key: id}
		}
	}
	addBrokerAccount := func(data []byte) *InvariantViolation {
		const loanBrokerIDFieldCode = 37
		err := state.WalkFields(data, func(field state.Field) error {
			if field.TypeCode == state.FieldTypeHash256 && field.FieldCode == loanBrokerIDFieldCode {
				addBroker(field.Hash256())
			}
			return nil
		})
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode AccountRoot: %v", err))
		}
		return nil
	}

	for _, e := range entries {
		if e.EntryType == entry.TypeLoanBroker {
			if e.IsDelete || e.After == nil {
				beforeData := e.Before
				finalData := e.DeleteFinal
				if finalData == nil {
					finalData = beforeData
				}
				deletedBrokers = append(deletedBrokers, brokerState{before: beforeData, after: finalData, key: e.Key})
				if e.Key != ([32]byte{}) {
					// Erased brokers still undergo the ordinary consistency checks.
					brokers[e.Key] = brokerState{before: beforeData, after: finalData, key: e.Key}
				} else if finalData != nil {
					unkeyedBrokers = append(unkeyedBrokers, brokerState{before: beforeData, after: finalData})
				}
				continue
			}
			broker := brokerState{before: e.Before, after: e.After, key: e.Key}
			if e.Key == ([32]byte{}) {
				unkeyedBrokers = append(unkeyedBrokers, broker)
			} else {
				brokers[e.Key] = broker
			}
			continue
		}
		data := e.After
		if data == nil {
			data = e.DeleteFinal
		}
		if data == nil {
			continue
		}
		switch e.EntryType {
		case entry.TypeAccountRoot:
			if violation := addBrokerAccount(data); violation != nil {
				return violation
			}
		case entry.TypeRippleState:
			lines = append(lines, data)
		case entry.TypeMPToken:
			mpts = append(mpts, data)
		}
	}

	if lpV11 {
		if len(deletedBrokers) > 1 {
			return lendingViolation("ValidLoanBroker", "more than one LoanBroker deleted in a single transaction")
		}
		for _, deleted := range deletedBrokers {
			if txn != nil && txType != protocol.TxTypeLoanBrokerDelete {
				return lendingViolation("ValidLoanBroker", "LoanBroker deleted by a transaction other than LoanBrokerDelete")
			}
			// DebtTotal and OwnerCount authorize deletion from the original
			// pre-transaction image. DeleteFinal may already contain cleanup
			// mutations and cannot establish the deletion privilege.
			data := deleted.before
			if data == nil {
				data = deleted.after
			}
			if data == nil {
				return lendingViolation("ValidLoanBroker", "deleted LoanBroker has no pre-transaction image")
			}
			fields, err := decodeEntry(data)
			if err != nil {
				return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode deleted LoanBroker: %v", err))
			}
			if u32Field(fields, "OwnerCount") != 0 {
				return lendingViolation("ValidLoanBroker", "LoanBroker deleted with non-zero owner count")
			}
			debt, ok := loanNumber(fields, "DebtTotal", numberContext)
			if !ok {
				return lendingViolation("ValidLoanBroker", "deleted LoanBroker debt total is malformed")
			}
			if !debt.IsZero() {
				if view == nil {
					return lendingViolation("ValidLoanBroker", "deleted LoanBroker debt has no live Vault scale")
				}
				vaultID, ok := fields.hash32("VaultID")
				if !ok {
					return lendingViolation("ValidLoanBroker", "deleted LoanBroker vault ID is malformed")
				}
				vaultData, err := view.Read(keylet.VaultByID(vaultID))
				if err != nil || vaultData == nil {
					return lendingViolation("ValidLoanBroker", "deleted LoanBroker debt has no live Vault scale")
				}
				vaultFields, err := decodeEntry(vaultData)
				if err != nil {
					return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode Vault: %v", err))
				}
				asset, assetOK := loanVaultAsset(vaultFields, numberContext.Scale())
				if !assetOK {
					return lendingViolation("ValidLoanBroker", "deleted LoanBroker vault asset is malformed")
				}
				assetsTotal, totalOK := loanNumber(vaultFields, "AssetsTotal", numberContext)
				if !totalOK {
					return lendingViolation("ValidLoanBroker", "deleted LoanBroker vault total is malformed")
				}
				scale := asset.scaleOf(assetsTotal)
				if !asset.roundMode(debt, scale, state.RoundTowardsZero).IsZero() {
					return lendingViolation("ValidLoanBroker", "LoanBroker deleted with non-zero debt total")
				}
			}
		}
	}
	if view == nil {
		return nil
	}

	addBrokerForAccount := func(accountID [20]byte) *InvariantViolation {
		data, err := view.Read(keylet.Account(accountID))
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not read account: %v", err))
		}
		if data == nil {
			return nil
		}
		if violation := addBrokerAccount(data); violation != nil {
			return violation
		}
		return nil
	}
	for _, data := range lines {
		line, err := state.ParseRippleState(data)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode RippleState: %v", err))
		}
		for _, address := range []string{line.LowLimit.Issuer, line.HighLimit.Issuer} {
			accountID, err := state.DecodeAccountID(address)
			if err != nil {
				return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode trust line account: %v", err))
			}
			if violation := addBrokerForAccount(accountID); violation != nil {
				return violation
			}
		}
	}
	for _, data := range mpts {
		token, err := state.ParseMPToken(data)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode MPToken: %v", err))
		}
		if violation := addBrokerForAccount(token.Account); violation != nil {
			return violation
		}
	}

	goodZeroDirectory := func(accountID [20]byte) *InvariantViolation {
		data, err := view.Read(keylet.OwnerDir(accountID))
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not read owner directory: %v", err))
		}
		if data == nil {
			return nil
		}
		dir, err := state.ParseDirectoryNode(data)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode owner directory: %v", err))
		}
		if dir.IndexNext != 0 || dir.IndexPrevious != 0 {
			return lendingViolation("ValidLoanBroker", "LoanBroker with zero OwnerCount has multiple directory pages")
		}
		if len(dir.Indexes) > 1 {
			return lendingViolation("ValidLoanBroker", "LoanBroker with zero OwnerCount has multiple indexes in the Directory root")
		}
		if len(dir.Indexes) == 1 {
			child, err := view.Read(keylet.Child(dir.Indexes[0]))
			if err != nil || child == nil {
				return lendingViolation("ValidLoanBroker", "LoanBroker directory is corrupt")
			}
			typ, err := state.DecodeType(child)
			if err != nil || (typ != entry.TypeRippleState && typ != entry.TypeMPToken) {
				return lendingViolation("ValidLoanBroker", "LoanBroker with zero OwnerCount has an unexpected entry in the directory")
			}
		}
		return nil
	}

	checkBroker := func(beforeData, afterData []byte) *InvariantViolation {
		after, err := decodeEntry(afterData)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode LoanBroker: %v", err))
		}
		if numFieldIsNegative(after, "DebtTotal") {
			return lendingViolation("ValidLoanBroker", "debt total is negative")
		}
		if numFieldIsNegative(after, "CoverAvailable") {
			return lendingViolation("ValidLoanBroker", "cover available is negative")
		}
		if beforeData != nil {
			before, berr := decodeEntry(beforeData)
			if berr != nil {
				return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode prior LoanBroker: %v", berr))
			}
			if u32Field(before, "LoanSequence") > u32Field(after, "LoanSequence") {
				return lendingViolation("ValidLoanBroker", "loan sequence number decreased")
			}
		}
		vaultID, ok := after.hash32("VaultID")
		if !ok {
			return lendingViolation("ValidLoanBroker", "loan broker has no vault ID")
		}
		vaultData, err := view.Read(keylet.VaultByID(vaultID))
		if err != nil || vaultData == nil {
			return lendingViolation("ValidLoanBroker", "loan broker vault ID is invalid")
		}
		pseudoID, ok := after.account("Account")
		if !ok {
			return lendingViolation("ValidLoanBroker", "loan broker has no account")
		}
		pseudoBalance, ok := vault.PseudoAssetHoldsWithNumberContext(view, pseudoID, vaultData, numberContext)
		if !ok {
			return lendingViolation("ValidLoanBroker", "could not read pseudo-account asset balance")
		}
		coverAvailable, ok := loanNumber(after, "CoverAvailable", numberContext)
		if !ok {
			return lendingViolation("ValidLoanBroker", "cover available is malformed")
		}
		if coverAvailable.Cmp(pseudoBalance) < 0 {
			return lendingViolation("ValidLoanBroker", "cover available is less than pseudo-account asset balance")
		}
		if rules.Enabled(amendment.FeatureFixCleanup3_1_3) && txType != protocol.TxTypeLoanBrokerDelete && coverAvailable.Cmp(pseudoBalance) > 0 {
			return lendingViolation("ValidLoanBroker", "cover available is greater than pseudo-account asset balance")
		}
		if u32Field(after, "OwnerCount") == 0 {
			if violation := goodZeroDirectory(pseudoID); violation != nil {
				return violation
			}
		}
		return nil
	}

	for _, broker := range unkeyedBrokers {
		if violation := checkBroker(broker.before, broker.after); violation != nil {
			return violation
		}
	}
	for brokerID, broker := range brokers {
		after := broker.after
		if after == nil {
			var err error
			after, err = view.Read(keylet.LoanBrokerByID(brokerID))
			if err != nil {
				return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not read LoanBroker: %v", err))
			}
			if after == nil {
				return lendingViolation("ValidLoanBroker", "loan broker is missing")
			}
		}
		if violation := checkBroker(broker.before, after); violation != nil {
			return violation
		}
	}
	return nil
}
