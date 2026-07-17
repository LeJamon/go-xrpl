package payment

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func (s *BookStep) offerTakerGets(offer *state.LedgerOffer) EitherAmount {
	return ToEitherAmount(offer.TakerGets)
}

// offerTakerPays returns what the taker pays to this offer
func (s *BookStep) offerTakerPays(offer *state.LedgerOffer) EitherAmount {
	return ToEitherAmount(offer.TakerPays)
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
	if s.book.Out.IsMPT && offerOwner == s.book.Out.Issuer {
		if result := mptutil.RecordIssuerSelfDebit(sb, s.book.Out.MPTID, uint64(consumedOut.MPT)); result != ter.TesSUCCESS {
			return mptTransferResult(result)
		}
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
		if err := s.deleteOffer(sb, offer, offerOwner); err != nil {
			return err
		}
	} else {
		// Partially consumed — just update the offer amounts. Do NOT stamp
		// PreviousTxnID here: threading is the ApplyStateTable's job and runs
		// only after the node-changed check, so an offer whose recomputed
		// amounts round back to byte-identical values is correctly left
		// untouched instead of emitting a ghost ModifiedNode.
		// Do NOT check remaining funding here either; the OfferStream handles
		// unfunded detection on the next step() call.
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
	if s.book.Out.IsMPT {
		return ZeroMPTEitherAmount(s.book.Out.MPTID)
	}
	return ZeroIOUEitherAmount(s.book.Out.Currency, state.EncodeAccountIDSafe(s.book.Out.Issuer))
}

// zeroIn returns a zero EitherAmount for the input currency.
func (s *BookStep) zeroIn() EitherAmount {
	if s.book.In.IsXRP() {
		return ZeroXRPEitherAmount()
	}
	if s.book.In.IsMPT {
		return ZeroMPTEitherAmount(s.book.In.MPTID)
	}
	return ZeroIOUEitherAmount(s.book.In.Currency, state.EncodeAccountIDSafe(s.book.In.Issuer))
}

// deleteOffer properly deletes an offer from the ledger.
func (s *BookStep) deleteOffer(sb *PaymentSandbox, offer *state.LedgerOffer, owner [20]byte) error {
	offerKey := keylet.Offer(owner, offer.Sequence)
	removed, err := state.DeleteOffer(sb, offerKey, offer)
	if err != nil {
		return err
	}
	if !removed {
		return nil
	}

	if err := s.adjustOwnerCount(sb, owner, -1); err != nil {
		return err
	}
	return nil
}

func (s *BookStep) subtractFromAmount(original, consumed EitherAmount) EitherAmount {
	return original.Sub(consumed)
}

// eitherAmountToTxAmount converts EitherAmount to tx.Amount
func (s *BookStep) eitherAmountToTxAmount(ea EitherAmount, issue Issue) tx.Amount {
	if ea.IsNative {
		return tx.NewXRPAmount(ea.XRP)
	}
	if ea.IsMPT {
		return newMPTAmount(ea.MPT, issue.MPTID)
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
func retagToIssue(m numberMath, amt tx.Amount, issue Issue) tx.Amount {
	if amt.IsNative() || issue.IsXRP() {
		return amt
	}
	if issue.IsMPT {
		if raw, ok := amt.MPTRaw(); ok {
			return newMPTAmount(raw, issue.MPTID)
		}
		return m.toAmount(m.fromAmount(amt, state.RoundToNearest), newMPTAmount(0, issue.MPTID), state.RoundToNearest)
	}
	return tx.NewIssuedAmount(amt.Mantissa(), amt.Exponent(), issue.Currency, state.EncodeAccountIDSafe(issue.Issuer))
}
