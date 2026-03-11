package invariants

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// ---------------------------------------------------------------------------
// AccountRootsDeletedClean
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — AccountRootsDeletedClean (lines 416-501)
//
// visitEntry: Collect deleted AccountRoot entries.
//
// finalize: For each deleted account, verify that no directly-derivable objects
// remain in the view (account root, owner directory, signer list, NFT pages,
// DID). If the account had sfAMMID, verify the AMM object is also gone.
//
// Gating: Only enforced when featureInvariantsV1_1 is enabled.

// deletedAccount holds info about a deleted AccountRoot entry.
type deletedAccount struct {
	accountID [20]byte
	ammID     [32]byte
	hasAMMID  bool
}

func checkAccountRootsDeletedClean(entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	// Only enforce when InvariantsV1_1 is enabled.
	// Reference: rippled lines 438-439
	enforce := rules != nil && rules.Enabled(amendment.FeatureInvariantsV1_1)
	if !enforce {
		return nil
	}

	if view == nil {
		return nil
	}

	// Collect deleted AccountRoot entries
	var deletedAccounts []deletedAccount

	for _, e := range entries {
		if e.EntryType != "AccountRoot" || !e.IsDelete {
			continue
		}
		if e.Before == nil {
			continue
		}
		acct, err := state.ParseAccountRoot(e.Before)
		if err != nil {
			continue
		}
		accID, err := state.DecodeAccountID(acct.Account)
		if err != nil {
			continue
		}
		var zeroHash [32]byte
		da := deletedAccount{
			accountID: accID,
			hasAMMID:  acct.AMMID != zeroHash,
			ammID:     acct.AMMID,
		}
		deletedAccounts = append(deletedAccounts, da)
	}

	if len(deletedAccounts) == 0 {
		return nil
	}

	for _, da := range deletedAccounts {
		// Check direct account keylets.
		// Reference: rippled directAccountKeylets (Indexes.h lines 382-390)
		directKeylets := []keylet.Keylet{
			keylet.Account(da.accountID),
			keylet.OwnerDir(da.accountID),
			keylet.SignerList(da.accountID),
			keylet.NFTokenPageMin(da.accountID),
			keylet.NFTokenPageMax(da.accountID),
			keylet.DID(da.accountID),
		}

		for _, kl := range directKeylets {
			exists, err := view.Exists(kl)
			if err == nil && exists {
				return &InvariantViolation{
					Name:    "AccountRootsDeletedClean",
					Message: "account deletion left behind a ledger object",
				}
			}
		}

		// Check for NFT pages between min and max using Succ.
		// rippled uses view.succ(first.key, last.key.next()) to find any
		// NFT page in the range. Our Succ(key) returns the first entry
		// with key > given key. We check if the successor is within the
		// NFT page range for this account.
		// Reference: rippled lines 477-490
		firstKey := keylet.NFTokenPageMin(da.accountID).Key
		lastKey := keylet.NFTokenPageMax(da.accountID).Key

		succKey, _, found, err := view.Succ(firstKey)
		if err == nil && found {
			// If the successor key is within [firstKey, lastKey],
			// there's a leftover NFT page.
			if compareKey256(succKey, lastKey) <= 0 {
				return &InvariantViolation{
					Name:    "AccountRootsDeletedClean",
					Message: "account deletion left behind a ledger object",
				}
			}
		}

		// Check AMM object if sfAMMID was present.
		// Reference: rippled lines 492-497
		if da.hasAMMID {
			ammKL := keylet.AMMByID(da.ammID)
			exists, err := view.Exists(ammKL)
			if err == nil && exists {
				return &InvariantViolation{
					Name:    "AccountRootsDeletedClean",
					Message: "account deletion left behind a ledger object",
				}
			}
		}
	}

	return nil
}
