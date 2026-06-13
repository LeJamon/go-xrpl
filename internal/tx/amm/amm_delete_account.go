package amm

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// maxDeletableAMMTrustLines is the maximum number of trust lines that can be
// deleted in a single transaction when cleaning up an AMM account.
// Reference: rippled Protocol.h maxDeletableAMMTrustLines = 512
const maxDeletableAMMTrustLines = 512

// deleteAMMTrustLine deletes a single trust line owned by the AMM account.
// It removes the trust line from both accounts' owner directories, erases it,
// and decrements the non-AMM account's OwnerCount.
// Reference: rippled View.cpp deleteAMMTrustLine (line 2720)
func deleteAMMTrustLine(view tx.LedgerView, lineKey keylet.Keylet, rs *state.RippleState, ammAccountID [20]byte) tx.Result {
	lowAccountID, err := state.DecodeAccountID(rs.LowLimit.Issuer)
	if err != nil {
		return tx.TecINTERNAL
	}
	highAccountID, err := state.DecodeAccountID(rs.HighLimit.Issuer)
	if err != nil {
		return tx.TecINTERNAL
	}

	lowAccountData, err := view.Read(keylet.Account(lowAccountID))
	if err != nil || lowAccountData == nil {
		return tx.TecINTERNAL
	}
	lowAccount, err := state.ParseAccountRoot(lowAccountData)
	if err != nil {
		return tx.TecINTERNAL
	}
	highAccountData, err := view.Read(keylet.Account(highAccountID))
	if err != nil || highAccountData == nil {
		return tx.TecINTERNAL
	}
	highAccount, err := state.ParseAccountRoot(highAccountData)
	if err != nil {
		return tx.TecINTERNAL
	}

	zeroHash := [32]byte{}
	ammLow := lowAccount.AMMID != zeroHash
	ammHigh := highAccount.AMMID != zeroHash

	// Can't both be AMM
	if ammLow && ammHigh {
		return tx.TecINTERNAL
	}
	// At least one must be AMM
	if !ammLow && !ammHigh {
		return tx.TerNO_AMM
	}
	// One must be the target AMM
	if lowAccountID != ammAccountID && highAccountID != ammAccountID {
		return tx.TerNO_AMM
	}

	if trustDelete(view, lineKey, lowAccountID, highAccountID, rs.LowNode, rs.HighNode) != nil {
		return tx.TefBAD_LEDGER
	}

	// Decrement OwnerCount for each non-AMM side that has a reserve flag set.
	// Unlike rippled — which deletes AMM pool lines during withdraw (so its
	// deleteAMMTrustLine only ever runs on LP-holder lines where the non-AMM
	// side carries the reserve) — goXRPL keeps the pool lines until the whole
	// AMM account is deleted here. So this also runs on AMM-reserve-side pool
	// lines, where the non-AMM (issuer) side has no reserve flag and must be
	// skipped. Reference: rippled View.cpp deleteAMMTrustLine line 2759-2763.
	if rs.Flags&state.LsfLowReserve != 0 && !ammLow {
		if lowAccount.OwnerCount > 0 {
			lowAccount.OwnerCount--
		}
		lowBytes, err := state.SerializeAccountRoot(lowAccount)
		if err != nil {
			return tx.TecINTERNAL
		}
		if err := view.Update(keylet.Account(lowAccountID), lowBytes); err != nil {
			return tx.TecINTERNAL
		}
	}
	if rs.Flags&state.LsfHighReserve != 0 && !ammHigh {
		if highAccount.OwnerCount > 0 {
			highAccount.OwnerCount--
		}
		highBytes, err := state.SerializeAccountRoot(highAccount)
		if err != nil {
			return tx.TecINTERNAL
		}
		if err := view.Update(keylet.Account(highAccountID), highBytes); err != nil {
			return tx.TecINTERNAL
		}
	}

	return tx.TesSUCCESS
}

