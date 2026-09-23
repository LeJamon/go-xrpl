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

func fieldChanged(before, after map[string]any, name string) bool {
	b, bok := before[name]
	a, aok := after[name]
	if bok != aok {
		return true
	}
	return bok && !reflect.DeepEqual(b, a)
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
