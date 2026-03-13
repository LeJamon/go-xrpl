package account

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/ledger/entry"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/credential"
	"github.com/LeJamon/goXRPLd/internal/tx/oracle"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

func init() {
	tx.Register(tx.TypeAccountDelete, func() tx.Transaction {
		return &AccountDelete{BaseTx: *tx.NewBaseTx(tx.TypeAccountDelete, "")}
	})

}

// AccountDelete deletes an account from the ledger.
type AccountDelete struct {
	tx.BaseTx

	// Destination is the account to receive remaining XRP (required)
	Destination string `json:"Destination" xrpl:"Destination"`

	// DestinationTag is an arbitrary tag for the destination (optional)
	DestinationTag *uint32 `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`
}

// NewAccountDelete creates a new AccountDelete transaction
func NewAccountDelete(account, destination string) *AccountDelete {
	return &AccountDelete{
		BaseTx:      *tx.NewBaseTx(tx.TypeAccountDelete, account),
		Destination: destination,
	}
}

// TxType returns the transaction type
func (a *AccountDelete) TxType() tx.Type {
	return tx.TypeAccountDelete
}

// Validate validates the AccountDelete transaction
func (a *AccountDelete) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	// Reference: rippled DeleteAccount.cpp preflight() line 51
	if err := tx.CheckFlags(a.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	if a.Destination == "" {
		return tx.Errorf(tx.TemDST_NEEDED, "Destination is required")
	}

	// Cannot delete to self
	// Reference: rippled AccountDelete.cpp preflight()
	if a.Account == a.Destination {
		return tx.Errorf(tx.TemDST_IS_SRC, "cannot delete account to self")
	}

	return nil
}

// CalculateBaseFee returns the minimum fee for AccountDelete.
// AccountDelete requires one owner reserve increment as the base fee.
// Reference: rippled DeleteAccount.cpp calculateBaseFee() — returns view.fees().increment
//
// In rippled, this reads from the live ledger's FeeSettings SLE via
// view.fees().increment, which is populated during Ledger::setup().
// We do the same: read from the FeeSettings SLE on the provided view,
// falling back to the engine config if the SLE cannot be read.
func (a *AccountDelete) CalculateBaseFee(view tx.LedgerView, config tx.EngineConfig) uint64 {
	// Read reserve increment from the FeeSettings SLE on the ledger,
	// matching rippled's view.fees().increment behavior.
	if view != nil {
		data, err := view.Read(keylet.Fees())
		if err == nil && data != nil {
			feeSettings, err := state.ParseFeeSettings(data)
			if err == nil {
				return feeSettings.GetReserveIncrement()
			}
		}
	}
	// Fallback: use the engine config value (from genesis/env config).
	return config.ReserveIncrement
}

