package invariants

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
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

		hasSponsorship, err := accountRootHasSponsorshipFields(e.After)
		if err != nil {
			return &InvariantViolation{
				Name:    "ValidPseudoAccounts",
				Message: fmt.Sprintf("could not inspect AccountRoot sponsorship fields: %v", err),
			}
		}
		if v := validatePseudoAccount(e.Before, after, hasSponsorship); v != nil {
			return v
		}
	}
	return nil
}

// validatePseudoAccount enforces the pseudo-account invariants against a
// modified/created AccountRoot. before is the prior image (nil on creation).
func validatePseudoAccount(before []byte, after *state.AccountRoot, sponsorshipPresent ...bool) *InvariantViolation {
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

	hasSponsorship := after.SponsoredOwnerCount != 0 ||
		after.SponsoringOwnerCount != 0 ||
		after.SponsoringAccountCount != 0 ||
		after.HasSponsor ||
		(len(sponsorshipPresent) > 0 && sponsorshipPresent[0])
	if hasSponsorship {
		return &InvariantViolation{
			Name:    "ValidPseudoAccounts",
			Message: "pseudo-account has a sponsorship field",
		}
	}

	return nil
}

// checkPseudoAccountOwnerDirs enforces, under fixCleanup3_5_0, that a
// pseudo-account's owner directory only gains entries belonging to its own AMM,
// Vault or LoanBroker. A pseudo-account cannot sign, so any other linked object
// would pin the directory and block deletion of the owning object.
// Reference: rippled InvariantCheck.cpp ValidPseudoAccounts (rippled #8181).
func checkPseudoAccountOwnerDirs(entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	if rules == nil || view == nil || !rules.Enabled(amendment.FeatureFixCleanup3_5_0) {
		return nil
	}

	// Visit entries and owners in key order so the reported violation is
	// deterministic.
	ordered := slices.Clone(entries)
	slices.SortFunc(ordered, func(a, b InvariantEntry) int { return bytes.Compare(a.Key[:], b.Key[:]) })
	var owners [][20]byte
	added := make(map[[20]byte][][32]byte)
	for _, e := range ordered {
		if e.IsDelete || e.EntryType != entry.TypeDirectoryNode || e.After == nil {
			continue
		}
		after, err := state.ParseDirectoryNode(e.After)
		if err != nil {
			return pseudoOwnerViolation(fmt.Sprintf("could not parse DirectoryNode: %v", err))
		}
		// Only owner directory pages carry an Owner; book directories don't.
		if after.Owner == ([20]byte{}) {
			continue
		}
		var before map[[32]byte]struct{}
		if e.Before != nil {
			prev, err := state.ParseDirectoryNode(e.Before)
			if err != nil {
				return pseudoOwnerViolation(fmt.Sprintf("could not parse DirectoryNode: %v", err))
			}
			before = make(map[[32]byte]struct{}, len(prev.Indexes))
			for _, index := range prev.Indexes {
				before[index] = struct{}{}
			}
		}
		for _, index := range after.Indexes {
			if _, existed := before[index]; existed {
				continue
			}
			if _, seen := added[after.Owner]; !seen {
				owners = append(owners, after.Owner)
			}
			added[after.Owner] = append(added[after.Owner], index)
		}
	}

	slices.SortFunc(owners, func(a, b [20]byte) int { return bytes.Compare(a[:], b[:]) })
	for _, owner := range owners {
		data, err := view.Read(keylet.Account(owner))
		if err != nil {
			return pseudoOwnerViolation(fmt.Sprintf("could not read owner AccountRoot: %v", err))
		}
		if data == nil {
			continue
		}
		root, err := state.ParseAccountRoot(data)
		if err != nil {
			return pseudoOwnerViolation(fmt.Sprintf("could not parse owner AccountRoot: %v", err))
		}
		if !root.IsPseudoAccount() {
			continue
		}
		for _, index := range added[owner] {
			object, err := view.Read(keylet.Keylet{Key: index})
			if err != nil || object == nil {
				return pseudoOwnerViolation(fmt.Sprintf("pseudo-account owner directory links a missing object %X", index))
			}
			if !pseudoAccountOwns(view, owner, root, index, object) {
				return pseudoOwnerViolation(fmt.Sprintf("pseudo-account may not own object %X", index))
			}
		}
	}
	return nil
}

