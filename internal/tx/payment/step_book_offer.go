package payment

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/permissioneddomain"
	"github.com/LeJamon/go-xrpl/keylet"
)

// stepOfferCounter counts a single offer the book walk has advanced to and
// reports whether the walk may continue. It mirrors rippled's
// TOfferStreamBase::StepCounter::step(): once the per-execution limit is
// reached it returns false, leaving the offer uncounted and untouched — exactly
// as counter_.step() returning false ends OfferStream::step. The synthetic AMM
// offer is generated outside this walk, so it never flows through here and is
// never counted toward offersUsed.
func (s *BookStep) stepOfferCounter() bool {
	if s.offersUsed >= s.maxOffersToConsume {
		return false
	}
	s.offersUsed++
	return true
}

// getNextOfferSkipVisited returns the next offer at the best quality, skipping offers in ofrsToRm and visited.
// Uses Succ() for efficient O(log n) ordered traversal of book directories.
// Follows IndexNext chains through multi-page directories at each quality level.
// Reference: rippled OfferStream::step() + BookTip::step()
func (s *BookStep) getNextOfferSkipVisited(sb *PaymentSandbox, afView *PaymentSandbox, ofrsToRm map[[32]byte]bool, visited map[[32]byte]bool, enforceQualityBound bool) (*state.LedgerOffer, [32]byte, error) {
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

		// pageBeyondLimit: this directory's quality is worse than the taker's
		// limit. selfCrossEligible: an offer-crossing default-path strand, where
		// the taker is both the strand source and destination. See the per-offer
		// check below for how the two bound the walk.
		pageBeyondLimit := enforceQualityBound && s.qualityLimit != nil &&
			QualityFromKey(foundKey).WorseThan(*s.qualityLimit)
		selfCrossEligible := s.defaultPath && s.strandSrc == s.strandDst

		// Iterate through all pages of this directory (root + linked pages)
		dir, err := state.ParseDirectoryNode(foundData)
		if err != nil {
			searchKey = foundKey
			continue
		}

		// Iterate root page + all subsequent pages via IndexNext chain
		rootKey := foundKey
		pageKey := keylet.DirPage(rootKey, 0)
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

				// In a beyond-limit directory the taker crosses nothing past its
				// limit. rippled's OfferStream::step still advances the book tip into
				// such a directory and perm-removes any offer it walks over that it
				// would groom regardless of quality — a FOUND-unfunded offer, a
				// FOUND-tiny offer (small/increased-quality, OfferStream.cpp:343-404),
				// or an expired one — before execOffer's checkQualityThreshold ever
				// applies the limit; that threshold only stops the *crossing* at a
				// FUNDED, non-groomable beyond-limit tip. So at a beyond-limit page we
				// yield an offer only when step() would groom it as a perm (FOUND)
				// removal — zero funds, or a tiny-quality offer whose funds are
				// unchanged from the pristine afView — and let forEachOffer's
				// found-unfunded / found-tiny branch perm-remove it. The taker's OWN
				// offer on the default path (a self-cross) is also yielded: rippled
				// removes a self-crossed own offer "even if no crossing occurs". A
				// FUNDED, non-groomable non-own beyond-limit offer (and any BECAME-
				// unfunded/tiny one) still stops the walk here — exactly as rippled
				// never crosses past a funded beyond-limit tip — so an over-extended
				// reverse pass can never reach and remove a deeper BECAME-unfunded
				// offer behind it.
				// Reference: rippled OfferStream.cpp step() lines 314-404 (found vs
				// became unfunded/tiny) and BookStep.cpp checkQualityThreshold/do-while.
				if pageBeyondLimit {
					ownData, ownErr := sb.Read(keylet.Keylet{Key: offerKey})
					isOwnOffer := false
					if selfCrossEligible && ownErr == nil && ownData != nil {
						if ownOffer, pErr := state.ParseLedgerOffer(ownData); pErr == nil {
							if ownerID, derr := state.DecodeAccountID(ownOffer.Account); derr == nil && ownerID == s.strandSrc {
								isOwnOffer = true
							}
						}
					}
					if !isOwnOffer {
						groomable := false
						if ownErr == nil && ownData != nil {
							if probeOffer, pErr := state.ParseLedgerOffer(ownData); pErr == nil {
								groomable = s.isFoundPermGroomable(sb, afView, probeOffer)
							}
						}
						if !groomable {
							return nil, [32]byte{}, nil
						}
					}
				}

				// Count this offer before the expiry/funding/removal checks,
				// mirroring rippled's OfferStream::step where counter_.step()
				// runs before those checks. When the limit is reached, stop the
				// walk without counting or touching this offer. Mark it visited
				// so every offer the walk advances to is counted exactly once —
				// even one skipped just below as expired, missing or
				// domain-removed — and is never re-counted on a later walk.
				if !s.stepOfferCounter() {
					return nil, [32]byte{}, nil
				}
				if visited != nil {
					visited[offerKey] = true
				}

				offerData, err := sb.Read(keylet.Keylet{Key: offerKey})
				if err != nil || offerData == nil {
					// Dangling sfIndexes entry: the directory page references an
					// offer SLE that no longer exists. Erase the stale index from
					// the current page in both the execution sandbox and the cancel
					// view, mirroring rippled's OfferStream::step removal of a
					// directory item with no corresponding ledger entry.
					s.eraseDanglingOffer(sb, pageKey, offerKey)
					s.eraseDanglingOffer(afView, pageKey, offerKey)
					continue
				}

				offer, err := state.ParseLedgerOffer(offerData)
				if err != nil {
					continue
				}

				// Autobridge self-payment: in offer crossing, an offer whose owner
				// is the strand destination and whose previous step is a BookStep
				// (the XRP-bridge second leg) would deliver the destination its own
				// asset — a self-payment that nets to zero, providing no real
				// liquidity. rippled keeps it out of the consumed book (the offer is
				// left untouched), falling back to the AMM/next offer; skip it here
				// without removal so the same liquidity is consumed.
				if s.offerCrossing {
					if _, prevIsBook := s.prevStep.(*BookStep); prevIsBook {
						if ownerID, derr := state.DecodeAccountID(offer.Account); derr == nil && ownerID == s.strandDst {
							if visited != nil {
								visited[offerKey] = true
							}
							continue
						}
					}
				}

				// Check offer expiration
				// Reference: rippled OfferStream.cpp lines 256-265
				if s.parentCloseTime > 0 && offer.Expiration > 0 &&
					offer.Expiration <= s.parentCloseTime {
					s.removeExpiredOffer(sb, offer, offerKey)
					if ofrsToRm != nil {
						ofrsToRm[offerKey] = true
					}
					s.recordPermRm(offerKey)
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
						s.recordPermRm(offerKey)
						continue
					}
				}

				return offer, offerKey, nil
			}

			// Follow IndexNext to next page
			if dir.IndexNext == 0 {
				break // No more pages at this quality
			}
			pageKey = keylet.DirPage(rootKey, dir.IndexNext)
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

