package invariants

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
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

// decodeEntry decodes a serialized SLE into its field map.
func decodeEntry(data []byte) (map[string]any, error) {
	return binarycodec.Decode(hex.EncodeToString(data))
}

// numFieldIsNegative reports whether a NUMBER field is present and negative (the
// codec renders negatives with a leading '-').
func numFieldIsNegative(fields map[string]any, key string) bool {
	v, ok := fields[key].(string)
	return ok && strings.HasPrefix(v, "-")
}

// u32FieldPresent reads a UInt32 field, tolerating the codec's numeric representations.
func u32FieldPresent(fields map[string]any, key string) (uint32, bool) {
	if _, present := fields[key]; !present {
		return 0, false
	}
	switch v := fields[key].(type) {
	case uint8:
		return uint32(v), true
	case uint16:
		return uint32(v), true
	case uint32:
		return v, true
	case uint64:
		return uint32(v), true
	case int8:
		return uint32(v), true
	case int16:
		return uint32(v), true
	case int32:
		return uint32(v), true
	case int:
		return uint32(v), true
	case int64:
		return uint32(v), true
	case float64:
		return uint32(v), true
	default:
		return 0, false
	}
}

// u32Field reads a UInt32 field, tolerating the codec's numeric representations.
func u32Field(fields map[string]any, key string) uint32 {
	v, _ := u32FieldPresent(fields, key)
	return v
}