func pseudoOwnerViolation(message string) *InvariantViolation {
	return &InvariantViolation{Name: "ValidPseudoAccounts", Message: message}
}

// pseudoAccountOwns reports whether the object at index belongs to the AMM,
// Vault or LoanBroker that the pseudo-account was created for.
func pseudoAccountOwns(view ReadView, pseudo [20]byte, root *state.AccountRoot, index [32]byte, object []byte) bool {
	objectType, err := state.DecodeType(object)
	if err != nil {
		return false
	}

	switch {
	case root.HasAMMID():
		if objectType == entry.TypeAMM {
			return index == root.AMMID
		}
		var amm entry.AMM
		if data, err := view.Read(keylet.AMMByID(root.AMMID)); err != nil || data == nil || amm.Decode(data) != nil {
			return false
		}
		for _, get := range []func() (entry.IssueValue, error){amm.GetAsset, amm.GetAsset2} {
			if asset, err := get(); err == nil && isPseudoHolding(index, objectType, asset, pseudo) {
				return true
			}
		}
		// The AMM issues LP tokens, so each liquidity provider's trust line
		// links into its directory.
		if objectType != entry.TypeRippleState {
			return false
		}
		line, err := state.ParseRippleState(object)
		if err != nil {
			return false
		}
		low, lowErr := state.DecodeAccountID(line.LowLimit.Issuer)
		high, highErr := state.DecodeAccountID(line.HighLimit.Issuer)
		lptValue, lptErr := amm.GetLPTokenBalance()
		if lowErr != nil || highErr != nil || lptErr != nil {
			return false
		}
		lpt, err := state.AmountFromLedgerValue(lptValue)
		if err != nil {
			return false
		}
		return (low == pseudo || high == pseudo) && index == keylet.Line(low, high, lpt.Currency).Key

	case root.HasVaultID():
		if objectType == entry.TypeLoanBroker {
			var broker entry.LoanBroker
			if broker.Decode(object) != nil {
				return false
			}
			vaultID, err := broker.GetVaultID()
			return err == nil && vaultID == root.VaultID
		}
		var vault entry.Vault
		if data, err := view.Read(keylet.VaultByID(root.VaultID)); err != nil || data == nil || vault.Decode(data) != nil {
			return false
		}
		if share, err := vault.GetShareMPTID(); err == nil && objectType == entry.TypeMPTokenIssuance && index == keylet.MPTIssuance(share).Key {
			return true
		}
		asset, err := vault.GetAsset()
		return err == nil && isPseudoHolding(index, objectType, asset, pseudo)

	case root.HasLoanBrokerID():
		if objectType == entry.TypeLoan {
			var loan entry.Loan
			if loan.Decode(object) != nil {
				return false
			}
			brokerID, err := loan.GetLoanBrokerID()
			return err == nil && brokerID == root.LoanBrokerID
		}
		var broker entry.LoanBroker
		if data, err := view.Read(keylet.LoanBrokerByID(root.LoanBrokerID)); err != nil || data == nil || broker.Decode(data) != nil {
			return false
		}
		vaultID, err := broker.GetVaultID()
		if err != nil {
			return false
		}
		var vault entry.Vault
		if data, err := view.Read(keylet.VaultByID(vaultID)); err != nil || data == nil || vault.Decode(data) != nil {
			return false
		}
		asset, err := vault.GetAsset()
		return err == nil && isPseudoHolding(index, objectType, asset, pseudo)
	}
	return false
}

// isPseudoHolding reports whether index is the trust line or MPToken through
// which account holds asset. XRP has no such entry.
func isPseudoHolding(index [32]byte, objectType entry.Type, asset entry.IssueValue, account [20]byte) bool {
	if asset.MPTIssuanceID != "" {
		raw, err := hex.DecodeString(asset.MPTIssuanceID)
		if err != nil || len(raw) != 24 {
			return false
		}
		var id [24]byte
		copy(id[:], raw)
		return objectType == entry.TypeMPToken && index == keylet.MPTokenByID(id, account).Key
	}
	if asset.Currency == "" || asset.Currency == "XRP" || asset.Issuer == "" {
		return false
	}
	issuer, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return false
	}
	return objectType == entry.TypeRippleState && index == keylet.Line(account, issuer, asset.Currency).Key
}
