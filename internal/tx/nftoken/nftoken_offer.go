package nftoken

import (
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// errOfferDirRemove reports that an NFTokenOffer could not be removed from one
// of its directories, mirroring the false return of rippled's deleteTokenOffer
// (which the caller turns into tecINTERNAL).
var errOfferDirRemove = errors.New("nftoken offer directory removal failed")

// ---------------------------------------------------------------------------
// Offer management — deleteTokenOffer with proper directory cleanup
// Reference: rippled NFTokenUtils.cpp deleteTokenOffer
// ---------------------------------------------------------------------------

// deleteTokenOffer deletes an NFToken offer and removes it from directories.
// It handles:
// 1. Reading the offer to get owner, token ID, flags
// 2. Removing from owner's directory (using OwnerNode)
// 3. Removing from NFTBuys/NFTSells directory (using NFTokenOfferNode)
// 4. Erasing the offer SLE
// 5. Decrementing owner's OwnerCount
// 6. Refunding escrowed amount for buy offers
func deleteTokenOffer(view tx.LedgerView, offerKL keylet.Keylet) error {
	offerData, err := view.Read(offerKL)
	if err != nil {
		return err
	}

	offer, err := state.ParseNFTokenOffer(offerData)
	if err != nil {
		return err
	}

	ownerDirKey := keylet.OwnerDir(offer.Owner)
	ownerResult, err := state.DirRemove(view, ownerDirKey, offer.OwnerNode, offerKL.Key, false)
	if err != nil {
		return err
	}
	if !ownerResult.Success {
		return errOfferDirRemove
	}

	isSellOffer := offer.Flags&lsfSellNFToken != 0
	var tokenDirKey keylet.Keylet
	if isSellOffer {
		tokenDirKey = keylet.NFTSells(offer.NFTokenID)
	} else {
		tokenDirKey = keylet.NFTBuys(offer.NFTokenID)
	}
	tokenResult, err := state.DirRemove(view, tokenDirKey, offer.NFTokenOfferNode, offerKL.Key, false)
	if err != nil {
		return err
	}
	if !tokenResult.Success {
		return errOfferDirRemove
	}

	return view.Erase(offerKL)
}

// deleteNFTokenOffersResult holds the result of deleting NFToken offers
type deleteNFTokenOffersResult struct {
	TotalDeleted int
	SelfDeleted  int // offers owned by selfAccountID
}

// deleteNFTokenOffers deletes offers for an NFToken, walking the offer
// directory page by page and removing entries within each page in reverse
// order, matching rippled's removeTokenOffersWithLimit. The order is
// observable when the deletion limit truncates the offer set.
// selfAccountID identifies the ctx.Account — offers from this account
// are counted separately so the caller can adjust ctx.Account.OwnerCount
// (since the engine overwrites view changes for ctx.Account).
//
// A failure to delete a collected offer (corrupt entry, directory removal
// failure) aborts the transaction with tefEXCEPTION: rippled throws from
// removeTokenOffersWithLimit when deleteTokenOffer returns false
// (NFTokenUtils.cpp:638-643) and doApply converts the exception to
// tefEXCEPTION. Offers missing from the directory are skipped, matching
// rippled's null peek.
// Reference: rippled NFTokenUtils.cpp removeTokenOffersWithLimit
func deleteNFTokenOffers(tokenID [32]byte, sellOffers bool, limit int, view tx.LedgerView, selfAccountID [20]byte) (deleteNFTokenOffersResult, tx.Result) {
	result := deleteNFTokenOffersResult{}
	if limit <= 0 {
		return result, tx.TesSUCCESS
	}

	var dirKey keylet.Keylet
	if sellOffers {
		dirKey = keylet.NFTSells(tokenID)
	} else {
		dirKey = keylet.NFTBuys(tokenID)
	}

	pageIndex := uint64(0)
	for {
		pageData, err := view.Read(keylet.DirPage(dirKey.Key, pageIndex))
		if err != nil {
			return result, tx.TefEXCEPTION
		}
		if pageData == nil {
			break
		}

		page, err := state.ParseDirectoryNode(pageData)
		if err != nil {
			return result, tx.TefEXCEPTION
		}

		// Capture the next page before deleting: removing the last entry
		// erases the current page.
		pageIndex = page.IndexNext

		for i := len(page.Indexes) - 1; i >= 0; i-- {
			offerKL := keylet.Keylet{Key: page.Indexes[i]}

			offerData, err := view.Read(offerKL)
			if err != nil {
				return result, tx.TefEXCEPTION
			}
			if offerData == nil {
				continue
			}

			offer, err := state.ParseNFTokenOffer(offerData)
			if err != nil {
				return result, tx.TefEXCEPTION
			}

			isSelf := offer.Owner == selfAccountID

			// NFToken buy offers do NOT escrow XRP — no refund needed on deletion.
			// Reference: rippled NFTokenUtils.cpp deleteTokenOffer — no balance adjustment

			// Decrement owner count (only via view for non-self accounts)
			if !isSelf {
				adjustOwnerCountViaView(view, offer.Owner, -1)
			}

			ownerDirKey := keylet.OwnerDir(offer.Owner)
			if res, err := state.DirRemove(view, ownerDirKey, offer.OwnerNode, offerKL.Key, false); err != nil || !res.Success {
				return result, tx.TefEXCEPTION
			}

			// Remove the offer from the NFT buy/sell offer directory we are
			// iterating. rippled's deleteTokenOffer issues a second dirRemove on
			// nft_sells/nft_buys (NFTokenUtils.cpp:698-704); when this empties the
			// directory page, dirRemove erases it (keepRoot=false), emitting the
			// DeletedNode:DirectoryNode. Without this the page is left in state with
			// stale Indexes, diverging both account_hash and transaction_hash.
			if res, err := state.DirRemove(view, dirKey, offer.NFTokenOfferNode, offerKL.Key, false); err != nil || !res.Success {
				return result, tx.TefEXCEPTION
			}

			if err := view.Erase(offerKL); err != nil {
				return result, tx.TefEXCEPTION
			}

			result.TotalDeleted++
			if isSelf {
				result.SelfDeleted++
			}
			if result.TotalDeleted == limit {
				return result, tx.TesSUCCESS
			}
		}

		if pageIndex == 0 {
			break
		}
	}

	return result, tx.TesSUCCESS
}

// notTooManyOffers checks whether the total number of buy + sell offers
// for a token exceeds maxDeletableTokenOfferEntries.
// Reference: rippled NFTokenUtils.cpp notTooManyOffers
func notTooManyOffers(view tx.LedgerView, tokenID [32]byte) tx.Result {
	totalOffers := 0

	// Count buy offers
	buysKey := keylet.NFTBuys(tokenID)
	if exists, _ := view.Exists(buysKey); exists {
		state.DirForEach(view, buysKey, func(itemKey [32]byte) error {
			totalOffers++
			if totalOffers > maxDeletableTokenOfferEntries {
				return fmt.Errorf("too many")
			}
			return nil
		})
	}

	// Count sell offers
	sellsKey := keylet.NFTSells(tokenID)
	if exists, _ := view.Exists(sellsKey); exists {
		state.DirForEach(view, sellsKey, func(itemKey [32]byte) error {
			totalOffers++
			if totalOffers > maxDeletableTokenOfferEntries {
				return fmt.Errorf("too many")
			}
			return nil
		})
	}

	if totalOffers > maxDeletableTokenOfferEntries {
		return tx.TefTOO_BIG
	}

	return tx.TesSUCCESS
}

// adjustOwnerCountViaView adjusts an account's OwnerCount through the view.
// Use this for accounts that are NOT ctx.Account (the submitter).
func adjustOwnerCountViaView(view tx.LedgerView, accountID [20]byte, delta int) {
	_ = tx.AdjustOwnerCount(view, accountID, delta)
}

// tokenOfferCreateApply creates a sell offer for a newly minted NFToken.
// This is the shared logic used by both NFTokenCreateOffer and NFTokenMint (with Amount).
// Reference: rippled NFTokenUtils.cpp tokenOfferCreateApply
func tokenOfferCreateApply(
	ctx *tx.ApplyContext,
	accountID [20]byte,
	tokenID [32]byte,
	amount *tx.Amount,
	destination string,
	expiration *uint32,
	seqProxy uint32,
	priorBalance uint64,
) tx.Result {
	// Check reserve using priorBalance (balance before fee deduction)
	// Reference: rippled NFTokenUtils.cpp tokenOfferCreateApply line 1037
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if priorBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	offerKey := keylet.NFTokenOffer(accountID, seqProxy)

	ownerDirKey := keylet.OwnerDir(accountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, offerKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = accountID
	})
	if err != nil {
		return tx.TefINTERNAL
	}
	ownerNode := dirResult.Page

	// Insert into NFTSells directory (mint always creates sell offers). rippled
	// stamps the offer directory root with sfFlags (lsfNFTokenSellOffers) and
	// sfNFTokenID via the describe callback (NFTokenUtils.cpp:1059-1063).
	tokenDirKey := keylet.NFTSells(tokenID)
	tokenDirResult, err := state.DirInsert(ctx.View, tokenDirKey, offerKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Flags = lsfNFTokenSellOffers
		dir.NFTokenID = tokenID
	})
	if err != nil {
		return tx.TefINTERNAL
	}
	offerNode := tokenDirResult.Page

	flags := NFTokenCreateOfferFlagSellNFToken // Always a sell offer

	offerData, err := serializeNFTokenOfferRaw(
		accountID, tokenID,
		amountToCodecFormat(*amount), flags,
		ownerNode, offerNode,
		destination, expiration,
	)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Insert(offerKey, offerData); err != nil {
		return tx.TefINTERNAL
	}

	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}
