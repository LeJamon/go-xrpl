package invariants

// ---------------------------------------------------------------------------
// ValidMPTIssuance
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidMPTIssuance (lines 1366-1534)
//
// visitEntry: counts created and deleted MPTokenIssuance and MPToken entries.
// finalize: switch on transaction type with specific count requirements.

func checkValidMPTIssuance(tx Transaction, result Result, entries []InvariantEntry) *InvariantViolation {
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
		switch txType {
		case TypeMPTokenIssuanceCreate, TypeVaultCreate:
			// Must create exactly 1 issuance, delete 0.
			if mptIssuancesCreated != 1 || mptIssuancesDeleted != 0 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT issuance create: expected exactly 1 issuance created and 0 deleted",
				}
			}
			return nil

		case TypeMPTokenIssuanceDestroy, TypeVaultDelete:
			// Must delete exactly 1 issuance, create 0.
			if mptIssuancesCreated != 0 || mptIssuancesDeleted != 1 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT issuance destroy: expected exactly 0 issuances created and 1 deleted",
				}
			}
			return nil

		case TypeMPTokenAuthorize, TypeVaultDeposit:
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
			// If holder submitted (not VaultDeposit), exactly 1 MPToken must be created or deleted.
			if !submittedByIssuer && txType != TypeVaultDeposit &&
				(mptokensCreated+mptokensDeleted != 1) {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT authorize submitted by holder succeeded but created/deleted bad number of mptokens",
				}
			}
			return nil

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