// deleteAMMTrustLines iterates the AMM account's owner directory and deletes
// trust lines up to maxTrustlinesToDelete. If more trust lines remain, returns
// tecINCOMPLETE. Skips AMM entries (ltAMM type).
// Reference: rippled AMMUtils.cpp deleteAMMTrustLines (line 237)
func deleteAMMTrustLines(view tx.LedgerView, ammAccountID [20]byte, maxTrustlinesToDelete int) tx.Result {
	ownerDirKey := keylet.OwnerDir(ammAccountID)

	rootData, err := view.Read(ownerDirKey)
	if err != nil || rootData == nil {
		return tx.TesSUCCESS // No directory = nothing to delete
	}

	root, err := state.ParseDirectoryNode(rootData)
	if err != nil {
		return tx.TecINTERNAL
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
					return tx.TecINCOMPLETE
				}
			}

			itemKey := currentPage.Indexes[i]
			itemKeylet := keylet.Keylet{Key: itemKey}

			itemData, err := view.Read(itemKeylet)
			if err != nil || itemData == nil {
				return tx.TefBAD_LEDGER
			}

			entryType, err := state.GetLedgerEntryType(itemData)
			if err != nil {
				return tx.TecINTERNAL
			}

			// Skip the AMM SLE that coexists with the trust lines in this dir.
			if entry.Type(entryType) == entry.TypeAMM {
				i++
				continue
			}

			if entry.Type(entryType) != entry.TypeRippleState {
				return tx.TecINTERNAL
			}

			rs, err := state.ParseRippleState(itemData)
			if err != nil {
				return tx.TecINTERNAL
			}
			if !rs.Balance.IsZero() {
				return tx.TecINTERNAL
			}

			result := deleteAMMTrustLine(view, itemKeylet, rs, ammAccountID)
			if result != tx.TesSUCCESS {
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
				return tx.TecINTERNAL
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
			return tx.TecINTERNAL
		}
	}

	return tx.TesSUCCESS
}

// DeleteAMMAccount performs full cleanup of an AMM account:
// 1. Deletes trust lines from the AMM's owner directory (bounded)
// 2. Removes AMM SLE from owner directory
// 3. Deletes empty owner directory
// 4. Erases AMM SLE and account root
// Reference: rippled AMMUtils.cpp deleteAMMAccount (line 283)
func DeleteAMMAccount(view tx.LedgerView, asset, asset2 tx.Asset) tx.Result {
	ammKey := computeAMMKeylet(asset, asset2)
	ammRawData, err := view.Read(ammKey)
	if err != nil || ammRawData == nil {
		return tx.TecINTERNAL
	}

	amm, err := parseAMMData(ammRawData)
	if err != nil {
		return tx.TecINTERNAL
	}

	ammAccountID := amm.Account

	ammAccountKey := keylet.Account(ammAccountID)
	ammAccountData, err := view.Read(ammAccountKey)
	if err != nil || ammAccountData == nil {
		return tx.TecINTERNAL
	}

	if result := deleteAMMTrustLines(view, ammAccountID, maxDeletableAMMTrustLines); result != tx.TesSUCCESS {
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
		return tx.TecINTERNAL
	}
	if err := view.Erase(ammAccountKey); err != nil {
		return tx.TecINTERNAL
	}

	return tx.TesSUCCESS
}

// deleteAMMAccountIfEmpty is called from AMMWithdraw when LP tokens reach zero.
// If deleteAMMAccount returns tesSUCCESS, the AMM is fully deleted.
// If it returns tecINCOMPLETE, the AMM stays in an empty state (LPTokenBalance=0)
// and requires AMMDelete to finish cleanup.
// Reference: rippled AMMWithdraw.cpp deleteAMMAccountIfEmpty (line 718)
func deleteAMMAccountIfEmpty(view tx.LedgerView, ammKey keylet.Keylet, ammAccountKey keylet.Keylet,
	lpTokenBalance tx.Amount, asset, asset2 tx.Asset, amm *AMMData, ammAccount *state.AccountRoot) tx.Result {
	if !lpTokenBalance.IsZero() {
		// Not empty, just update the AMM
		amm.LPTokenBalance = lpTokenBalance
		ammBytes, err := serializeAMMData(amm)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := view.Update(ammKey, ammBytes); err != nil {
			return tx.TefINTERNAL
		}
		ammAccountBytes, err := state.SerializeAccountRoot(ammAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := view.Update(ammAccountKey, ammAccountBytes); err != nil {
			return tx.TefINTERNAL
		}
		return tx.TesSUCCESS
	}

	// LP tokens are zero — try to delete the AMM account
	result := DeleteAMMAccount(view, asset, asset2)
	if result != tx.TesSUCCESS && result != tx.TecINCOMPLETE {
		return result
	}

	if result == tx.TecINCOMPLETE {
		// Too many trust lines to delete in one tx. Set LPTokenBalance=0 but
		// keep the AMM entry so AMMDelete can finish cleanup.
		amm.LPTokenBalance = lpTokenBalance // zero
		ammBytes, err := serializeAMMData(amm)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := view.Update(ammKey, ammBytes); err != nil {
			return tx.TefINTERNAL
		}
		ammAccountBytes, err := state.SerializeAccountRoot(ammAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := view.Update(ammAccountKey, ammAccountBytes); err != nil {
			return tx.TefINTERNAL
		}
	}

	return result
}
