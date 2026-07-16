package invariants

import "github.com/LeJamon/go-xrpl/amendment"

// ---------------------------------------------------------------------------
// ValidMPTIssuance
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidMPTIssuance (lines 1366-1534)
//
// visitEntry: counts created and deleted MPTokenIssuance and MPToken entries.
// finalize: switch on transaction type with specific count requirements.

func checkValidMPTIssuance(tx Transaction, result Result, entries []InvariantEntry, rules *amendment.Rules) *InvariantViolation {
	// visitEntry phase: count created/deleted MPTokenIssuance and MPToken entries.
	// In rippled, visitEntry receives (isDelete, before, after) where `after` is
	// always the SLE data (even for deletions). In Go's CollectEntries, deleted
	// entries have After=nil but EntryType is set from Before data. We use
	// EntryType + IsDelete + Before==nil to match rippled's counting logic:
	//   Created = !isDelete && before==nil  (entry with After data, no Before)
	//   Deleted = isDelete                  (entry marked as erased)
	var mptIssuancesCreated, mptIssuancesDeleted int
	var mptokensCreated, mptokensDeleted int

	for _, e := range entries {
		if e.EntryType == "MPTokenIssuance" {
			if e.IsDelete {
				mptIssuancesDeleted++
			} else if e.Before == nil {
				mptIssuancesCreated++
			}
		}
		if e.EntryType == "MPToken" {
			if e.IsDelete {
				mptokensDeleted++
			} else if e.Before == nil {
				mptokensCreated++
			}
		}
	}

	// finalize phase
	txType := tx.TxType()

	if result == TesSUCCESS {
		// A createMPTIssuance transaction (MPTokenIssuanceCreate/VaultCreate)
		// must create exactly 1 issuance and delete 0.
		if hasPrivilege(txType, createMPTIssuance) {
			if mptIssuancesCreated != 1 || mptIssuancesDeleted != 0 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT issuance create: expected exactly 1 issuance created and 0 deleted",
				}
			}
			return nil
		}

		// A destroyMPTIssuance transaction (MPTokenIssuanceDestroy/VaultDelete)
		// must delete exactly 1 issuance and create 0.
		if hasPrivilege(txType, destroyMPTIssuance) {
			if mptIssuancesCreated != 0 || mptIssuancesDeleted != 1 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT issuance destroy: expected exactly 0 issuances created and 1 deleted",
				}
			}
			return nil
		}

		if hasPrivilege(txType, mustAuthorizeMPT|mayAuthorizeMPT) {
			// No issuance changes allowed.
			if mptIssuancesCreated > 0 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT authorize succeeded but created MPT issuances",
				}
			}
			if mptIssuancesDeleted > 0 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT authorize succeeded but deleted issuances",
				}
			}
			if rules != nil && rules.Enabled(amendment.FeatureLendingProtocol) &&
				mptokensCreated+mptokensDeleted > 1 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT authorize succeeded but created/deleted bad number of mptokens",
				}
			}

			// Check if submitted by issuer (Holder field present).
			// Use HasHolder() interface for reliable detection since
			// Common.HasField may not be populated for programmatically
			// constructed transactions.
			submittedByIssuer := false
			if hp, ok := tx.(HolderFieldProvider); ok {
				submittedByIssuer = hp.HasHolder()
			} else {
				submittedByIssuer = tx.TxHasField("Holder")
			}
			if submittedByIssuer && (mptokensCreated > 0 || mptokensDeleted > 0) {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT authorize submitted by issuer succeeded but created/deleted mptokens",
				}
			}
			// A holder-submitted must-authorize transaction must create or delete one MPToken.
			if !submittedByIssuer && hasPrivilege(txType, mustAuthorizeMPT) &&
				(mptokensCreated+mptokensDeleted != 1) {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT authorize submitted by holder succeeded but created/deleted bad number of mptokens",
				}
			}
			return nil
		}

		switch txType {
		case TypeMPTokenIssuanceSet:
			// Must not create/delete any.
			if mptIssuancesCreated != 0 || mptIssuancesDeleted != 0 ||
				mptokensCreated != 0 || mptokensDeleted != 0 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT issuance set succeeded but created/deleted MPT issuances or MPTokens",
				}
			}
			return nil

		case TypeEscrowFinish:
			// EscrowFinish is fully permissive — may create MPTokens for MPT escrows.
			return nil
		}
	}

	// A transaction with the mayDeleteMPT privilege (VaultWithdraw /
	// VaultClawback) may delete exactly one MPToken with no other MPT changes.
	if result == TesSUCCESS && hasPrivilege(txType, mayDeleteMPT) &&
		mptokensDeleted == 1 && mptokensCreated == 0 &&
		mptIssuancesCreated == 0 && mptIssuancesDeleted == 0 {
		return nil
	}

	// For all other tx types (or non-success results), no MPT changes at all.
	if mptIssuancesCreated != 0 || mptIssuancesDeleted != 0 ||
		mptokensCreated != 0 || mptokensDeleted != 0 {
		return &InvariantViolation{
			Name:    "ValidMPTIssuance",
			Message: "unexpected MPTokenIssuance or MPToken changes",
		}
	}

	return nil
}
