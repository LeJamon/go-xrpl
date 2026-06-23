package payment

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

func (s *BookStep) offerTakerGets(offer *state.LedgerOffer) EitherAmount {
	if s.book.Out.IsXRP() {
		return NewXRPEitherAmount(offer.TakerGets.Drops())
	}
	return NewIOUEitherAmount(offer.TakerGets)
}

// offerTakerPays returns what the taker pays to this offer
func (s *BookStep) offerTakerPays(offer *state.LedgerOffer) EitherAmount {
	if s.book.In.IsXRP() {
		return NewXRPEitherAmount(offer.TakerPays.Drops())
	}
	return NewIOUEitherAmount(offer.TakerPays)
}

// offerQuality returns the quality of an offer, taken from its BookDirectory key
// rather than recomputed from the offer's current TakerPays/TakerGets.
//
// The quality is computed when the offer is placed and never changes for its
// lifetime: subsequent partial fills use the original quality. Recomputing from
// the partially-filled amounts drifts ~1 ULP from the placement tier, which then
// feeds the strict crossing round and makes the fill consume a slightly
// different amount than rippled.
func (s *BookStep) offerQuality(offer *state.LedgerOffer) Quality {
	return QualityFromKey(offer.BookDirectory)
}

// consumeOffer reduces the offer's amounts by the consumed amounts and transfers funds.
// consumedInGross is the GROSS amount (what taker pays, includes trIn transfer fee)
// consumedInNet is the NET amount (what offer owner receives, after trIn transfer fee)
// consumedOut is the NET amount the taker receives (offer's TakerGets portion)
// ownerGives is the GROSS amount the offer owner debits (consumedOut * trOut, includes trOut fee)
// Note: ownerGives >= consumedOut; the difference is the transfer fee that stays with the issuer.
// Reference: rippled BookStep.cpp consumeOffer() passes ownerGives to accountSend(owner → book.out.account)
func (s *BookStep) consumeOffer(sb *PaymentSandbox, offer *state.LedgerOffer, consumedInGross, consumedInNet, consumedOut, ownerGives EitherAmount) error {
	offerOwner, err := state.DecodeAccountID(offer.Account)
	if err != nil {
		return err
	}

	txHash, ledgerSeq := sb.GetTransactionContext()

	grossIn := consumedInGross
	netIn := consumedInNet

	// 1. Transfer input currency with transfer fee:
	//    - For IOU: Transfer from input issuer (book.In.Issuer) to offer owner
	//    - For XRP: Transfer from XRP pseudo-account (zero) to offer owner.
	//      The XRPEndpointStep before BookStep handles deducting XRP from the source account.
	//    Reference: rippled BookStep.cpp - sends from book_.in.account (issuer for IOU, zero for XRP)
	inSource := s.book.In.Issuer // For XRP: zero account; for IOU: the issuer
	if err := s.transferFundsWithFee(sb, inSource, offerOwner, grossIn, netIn, s.book.In); err != nil {
		return err
	}

	// 2. Debit ownerGives from offer owner → book.out.account (issuer for IOU, zero for XRP).
	//    ownerGives is the GROSS amount the owner pays (consumedOut * trOut), not just consumedOut.
	//    The difference (ownerGives - consumedOut) is the transfer fee retained by the issuer.
	//    The DirectStepI or XRPEndpointStep after BookStep issues consumedOut to the actual destination.
	//    Reference: rippled BookStep.cpp consumeOffer: accountSend(offer.owner(), book_.out.account, ownerGives)
	outRecipient := s.book.Out.Issuer // For XRP: zero account; for IOU: the issuer
	if err := s.transferFunds(sb, offerOwner, outRecipient, ownerGives, s.book.Out); err != nil {
		return err
	}

	// 3. Update offer's remaining amounts (use NET input for offer consumption)
	offerKey := keylet.Offer(offerOwner, offer.Sequence)

	origPays := s.offerTakerPays(offer)
	origGets := s.offerTakerGets(offer)
	newTakerPays := s.subtractFromAmount(origPays, netIn)
	newTakerGets := s.subtractFromAmount(origGets, consumedOut)

	// Update offer's remaining amounts.
	// Reference: rippled Offer.h consume() — just subtracts consumed amounts
	// and updates the SLE. Does NOT check remaining funding or delete.
	// The OfferStream's step() function handles unfunded offer detection
	// on subsequent iterations.
	offer.TakerPays = s.eitherAmountToTxAmount(newTakerPays, s.book.In)
	offer.TakerGets = s.eitherAmountToTxAmount(newTakerGets, s.book.Out)
	if newTakerPays.IsZero() || newTakerGets.IsZero() {
		// Fully consumed — update with zero amounts for metadata, then delete.
		offerData, err := state.SerializeLedgerOffer(offer)
		if err != nil {
			return err
		}
		if err := sb.Update(offerKey, offerData); err != nil {
			return err
		}
		if err := s.deleteOffer(sb, offer, offerOwner, txHash, ledgerSeq); err != nil {
			return err
		}
	} else {
		// Partially consumed — just update the offer amounts.
		// Do NOT check remaining funding here. Rippled's consume() does not
		// check funding; the OfferStream handles unfunded detection on the
		// next step() call.
		//
		// Do NOT stamp PreviousTxnID/PreviousTxnLgrSeq here. Threading is the
		// ApplyStateTable's job (threadItem), which runs only after its
		// *curNode == *origNode check (ApplyStateTable.cpp:156-157). When a
		// crossing consumes an amount that rounds away against a near-infinite
		// offer (e.g. TakerGets 9999999999999999e80), the recomputed amounts
		// are byte-identical to the original; rippled then leaves the offer
		// entirely untouched (no thread, no metadata node). Stamping here would
		// make Current differ from Original only in PreviousTxn*, defeating that
		// skip and forking the state tree with a ghost ModifiedNode.
		offerData, err := state.SerializeLedgerOffer(offer)
		if err != nil {
			return err
		}
		if err := sb.Update(offerKey, offerData); err != nil {
			return err
		}
	}

	return nil
}

