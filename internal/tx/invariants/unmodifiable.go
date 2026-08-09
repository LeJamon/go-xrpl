package invariants

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// ---------------------------------------------------------------------------
// NoModifiedUnmodifiableFields
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — NoModifiedUnmodifiableFields
//
// Fields declared unmodifiable must not change when an existing object is
// modified. Creation and deletion are ignored. Every ledger entry type shares
// the default rule that sfLedgerEntryType and sfLedgerIndex are immutable;
// Loan and LoanBroker entries additionally pin their structural fields, but
// those entry types are not yet implemented (tracked in #1245) so only the
// default rule can be exercised here.
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

	for _, e := range entries {
		// Only modifications are checked (before and after both present).
		if e.IsDelete || e.Before == nil || e.After == nil {
			continue
		}

		beforeType, beforeErr := state.DecodeType(e.Before)
		afterType, afterErr := state.DecodeType(e.After)
		if beforeErr != nil || afterErr != nil || beforeType != afterType || ledgerIndexChanged(e.Before, e.After) {
			return &InvariantViolation{
				Name:    "NoModifiedUnmodifiableFields",
				Message: "changed an unmodifiable field",
			}
		}
	}
	return nil
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