// isFoundPermGroomable reports whether a beyond-limit non-own offer is one that
// rippled's OfferStream::step would perm-remove as a FOUND removal — i.e. it was
// already removable in the pristine view before any crossing touched it. This is
// the set the beyond-limit walk may step past: rippled grooms it quality-blind,
// ahead of the crossing quality threshold. Two cases qualify, both requiring the
// owner's funds to be unchanged from the pristine afView (a FOUND, not a BECAME,
// removal):
//   - found-unfunded: zero funds in both the execution sandbox and afView, and
//   - found-tiny: a small/increased-quality offer (shouldRmSmallIncreasedQOffer)
//     whose funds are identical in the execution sandbox and afView.
//
// A funded, non-groomable offer — or one that only BECAME unfunded/tiny because
// the crossing drained its owner — returns false and stops the walk, so a deeper
// became-unfunded/tiny offer is never reached, exactly as rippled never crosses
// past a funded beyond-limit tip.
// Reference: rippled OfferStream.cpp step() lines 314-404.
func (s *BookStep) isFoundPermGroomable(sb, afView *PaymentSandbox, offer *state.LedgerOffer) bool {
	fundsSb := s.getOfferFundedAmount(sb, offer)
	if fundsSb.IsZero() {
		return s.getOfferFundedAmount(afView, offer).IsZero()
	}
	if s.shouldRmSmallIncreasedQOffer(sb, offer, fundsSb) {
		return s.getOfferFundedAmount(afView, offer).Compare(fundsSb) == 0
	}
	return false
}

