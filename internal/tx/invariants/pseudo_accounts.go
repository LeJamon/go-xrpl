package invariants

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// ---------------------------------------------------------------------------
// ValidPseudoAccounts
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidPseudoAccounts
//
// Pseudo-accounts (AMM, Vault, LoanBroker) have unique, consistent properties.
// An AccountRoot is treated as a pseudo-account if it carries any designator
// field (sfAMMID / sfVaultID / sfLoanBrokerID) OR has a zero Sequence — not all
// pseudo-accounts have a zero sequence, but any account with a zero sequence had
// better be a pseudo-account. Such an account must:
//   1. Have exactly one designator field set.
//   2. Not change its Sequence when modified.
//   3. Have lsfDisableMaster, lsfDefaultRipple and lsfDepositAuth all set.
//   4. Have no RegularKey.
//
// Enforcement is gated on featureSingleAssetVault; while disabled the check is
// detection-only and never fails a transaction, matching rippled.

func checkValidPseudoAccounts(entries []InvariantEntry, rules *amendment.Rules) *InvariantViolation {
	if rules == nil || !rules.Enabled(amendment.FeatureSingleAssetVault) {
		return nil
	}

	for _, e := range entries {
		// Creation and modification are inspected; deletion is ignored.
		if e.IsDelete || e.EntryType != entry.TypeAccountRoot || e.After == nil {
			continue
		}

		after, err := state.ParseAccountRoot(e.After)
		if err != nil {
			return &InvariantViolation{
				Name:    "ValidPseudoAccounts",
				Message: fmt.Sprintf("could not parse AccountRoot SLE: %v", err),
			}
		}

		if !after.IsPseudoAccount() && after.Sequence != 0 {
			continue
		}

		if v := validatePseudoAccount(e.Before, after); v != nil {
			return v
		}
	}
	return nil
}

// validatePseudoAccount enforces the four pseudo-account invariants against a
// modified/created AccountRoot. before is the prior image (nil on creation).
func validatePseudoAccount(before []byte, after *state.AccountRoot) *InvariantViolation {
	if n := after.PseudoAccountFieldCount(); n != 1 {
		return &InvariantViolation{
			Name:    "ValidPseudoAccounts",
			Message: fmt.Sprintf("pseudo-account has %d pseudo-account fields set", n),
		}
	}

	if before != nil {
		if prev, err := state.ParseAccountRoot(before); err == nil && prev.Sequence != after.Sequence {
			return &InvariantViolation{
				Name:    "ValidPseudoAccounts",
				Message: "pseudo-account sequence changed",
			}
		}
	}

	const expectedFlags = LsfDisableMaster | LsfDefaultRipple | LsfDepositAuth
	if after.Flags&expectedFlags != expectedFlags {
		return &InvariantViolation{
			Name:    "ValidPseudoAccounts",
			Message: "pseudo-account flags are not set",
		}
	}

	if after.RegularKey != "" {
		return &InvariantViolation{
			Name:    "ValidPseudoAccounts",
			Message: "pseudo-account has a regular key",
		}
	}

	return nil
}