func i32Field(fields map[string]any, key string) int {
	switch v := fields[key].(type) {
	case int8:
		return int(v)
	case int16:
		return int(v)
	case int32:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case uint8:
		return int(v)
	case uint16:
		return int(v)
	case uint32:
		return int(v)
	case uint64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func lendingViolation(name, msg string) *InvariantViolation {
	return &InvariantViolation{Name: name, Message: msg}
}

func loanNumber(fields map[string]any, key string, numberContext state.NumberContext) (state.XRPLNumber, bool) {
	zero := numberContext.Int(0)
	raw, present := fields[key]
	if !present {
		return zero, true
	}
	s, ok := raw.(string)
	if !ok {
		return zero, false
	}
	if s == "" || s == "0" {
		return zero, true
	}
	n, ok := vault.ParseLedgerNumberWithNumberContext(s, numberContext)
	return n, ok
}

func loanAllZero(fields map[string]any, numberContext state.NumberContext) (bool, bool) {
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

func loanFlag(fields map[string]any, flag uint32) bool {
	return u32Field(fields, "Flags")&flag != 0
}

func loanDueDate(fields map[string]any) (uint32, bool) {
	raw, present := fields["NextPaymentDueDate"]
	if !present {
		return 0, true
	}
	switch v := raw.(type) {
	case uint8:
		return uint32(v), true
	case uint16:
		return uint32(v), true
	case uint32:
		return v, true
	case uint64:
		return uint32(v), true
	case int8:
		return uint32(v), true
	case int16:
		return uint32(v), true
	case int32:
		return uint32(v), true
	case int:
		return uint32(v), true
	case int64:
		return uint32(v), true
	case float64:
		return uint32(v), true
	default:
		return 0, false
	}
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
		var before map[string]any
		if e.Before != nil {
			before, err = decodeEntry(e.Before)
			if err != nil {
				return lendingViolation("ValidLoan", fmt.Sprintf("could not decode prior Loan: %v", err))
			}
		}
		if before == nil && result == TesSUCCESS && view != nil {
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

		if !lpV11 && before != nil && loanFlag(before, lsfLoanOverpaymentFlag) != loanFlag(after, lsfLoanOverpaymentFlag) {
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
		if before == nil && txType != protocol.TxTypeLoanSet {
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
		if before != nil {
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

		if result == TesSUCCESS && txType == protocol.TxTypeLoanPay && before != nil && paymentRemaining != 0 {
			beforePrincipal, bok := loanNumber(before, "PrincipalOutstanding", numberContext)
			afterPrincipal, aok := loanNumber(after, "PrincipalOutstanding", numberContext)
			beforeRemaining := u32Field(before, "PaymentRemaining")
			if !bok || !aok {
				return lendingViolation("ValidLoan", "loan principal is malformed")
			}
			if afterPrincipal.Cmp(beforePrincipal) >= 0 {
				return lendingViolation("ValidLoan", "loan pay must strictly decrease PrincipalOutstanding on a non-full-repayment")
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
func loanVaultForSchedule(view ReadView, loan map[string]any) ([]byte, bool, *InvariantViolation) {
	brokerIDText, ok := loan["LoanBrokerID"].(string)
	if !ok {
		return nil, false, lendingViolation("ValidLoan", "loan broker ID is malformed")
	}
	brokerID, err := hexDecode32(brokerIDText)
	if err != nil {
		return nil, false, lendingViolation("ValidLoan", "loan broker ID is malformed")
	}
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
	vaultIDText, ok := broker["VaultID"].(string)
	if !ok {
		return nil, false, lendingViolation("ValidLoan", "loan broker vault ID is malformed")
	}
	vaultID, err := hexDecode32(vaultIDText)
	if err != nil {
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

func loanBrokerVault(view ReadView, loan map[string]any) (map[string]any, []byte, *InvariantViolation) {
	brokerIDText, ok := loan["LoanBrokerID"].(string)
	if !ok {
		return nil, nil, lendingViolation("ValidLoan", "loan broker ID is malformed")
	}
	brokerID, err := hexDecode32(brokerIDText)
	if err != nil {
		return nil, nil, lendingViolation("ValidLoan", "loan broker ID is malformed")
	}
	brokerData, err := view.Read(keylet.LoanBrokerByID(brokerID))
	if err != nil {
		return nil, nil, lendingViolation("ValidLoan", fmt.Sprintf("could not read LoanBroker: %v", err))
	}
	if brokerData == nil {
		return nil, nil, lendingViolation("ValidLoan", "loan broker does not exist")
	}
	broker, err := decodeEntry(brokerData)
	if err != nil {
		return nil, nil, lendingViolation("ValidLoan", fmt.Sprintf("could not decode LoanBroker: %v", err))
	}
	vaultIDText, ok := broker["VaultID"].(string)
	if !ok {
		return nil, nil, lendingViolation("ValidLoan", "loan broker vault ID is malformed")
	}
	vaultID, err := hexDecode32(vaultIDText)
	if err != nil {
		return nil, nil, lendingViolation("ValidLoan", "loan broker vault ID is malformed")
	}
	vaultData, err := view.Read(keylet.VaultByID(vaultID))
	if err != nil {
		return nil, nil, lendingViolation("ValidLoan", fmt.Sprintf("could not read Vault: %v", err))
	}
	if vaultData == nil {
		return nil, nil, lendingViolation("ValidLoan", "loan broker vault does not exist")
	}
	return broker, vaultData, nil
}

func checkLoanRedemptionSchedule(loan map[string]any, vaultData []byte) *InvariantViolation {
	vaultFields, err := decodeEntry(vaultData)
	if err != nil {
		return lendingViolation("ValidLoan", fmt.Sprintf("could not decode Vault: %v", err))
	}
	vaultKind, present := vvU64Present(vaultFields, "VaultKind")
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
	redemption, present := vvU64Present(vaultFields, "RedemptionDate")
	if !present {
		return lendingViolation("ValidLoan", "closed-ended vault RedemptionDate is malformed")
	}
	if uint64(start)+uint64(interval)*remaining+vault.LoanRedemptionBuffer > redemption {
		return lendingViolation("ValidLoan", "closed-ended loan final payment must precede RedemptionDate by at least the redemption buffer")
	}
	return nil
}

func checkLoanInterest(loan, vaultData map[string]any, numberContext state.NumberContext) *InvariantViolation {
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

func loanVaultAsset(fields map[string]any, scale state.MantissaScale) (vvAsset, bool) {
	asset, ok := fields["Asset"].(map[string]any)
	if !ok {
		return vvAsset{}, false
	}
	return vvAssetFromMap(asset, scale), true
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
		account, err := state.ParseAccountRoot(data)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode AccountRoot: %v", err))
		}
		fields, err := decodeEntry(data)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode AccountRoot: %v", err))
		}
		if _, present := fields["LoanBrokerID"]; present {
			addBroker(account.LoanBrokerID)
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
				vaultIDText, ok := fields["VaultID"].(string)
				if !ok {
					return lendingViolation("ValidLoanBroker", "deleted LoanBroker vault ID is malformed")
				}
				vaultID, err := hexDecode32(vaultIDText)
				if err != nil {
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
		vaultIDText, ok := after["VaultID"].(string)
		if !ok {
			return lendingViolation("ValidLoanBroker", "loan broker has no vault ID")
		}
		vaultID, err := hexDecode32(vaultIDText)
		if err != nil {
			return lendingViolation("ValidLoanBroker", "loan broker vault ID is malformed")
		}
		vaultData, err := view.Read(keylet.VaultByID(vaultID))
		if err != nil || vaultData == nil {
			return lendingViolation("ValidLoanBroker", "loan broker vault ID is invalid")
		}
		pseudoAddr, ok := after["Account"].(string)
		if !ok {
			return lendingViolation("ValidLoanBroker", "loan broker has no account")
		}
		pseudoID, err := state.DecodeAccountID(pseudoAddr)
		if err != nil {
			return lendingViolation("ValidLoanBroker", "loan broker account is malformed")
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

// hexDecode32 decodes a 64-char hex string to a [32]byte.
func hexDecode32(s string) ([32]byte, error) {
	var h [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return h, err
	}
	if len(b) != 32 {
		return h, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(h[:], b)
	return h, nil
}
