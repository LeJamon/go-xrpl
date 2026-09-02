package offer

import (
	"errors"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
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
) (ter.Result, bool) {
	// Create the offer in the ledger (in main sandbox)
	// Reference: lines 837-925
	offerSequence := o.GetCommon().SeqProxy()
	offerKey := keylet.Offer(ctx.AccountID, offerSequence)

	bookBase, err := offerBookBase(saTakerPays, saTakerGets, o.DomainID)
	if err != nil {
		return ter.TefINTERNAL, false
	}
	bookDirKey := keylet.Quality(bookBase, uRate)

	// Reference: lines 839-848
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	ownerDirResult, err := state.DirInsert(sb, ownerDirKey, offerKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		result := mapOfferDirInsertError(err)
		return result, result == ter.TecDIR_FULL
	}

	// Reference: line 851
	ctx.Account.OwnerCount++

	// Reference: lines 884-893
	bookDirResult, err := state.DirInsert(sb, bookDirKey, offerKey.Key, true, func(dir *state.DirectoryNode) {
		_ = setBookDirectoryAssets(dir, saTakerPays, saTakerGets)
		dir.ExchangeRate = uRate
		if o.DomainID != nil {
			dir.DomainID = *o.DomainID
		}
	})
	if err != nil {
		result := mapOfferDirInsertError(err)
		return result, result == ter.TecDIR_FULL
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

	// Handle hybrid offers. Post-fixCleanup3_2_0 the open-book directory uses the
	// original placement rate (the same uRate as the domain-book directory) so the
	// two book pages agree; pre-amendment it was recomputed from the post-crossing
	// amounts and could land on a differently-keyed page due to rounding.
	// Reference: lines 912-919, 944-954.
	if bHybrid {
		openRate := state.GetRateWithNumberContext(saTakerGets, saTakerPays, ctx.NumberContext())
		if ctx.Rules().Enabled(amendment.FeatureFixCleanup3_2_0) {
			openRate = uRate
		}
		if result := applyHybridInSandbox(sb, ledgerOffer, offerKey, saTakerPays, saTakerGets, openRate); result != ter.TesSUCCESS {
			return result, result == ter.TecDIR_FULL
		}
	}

	// Serialize and store the offer
	offerData, err := state.SerializeLedgerOffer(ledgerOffer)
	if err != nil {
		return ter.TefINTERNAL, false
	}

	if err := sb.Insert(offerKey, offerData); err != nil {
		return ter.TefINTERNAL, false
	}

	return ter.TesSUCCESS, true // Apply main sandbox
}

func mapOfferDirInsertError(err error) ter.Result {
	if errors.Is(err, state.ErrDirFull) {
		return ter.TecDIR_FULL
	}
	return ter.TefINTERNAL
}

func offerBookBase(takerPays, takerGets tx.Amount, domainID *[32]byte) (keylet.Keylet, error) {
	pays, err := amountBookSide(takerPays)
	if err != nil {
		return keylet.Keylet{}, err
	}
	gets, err := amountBookSide(takerGets)
	if err != nil {
		return keylet.Keylet{}, err
	}
	return keylet.BookBase(pays, gets, domainID), nil
}

func amountBookSide(amount tx.Amount) (keylet.BookSide, error) {
	if amount.IsMPT() {
		id, err := mptutil.DecodeID(amount.MPTIssuanceID())
		if err != nil {
			return keylet.BookSide{}, err
		}
		return keylet.MPTSide(id), nil
	}
	if !amount.IsNative() && amount.Currency == "" {
		return keylet.BookSide{}, errors.New("issued amount has no currency")
	}
	return keylet.IssueSide(
		keylet.CurrencyBytes(amount.Currency),
		state.GetIssuerBytes(amount.Issuer),
	), nil
}

func setBookDirectoryAssets(dir *state.DirectoryNode, takerPays, takerGets tx.Amount) error {
	pays, err := amountBookSide(takerPays)
	if err != nil {
		return err
	}
	gets, err := amountBookSide(takerGets)
	if err != nil {
		return err
	}
	if pays.IsMPT {
		id := pays.MPTID
		dir.TakerPaysMPT = &id
	} else {
		dir.TakerPaysCurrency = pays.Currency
		dir.TakerPaysIssuer = pays.Issuer
	}
	if gets.IsMPT {
		id := gets.MPTID
		dir.TakerGetsMPT = &id
	} else {
		dir.TakerGetsCurrency = gets.Currency
		dir.TakerGetsIssuer = gets.Issuer
	}
	return nil
}
