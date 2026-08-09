package invariants

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// ---------------------------------------------------------------------------
// ValidMPTIssuance
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidMPTIssuance (lines 1366-1534)
//
// visitEntry: counts created and deleted MPTokenIssuance and MPToken entries.
// finalize: switch on transaction type with specific count requirements.

func checkValidMPTIssuance(tx Transaction, result Result, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	// visitEntry phase: count created/deleted MPTokenIssuance and MPToken entries.
	// In rippled, visitEntry receives (isDelete, before, after) where `after` is
	// always the SLE data (even for deletions). In Go's CollectEntries, deleted
	// entries have After=nil but EntryType is set from Before data. We use
	// EntryType + IsDelete + Before==nil to match rippled's counting logic:
	//   Created = !isDelete && before==nil  (entry with After data, no Before)
	//   Deleted = isDelete                  (entry marked as erased)
	var mptIssuancesCreated, mptIssuancesDeleted int
	var mptokensCreated, mptokensDeleted int
	var referenceHoldingSetOnCreate, referenceHoldingMutated bool
	var mptCreatedByIssuer bool
	var deletedHoldingAccounts [][20]byte
	fixCleanup := rules != nil && rules.Enabled(amendment.FeatureFixCleanup3_2_0)
	enforceCreatedByIssuer := rules != nil &&
		(rules.Enabled(amendment.FeatureSingleAssetVault) || rules.Enabled(amendment.FeatureLendingProtocol))

	for _, e := range entries {
		if e.EntryType == entry.TypeMPTokenIssuance {
			if e.IsDelete {
				mptIssuancesDeleted++
			} else if e.Before == nil {
				mptIssuancesCreated++
				if fixCleanup {
					issuance, err := state.ParseMPTokenIssuance(e.After)
					if err != nil {
						return invalidMPTEntry("MPTokenIssuance", err)
					}
					referenceHoldingSetOnCreate = referenceHoldingSetOnCreate || issuance.ReferenceHolding != nil
				}
			} else if fixCleanup {
				before, err := state.ParseMPTokenIssuance(e.Before)
				if err != nil {
					return invalidMPTEntry("MPTokenIssuance before", err)
				}
				after, err := state.ParseMPTokenIssuance(e.After)
				if err != nil {
					return invalidMPTEntry("MPTokenIssuance after", err)
				}
				referenceHoldingMutated = referenceHoldingMutated ||
					!sameOptionalString(before.ReferenceHolding, after.ReferenceHolding)
			}
		}
		if e.EntryType == entry.TypeMPToken {
			if e.IsDelete {
				mptokensDeleted++
				if fixCleanup {
					token, err := state.ParseMPToken(e.Before)
					if err != nil {
						return invalidMPTEntry("deleted MPToken", err)
					}
					deletedHoldingAccounts = append(deletedHoldingAccounts, token.Account)
				}
			} else if e.Before == nil {
				mptokensCreated++
				if enforceCreatedByIssuer {
					token, err := state.ParseMPToken(e.After)
					if err != nil {
						return invalidMPTEntry("created MPToken", err)
					}
					mptCreatedByIssuer = mptCreatedByIssuer || token.Account == mptIssuer(token.MPTokenIssuanceID)
				}
			}
		}
		if fixCleanup && e.IsDelete && e.EntryType == entry.TypeRippleState {
			line, err := state.ParseRippleState(e.Before)
			if err != nil {
				return invalidMPTEntry("deleted RippleState", err)
			}
			for _, address := range []string{line.LowLimit.Issuer, line.HighLimit.Issuer} {
				account, err := state.DecodeAccountID(address)
				if err != nil {
					return invalidMPTEntry("deleted RippleState account", err)
				}
				deletedHoldingAccounts = append(deletedHoldingAccounts, account)
			}
		}
	}

	// finalize phase
	txType := tx.TxType()
	if fixCleanup {
		if referenceHoldingMutated {
			return &InvariantViolation{
				Name:    "ValidMPTIssuance",
				Message: "ReferenceHolding was modified on an existing MPTokenIssuance",
			}
		}
		if referenceHoldingSetOnCreate && txType != TypeVaultCreate {
			return &InvariantViolation{
				Name:    "ValidMPTIssuance",
				Message: "ReferenceHolding was set by a non-VaultCreate transaction",
			}
		}
		if txType != TypeVaultDelete {
			for _, account := range deletedHoldingAccounts {
				vaultPseudo, err := isVaultPseudoAccount(view, account)
				if err != nil {
					return invalidMPTEntry("vault pseudo-account", err)
				}
				if vaultPseudo {
					return &InvariantViolation{
						Name:    "ValidMPTIssuance",
						Message: "vault pseudo-account holding deleted by a non-VaultDelete transaction",
					}
				}
			}
		}
	}

	if result == TesSUCCESS {
		if mptCreatedByIssuer && enforceCreatedByIssuer {
			return &InvariantViolation{
				Name:    "ValidMPTIssuance",
				Message: "MPToken created for the MPT issuer",
			}
		}

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

		lendingEnabled := rules != nil && rules.Enabled(amendment.FeatureLendingProtocol)
		enforceEscrowFinish := txType == TypeEscrowFinish && rules != nil &&
			(rules.Enabled(amendment.FeatureSingleAssetVault) || lendingEnabled)
		if hasPrivilege(txType, mustAuthorizeMPT|mayAuthorizeMPT) || enforceEscrowFinish {
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
			if lendingEnabled &&
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

		case TypeConfidentialMPTConvert, TypeConfidentialMPTMergeInbox, TypeConfidentialMPTConvertBack:
			if mptIssuancesCreated != 0 || mptIssuancesDeleted != 0 ||
				mptokensCreated != 0 || mptokensDeleted != 0 {
				return &InvariantViolation{Name: "ValidMPTIssuance", Message: "confidential MPT transaction created or deleted an MPT ledger entry"}
			}
			return nil

		case TypeEscrowFinish:
			// EscrowFinish is fully permissive — may create MPTokens for MPT escrows.
			return nil
		}
	}

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

type confidentialMPTChange struct {
	publicDelta              *big.Int
	confidentialDelta        *big.Int
	outstandingDelta         *big.Int
	issuance                 *state.MPTokenIssuanceData
	deletedWithBalance       bool
	badConsistency           bool
	badOutstanding           bool
	changedConfidentialField bool
	badVersion               bool
}

func newConfidentialMPTChange() *confidentialMPTChange {
	return &confidentialMPTChange{publicDelta: new(big.Int), confidentialDelta: new(big.Int), outstandingDelta: new(big.Int)}
}

func checkValidConfidentialMPToken(transaction Transaction, result Result, entries []InvariantEntry, view ReadView, _ *amendment.Rules) *InvariantViolation {
	if result != TesSUCCESS {
		return nil
	}
	changes := make(map[[24]byte]*confidentialMPTChange)
	get := func(id [24]byte) *confidentialMPTChange {
		if changes[id] == nil {
			changes[id] = newConfidentialMPTChange()
		}
		return changes[id]
	}

	for _, ledgerChange := range entries {
		switch ledgerChange.EntryType {
		case entry.TypeMPTokenIssuance:
			if ledgerChange.Before != nil {
				before, err := state.ParseMPTokenIssuance(ledgerChange.Before)
				if err != nil {
					return invalidConfidentialMPT("MPTokenIssuance before", err)
				}
				change := get(keylet.MakeMPTID(before.Sequence, before.Issuer))
				change.confidentialDelta.Sub(change.confidentialDelta, new(big.Int).SetUint64(before.ConfidentialOutstandingAmount))
				change.outstandingDelta.Sub(change.outstandingDelta, new(big.Int).SetUint64(before.OutstandingAmount))
			}
			if ledgerChange.After != nil {
				after, err := state.ParseMPTokenIssuance(ledgerChange.After)
				if err != nil {
					return invalidConfidentialMPT("MPTokenIssuance after", err)
				}
				change := get(keylet.MakeMPTID(after.Sequence, after.Issuer))
				change.confidentialDelta.Add(change.confidentialDelta, new(big.Int).SetUint64(after.ConfidentialOutstandingAmount))
				change.outstandingDelta.Add(change.outstandingDelta, new(big.Int).SetUint64(after.OutstandingAmount))
				change.issuance = after
				change.badOutstanding = after.ConfidentialOutstandingAmount > after.OutstandingAmount
			}

		case entry.TypeMPToken:
			var before *state.MPTokenData
			if ledgerChange.Before != nil {
				var err error
				before, err = state.ParseMPToken(ledgerChange.Before)
				if err != nil {
					return invalidConfidentialMPT("MPToken before", err)
				}
				change := get(before.MPTokenIssuanceID)
				change.publicDelta.Sub(change.publicDelta, new(big.Int).SetUint64(before.MPTAmount))
				if ledgerChange.After == nil {
					change.deletedWithBalance = before.MPTAmount > 0 || hasEncryptedBalances(before)
				}
			}
			if ledgerChange.After == nil {
				continue
			}
			after, err := state.ParseMPToken(ledgerChange.After)
			if err != nil {
				return invalidConfidentialMPT("MPToken after", err)
			}
			change := get(after.MPTokenIssuanceID)
			change.publicDelta.Add(change.publicDelta, new(big.Int).SetUint64(after.MPTAmount))
			hasInbox := len(after.ConfidentialBalanceInbox) != 0
			hasSpending := len(after.ConfidentialBalanceSpending) != 0
			hasIssuer := len(after.IssuerEncryptedBalance) != 0
			hasAuditor := len(after.AuditorEncryptedBalance) != 0
			change.badConsistency = change.badConsistency || hasInbox != hasSpending || hasInbox != hasIssuer || hasAuditor && !hasIssuer
			change.changedConfidentialField = change.changedConfidentialField || confidentialBalancesChanged(before, after)
			if before != nil && len(before.ConfidentialBalanceSpending) != 0 &&
				!bytes.Equal(before.ConfidentialBalanceSpending, after.ConfidentialBalanceSpending) &&
				before.ConfidentialBalanceVersion == after.ConfidentialBalanceVersion {
				change.badVersion = true
			}
		}
	}

	for id, change := range changes {
		issuance := change.issuance
		if issuance == nil {
			raw, err := view.Read(keylet.MPTIssuance(id))
			if err != nil || raw == nil {
				continue
			}
			issuance, err = state.ParseMPTokenIssuance(raw)
			if err != nil {
				return invalidConfidentialMPT("confidential MPToken issuance", err)
			}
		}
		if change.deletedWithBalance && issuance.ConfidentialOutstandingAmount > 0 {
			return confidentialViolation("MPToken deleted with encrypted fields while ConfidentialOutstandingAmount is non-zero")
		}
		if change.badConsistency {
			return confidentialViolation("MPToken encrypted field existence inconsistency")
		}
		if change.badOutstanding {
			return confidentialViolation("ConfidentialOutstandingAmount exceeds OutstandingAmount")
		}
		if change.changedConfidentialField && issuance.Flags&entry.LsfMPTCanHoldConfidentialBalance == 0 {
			return confidentialViolation("MPToken encrypted fields changed without issuance capability")
		}
		if change.confidentialDelta.Sign() != 0 {
			actual := new(big.Int).Add(new(big.Int).Set(change.publicDelta), change.confidentialDelta)
			if actual.Cmp(change.outstandingDelta) != 0 {
				return confidentialViolation("public and confidential MPT amounts are not conserved")
			}
		} else if isConfidentialMPTTransaction(transaction.TxType()) &&
			(change.publicDelta.Sign() != 0 || change.outstandingDelta.Sign() != 0) {
			return confidentialViolation("confidential transaction changed public MPT balances")
		}
		if change.badVersion {
			return confidentialViolation("ConfidentialBalanceSpending changed without ConfidentialBalanceVersion")
		}
	}
	return nil
}

func hasEncryptedBalances(token *state.MPTokenData) bool {
	return len(token.ConfidentialBalanceInbox) != 0 || len(token.ConfidentialBalanceSpending) != 0 ||
		len(token.IssuerEncryptedBalance) != 0 || len(token.AuditorEncryptedBalance) != 0
}

func confidentialBalancesChanged(before, after *state.MPTokenData) bool {
	if before == nil {
		return hasEncryptedBalances(after)
	}
	return len(after.ConfidentialBalanceInbox) != 0 && !bytes.Equal(before.ConfidentialBalanceInbox, after.ConfidentialBalanceInbox) ||
		len(after.ConfidentialBalanceSpending) != 0 && !bytes.Equal(before.ConfidentialBalanceSpending, after.ConfidentialBalanceSpending) ||
		len(after.IssuerEncryptedBalance) != 0 && !bytes.Equal(before.IssuerEncryptedBalance, after.IssuerEncryptedBalance) ||
		len(after.AuditorEncryptedBalance) != 0 && !bytes.Equal(before.AuditorEncryptedBalance, after.AuditorEncryptedBalance)
}

func isConfidentialMPTTransaction(txType TxType) bool {
	switch txType {
	case TypeConfidentialMPTConvert, TypeConfidentialMPTMergeInbox, TypeConfidentialMPTConvertBack,
		TypeConfidentialMPTSend, TypeConfidentialMPTClawback:
		return true
	default:
		return false
	}
}

func confidentialViolation(message string) *InvariantViolation {
	return &InvariantViolation{Name: "ValidConfidentialMPToken", Message: message}
}

func invalidConfidentialMPT(name string, err error) *InvariantViolation {
	return confidentialViolation(fmt.Sprintf("could not parse %s: %v", name, err))
}

func invalidMPTEntry(entry string, err error) *InvariantViolation {
	return &InvariantViolation{
		Name:    "ValidMPTIssuance",
		Message: fmt.Sprintf("could not parse %s: %v", entry, err),
	}
}

func sameOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func mptIssuer(id [24]byte) [20]byte {
	var issuer [20]byte
	copy(issuer[:], id[4:])
	return issuer
}

func isVaultPseudoAccount(view ReadView, account [20]byte) (bool, error) {
	data, err := view.Read(keylet.Account(account))
	if err != nil || data == nil {
		return false, err
	}
	root, err := state.ParseAccountRoot(data)
	if err != nil {
		return false, err
	}
	return root.HasVaultID(), nil
}
