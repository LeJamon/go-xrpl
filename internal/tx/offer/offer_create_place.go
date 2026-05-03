package offer

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/keylet"
)

// placeRemainingOffer creates the order-book and owner-directory entries for
// the un-crossed remainder of an OfferCreate, then writes the new Offer SLE
// into the main sandbox. Hybrid offers additionally get an open-book entry
// via applyHybridInSandbox.
//
// Reference: rippled CreateOffer.cpp lines 836-928
func (o *OfferCreate) placeRemainingOffer(
	ctx *tx.ApplyContext,
	sb *payment.PaymentSandbox,
	saTakerPays, saTakerGets tx.Amount,
	uRate uint64,
	bPassive, bSell, bHybrid bool,
) (tx.Result, bool) {
	// Create the offer in the ledger (in main sandbox)
	// Reference: lines 837-925
	offerSequence := o.getOfferSequence()
	offerKey := keylet.Offer(ctx.AccountID, offerSequence)

	// Calculate book directory fields first (needed for both owner and book directories
	// when SortedDirectories is not enabled)
	// Reference: lines 857-887
	takerPaysCurrency := state.GetCurrencyBytes(saTakerPays.Currency)
	takerPaysIssuer := state.GetIssuerBytes(saTakerPays.Issuer)
	takerGetsCurrency := state.GetCurrencyBytes(saTakerGets.Currency)
	takerGetsIssuer := state.GetIssuerBytes(saTakerGets.Issuer)

	// Domain offers go in a separate domain-keyed book directory.
	// Reference: rippled Indexes.cpp getBookBase() includes domain in hash when set
	var bookBase keylet.Keylet
	if o.DomainID != nil {
		bookBase = keylet.BookDirWithDomain(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer, *o.DomainID)
	} else {
		bookBase = keylet.BookDir(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer)
	}
	bookDirKey := keylet.Quality(bookBase, uRate)

	// Reference: lines 839-848
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	ownerDirResult, err := state.DirInsert(sb, ownerDirKey, offerKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		return tx.TefINTERNAL, false
	}

	// Reference: line 851
	ctx.Account.OwnerCount++

	// Reference: lines 884-893
	bookDirResult, err := state.DirInsert(sb, bookDirKey, offerKey.Key, func(dir *state.DirectoryNode) {
		dir.TakerPaysCurrency = takerPaysCurrency
		dir.TakerPaysIssuer = takerPaysIssuer
		dir.TakerGetsCurrency = takerGetsCurrency
		dir.TakerGetsIssuer = takerGetsIssuer
		dir.ExchangeRate = uRate
		// Note: DomainID is stored on the offer itself, not the directory
	})
	if err != nil {
		return tx.TefINTERNAL, false
	}

	// Reference: lines 895-910
	ledgerOffer := &state.LedgerOffer{
		Account:           ctx.Account.Account,
		Sequence:          offerSequence,
		TakerPays:         saTakerPays,
		TakerGets:         saTakerGets,
		BookDirectory:     bookDirKey.Key,
		BookNode:          bookDirResult.Page,
		OwnerNode:         ownerDirResult.Page,
		Flags:             0,
		PreviousTxnID:     ctx.TxHash,
		PreviousTxnLgrSeq: ctx.Config.LedgerSequence,
	}

	// Reference: line 903-904
	if o.Expiration != nil {
		ledgerOffer.Expiration = *o.Expiration
	}

	// Reference: lines 905-910
	if bPassive {
		ledgerOffer.Flags |= lsfOfferPassive
	}
	if bSell {
		ledgerOffer.Flags |= lsfOfferSell
	}

	if o.DomainID != nil {
		ledgerOffer.DomainID = *o.DomainID
	}

	// Handle hybrid offers
	// Reference: lines 912-919
	if bHybrid {
		if result := applyHybridInSandbox(sb, ctx, ledgerOffer, offerKey, saTakerPays, saTakerGets, bookDirKey); result != tx.TesSUCCESS {
			return result, false
		}
	}

	// Serialize and store the offer
	offerData, err := state.SerializeLedgerOffer(ledgerOffer)
	if err != nil {
		return tx.TefINTERNAL, false
	}

	if err := sb.Insert(offerKey, offerData); err != nil {
		return tx.TefINTERNAL, false
	}

	return tx.TesSUCCESS, true // Apply main sandbox
}