// zeroOut returns a zero EitherAmount for the output currency.
func (s *BookStep) zeroOut() EitherAmount {
	if s.book.Out.IsXRP() {
		return ZeroXRPEitherAmount()
	}
	return ZeroIOUEitherAmount(s.book.Out.Currency, state.EncodeAccountIDSafe(s.book.Out.Issuer))
}

// zeroIn returns a zero EitherAmount for the input currency.
func (s *BookStep) zeroIn() EitherAmount {
	if s.book.In.IsXRP() {
		return ZeroXRPEitherAmount()
	}
	return ZeroIOUEitherAmount(s.book.In.Currency, state.EncodeAccountIDSafe(s.book.In.Issuer))
}

// deleteOffer properly deletes an offer from the ledger.
func (s *BookStep) deleteOffer(sb *PaymentSandbox, offer *state.LedgerOffer, owner [20]byte, txHash [32]byte, ledgerSeq uint32) error {
	offerKey := keylet.Offer(owner, offer.Sequence)

	ownerDirKey := keylet.OwnerDir(owner)
	ownerResult, err := state.DirRemove(sb, ownerDirKey, offer.OwnerNode, offerKey.Key, false)
	if err != nil {
	}
	if ownerResult != nil {
		s.applyDirRemoveResult(sb, ownerResult)
	}

	bookDirKey := keylet.Keylet{Key: offer.BookDirectory}
	bookResult, err := state.DirRemove(sb, bookDirKey, offer.BookNode, offerKey.Key, false)
	if err != nil {
	}
	if bookResult != nil {
		s.applyDirRemoveResult(sb, bookResult)
	}

	if err := s.adjustOwnerCount(sb, owner, -1, txHash, ledgerSeq); err != nil {
		return err
	}

	if err := sb.Erase(offerKey); err != nil {
		return err
	}

	return nil
}

// applyDirRemoveResult applies directory removal changes to the sandbox
func (s *BookStep) applyDirRemoveResult(sb *PaymentSandbox, result *state.DirRemoveResult) {
	for _, mod := range result.ModifiedNodes {
		isBookDir := mod.NewState.TakerPaysCurrency != [20]byte{} || mod.NewState.TakerGetsCurrency != [20]byte{}
		data, err := state.SerializeDirectoryNode(mod.NewState, isBookDir)
		if err != nil {
			continue
		}
		if err := sb.Update(keylet.Keylet{Key: mod.Key}, data); err != nil {
		}
	}

	for _, del := range result.DeletedNodes {
		if err := sb.Erase(keylet.Keylet{Key: del.Key}); err != nil {
		}
	}
}

func (s *BookStep) subtractFromAmount(original, consumed EitherAmount) EitherAmount {
	if original.IsNative {
		return NewXRPEitherAmount(original.XRP - consumed.XRP)
	}
	result, _ := original.IOU.Sub(consumed.IOU)
	return NewIOUEitherAmount(result)
}

// eitherAmountToTxAmount converts EitherAmount to tx.Amount
func (s *BookStep) eitherAmountToTxAmount(ea EitherAmount, issue Issue) tx.Amount {
	if ea.IsNative {
		return tx.NewXRPAmount(ea.XRP)
	}
	return ea.IOU
}

// retagToIssue returns amt with its currency/issuer set to the given book issue,
// preserving the numeric magnitude. The flow engine threads amounts whose
// currency/issuer can carry the strand-destination issue rather than the issue of
// the book actually being traversed. The CLOB transfer path takes the target
// Issue explicitly, but the AMM send routes by amount.Issuer, so AMM transfers
// must be re-tagged to the book's own issue before sending. XRP is returned
// unchanged.
func retagToIssue(amt tx.Amount, issue Issue) tx.Amount {
	if amt.IsNative() || issue.IsXRP() {
		return amt
	}
	return tx.NewIssuedAmount(amt.Mantissa(), amt.Exponent(), issue.Currency, state.EncodeAccountIDSafe(issue.Issuer))
}
