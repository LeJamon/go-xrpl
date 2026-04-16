package payment

import (
	"bytes"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/permissioneddomain"
	"github.com/LeJamon/goXRPLd/keylet"
)

// getNextOfferSkipVisited returns the next offer at the best quality, skipping offers in ofrsToRm and visited.
// Uses Succ() for efficient O(log n) ordered traversal of book directories.
// Follows IndexNext chains through multi-page directories at each quality level.
// Reference: rippled OfferStream::step() + BookTip::step()
func (s *BookStep) getNextOfferSkipVisited(sb *PaymentSandbox, afView *PaymentSandbox, ofrsToRm map[[32]byte]bool, visited map[[32]byte]bool) (*state.LedgerOffer, [32]byte, error) {
	bookBase := s.bookBaseKey()
	bookPrefix := bookBase[:24]

	// Walk through book directories in quality order using Succ.
	// bookBase has quality=0 (bytes 24-31 zeroed), so Succ finds the first quality entry.
	searchKey := bookBase
	for {
		foundKey, foundData, found, err := sb.Succ(searchKey)
		if err != nil || !found {
			return nil, [32]byte{}, nil
		}
		// Check if still within the book prefix
		if !bytes.Equal(foundKey[:24], bookPrefix) {
			return nil, [32]byte{}, nil
		}

		// Iterate through all pages of this directory (root + linked pages)
		dir, err := state.ParseDirectoryNode(foundData)
		if err != nil {
			searchKey = foundKey
			continue
		}

		// Iterate root page + all subsequent pages via IndexNext chain
		rootKey := foundKey
		for {
			for _, idx := range dir.Indexes {
				var offerKey [32]byte
				copy(offerKey[:], idx[:])

				// Skip offers already in ofrsToRm or visited
				if ofrsToRm != nil && ofrsToRm[offerKey] {
					continue
				}
				if visited != nil && visited[offerKey] {
					continue
				}

				offerData, err := sb.Read(keylet.Keylet{Key: offerKey})
				if err != nil || offerData == nil {
					continue
				}

				offer, err := state.ParseLedgerOffer(offerData)
				if err != nil {
					continue
				}

				// Check offer expiration
				// Reference: rippled OfferStream.cpp lines 256-265
				if s.parentCloseTime > 0 && offer.Expiration > 0 &&
					offer.Expiration <= s.parentCloseTime {
					s.removeExpiredOffer(sb, offer, offerKey)
					if ofrsToRm != nil {
						ofrsToRm[offerKey] = true
					}
					continue
				}

				// Domain membership check: if the offer has a DomainID (domain or
				// hybrid offer), verify the owner is still in that domain. Owners
				// who have left the domain (or whose credential has expired) have
				// their offers treated as unfunded and removed.
				// This applies to ALL payment streams, not just domain payments —
				// hybrid offers in the open book must also be validated.
				// Reference: rippled OfferStream.cpp lines 294-303
				var zeroDomainID [32]byte
				if offer.DomainID != zeroDomainID {
					if !permissioneddomain.OfferInDomain(sb, offer, offer.DomainID, s.parentCloseTime) {
						ofrsToRm[offerKey] = true
						continue
					}
				}

				return offer, offerKey, nil
			}

			// Follow IndexNext to next page
			if dir.IndexNext == 0 {
				break // No more pages at this quality
			}
			pageKey := keylet.DirPage(rootKey, dir.IndexNext)
			pageData, err := sb.Read(pageKey)
			if err != nil || pageData == nil {
				break
			}
			dir, err = state.ParseDirectoryNode(pageData)
			if err != nil {
				break
			}
		}

		// All offers at this quality consumed — move to next quality
		searchKey = foundKey
	}
}

// removeExpiredOffer removes an expired offer from the ledger.
// Reference: rippled OfferStream::permRmOffer
func (s *BookStep) removeExpiredOffer(sb *PaymentSandbox, offer *state.LedgerOffer, offerKey [32]byte) {
	ownerID, err := state.DecodeAccountID(offer.Account)
	if err != nil {
		return
	}

	txHash, ledgerSeq := sb.GetTransactionContext()

	// Remove from owner directory
	ownerDirKey := keylet.OwnerDir(ownerID)
	state.DirRemove(sb, ownerDirKey, offer.OwnerNode, offerKey, false)

	// Remove from book directory
	bookDirKey := keylet.Keylet{Type: 100, Key: offer.BookDirectory}
	state.DirRemove(sb, bookDirKey, offer.BookNode, offerKey, false)

	// Erase the offer
	sb.Erase(keylet.Keylet{Key: offerKey})

	// Decrement owner count
	s.adjustOwnerCount(sb, ownerID, -1, txHash, ledgerSeq)
}

