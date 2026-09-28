package invariants

import (
	"reflect"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// ---------------------------------------------------------------------------
// NoModifiedUnmodifiableFields
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — NoModifiedUnmodifiableFields
//
// Fields declared unmodifiable must not change when an existing object is
// modified. Creation and deletion are ignored. Every ledger entry type shares
// the default rule that sfLedgerEntryType and sfLedgerIndex are immutable.
//
// Enforcement is gated on featureLendingProtocol (the amendment under which the
// check was introduced, even though it guards all entry types); while disabled
// the check never fails a transaction, matching rippled.

// sfLedgerIndex is a Hash256 field (code 6) that is not normally stored in an
// SLE — its ledger key is the index. rippled still treats it as unmodifiable.
const fieldCodeLedgerIndex = 6

func checkNoModifiedUnmodifiableFields(entries []InvariantEntry, rules *amendment.Rules) *InvariantViolation {
	if rules == nil || !rules.Enabled(amendment.FeatureLendingProtocol) {
		return nil
	}
	lpV11 := rules.Enabled(amendment.FeatureLendingProtocolV1_1)

	for _, e := range entries {
		// Only modifications are checked (before and after both present).
		if e.IsDelete || e.Before == nil || e.After == nil {
			continue
		}

		beforeType, beforeErr := state.DecodeType(e.Before)
		afterType, afterErr := state.DecodeType(e.After)
		if beforeErr != nil || afterErr != nil || beforeType != afterType || ledgerIndexChanged(e.Before, e.After) {
			return unmodifiableViolation()
		}

		before, beforeErr := decodeEntry(e.Before)
		after, afterErr := decodeEntry(e.After)
		if beforeErr != nil || afterErr != nil {
			return unmodifiableViolation()
		}

		fields := unmodifiableFields(e.EntryType, lpV11)
		for _, field := range fields {
			if fieldChanged(before, after, field) {
				return unmodifiableViolation()
			}
		}

		if e.EntryType == entry.TypeLoan && lpV11 {
			beforeFlags := u32Field(before, "Flags")
			afterFlags := u32Field(after, "Flags")
			if (beforeFlags^afterFlags)&entry.LsfLoanOverpayment != 0 {
				return unmodifiableViolation()
			}
			// lsfLoanDefault may be set by a valid lifecycle transition, but
			// clearing it would erase a recorded default.
			if beforeFlags&entry.LsfLoanDefault != 0 && afterFlags&entry.LsfLoanDefault == 0 {
				return unmodifiableViolation()
			}
		}
	}
	return nil
}

func unmodifiableViolation() *InvariantViolation {
	return &InvariantViolation{
		Name:    "NoModifiedUnmodifiableFields",
		Message: "changed an unmodifiable field",
	}
}

func fieldChanged(before, after decodedLendingEntry, name string) bool {
	b, bok := lendingFieldValue(before, name)
	a, aok := lendingFieldValue(after, name)
	if bok != aok {
		return true
	}
	return bok && !reflect.DeepEqual(b, a)
}

func typedLendingField[T any](has func() bool, get func() (T, error)) (any, bool) {
	if !has() {
		return nil, false
	}
	value, err := get()
	if err != nil {
		return nil, true
	}
	return value, true
}

func lendingFieldValue(decoded decodedLendingEntry, name string) (any, bool) {
	switch model := decoded.model.(type) {
	case *entry.LoanBroker:
		switch name {
		case "Sequence":
			return typedLendingField(model.HasSequence, model.GetSequence)
		case "OwnerNode":
			return typedLendingField(model.HasOwnerNode, model.GetOwnerNode)
		case "VaultNode":
			return typedLendingField(model.HasVaultNode, model.GetVaultNode)
		case "VaultID":
			return typedLendingField(model.HasVaultID, model.GetVaultID)
		case "Account":
			return typedLendingField(model.HasAccount, model.GetAccount)
		case "Owner":
			return typedLendingField(model.HasOwner, model.GetOwner)
		case "ManagementFeeRate":
			return typedLendingField(model.HasManagementFeeRate, model.GetManagementFeeRate)
		case "CoverRateMinimum":
			return typedLendingField(model.HasCoverRateMinimum, model.GetCoverRateMinimum)
		case "CoverRateLiquidation":
			return typedLendingField(model.HasCoverRateLiquidation, model.GetCoverRateLiquidation)
		}
	case *entry.Loan:
		switch name {
		case "LoanSequence":
			return typedLendingField(model.HasLoanSequence, model.GetLoanSequence)
		case "OwnerNode":
			return typedLendingField(model.HasOwnerNode, model.GetOwnerNode)
		case "LoanBrokerNode":
			return typedLendingField(model.HasLoanBrokerNode, model.GetLoanBrokerNode)
		case "LoanBrokerID":
			return typedLendingField(model.HasLoanBrokerID, model.GetLoanBrokerID)
		case "Borrower":
			return typedLendingField(model.HasBorrower, model.GetBorrower)
		case "LoanOriginationFee":
			return typedLendingField(model.HasLoanOriginationFee, model.GetLoanOriginationFee)
		case "LoanServiceFee":
			return typedLendingField(model.HasLoanServiceFee, model.GetLoanServiceFee)
		case "LatePaymentFee":
			return typedLendingField(model.HasLatePaymentFee, model.GetLatePaymentFee)
		case "ClosePaymentFee":
			return typedLendingField(model.HasClosePaymentFee, model.GetClosePaymentFee)
		case "OverpaymentFee":
			return typedLendingField(model.HasOverpaymentFee, model.GetOverpaymentFee)
		case "InterestRate":
			return typedLendingField(model.HasInterestRate, model.GetInterestRate)
		case "LateInterestRate":
			return typedLendingField(model.HasLateInterestRate, model.GetLateInterestRate)
		case "CloseInterestRate":
			return typedLendingField(model.HasCloseInterestRate, model.GetCloseInterestRate)
		case "OverpaymentInterestRate":
			return typedLendingField(model.HasOverpaymentInterestRate, model.GetOverpaymentInterestRate)
		case "StartDate":
			return typedLendingField(model.HasStartDate, model.GetStartDate)
		case "PaymentInterval":
			return typedLendingField(model.HasPaymentInterval, model.GetPaymentInterval)
		case "GracePeriod":
			return typedLendingField(model.HasGracePeriod, model.GetGracePeriod)
		case "LoanScale":
			if !model.HasLoanScale() {
				return nil, false
			}
			return model.LoanScale, true
		}
	case *entry.Vault:
		switch name {
		case "VaultKind":
			return typedLendingField(model.HasVaultKind, model.GetVaultKind)
		case "SubscriptionDate":
			return typedLendingField(model.HasSubscriptionDate, model.GetSubscriptionDate)
		case "RedemptionDate":
			return typedLendingField(model.HasRedemptionDate, model.GetRedemptionDate)
		case "Sequence":
			return typedLendingField(model.HasSequence, model.GetSequence)
		case "OwnerNode":
			return typedLendingField(model.HasOwnerNode, model.GetOwnerNode)
		case "Owner":
			return typedLendingField(model.HasOwner, model.GetOwner)
		case "WithdrawalPolicy":
			return typedLendingField(model.HasWithdrawalPolicy, model.GetWithdrawalPolicy)
		case "Scale":
			return typedLendingField(model.HasScale, model.GetScale)
		case "LEVersion":
			return typedLendingField(model.HasLEVersion, model.GetLEVersion)
		case "Asset":
			return typedLendingField(model.HasAsset, model.GetAsset)
		case "Account":
			return typedLendingField(model.HasAccount, model.GetAccount)
		case "ShareMPTID":
			return typedLendingField(model.HasShareMPTID, model.GetShareMPTID)
		}
	}
	return nil, false
}

// unmodifiableFields is the field list from rippled's
// NoModifiedUnmodifiableFields. Vault structural fields were moved here when
// LendingProtocolV1_1 activated; before that amendment ValidVault owns the
// Asset/Account/ShareMPTID checks.
func unmodifiableFields(typ entry.Type, lpV11 bool) []string {
	switch typ {
	case entry.TypeLoanBroker:
		return []string{
			"Sequence", "OwnerNode", "VaultNode", "VaultID", "Account", "Owner",
			"ManagementFeeRate", "CoverRateMinimum", "CoverRateLiquidation",
		}
	case entry.TypeLoan:
		return []string{
			"Sequence", "OwnerNode", "LoanBrokerNode", "LoanBrokerID", "Borrower",
			"LoanOriginationFee", "LoanServiceFee", "LatePaymentFee", "ClosePaymentFee",
			"OverpaymentFee", "InterestRate", "LateInterestRate", "CloseInterestRate",
			"OverpaymentInterestRate", "StartDate", "PaymentInterval", "GracePeriod",
			"LoanScale",
		}
	case entry.TypeVault:
		if !lpV11 {
			return nil
		}
		return []string{
			"VaultKind", "SubscriptionDate", "RedemptionDate", "Sequence", "OwnerNode",
			"Owner", "WithdrawalPolicy", "Scale", "LEVersion", "Asset", "Account",
			"ShareMPTID",
		}
	default:
		return nil
	}
}

// ledgerIndexChanged reports whether the sfLedgerIndex field appears, disappears,
// or changes value between the two images, mirroring rippled's fieldChanged.
func ledgerIndexChanged(before, after []byte) bool {
	bPresent, bVal := findHash256Field(before, fieldCodeLedgerIndex)
	aPresent, aVal := findHash256Field(after, fieldCodeLedgerIndex)
	if bPresent != aPresent {
		return true
	}
	return aPresent && bVal != aVal
}

// findHash256Field returns whether a Hash256 field with the given field code is
// present in the serialized SLE and, if so, its value.
func findHash256Field(data []byte, fieldCode int) (bool, [32]byte) {
	var found bool
	var val [32]byte
	_ = state.WalkFields(data, func(f state.Field) error {
		if f.TypeCode == state.FieldTypeHash256 && f.FieldCode == fieldCode {
			found = true
			val = f.Hash256()
			return errStopWalk
		}
		return nil
	})
	return found, val
}
