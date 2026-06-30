package amm

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// updateAMMAccountIfChanged persists the AMM account only when its serialized
// bytes actually differ from what the view already holds. An all-IOU withdraw
// leaves the AMM account's own fields untouched (only its trust lines change);
// rewriting it would promote it to a modified node, whereas rippled leaves it
// as a bare threaded owner of the changed trust lines (no FinalFields). An
// XRP-side withdraw does change its Balance, so it is still written.
func updateAMMAccountIfChanged(view tx.LedgerView, ammAccountKey keylet.Keylet, ammAccount *state.AccountRoot) ter.Result {
	ammAccountBytes, err := state.SerializeAccountRoot(ammAccount)
	if err != nil {
		return ter.TefINTERNAL
	}
	if cur, _ := view.Read(ammAccountKey); bytes.Equal(cur, ammAccountBytes) {
		return ter.TesSUCCESS
	}
	if err := view.Update(ammAccountKey, ammAccountBytes); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// maxDeletableAMMTrustLines is the maximum number of trust lines that can be
// deleted in a single transaction when cleaning up an AMM account.
// Reference: rippled Protocol.h maxDeletableAMMTrustLines = 512
const maxDeletableAMMTrustLines = 512

// deleteAMMTrustLine deletes a single trust line owned by the AMM account.
// It removes the trust line from both accounts' owner directories, erases it,
// and decrements the non-AMM account's OwnerCount.
// Reference: rippled View.cpp deleteAMMTrustLine (line 2720)
func deleteAMMTrustLine(view tx.LedgerView, lineKey keylet.Keylet, rs *state.RippleState, ammAccountID [20]byte) ter.Result {
	lowAccountID, err := state.DecodeAccountID(rs.LowLimit.Issuer)
	if err != nil {
		return ter.TecINTERNAL
	}
	highAccountID, err := state.DecodeAccountID(rs.HighLimit.Issuer)
	if err != nil {
		return ter.TecINTERNAL
	}

	lowAccountData, err := view.Read(keylet.Account(lowAccountID))
	if err != nil || lowAccountData == nil {
		return ter.TecINTERNAL
	}
	lowAccount, err := state.ParseAccountRoot(lowAccountData)
	if err != nil {
		return ter.TecINTERNAL
	}
	highAccountData, err := view.Read(keylet.Account(highAccountID))
	if err != nil || highAccountData == nil {
		return ter.TecINTERNAL
	}
	highAccount, err := state.ParseAccountRoot(highAccountData)
	if err != nil {
		return ter.TecINTERNAL
	}

	zeroHash := [32]byte{}
	ammLow := lowAccount.AMMID != zeroHash
	ammHigh := highAccount.AMMID != zeroHash

	// Can't both be AMM
	if ammLow && ammHigh {
		return ter.TecINTERNAL
	}
	// At least one must be AMM
	if !ammLow && !ammHigh {
		return ter.TerNO_AMM
	}
	// One must be the target AMM
	if lowAccountID != ammAccountID && highAccountID != ammAccountID {
		return ter.TerNO_AMM
	}

	// The reserve-holding side's flag must be present before the line is erased;
	// an AMM pool line always carries at least the AMM side's reserve flag, so its
	// total absence is an internal inconsistency. Mirrors rippled's tecINTERNAL
	// guard. Reference: rippled View.cpp:2759-2761.
	if rs.Flags&(state.LsfLowReserve|state.LsfHighReserve) == 0 {
		return ter.TecINTERNAL
	}

	// Clear the reserve flag only on the AMM side, reproducing rippled: it
	// releases the AMM side's reserve during the payout credit and leaves the
	// non-AMM (holder) side's flag set (e.g. on the LP-token line).
	if rs.Flags&state.LsfLowReserve != 0 {
		if ammLow {
			rs.Flags &^= state.LsfLowReserve
		}
		if err := decrementLineOwner(view, lowAccountID, lowAccount, ammLow); err != nil {
			return ter.TecINTERNAL
		}
	}
	if rs.Flags&state.LsfHighReserve != 0 {
		if ammHigh {
			rs.Flags &^= state.LsfHighReserve
		}
		if err := decrementLineOwner(view, highAccountID, highAccount, ammHigh); err != nil {
			return ter.TecINTERNAL
		}
	}

	// Persist the cleared reserve flag before erasing so the DeletedNode records
	// the flag change as PreviousFields.
	rsBytes, err := state.SerializeRippleState(rs)
	if err != nil {
		return ter.TecINTERNAL
	}
	if err := view.Update(lineKey, rsBytes); err != nil {
		return ter.TecINTERNAL
	}

	if trustDelete(view, lineKey, lowAccountID, highAccountID, rs.LowNode, rs.HighNode) != nil {
		return ter.TefBAD_LEDGER
	}

	return ter.TesSUCCESS
}

// decrementLineOwner decrements the OwnerCount of a trust line's reserve-holding
// side through the view, so the change is recorded as an in-place modification
// before the line (and, for the AMM side, the AMM AccountRoot) is erased. For
// the AMM side it routes through tx.AdjustOwnerCount, which re-reads the account
// from the view each call so repeated decrements (one per pool line) compose to
// the correct final OwnerCount and the AMM AccountRoot's later erase becomes a
// DeletedNode carrying PreviousFields.OwnerCount.
func decrementLineOwner(view tx.LedgerView, accountID [20]byte, account *state.AccountRoot, isAMM bool) error {
	if isAMM {
		return tx.AdjustOwnerCount(view, accountID, -1)
	}
	if account.OwnerCount > 0 {
		account.OwnerCount--
	}
	bytes, err := state.SerializeAccountRoot(account)
	if err != nil {
		return err
	}
	return view.Update(keylet.Account(accountID), bytes)
}

// deleteAMMTrustLines iterates the AMM account's owner directory and deletes
// trust lines up to maxTrustlinesToDelete. If more trust lines remain, returns
// tecINCOMPLETE. Skips AMM entries (ltAMM type).
// Reference: rippled AMMUtils.cpp deleteAMMTrustLines (line 237)
func deleteAMMTrustLines(view tx.LedgerView, ammAccountID [20]byte, maxTrustlinesToDelete int) ter.Result {
	ownerDirKey := keylet.OwnerDir(ammAccountID)

	rootData, err := view.Read(ownerDirKey)
	if err != nil || rootData == nil {
		return ter.TesSUCCESS // No directory = nothing to delete
	}

	root, err := state.ParseDirectoryNode(rootData)
	if err != nil {
		return ter.TecINTERNAL
	}

	deleted := 0

	// Process pages using dirFirst/dirNext pattern with iterator re-validation
	// Reference: rippled View.cpp cleanupOnAccountDelete (line 2642)
	currentPage := root
	currentPageNum := uint64(0)

	for {
		i := 0
		for i < len(currentPage.Indexes) {
			if maxTrustlinesToDelete > 0 {
				deleted++
				if deleted > maxTrustlinesToDelete {
					return ter.TecINCOMPLETE
				}
			}

			itemKey := currentPage.Indexes[i]
			itemKeylet := keylet.Keylet{Key: itemKey}

			itemData, err := view.Read(itemKeylet)
			if err != nil || itemData == nil {
				return ter.TefBAD_LEDGER
			}

			entryType, err := state.GetLedgerEntryType(itemData)
			if err != nil {
				return ter.TecINTERNAL
			}

			// Skip the AMM SLE that coexists with the trust lines in this dir.
			if entry.Type(entryType) == entry.TypeAMM {
				i++
				continue
			}

			if entry.Type(entryType) != entry.TypeRippleState {
				return ter.TecINTERNAL
			}

			rs, err := state.ParseRippleState(itemData)
			if err != nil {
				return ter.TecINTERNAL
			}
			if !rs.Balance.IsZero() {
				return ter.TecINTERNAL
			}

			result := deleteAMMTrustLine(view, itemKeylet, rs, ammAccountID)
			if result != ter.TesSUCCESS {
				return result
			}

			// Re-read the current page since directory was modified
			// The entry at index i was removed, so the next entry shifted down
			// We do NOT increment i (matching rippled's uDirEntry-- pattern)
			pageKeylet := keylet.DirPage(ownerDirKey.Key, currentPageNum)
			pageData, err := view.Read(pageKeylet)
			if err != nil || pageData == nil {
				// Page was deleted (empty), move on
				goto nextPage
			}
			currentPage, err = state.ParseDirectoryNode(pageData)
			if err != nil {
				return ter.TecINTERNAL
			}
		}

	nextPage:
		if currentPage == nil || currentPage.IndexNext == 0 {
			break
		}
		nextPageNum := currentPage.IndexNext
		if nextPageNum == currentPageNum {
			break // Prevent infinite loop
		}
		currentPageNum = nextPageNum
		pageKeylet := keylet.DirPage(ownerDirKey.Key, currentPageNum)
		pageData, err := view.Read(pageKeylet)
		if err != nil || pageData == nil {
			break
		}
		currentPage, err = state.ParseDirectoryNode(pageData)
		if err != nil {
			return ter.TecINTERNAL
		}
	}

	return ter.TesSUCCESS
}

// DeleteAMMAccount performs full cleanup of an AMM account:
// 1. Deletes trust lines from the AMM's owner directory (bounded)
// 2. Removes AMM SLE from owner directory
// 3. Deletes empty owner directory
// 4. Erases AMM SLE and account root
// Reference: rippled AMMUtils.cpp deleteAMMAccount (line 283)
func DeleteAMMAccount(view tx.LedgerView, asset, asset2 tx.Asset) ter.Result {
	ammKey := computeAMMKeylet(asset, asset2)
	ammRawData, err := view.Read(ammKey)
	if err != nil || ammRawData == nil {
		return ter.TecINTERNAL
	}

	amm, err := parseAMMData(ammRawData)
	if err != nil {
		return ter.TecINTERNAL
	}

	ammAccountID := amm.Account

	ammAccountKey := keylet.Account(ammAccountID)
	ammAccountData, err := view.Read(ammAccountKey)
	if err != nil || ammAccountData == nil {
		return ter.TecINTERNAL
	}

	if result := deleteAMMTrustLines(view, ammAccountID, maxDeletableAMMTrustLines); result != ter.TesSUCCESS {
		return result
	}

	// Reference: rippled AMMUtils.cpp deleteAMMAccount line 315-323
	ownerDirKey := keylet.OwnerDir(ammAccountID)
	state.DirRemove(view, ownerDirKey, amm.OwnerNode, ammKey.Key, false)

	// Delete the owner directory if it is now empty.
	// Reference: rippled AMMUtils.cpp deleteAMMAccount line 324-331
	if exists, _ := view.Exists(ownerDirKey); exists {
		rootData, err := view.Read(ownerDirKey)
		if err == nil && rootData != nil {
			rootNode, err := state.ParseDirectoryNode(rootData)
			if err == nil && len(rootNode.Indexes) == 0 && rootNode.IndexNext == 0 {
				view.Erase(ownerDirKey)
			}
		}
	}

	if err := view.Erase(ammKey); err != nil {
		return ter.TecINTERNAL
	}
	if err := view.Erase(ammAccountKey); err != nil {
		return ter.TecINTERNAL
	}

	return ter.TesSUCCESS
}

// deleteAMMAccountIfEmpty is called from AMMWithdraw when LP tokens reach zero.
// If deleteAMMAccount returns tesSUCCESS, the AMM is fully deleted.
// If it returns tecINCOMPLETE, the AMM stays in an empty state (LPTokenBalance=0)
// and requires AMMDelete to finish cleanup.
// Reference: rippled AMMWithdraw.cpp deleteAMMAccountIfEmpty (line 718)
func deleteAMMAccountIfEmpty(view tx.LedgerView, ammKey keylet.Keylet, ammAccountKey keylet.Keylet,
	lpTokenBalance tx.Amount, asset, asset2 tx.Asset, amm *AMMData, ammAccount *state.AccountRoot) ter.Result {
	if !lpTokenBalance.IsZero() {
		// Not empty, just update the AMM
		amm.LPTokenBalance = lpTokenBalance
		ammBytes, err := serializeAMMData(amm)
		if err != nil {
			return ter.TefINTERNAL
		}
		if err := view.Update(ammKey, ammBytes); err != nil {
			return ter.TefINTERNAL
		}
		if r := updateAMMAccountIfChanged(view, ammAccountKey, ammAccount); r != ter.TesSUCCESS {
			return r
		}
		return ter.TesSUCCESS
	}

	// LP tokens are zero — try to delete the AMM account. First persist the AMM
	// account's XRP-side balance change: a withdrawn XRP asset was debited from
	// ammAccount.Balance (an in-memory struct) but not yet written, and
	// DeleteAMMAccount re-reads the account from the view. Without this write the
	// DeletedNode metadata records the pre-withdrawal balance instead of the
	// drained balance, mirroring rippled which transfers the XRP out (draining the
	// account) before deleting it.
	if r := updateAMMAccountIfChanged(view, ammAccountKey, ammAccount); r != ter.TesSUCCESS {
		return r
	}
	result := DeleteAMMAccount(view, asset, asset2)
	if result != ter.TesSUCCESS && result != ter.TecINCOMPLETE {
		return result
	}

	if result == ter.TecINCOMPLETE {
		// Too many trust lines to delete in one tx. Set LPTokenBalance=0 but
		// keep the AMM entry so AMMDelete can finish cleanup.
		amm.LPTokenBalance = lpTokenBalance // zero
		ammBytes, err := serializeAMMData(amm)
		if err != nil {
			return ter.TefINTERNAL
		}
		if err := view.Update(ammKey, ammBytes); err != nil {
			return ter.TefINTERNAL
		}
		if r := updateAMMAccountIfChanged(view, ammAccountKey, ammAccount); r != ter.TesSUCCESS {
			return r
		}
	}

	return result
}