// isOfferFunded checks if an offer has sufficient funding
// isOfferOwnerAuthorized checks if the offer owner is authorized to hold currency
// from the issuer. Returns true if authorized or if no auth is required.
// Reference: BookStep.cpp lines 760-790
// isOfferOwnerAuthorized checks if the offer owner is authorized to hold currency
// from the issuer. Returns true if authorized or if no auth is required.
// Reference: BookStep.cpp lines 760-790
func (s *BookStep) isOfferOwnerAuthorized(
	view *PaymentSandbox, owner, issuer [20]byte, currency string,
) bool {
	// Read issuer account to check RequireAuth flag
	issuerKey := keylet.Account(issuer)
	issuerData, err := view.Read(issuerKey)
	if err != nil || issuerData == nil {
		return true // No issuer account = no auth check
	}
	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return true
	}
	if (issuerAccount.Flags & state.LsfRequireAuth) == 0 {
		return true // Issuer doesn't require auth
	}

	// Issuer requires auth — check if owner has authorization on trust line
	// Reference: rippled uses lsfHighAuth/lsfLowAuth based on account ordering
	lineKey := keylet.Line(owner, issuer, currency)
	lineData, err := view.Read(lineKey)
	if err != nil || lineData == nil {
		return false // No trust line = not authorized
	}
	line, err := state.ParseRippleState(lineData)
	if err != nil {
		return false
	}

	// Determine which auth flag to check based on account ordering
	// Reference: rippled BookStep.cpp line 774: issuerID > ownerID ? lsfHighAuth : lsfLowAuth
	var authFlag uint32
	if bytes.Compare(issuer[:], owner[:]) > 0 {
		authFlag = state.LsfHighAuth
	} else {
		authFlag = state.LsfLowAuth
	}

	return (line.Flags & authFlag) != 0
}

// isFrozen checks if an account's trust line for the given currency/issuer is frozen.
// Returns true if:
//   - The issuer has GlobalFreeze set on their AccountRoot, OR
//   - The issuer has individually frozen the account's trust line (lsfHighFreeze/lsfLowFreeze)
//
// XRP cannot be frozen, so this always returns false for XRP.
// Reference: rippled View.cpp isFrozen(view, account, currency, issuer)
func (s *BookStep) isFrozen(sb *PaymentSandbox, account [20]byte, currency string, issuer [20]byte) bool {
	// XRP cannot be frozen
	if currency == "" || currency == "XRP" {
		return false
	}

	// Check global freeze on the issuer
	issuerData, err := sb.Read(keylet.Account(issuer))
	if err == nil && issuerData != nil {
		issuerAcct, err := state.ParseAccountRoot(issuerData)
		if err == nil && (issuerAcct.Flags&state.LsfGlobalFreeze) != 0 {
			return true
		}
	}

	// If the account IS the issuer, no individual freeze to check
	if issuer == account {
		return false
	}

	// Check individual freeze on the trust line
	// The issuer's freeze flag depends on which side (high/low) the issuer is on
	// Reference: rippled View.cpp isFrozen():
	//   (issuer > account) ? lsfHighFreeze : lsfLowFreeze
	lineKey := keylet.Line(account, issuer, currency)
	lineData, err := sb.Read(lineKey)
	if err != nil || lineData == nil {
		return false
	}
	rs, err := state.ParseRippleState(lineData)
	if err != nil {
		return false
	}

	issuerIsHigh := state.CompareAccountIDsForLine(issuer, account) > 0
	if issuerIsHigh {
		return (rs.Flags & state.LsfHighFreeze) != 0
	}
	return (rs.Flags & state.LsfLowFreeze) != 0
}

// isDeepFrozen checks if an account's trust line for the given currency/issuer
// has either the high or low deep freeze flag set.
// Deep freeze is more restrictive than regular freeze — it prevents both
// sending AND receiving, and causes existing offers to be removed.
// XRP cannot be frozen, so this always returns false for XRP.
// If the account is the issuer, deep freeze does not apply.
// Reference: rippled View.cpp isDeepFrozen(view, account, currency, issuer)
func (s *BookStep) isDeepFrozen(sb *PaymentSandbox, account [20]byte, currency string, issuer [20]byte) bool {
	// XRP cannot be frozen
	if currency == "" || currency == "XRP" {
		return false
	}

	// Issuer is never deep frozen for their own currency
	if issuer == account {
		return false
	}

	lineKey := keylet.Line(account, issuer, currency)
	lineData, err := sb.Read(lineKey)
	if err != nil || lineData == nil {
		return false
	}
	rs, err := state.ParseRippleState(lineData)
	if err != nil {
		return false
	}

	return (rs.Flags&state.LsfHighDeepFreeze) != 0 || (rs.Flags&state.LsfLowDeepFreeze) != 0
}