// isBecameGroomableAgainst reports whether the trailing offer is a BECAME
// removal (unfunded or tiny) when its owner's funds are read from baseView — the
// flow's iteration base, i.e. the state after all PRIOR iterations but before
// the current strand pass's consumption — yet was funded before this whole flow
// ran (pristine afView). This is rippled's OfferStream::step grooming a became-
// unfunded/became-tiny offer the cross advances over: its owner was drained by
// an earlier cross, so the offer is genuinely gone, but it is a conditional (not
// perm) removal. Reading from baseView rather than the working sandbox is what
// distinguishes a truly-drained owner from one the current over-walk merely
// over-consumed (whose drain a limiting reset discards), matching the offers
// rippled actually deletes. The found cases (unfunded/tiny in pristine afView
// too) are handled separately by isFoundPermGroomable, so this returns false for
// them to avoid double-classifying a perm removal as conditional.
func (s *BookStep) isBecameGroomableAgainst(baseView, afView *PaymentSandbox, offer *state.LedgerOffer) bool {
	fundsBase := s.getOfferFundedAmount(baseView, offer)
	fundsAf := s.getOfferFundedAmount(afView, offer)
	if fundsBase.IsZero() {
		// Unfunded in the iteration base; a BECAME removal only if it was funded
		// pre-flow (otherwise isFoundPermGroomable already handles the found case).
		return !fundsAf.IsZero()
	}
	if s.shouldRmSmallIncreasedQOffer(baseView, offer, fundsBase) {
		// Tiny in the iteration base; a BECAME-tiny removal only if its pre-flow
		// funds differ (a found-tiny offer is the perm case handled elsewhere).
		return fundsAf.Compare(fundsBase) != 0
	}
	return false
}

// tipFullyConsumed reports whether the offer just consumed by the callback is now
// fully consumed, mirroring rippled's offer.fully_consumed() (no funds can flow
// through it: remaining in or out <= 0). consumeOffer updates the CLOB offer's
// TakerPays/TakerGets in place, so this reads the post-consume amounts; AMM
// offers track the flag directly. This is the partial-take return value of
// rippled's eachOffer, which decides whether the do-while steps again to groom
// trailing offers.
// Reference: rippled Offer.h fully_consumed() / AMMOffer.h fully_consumed();
// BookStep.cpp eachOffer return (rev line 1080, fwd line 1252).
func (s *BookStep) tipFullyConsumed(e offerExec) bool {
	if e.isAMM {
		return e.ammOffer.FullyConsumed()
	}
	return s.offerTakerPays(e.clobOffer).IsZero() ||
		s.offerTakerGets(e.clobOffer).IsZero()
}

// eraseDanglingOffer removes a stale index from a book directory page whose
// offer SLE no longer exists, mirroring rippled's OfferStream::erase. It
// rewrites the page's sfIndexes in place rather than calling DirRemove, leaving
// an emptied page intact: collapsing the page here would be a protocol-breaking
// change. The page is re-read from the view on each call so successive erasures
// on the same page compose.
func (s *BookStep) eraseDanglingOffer(view *PaymentSandbox, pageKey keylet.Keylet, offerKey [32]byte) {
	pageData, err := view.Read(pageKey)
	if err != nil || pageData == nil {
		return
	}
	page, err := state.ParseDirectoryNode(pageData)
	if err != nil {
		return
	}

	newIndexes := make([][32]byte, 0, len(page.Indexes))
	found := false
	for _, idx := range page.Indexes {
		if idx == offerKey {
			found = true
			continue
		}
		newIndexes = append(newIndexes, idx)
	}
	if !found {
		return
	}
	page.Indexes = newIndexes

	isBookDir := page.TakerPaysCurrency != [20]byte{} || page.TakerGetsCurrency != [20]byte{}
	data, err := state.SerializeDirectoryNode(page, isBookDir)
	if err != nil {
		return
	}
	_ = view.Update(pageKey, data)
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

	issuerIsHigh := state.CompareAccountIDs(issuer, account) > 0
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

	// Subtract deferred credits so self-cross round-trips net to zero, matching
	// rippled accountHolds() which returns view.balanceHook(account, issuer, amount)
	// for IOU exactly as the XRP branch above already does for XRP. Without this an
	// offer owner that is also the strand destination (e.g. an autobridge self-cross)
	// funds its offer from its full balance and over-delivers liquidity that rippled
	// correctly nets to zero. Reference: rippled ledger/detail/View.cpp accountHolds().
	if offerOwner != issuer {
		ownerBalance = NewIOUEitherAmount(sb.BalanceHook(offerOwner, issuer, ownerBalance.IOU))
	}

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
	accountIsLow := state.CompareAccountIDs(account, issuer) < 0

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