// Flatten returns a flat map of all transaction fields
func (a *AccountDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// Apply applies the AccountDelete transaction to ledger state.
// Reference: rippled DeleteAccount.cpp DeleteAccount::preclaim() + doApply()
func (a *AccountDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	// Check minimum ledger gap: account sequence must be far enough behind the ledger.
	// Uses addition (seq + 255 > ledgerSeq) instead of subtraction to avoid uint32 underflow.
	// Reference: rippled DeleteAccount.cpp preclaim():
	//   constexpr std::uint32_t seqDelta{255};
	//   if ((*sleAccount)[sfSequence] + seqDelta > ctx.view.seq())
	//       return tecTOO_SOON;
	//
	// Note: In rippled this check is in preclaim() before sequence consumption.
	// In our engine, Apply() runs after the sequence has already been incremented,
	// so we use the transaction's Sequence field (pre-increment value) for non-ticket
	// transactions, and ctx.Account.Sequence (unchanged) for ticket transactions.
	const seqDelta uint32 = 255
	acctSeq := ctx.Account.Sequence
	if a.GetCommon().TicketSequence == nil && a.GetCommon().Sequence != nil {
		// Non-ticket: the engine already incremented ctx.Account.Sequence by 1.
		// Use the transaction's original sequence for the preclaim check.
		acctSeq = *a.GetCommon().Sequence
	}
	if acctSeq+seqDelta > ctx.Config.LedgerSequence {
		return tx.TecTOO_SOON
	}

	destAccount, destID, result := ctx.LookupAccount(a.Destination)
	if result != tx.TesSUCCESS {
		return result
	}
	destKey := keylet.Account(destID)

	// Check if destination requires a destination tag
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && a.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// Check if destination requires deposit authorization
	if (destAccount.Flags & state.LsfDepositAuth) != 0 {
		preauthKey := keylet.DepositPreauth(destID, ctx.AccountID)
		preauthExists, _ := ctx.View.Exists(preauthKey)
		if !preauthExists {
			return tx.TecNO_PERMISSION
		}
	}

	// --- NFToken preclaim checks ---
	// Reference: rippled DeleteAccount.cpp preclaim() lines 268-285
	rules := ctx.Rules()

	// If NonFungibleTokensV1 is enabled, check NFT-related obligations.
	if rules.Enabled(amendment.FeatureNonFungibleTokensV1) {
		// An issuer cannot be deleted if they have outstanding NFTs in the ledger.
		if ctx.Account.MintedNFTokens != ctx.Account.BurnedNFTokens {
			return tx.TecHAS_OBLIGATIONS
		}

		// An account that owns any NFTs cannot be deleted.
		// Check if any NFToken page exists in the account's NFT page range.
		first := keylet.NFTokenPageMin(ctx.AccountID)
		last := keylet.NFTokenPageMax(ctx.AccountID)

		succKey, _, succFound, succErr := ctx.View.Succ(first.Key)
		if succErr == nil && succFound {
			// Check if the successor key is within the account's NFT page range
			if keyLessEqual(succKey, last.Key) {
				return tx.TecHAS_OBLIGATIONS
			}
		}
	}

	// When fixNFTokenRemint is enabled, enforce the additional constraint:
	// FirstNFTokenSequence + MintedNFTokens + seqDelta > LedgerSequence
	// Reference: rippled DeleteAccount.cpp preclaim() lines 297-312
	if rules.Enabled(amendment.FeatureFixNFTokenRemint) {
		firstNFTSeq := uint32(0)
		if ctx.Account.HasFirstNFTSeq {
			firstNFTSeq = ctx.Account.FirstNFTokenSequence
		}
		if uint64(firstNFTSeq)+uint64(ctx.Account.MintedNFTokens)+uint64(seqDelta) > uint64(ctx.Config.LedgerSequence) {
			return tx.TecTOO_SOON
		}
	}

	// --- Cascade-delete all directory entries ---
	// Reference: rippled DeleteAccount.cpp preclaim() lines 316-358 + doApply() nonObligationDeleter()
	// We check each entry is deletable, count entries (tefTOO_BIG if > 1000),
	// and delete in a single pass.
	const maxDeletableDirEntries = 1000

	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	var entryKeys [][32]byte

	deletableCount := 0
	err := state.DirForEach(ctx.View, ownerDirKey, func(itemKey [32]byte) error {
		entryKeys = append(entryKeys, itemKey)
		return nil
	})
	if err != nil {
		return tx.TefINTERNAL
	}

	for _, itemKey := range entryKeys {
		itemKeylet := keylet.Keylet{Key: itemKey}
		data, err := ctx.View.Read(itemKeylet)
		if err != nil || data == nil {
			continue
		}

		entryType, err := state.GetLedgerEntryType(data)
		if err != nil {
			return tx.TecHAS_OBLIGATIONS
		}

		if !isNonObligationDeletable(entry.Type(entryType)) {
			return tx.TecHAS_OBLIGATIONS
		}

		deletableCount++
		if deletableCount > maxDeletableDirEntries {
			return tx.TefTOO_BIG
		}

		switch entry.Type(entryType) {
		case entry.TypeOffer:
			// Cascade-delete DEX offers: remove from owner dir, book dir, erase.
			// Reference: rippled DeleteAccount.cpp nonObligationDeleter case ltOFFER → offerDelete()
			offer, err := state.ParseLedgerOfferFromBytes(data)
			if err != nil {
				return tx.TefBAD_LEDGER
			}
			// Remove from owner directory
			_, err = state.DirRemove(ctx.View, ownerDirKey, offer.OwnerNode, itemKeylet.Key, false)
			if err != nil {
				return tx.TefBAD_LEDGER
			}
			// Remove from book directory
			bookDirKey := keylet.Keylet{Type: 100, Key: offer.BookDirectory}
			_, err = state.DirRemove(ctx.View, bookDirKey, offer.BookNode, itemKeylet.Key, false)
			if err != nil {
				return tx.TefBAD_LEDGER
			}
			// Erase the offer
			if err := ctx.View.Erase(itemKeylet); err != nil {
				return tx.TefBAD_LEDGER
			}
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}

		case entry.TypeTicket:
			// Cascade-delete tickets: remove from owner dir, erase.
			// Reference: rippled DeleteAccount.cpp nonObligationDeleter case ltTICKET → ticketDelete()
			state.DirRemove(ctx.View, ownerDirKey, 0, itemKeylet.Key, true)
			if err := ctx.View.Erase(itemKeylet); err != nil {
				return tx.TefBAD_LEDGER
			}
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}
			if ctx.Account.TicketCount > 0 {
				ctx.Account.TicketCount--
			}

		case entry.TypeNFTokenOffer:
			// Cascade-delete NFToken offers: remove from owner dir, NFTBuys/NFTSells dir, erase.
			// Reference: rippled DeleteAccount.cpp nonObligationDeleter case ltNFTOKEN_OFFER → nft::deleteTokenOffer()
			nftOffer, err := state.ParseNFTokenOffer(data)
			if err != nil {
				return tx.TefBAD_LEDGER
			}
			// Remove from owner's directory
			state.DirRemove(ctx.View, ownerDirKey, nftOffer.OwnerNode, itemKeylet.Key, false)
			// Remove from NFTBuys or NFTSells directory
			const lsfSellNFToken uint32 = 0x00000001
			isSellOffer := nftOffer.Flags&lsfSellNFToken != 0
			var tokenDirKey keylet.Keylet
			if isSellOffer {
				tokenDirKey = keylet.NFTSells(nftOffer.NFTokenID)
			} else {
				tokenDirKey = keylet.NFTBuys(nftOffer.NFTokenID)
			}
			state.DirRemove(ctx.View, tokenDirKey, nftOffer.NFTokenOfferNode, itemKeylet.Key, false)
			// Erase the offer
			ctx.View.Erase(itemKeylet)
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}

		case entry.TypeDepositPreauth:
			// Cascade-delete deposit preauth: remove from owner dir, erase.
			// Reference: rippled DeleteAccount.cpp nonObligationDeleter case ltDEPOSIT_PREAUTH → DepositPreauth::removeFromLedger()
			preauthEntry, err := state.ParseDepositPreauth(data)
			if err != nil {
				return tx.TefBAD_LEDGER
			}
			_, err = state.DirRemove(ctx.View, ownerDirKey, preauthEntry.OwnerNode, itemKeylet.Key, false)
			if err != nil {
				return tx.TefBAD_LEDGER
			}
			if err := ctx.View.Erase(itemKeylet); err != nil {
				return tx.TefBAD_LEDGER
			}
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}

		case entry.TypeDID:
			// Cascade-delete DID: remove from owner dir, erase.
			// Reference: rippled DeleteAccount.cpp nonObligationDeleter case ltDID → DIDDelete::deleteSLE()
			didData, err := state.ParseDID(data)
			if err != nil {
				return tx.TefBAD_LEDGER
			}
			_, err = state.DirRemove(ctx.View, ownerDirKey, didData.OwnerNode, itemKeylet.Key, false)
			if err != nil {
				return tx.TefBAD_LEDGER
			}
			if err := ctx.View.Erase(itemKeylet); err != nil {
				return tx.TefBAD_LEDGER
			}
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}

		case entry.TypeCredential:
			cred, err := credential.ParseCredentialEntry(data)
			if err != nil {
				return tx.TecHAS_OBLIGATIONS
			}
			if err := credential.DeleteSLE(ctx.View, itemKeylet, cred); err != nil {
				return tx.TecHAS_OBLIGATIONS
			}

		case entry.TypeOracle:
			oracleData, err := state.ParseOracle(data)
			if err != nil {
				return tx.TecHAS_OBLIGATIONS
			}
			// nil ownerCount — account is being deleted, no need to adjust
			if result := oracle.DeleteOracleFromView(ctx.View, itemKeylet, oracleData, ctx.AccountID, nil); result != tx.TesSUCCESS {
				return tx.TecHAS_OBLIGATIONS
			}

		case entry.TypeSignerList:
			// Signer lists are not obligations — cascade-delete them.
			// Reference: rippled DeleteAccount.cpp nonObligationDeleter case ltSIGNER_LIST
			state.DirRemove(ctx.View, ownerDirKey, 0, itemKeylet.Key, true)
			if err := ctx.View.Erase(itemKeylet); err != nil {
				return tx.TecHAS_OBLIGATIONS
			}
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}

		case entry.TypeDelegate:
			// Cascade-delete delegate: remove from owner dir, erase.
			// Reference: rippled DeleteAccount.cpp nonObligationDeleter case ltDELEGATE → DelegateSet::deleteDelegate()
			delegateData, err := state.ParseDelegate(data)
			if err != nil {
				return tx.TefBAD_LEDGER
			}
			_, err = state.DirRemove(ctx.View, ownerDirKey, delegateData.OwnerNode, itemKeylet.Key, false)
			if err != nil {
				return tx.TefBAD_LEDGER
			}
			if err := ctx.View.Erase(itemKeylet); err != nil {
				return tx.TefBAD_LEDGER
			}
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}

		default:
			return tx.TecHAS_OBLIGATIONS
		}
	}

	// Erase any remaining empty owner directory root page
	if dirData, err := ctx.View.Read(ownerDirKey); err == nil && dirData != nil {
		ctx.View.Erase(ownerDirKey)
	}

	// Re-read destination in case it was modified during cascade deletions
	destData, err := ctx.View.Read(destKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	destAccount, err = state.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Transfer balance from source to destination.
	// Reference: rippled DeleteAccount.cpp doApply() lines 414-416:
	//   (*dst)[sfBalance] = (*dst)[sfBalance] + mSourceBalance;
	//   (*src)[sfBalance] = (*src)[sfBalance] - mSourceBalance;
	sourceBalance := ctx.Account.Balance
	destAccount.Balance += sourceBalance
	ctx.Account.Balance -= sourceBalance

	// Re-arm the password change fee if we can and need to.
	// When XRP is transferred to the destination, clear the PasswordSpent flag
	// so the destination can use the free SetRegularKey password change again.
	// Reference: rippled DeleteAccount.cpp doApply() lines 431-433:
	//   if (mSourceBalance > XRPAmount(0) && dst->isFlag(lsfPasswordSpent))
	//       dst->clearFlag(lsfPasswordSpent);
	if sourceBalance > 0 && (destAccount.Flags&state.LsfPasswordSpent) != 0 {
		destAccount.Flags &^= state.LsfPasswordSpent
	}

	if result := ctx.UpdateAccountRoot(destID, destAccount); result != tx.TesSUCCESS {
		return result
	}

	// Update the source account in the view with balance=0 BEFORE erasing.
	// This ensures the erased entry's Current data has the correct final state
	// for invariant checks (XRPNotCreated verifies net XRP changes).
	// Reference: rippled DeleteAccount.cpp — sets src balance to 0, then erases.
	if result := ctx.UpdateAccountRoot(ctx.AccountID, ctx.Account); result != tx.TesSUCCESS {
		return result
	}
	srcKey := keylet.Account(ctx.AccountID)
	if err := ctx.View.Erase(srcKey); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// isNonObligationDeletable returns true if a ledger entry type can be cascade-deleted
// during AccountDelete. Entry types not in this list are obligations that prevent deletion.
// Reference: rippled DeleteAccount.cpp nonObligationDeleter()
func isNonObligationDeletable(t entry.Type) bool {
	switch t {
	case entry.TypeOffer,
		entry.TypeSignerList,
		entry.TypeTicket,
		entry.TypeDepositPreauth,
		entry.TypeNFTokenOffer,
		entry.TypeDID,
		entry.TypeOracle,
		entry.TypeCredential,
		entry.TypeDelegate:
		return true
	default:
		return false
	}
}

// keyLessEqual returns true if a <= b (byte-level comparison of 32-byte keys).
func keyLessEqual(a, b [32]byte) bool {
	for i := 0; i < 32; i++ {
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}
	return true // equal
}