// getOfferFundedAmount returns the actual amount an offer can deliver based on owner's balance.
// This matches rippled's calculation of funded amounts for offers.
// For IOU output, returns zero if the owner's trust line is frozen (matching fhZERO_IF_FROZEN).
// Reference: rippled OfferStream.cpp uses accountFundsHelper which calls accountHolds with fhZERO_IF_FROZEN.
func (s *BookStep) getOfferFundedAmount(sb *PaymentSandbox, offer *state.LedgerOffer) EitherAmount {
	offerOwner, err := state.DecodeAccountID(offer.Account)
	if err != nil {
		return ZeroXRPEitherAmount()
	}

	offerTakerGets := s.offerTakerGets(offer)

	if s.book.Out.IsXRP() {
		accountKey := keylet.Account(offerOwner)
		accountData, err := sb.Read(accountKey)
		if err != nil || accountData == nil {
			return ZeroXRPEitherAmount()
		}

		account, err := state.ParseAccountRoot(accountData)
		if err != nil {
			return ZeroXRPEitherAmount()
		}

		// Use OwnerCountHook to get adjusted owner count (accounts for pending changes)
		// Reference: rippled View.cpp xrpLiquid() line 627-628
		ownerCount := sb.OwnerCountHook(offerOwner, account.OwnerCount)

		// Read reserve values from ledger's FeeSettings
		// Reference: rippled View.cpp xrpLiquid() reads reserves from fees keylet
		baseReserve, incrementReserve := GetLedgerReserves(sb)
		reserve := baseReserve + int64(ownerCount)*incrementReserve

		// Use BalanceHook to get adjusted balance (accounts for pending credits)
		// Reference: rippled View.cpp xrpLiquid() line 637
		// For XRP, issuer is the zero account (xrpAccount)
		xrpIssuer := [20]byte{}
		xrpAmount := tx.NewXRPAmount(int64(account.Balance))
		adjustedBalance := sb.BalanceHook(offerOwner, xrpIssuer, xrpAmount)
		available := adjustedBalance.Drops() - reserve

		if available <= 0 {
			return ZeroXRPEitherAmount()
		}

		// Return the raw liquid balance (not capped at offerTakerGets).
		// Reference: rippled accountFundsHelper calls accountHolds() which returns
		// the full available balance. The funding cap comparison (funds < ownerGives)
		// handles the actual cap — capping here breaks ownerPaysTransferFee cases
		// where ownerGives > offerTakerGets.
		return NewXRPEitherAmount(available)
	}

	// For IOU TakerGets: check owner's trustline balance with issuer
	issuer := s.book.Out.Issuer
	currency := s.book.Out.Currency

	// Check freeze before returning balance (fhZERO_IF_FROZEN).
	// If the trust line is frozen or deep frozen, the offer is treated as unfunded.
	// Reference: rippled accountHolds() lines 407-413:
	//   if (zeroIfFrozen == fhZERO_IF_FROZEN) {
	//     if (isFrozen(...) || isDeepFrozen(...)) return false;
	//   }
	if offerOwner != issuer {
		if s.isFrozen(sb, offerOwner, currency, issuer) ||
			s.isDeepFrozen(sb, offerOwner, currency, issuer) {
			return ZeroIOUEitherAmount(currency, state.EncodeAccountIDSafe(issuer))
		}
	}

	ownerBalance := s.getIOUBalance(sb, offerOwner, issuer, currency)

	if ownerBalance.IsNegative() || ownerBalance.IsZero() {
		if offerOwner == issuer {
			return offerTakerGets
		}
		return ZeroIOUEitherAmount(currency, state.EncodeAccountIDSafe(issuer))
	}

	// Return the raw trust line balance (not capped at offerTakerGets).
	// Reference: rippled accountFundsHelper calls accountHolds() which returns
	// the full trust line balance. Capping at offerTakerGets causes a false
	// underfunded detection when ownerPaysTransferFee=true (ownerGives > offerTakerGets).
	return ownerBalance
}

// getIOUBalance returns an account's IOU balance with an issuer
func (s *BookStep) getIOUBalance(sb *PaymentSandbox, account, issuer [20]byte, currency string) EitherAmount {
	issuerStr := state.EncodeAccountIDSafe(issuer)

	if account == issuer {
		// Issuer has unlimited balance for their own currency
		return NewIOUEitherAmount(tx.NewIssuedAmount(1000000000000000, 15, currency, issuerStr))
	}

	lineKey := keylet.Line(account, issuer, currency)
	lineData, err := sb.Read(lineKey)
	if err != nil || lineData == nil {
		return ZeroIOUEitherAmount(currency, issuerStr)
	}

	rs, err := state.ParseRippleState(lineData)
	if err != nil {
		return ZeroIOUEitherAmount(currency, issuerStr)
	}

	// Balance is stored from the low account's perspective
	accountIsLow := state.CompareAccountIDsForLine(account, issuer) < 0

	var balance tx.Amount
	if accountIsLow {
		balance = rs.Balance
	} else {
		balance = rs.Balance.Negate()
	}

	// Create new Amount with correct issuer
	return NewIOUEitherAmount(state.NewIssuedAmountFromValue(balance.IOU().Mantissa(), balance.IOU().Exponent(), currency, issuerStr))
}

// shouldRmSmallIncreasedQOffer checks if a tiny underfunded offer should be removed
// because its effective quality has degraded.
//
// When an offer is underfunded (owner has less than TakerGets), the effective amounts
// are adjusted by the owner's funds. This can cause the effective input (TakerPays)
// to drop to 1 drop (XRP) or the minimum IOU amount. If the effective quality is
// worse than the offer's original quality, the offer is blocking the order book and
// should be removed.
//
// This check applies when:
//   - TakerPays is XRP (because of XRP drops granularity), OR
//   - Both TakerPays and TakerGets are IOU and TakerPays < TakerGets
//
// It does NOT apply when TakerGets is XRP (the worst quality change is ~10^-81
// TakerPays per 1 drop, which is good quality for any realistic asset).
//
// Reference: rippled OfferStream.cpp shouldRmSmallIncreasedQOffer() lines 141-222
func (s *BookStep) shouldRmSmallIncreasedQOffer(sb *PaymentSandbox, offer *state.LedgerOffer, ownerFunds EitherAmount) bool {
	if !s.fixRmSmallIncreasedQOffers {
		return false
	}

	inIsXRP := s.book.In.IsXRP()
	outIsXRP := s.book.Out.IsXRP()

	// If TakerGets is XRP, the worst quality change is ~10^-81 TakerPays per 1 drop.
	// This is remarkably good quality for any realistic asset, so skip the check.
	if outIsXRP {
		return false
	}

	ofrIn := s.offerTakerPays(offer)
	ofrOut := s.offerTakerGets(offer)

	// For IOU/IOU: only check if TakerPays < TakerGets
	if !inIsXRP && !outIsXRP {
		if ofrIn.Compare(ofrOut) >= 0 {
			return false
		}
	}

	offerOwner, err := state.DecodeAccountID(offer.Account)
	if err != nil {
		return false
	}

	// Compute effective amounts adjusted by owner funds
	effectiveIn := ofrIn
	effectiveOut := ofrOut
	if offerOwner != s.book.Out.Issuer && ownerFunds.Compare(ofrOut) < 0 {
		// Adjust amounts by owner funds using ceil_out or ceil_out_strict
		// Reference: rippled OfferStream.cpp lines 192-207
		offerQ := s.offerQuality(offer)
		if s.fixReducedOffersV1 {
			effectiveIn, effectiveOut = offerQ.CeilOutStrict(ofrIn, ofrOut, ownerFunds, false)
		} else {
			effectiveIn, effectiveOut = offerQ.CeilOut(ofrIn, ofrOut, ownerFunds)
		}
	}

	// If either effective amount is zero, remove the offer.
	// This can happen with fixReducedOffersV1 since it rounds down.
	if s.fixReducedOffersV1 {
		if effectiveIn.IsZero() || effectiveIn.IsNegative() ||
			effectiveOut.IsZero() || effectiveOut.IsNegative() {
			return true
		}
	}

	// Check if the effective input is at or below the minimum positive amount.
	// For XRP: 1 drop
	// For IOU: 1e-81 (mantissa=10^15, exponent=-96)
	if inIsXRP {
		// XRP: minPositiveAmount = 1 drop
		if effectiveIn.XRP > 1 {
			return false
		}
	} else {
		// IOU: minPositiveAmount = STAmount(minMantissa=10^15, minExponent=-96) = 1e-81
		minPositive := NewIOUEitherAmount(tx.NewIssuedAmount(1000000000000000, -96, s.book.In.Currency, state.EncodeAccountIDSafe(s.book.In.Issuer)))
		if effectiveIn.Compare(minPositive) > 0 {
			return false
		}
	}

	// Compare effective quality with the offer's original quality.
	// If effective quality is worse (higher), remove the offer.
	effectiveQuality := QualityFromAmounts(effectiveIn, effectiveOut)
	offerQuality := s.offerQuality(offer)
	return effectiveQuality.WorseThan(offerQuality)
}
