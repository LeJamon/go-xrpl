package payment

import (
	"bytes"
	"errors"
	"slices"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// maxOffersToConsume returns the per-execution offer-consumption limit.
// fix1515 lowered it from 2000 to 1000. Reference: rippled BookStep.cpp:86-91.
//
// When the sandbox has no rules (rules-free contexts such as pathfinding
// liquidity estimation), default to the active-network limit of 1000 — the
// value fix1515 has enforced on mainnet since activation.
func maxOffersToConsume(sb *PaymentSandbox) uint32 {
	rules := sb.Rules()
	if rules == nil || rules.Enabled(amendment.FeatureFix1515) {
		return 1000
	}
	return 2000
}

// fix1515Enabled reports whether fix1515 governs this execution, nil-defaulting
// to the active-network value (enabled) for rules-free contexts such as
// pathfinding liquidity estimation, matching maxOffersToConsume's convention.
func fix1515Enabled(sb *PaymentSandbox) bool {
	rules := sb.Rules()
	return rules == nil || rules.Enabled(amendment.FeatureFix1515)
}

// BookStep consumes liquidity from an order book.
// It iterates through offers at the best quality, consuming them until
// the requested amount is satisfied or liquidity is exhausted.
//
// Three variants exist based on in/out currency types:
// - BookStepII: IOU to IOU
// - BookStepIX: IOU to XRP
// - BookStepXI: XRP to IOU
//
// Based on rippled's BookStep implementation.
type BookStep struct {
	// book specifies the order book (in/out issues)
	book Book

	// strandSrc is the source account of the strand
	strandSrc [20]byte

	// strandDst is the destination account of the strand
	strandDst [20]byte

	// prevStep is the previous step (for transfer fee calculation)
	prevStep Step

	// ownerPaysTransferFee indicates if offer owner pays the transfer fee
	ownerPaysTransferFee bool

	// offerCrossing selects the offer-crossing step semantics (mirrors DirectStepI.offerCrossing).
	offerCrossing bool

	// maxOffersToConsume is the limit on offers consumed per execution
	maxOffersToConsume uint32

	// qualityLimit is the worst quality offer that should be consumed.
	// If set, offers with worse quality (higher value) are not crossed.
	// This is used for offer crossing to only cross offers at or better
	// than the taker's quality.
	qualityLimit *Quality

	// crossLimit is the taker's offer-crossing quality threshold. rippled's
	// BookOfferCrossingStep always carries qualityThreshold_ (from ctx.limitQuality)
	// on EVERY crossing step — direct and autobridge legs alike — and uses it in
	// qualityThreshold(lobQuality) to decide the AMM-offer generation threshold,
	// independent of the default-path-gated per-offer quality bound (qualityLimit).
	// Reference: rippled BookOfferCrossingStep::qualityThreshold_ and
	// BookOfferCrossingStep::qualityThreshold().
	crossLimit *Quality

	// parentCloseTime is the parent ledger close time (Ripple epoch seconds)
	// Used to check offer expiration during iteration
	parentCloseTime uint32

	// defaultPath indicates this step is on the default path (not an explicit path).
	// Used for self-cross detection during offer crossing.
	// Reference: rippled BookOfferCrossingStep::defaultPath_
	defaultPath bool

	// fixReducedOffersV2 gates ceil_in_strict vs ceil_in in limitStepIn.
	// When enabled, uses strict rounding (roundUp=false) to prevent order book blocking.
	// Reference: rippled Offer.h TOffer::limitIn() and fixReducedOffersV2 amendment
	fixReducedOffersV2 bool

	// fixReducedOffersV1 gates roundUp in CeilOutStrict calls for underfunded offers.
	// When enabled (roundUp=false), rounding down prevents quality degradation when
	// an offer is partially filled and the remaining amounts are adjusted.
	// Without the fix (roundUp=true), rounding up can make the remaining offer's
	// rate worse than the original, "polluting" the order book.
	// Reference: rippled fixReducedOffersV1 amendment + Offer.h TOffer::limitOut()
	fixReducedOffersV1 bool

	// fixRmSmallIncreasedQOffers gates removal of tiny underfunded offers whose
	// effective quality has increased (worsened) due to partial funding.
	// When an offer is underfunded, its effective amounts are adjusted by the owner's
	// available funds. If the resulting input amount is at or below the minimum positive
	// amount (1 drop for XRP, or 1e-81 for IOU) and the effective quality is worse than
	// the offer's original quality, the offer is removed to prevent order book blocking.
	// Reference: rippled fixRmSmallIncreasedQOffers amendment + OfferStream.cpp shouldRmSmallIncreasedQOffer()
	fixRmSmallIncreasedQOffers bool

	// inactive indicates the step is dry (too many offers consumed)
	inactive bool

	// lastTipFullyConsumed records whether the last offer crossed in the current
	// pass was fully consumed, i.e. whether rippled's eachOffer callback returned
	// true for it (so the do-while runs offers.step() again, grooming trailing
	// found-unfunded offers). The full-take branch always returns true; the
	// partial-take branch (demand met mid-offer) returns offer.fully_consumed(),
	// which is false for a still-funded tip — in that case rippled breaks without
	// another offers.step(), so no trailing groom happens.
	// Reference: rippled BookStep.cpp eachOffer lines 1041-1081.
	lastTipFullyConsumed bool

	// lastConsumedTipKey records the key of the last CLOB offer the callback fully
	// consumed in the current pass; lastConsumedTipValid reports whether the field
	// is set. rippled's forEachOffer do-while calls offers.step() after a
	// fully-consumed tip, and BookTip::step deletes that previous tip from the view
	// before advancing (BookTip.cpp:37-41). goXRPL consumes the tip in place —
	// consumeOffer erases it only when its remainder reaches zero — so a funded-cap
	// full take that drains the owner leaves a became-unfunded offer with a
	// non-zero remainder in the book directory. The trailing-offer drain removes it,
	// replicating that delete-on-advance, so the next flow iteration's strand-
	// quality re-sort and book walk both advance past it instead of re-reading its
	// stale (better) quality.
	lastConsumedTipKey   [32]byte
	lastConsumedTipValid bool

	// offersUsed tracks offers consumed in last execution
	offersUsed uint32

	// cache holds results from the last Rev() call
	cache *bookCache

	// domainID is set for permissioned domain payments.
	// When set, offers are fetched from the domain book directory, and each
	// offer is checked for domain membership before being consumed.
	// Reference: rippled PermissionedDEXHelpers.cpp offerInDomain()
	domainID *[32]byte

	// ammLiquidity provides synthetic AMM offers for this book.
	// Initialized in configureAMMOnBookSteps if an AMM pool exists for the book.
	// Reference: rippled BookStep::ammLiquidity_
	ammLiquidity *AMMLiquidity

	// fixAMMOverflowOffer gates the AMM pool product invariant check.
	// When enabled, throws tecINVARIANT_FAILED if the invariant is violated.
	// Reference: rippled fixAMMOverflowOffer amendment
	fixAMMOverflowOffer bool

	// permRm records offers this step removed unconditionally — self-crossed
	// (limitSelfCrossQuality), authorization-failed, expired, and domain-removed
	// offers. rippled puts these in FlowOfferStream::permToRemove_, which is
	// returned and unioned into the strand's ofrsToRm and survives "even if the
	// strand is not applied" (OfferStream.h) — it is NEVER rolled back when a
	// limiting step is reset and re-executed. The strand executor consults this
	// to keep such removals when its limiting-step reset would otherwise discard
	// an over-walk's removals (those discards target only "became unfunded"
	// offers, which are NOT recorded here). It accumulates across re-executions
	// within one strand build and is fresh per OfferCreate (strands are rebuilt).
	// Reference: rippled OfferStream.h permToRemove_; BookStep.cpp limitSelfCrossQuality.
	permRm map[[32]byte]bool
}

// recordPermRm marks an offer key as an unconditional ("perm") removal — the
// rippled FlowOfferStream::permToRemove semantics. Lazily allocates the set.
func (s *BookStep) recordPermRm(key [32]byte) {
	if s.permRm == nil {
		s.permRm = make(map[[32]byte]bool)
	}
	s.permRm[key] = true
}

// PermRemovals returns the offers this BookStep removed unconditionally during
// its reverse/forward walks (self-cross, auth, expiry, domain). The strand
// executor restores these into ofrsToRm after a limiting-step reset so they are
// never discarded as spurious over-walk removals.
func (s *BookStep) PermRemovals() map[[32]byte]bool {
	return s.permRm
}

// bookCache holds cached values from the reverse pass
type bookCache struct {
	in  EitherAmount
	out EitherAmount
}

// amountMultiset accumulates the per-offer step amounts a BookStep consumes and
// returns their sum in ascending magnitude order. It mirrors rippled's
// boost::container::flat_multiset<TIn>/<TOut> in BookStep::revImp/fwdImp, whose
// sum() helper folds the elements via std::accumulate over the sorted range
// (BookStep.cpp:1002-1010). Because each STAmount add re-canonicalizes the
// running total to a 16-significant-digit mantissa, the order in which the
// per-offer amounts are added changes the final 1-ULP rounding: summing
// smallest-first (rippled) can land on a different last digit than summing in
// consumption order. Recomputing the total as the ascending sum on every take
// reproduces rippled's result exactly.
type amountMultiset struct {
	elems []EitherAmount
}

// insert adds an amount to the multiset, keeping the slice sorted ascending so a
// later sum() folds smallest-first. Duplicates are retained (multiset).
func (m *amountMultiset) insert(a EitherAmount) {
	i := 0
	for i < len(m.elems) && m.elems[i].Compare(a) < 0 {
		i++
	}
	m.elems = append(m.elems, EitherAmount{})
	copy(m.elems[i+1:], m.elems[i:])
	m.elems[i] = a
}

// erase removes one element equal to a, mirroring rippled's
// savedOuts.erase(lastOut) (which erases the single iterator returned by the
// matching insert). A no-op when no element matches.
func (m *amountMultiset) erase(a EitherAmount) {
	for i := range m.elems {
		if m.elems[i].Compare(a) == 0 {
			m.elems = append(m.elems[:i], m.elems[i+1:]...)
			return
		}
	}
}

// sum folds the elements smallest-first, matching rippled's
// std::accumulate(begin()+1, end(), *begin()) over the sorted flat_multiset.
// zero is returned for an empty set (the currency-tagged zero the caller wants).
func (m *amountMultiset) sum(zero EitherAmount) EitherAmount {
	if len(m.elems) == 0 {
		return zero
	}
	total := m.elems[0]
	for _, e := range m.elems[1:] {
		total = total.Add(e)
	}
	return total
}

// NewBookStep creates a new BookStep for order book consumption
func NewBookStep(inIssue, outIssue Issue, strandSrc, strandDst [20]byte, prevStep Step, ownerPaysTransferFee bool) *BookStep {
	return &BookStep{
		book: Book{
			In:  inIssue,
			Out: outIssue,
		},
		strandSrc:            strandSrc,
		strandDst:            strandDst,
		prevStep:             prevStep,
		ownerPaysTransferFee: ownerPaysTransferFee,
		// Re-derived from the active rules at the start of Rev/Fwd; the
		// fix1515 value is the default until then.
		maxOffersToConsume: 1000,
		qualityLimit:       nil,
		inactive:           false,
		offersUsed:         0,
		cache:              nil,
	}
}

// offerExec carries the post-prologue values forEachOffer hands to a direction
// callback: the offer amounts after the funding cap, the adjusted step amounts,
// and the offer identity. Mirrors the arguments rippled's forEachOffer passes to
// its callback (BookStep.cpp:833-834).
type offerExec struct {
	ofrIn, ofrOut     EitherAmount
	stpIn, stpOut     EitherAmount
	ownerGives        EitherAmount
	offerQuality      Quality
	ofrTrIn, ofrTrOut uint32
	isAMM             bool
	ammOffer          *AMMOffer
	clobOffer         *state.LedgerOffer
}

// forEachOffer runs the shared order-book iteration skeleton: it walks CLOB
// offers at the best quality level (interleaving the synthetic AMM offer), runs
// the common per-offer prologue (quality tracking, self-cross removal, auth and
// quality-limit checks, transfer-rate adjustment and the funding cap), then
// hands the resulting amounts to the direction-specific callback.
//
// remainingZero reports whether the direction's remaining amount is exhausted
// (it stops the iteration); callback decides full vs partial take, consumes the
// offer and updates the direction's accumulators. callback returns false to stop
// iteration (e.g. quality level changed via the threshold check).
//
// Reference: rippled BookStep.cpp forEachOffer lines 717-873 (the C++ version
// takes a single per-direction Callback; revImp/fwdImp supply it).
func (s *BookStep) forEachOffer(
	sb *PaymentSandbox,
	afView *PaymentSandbox,
	ofrsToRm map[[32]byte]bool,
	trIn, trOut uint32,
	remainingZero func() bool,
	callback func(offerExec) bool,
) {
	visited := make(map[[32]byte]bool)

	// Track the current quality level — forEachOffer processes one quality at a
	// time. Reference: rippled BookStep.cpp forEachOffer lines 751-754:
	//   if (!ofrQ) ofrQ = offer.quality();
	//   else if (*ofrQ != offer.quality()) return false;
	var currentQuality *Quality
	offerAttempted := false

	// At any payment engine iteration the AMM offer can be consumed only once.
	ammProcessed := false

	// execOffer runs the shared prologue for a single offer (CLOB or AMM) and
	// then defers the take/consume decision to the direction callback. Returns
	// false to stop iteration. Reference: BookStep.cpp forEachOffer lines 748-835.
	execOffer := func(ofrIn, ofrOut EitherAmount, offerQuality Quality,
		ofrTrIn, ofrTrOut uint32, isAMM bool,
		ammOffer *AMMOffer, clobOffer *state.LedgerOffer, clobKey [32]byte,
	) bool {
		// Quality tracking
		if currentQuality == nil {
			currentQuality = &offerQuality
		} else if currentQuality.Value != offerQuality.Value {
			return false
		}

		// Self-cross detection (CLOB only, default path only)
		if !isAMM && s.defaultPath && s.qualityLimit != nil {
			offerOwner, ownerErr := state.DecodeAccountID(clobOffer.Account)
			if ownerErr == nil {
				if !offerQuality.WorseThan(*s.qualityLimit) &&
					s.strandSrc == offerOwner && s.strandDst == offerOwner {
					ofrsToRm[clobKey] = true
					s.recordPermRm(clobKey)
					if !offerAttempted {
						currentQuality = nil
					}
					return true
				}
			}
		}

		// Authorization check (CLOB only)
		if !isAMM && !s.book.In.IsXRP() {
			offerOwner, ownerErr := state.DecodeAccountID(clobOffer.Account)
			if ownerErr == nil && offerOwner != s.book.In.Issuer {
				if !s.isOfferOwnerAuthorized(afView, offerOwner, s.book.In.Issuer, s.book.In.Currency) {
					ofrsToRm[clobKey] = true
					s.recordPermRm(clobKey)
					if !offerAttempted {
						currentQuality = nil
					}
					return true
				}
			}
		}

		// Quality limit check
		if s.qualityLimit != nil && offerQuality.WorseThan(*s.qualityLimit) {
			return false
		}

		offerAttempted = true

		// AMM offers use adjustRates to waive output transfer fee
		if isAMM {
			ofrTrIn, ofrTrOut = ammOffer.AdjustRates(ofrTrIn, ofrTrOut)
		}

		// stpAmt.in = mulRatio(ofrAmt.in, ofrInRate, QUALITY_ONE, true)
		stpIn := MulRatio(ofrIn, ofrTrIn, QualityOne, true)
		stpOut := ofrOut
		ownerGives := MulRatio(ofrOut, ofrTrOut, QualityOne, false)

		// Funding cap (CLOB only — AMM is always funded)
		// Reference: rippled OfferStream reads ownerFunds from view_ (sb),
		// which is the execution sandbox, so consumed balances are visible.
		if !isAMM {
			offerOwner, _ := state.DecodeAccountID(clobOffer.Account)
			funds := s.getOfferFundedAmount(sb, clobOffer)
			isFundedByIssuer := offerOwner == s.book.Out.Issuer
			if !isFundedByIssuer && funds.Compare(ownerGives) < 0 {
				ownerGives = funds
				stpOut = MulRatio(ownerGives, QualityOne, ofrTrOut, false)
				if s.fixReducedOffersV1 {
					ofrIn, ofrOut = offerQuality.CeilOutStrict(ofrIn, ofrOut, stpOut, false)
				} else {
					ofrIn, ofrOut = offerQuality.CeilOut(ofrIn, ofrOut, stpOut)
				}
				stpIn = MulRatio(ofrIn, ofrTrIn, QualityOne, true)
			}
		}

		res := callback(offerExec{
			ofrIn:        ofrIn,
			ofrOut:       ofrOut,
			stpIn:        stpIn,
			stpOut:       stpOut,
			ownerGives:   ownerGives,
			offerQuality: offerQuality,
			ofrTrIn:      ofrTrIn,
			ofrTrOut:     ofrTrOut,
			isAMM:        isAMM,
			ammOffer:     ammOffer,
			clobOffer:    clobOffer,
		})
		// Track the CLOB offer the callback just fully consumed so the trailing
		// drain can replicate BookTip::step's delete-on-advance for a became-
		// unfunded full take whose remainder consumeOffer left in the book.
		if !isAMM {
			if s.lastTipFullyConsumed {
				s.lastConsumedTipKey = clobKey
				s.lastConsumedTipValid = true
			} else {
				s.lastConsumedTipValid = false
			}
		}
		return res
	}

	// tryAMM attempts to process an AMM offer with an optional CLOB quality
	// threshold. Reference: rippled BookStep.cpp forEachOffer lines 838-853.
	tryAMM := func(lobQuality *Quality) bool {
		if ammProcessed || s.ammLiquidity == nil {
			return true
		}
		// AMM doesn't support domain yet
		if s.domainID != nil {
			return true
		}
		// For offer crossing, generate the AMM offer against the LOB tip quality
		// or nil — passing nil when the taker's quality limit is better than the
		// LOB tip lets the AMM produce its maximum offer instead of being capped
		// to the lower-quality LOB tier (which would block an otherwise crossable
		// AMM). Reference: rippled BookStep.cpp tryAMM lines 845-851.
		qualityThreshold := lobQuality
		if fixAMMv1_1Enabled(sb) && lobQuality != nil {
			qualityThreshold = s.tipQualityThreshold(*lobQuality)
		}
		ammOffer := s.getAMMOffer(sb, qualityThreshold)
		if ammOffer == nil {
			return true
		}
		ammProcessed = true
		ofrIn := toEitherAmt(ammOffer.AmountIn())
		ofrOut := toEitherAmt(ammOffer.AmountOut())
		offerQ := ammOffer.Quality()
		return execOffer(ofrIn, ofrOut, offerQ, trIn, trOut, true,
			ammOffer, nil, [32]byte{})
	}

	// Main CLOB iteration with AMM interleaving.
	// Reference: rippled BookStep.cpp forEachOffer lines 855-873.
	firstCLOB := true
	// consumed tracks whether any offer has been crossed in this pass. Before the
	// first cross the walk is bounded by the taker's quality limit; after a cross
	// rippled's do-while keeps stepping, removing offers the cross left unfunded,
	// until the next funded tip fails the quality threshold. The bound never stops
	// the walk at a found-unfunded offer — only at a funded (crossable) one — so a
	// reserve-locked own offer at a beyond-limit tip is still removed.
	// rippled's do-while: `do { if(!execOffer(tip)) break; } while(step())`. The
	// loop is NOT bounded by remaining-amount at the top; instead each step()
	// (getNextOfferSkipVisited) grooms the unfunded/became/tiny run it advances
	// over, and the callback returns false once the demand is met (remainingZero
	// at entry) or a partial fill leaves a still-funded tip. So after the take
	// that meets demand, the loop runs one more getNextOfferSkipVisited — which
	// grooms the trailing run in the SAME (committed) pass — before the next
	// callback returns false and breaks. Reference: BookStep.cpp:855-865.
	consumed := false
	for s.offersUsed < s.maxOffersToConsume {
		offer, offerKey, err := s.getNextOfferSkipVisited(sb, afView, ofrsToRm, visited, !consumed)
		if err != nil {
			break
		}
		if offer == nil {
			break
		}

		// getNextOfferSkipVisited applied the single funded/groom rule as it
		// stepped onto this offer (deep-frozen, zero-amount, found/became unfunded
		// or tiny — rippled's OfferStream::step), so every offer it yields is
		// funded and crossable, or the taker's own offer that execOffer's
		// self-cross check removes. No funded check is repeated here.

		// On the first funded CLOB offer, try the AMM with the LOB quality.
		if firstCLOB {
			firstCLOB = false
			lobQ := s.offerQuality(offer)
			if !tryAMM(&lobQ) {
				break
			}
			// If the AMM (sized up to the LOB quality) already satisfied the
			// remaining output, stop before crossing the CLOB tip — rippled's
			// forEachOffer ends at remainingOut==0 and never reaches the next offer,
			// so the tip must not be touched by a zero cross.
			//
			// The self-crossed run behind a demand-satisfying AMM is NOT drained
			// here: when the AMM offer's quality differs from the LOB tip (the
			// changeSpotPriceQuality case), rippled's do-while breaks on the
			// same-quality gate (ofrQ != offer.quality()) BEFORE limitSelfCrossQuality
			// runs, so rippled leaves the self-cross too — removing it only on a later
			// flow iteration whose AMM offer is suppressed (spot-price gate) and whose
			// quality then matches. goXRPL's main loop reproduces that gate and removes
			// the self-cross on that later iteration as well (verified against rippled
			// testDirectToDirectPath, AMM B=320.02 with cam's offer1 removed). Draining
			// the self-cross here unconditionally would instead break the gate and let
			// the next iteration re-consume the AMM unbounded by any LOB tip, draining
			// the pool past the taker's limit (B=309) — a divergence, not a fix.
			// Reference: rippled BookStep.cpp forEachOffer do-while 855-863.
			if remainingZero() {
				break
			}
		}

		offerOwner, _ := state.DecodeAccountID(offer.Account)
		ofrTrIn := s.getOfrInRate(offerOwner, trIn)
		ofrTrOut := s.getOfrOutRate(offerOwner, trOut)
		ofrIn := s.offerTakerPays(offer)
		ofrOut := s.offerTakerGets(offer)
		offerQ := s.offerQuality(offer)
		if !execOffer(ofrIn, ofrOut, offerQ, ofrTrIn, ofrTrOut, false,
			nil, offer, offerKey) {
			break
		}
		consumed = true

		// Per-advance became-unfunded deletion. rippled's forEachOffer do-while
		// runs offers.step() after execOffer returns true (the requested amount is
		// not yet satisfied and the walk continues); that step()'s BookTip::step
		// first deletes the just-consumed tip from the view (offerDelete of
		// m_entry) before advancing to the next offer (BookTip.cpp:35-42). goXRPL's
		// consumeOffer only erases a tip whose remainder reached zero, so a
		// funded-cap full take — where the owner's funds, not the demand, bounded
		// the consume — is left in the book with a non-zero remainder backed by
		// zero owner funds (a became-unfunded tip). Delete it here, as the walk
		// advances past it, so a later tip or the AMM satisfying the demand does
		// not leave it stale in the book (its better quality would otherwise be
		// re-read on the next pass/iteration). The trailing-drain block below only
		// fires when the LAST crossed tip is the fully-consumed one
		// (lastTipFullyConsumed && remainingZero), so it misses an earlier
		// funded-cap tip when the demand is met by a subsequent offer or the AMM. A
		// tip that exactly meets demand breaks above (execOffer returned false) and
		// is not reached here, matching rippled's do-while break-without-step.
		// removeConsumedTipIfUnfunded only deletes when the owner is now drained
		// (funded amount zero) and writes straight into sb, so a limiting-step
		// reset rolls it back with the rest of an over-extended pass (issue #1029)
		// and the re-executed final pass re-derives it.
		if s.lastConsumedTipValid {
			s.removeConsumedTipIfUnfunded(sb)
		}
	}

	// If no CLOB offers found, try the AMM alone.
	if firstCLOB {
		tryAMM(nil)
	}
}

// removeConsumedTipIfUnfunded deletes the just-fully-consumed CLOB tip from the
// working sandbox when its funding cap drained the owner and consumeOffer left a
// non-zero remainder in the book. This replicates rippled's BookTip::step, which
// offerDeletes the previous tip from the view when the forEachOffer do-while
// steps past a fully-consumed offer (BookTip.cpp:37-41). consumeOffer already
// erases a tip whose remainder reached zero, so this only fires for a funded-cap
// full take: in that case stpAmt.out is sourced from the owner's funds, so the
// owner is drained and the remaining offer is became-unfunded. The delete goes
// straight into the working sandbox (the same erase path consumeOffer uses for a
// zero-remainder tip, and the same view offerDelete BookTip::step uses), so it
// rides the sandbox's reset/apply lifecycle: a limiting-step reset rolls it back
// with the rest of an over-extended pass, and the re-executed final pass re-runs
// it. Reference: rippled BookTip.cpp:37-41 (offerDelete of m_entry on view_).
func (s *BookStep) removeConsumedTipIfUnfunded(sb *PaymentSandbox) {
	key := s.lastConsumedTipKey
	offerData, err := sb.Read(keylet.Keylet{Key: key})
	if err != nil || offerData == nil {
		return
	}
	offer, err := state.ParseLedgerOffer(offerData)
	if err != nil {
		return
	}
	if !s.getOfferFundedAmount(sb, offer).IsZero() {
		return
	}
	owner, err := state.DecodeAccountID(offer.Account)
	if err != nil {
		return
	}
	txHash, ledgerSeq := sb.GetTransactionContext()
	_ = s.deleteOffer(sb, offer, owner, txHash, ledgerSeq)
}

// prevStepDebtDir returns the previous step's debt direction for the given
// strand direction, defaulting to DebtDirectionIssues when this BookStep is the
// first step in the strand (the strand source IS the issuer of the input
// currency), so no transfer fee is charged on the input side.
// Reference: rippled BookStep.cpp revImp lines 1085-1089 / fwdImp lines 1256-1260.
func (s *BookStep) prevStepDebtDir(sb *PaymentSandbox, dir StrandDirection) DebtDirection {
	if s.prevStep != nil {
		return s.prevStep.DebtDirection(sb, dir)
	}
	return DebtDirectionIssues
}

// tooManyOffersDiscard handles hitting the offer-consumption limit, shared by the
// tail of Rev and Fwd. It returns true (with the discard amount written into
// cache) when the limit was hit pre-fix1515 and the caller must discard this
// strand's liquidity entirely; post-fix1515 it instead marks the strand inactive.
// Reference: rippled BookStep.cpp:1096-1108 (rev) and 1267-1280 (fwd).
func (s *BookStep) tooManyOffersDiscard(sb *PaymentSandbox) bool {
	if s.offersUsed < s.maxOffersToConsume {
		return false
	}
	if !fix1515Enabled(sb) {
		// Pre-fix1515: discard this strand's liquidity entirely.
		s.cache = &bookCache{in: s.zeroIn(), out: s.zeroOut()}
		return true
	}
	// fix1515: keep the liquidity but mark the strand inactive so it is not
	// consulted further.
	s.inactive = true
	return false
}

// Rev calculates the input needed to produce the requested output
// by consuming offers from the order book.
// Matches rippled's BookStep::revImp() + forEachOffer() flow.
// Reference: BookStep.cpp lines 1014-1131 (revImp) + 717-873 (forEachOffer)
func (s *BookStep) Rev(
	sb *PaymentSandbox,
	afView *PaymentSandbox,
	ofrsToRm map[[32]byte]bool,
	out EitherAmount,
) (EitherAmount, EitherAmount) {
	s.cache = nil
	s.offersUsed = 0
	s.lastTipFullyConsumed = false
	s.lastConsumedTipValid = false
	s.maxOffersToConsume = maxOffersToConsume(sb)

	trIn := s.transferRateIn(sb, s.prevStepDebtDir(sb, StrandDirectionReverse))
	trOut := s.transferRateOut(sb)

	totalIn, totalOut := s.zeroIn(), s.zeroOut()
	remainingOut := out

	// savedIns/savedOuts accumulate the per-offer step amounts and are summed
	// smallest-first, matching rippled's flat_multiset<TIn>/<TOut> + sum() in
	// revImp (BookStep.cpp:1026-1072). Re-summing in ascending order on each take
	// reproduces rippled's 16-digit intermediate rounding, which differs from a
	// running consumption-order accumulation.
	var savedIns, savedOuts amountMultiset

	// revImp callback: decide full vs partial take, consume, and update accumulators.
	// Reference: rippled BookStep.cpp revImp callback lines 1056-1083.
	revCallback := func(e offerExec) bool {
		// rippled eachOffer: if (remainingOut <= 0) return false — demand already
		// met, so the do-while breaks (the prior take's step already groomed the run).
		if remainingOut.IsZero() {
			return false
		}
		if e.stpOut.Compare(remainingOut) <= 0 {
			// Full take
			savedIns.insert(e.stpIn)
			savedOuts.insert(e.stpOut)
			totalIn = savedIns.sum(s.zeroIn())
			totalOut = savedOuts.sum(s.zeroOut())
			remainingOut = out.Sub(totalOut)

			if e.isAMM {
				if err := s.consumeAMMOffer(sb, e.ammOffer, e.stpIn, e.ofrIn, e.stpOut, e.ownerGives); err != nil {
					throwConsumeFailure(err)
				}
			} else {
				if err := s.consumeOffer(sb, e.clobOffer, e.stpIn, e.ofrIn, e.stpOut, e.ownerGives); err != nil {
					throwConsumeFailure(err)
				}
			}
			// rippled's eachOffer returns true here unconditionally ("even if the
			// payment is satisfied, we need to consume the offer"), so the do-while
			// runs offers.step() again and grooms trailing offers.
			s.lastTipFullyConsumed = true
		} else {
			// Partial take: limitStepOut
			stpAdjOut := remainingOut
			var ofrAdjIn, ofrAdjOut EitherAmount
			if e.isAMM {
				ofrAdjIn, ofrAdjOut = e.ammOffer.LimitOut(e.ofrIn, e.ofrOut, stpAdjOut, true, s.fixReducedOffersV1)
			} else {
				if s.fixReducedOffersV1 {
					ofrAdjIn, ofrAdjOut = e.offerQuality.CeilOutStrict(e.ofrIn, e.ofrOut, stpAdjOut, true)
				} else {
					ofrAdjIn, ofrAdjOut = e.offerQuality.CeilOut(e.ofrIn, e.ofrOut, stpAdjOut)
				}
			}
			stpAdjIn := MulRatio(ofrAdjIn, e.ofrTrIn, QualityOne, true)
			ownerGivesAdj := MulRatio(stpAdjOut, e.ofrTrOut, QualityOne, false)
			_ = ofrAdjOut

			// rippled inserts stpAdjAmt.in into savedIns and sets result.in =
			// sum(savedIns); result.out = out (BookStep.cpp:1069-1072).
			savedIns.insert(stpAdjIn)
			totalIn = savedIns.sum(s.zeroIn())
			totalOut = out
			remainingOut = s.zeroOut()

			if e.isAMM {
				if err := s.consumeAMMOffer(sb, e.ammOffer, stpAdjIn, ofrAdjIn, stpAdjOut, ownerGivesAdj); err != nil {
					throwConsumeFailure(err)
				}
			} else {
				if err := s.consumeOffer(sb, e.clobOffer, stpAdjIn, ofrAdjIn, stpAdjOut, ownerGivesAdj); err != nil {
					throwConsumeFailure(err)
				}
			}
			// Demand met mid-offer: rippled returns offer.fully_consumed() here. A
			// still-funded partially-filled tip is NOT fully consumed, so the
			// do-while breaks without another offers.step() and trailing offers are
			// never groomed.
			s.lastTipFullyConsumed = s.tipFullyConsumed(e)
		}

		// rippled eachOffer return: a full take always returns true ("even if the
		// payment is satisfied, we need to consume the offer"), so the do-while
		// steps once more and grooms the trailing run; a partial fill returns
		// offer.fully_consumed(). lastTipFullyConsumed carries exactly that value.
		return s.lastTipFullyConsumed
	}

	s.forEachOffer(sb, afView, ofrsToRm, trIn, trOut,
		func() bool { return remainingOut.IsZero() }, revCallback)

	if s.tooManyOffersDiscard(sb) {
		return s.zeroIn(), s.zeroOut()
	}

	// Handle remainingOut == 0 but totalOut != out (normalization artifact)
	// Reference: BookStep.cpp lines 1122-1126
	if remainingOut.IsZero() || remainingOut.IsNegative() {
		totalOut = out
	}

	s.cache = &bookCache{
		in:  totalIn,
		out: totalOut,
	}

	return totalIn, totalOut
}

// Fwd executes the step with the given input.
// Matches rippled's BookStep::fwdImp() + forEachOffer() flow.
// Reference: BookStep.cpp lines 1133-1299 (fwdImp) + 717-873 (forEachOffer)
func (s *BookStep) Fwd(
	sb *PaymentSandbox,
	afView *PaymentSandbox,
	ofrsToRm map[[32]byte]bool,
	in EitherAmount,
) (EitherAmount, EitherAmount) {
	prevCache := s.cache
	s.cache = nil
	s.offersUsed = 0
	s.lastTipFullyConsumed = false
	s.lastConsumedTipValid = false
	s.maxOffersToConsume = maxOffersToConsume(sb)

	trIn := s.transferRateIn(sb, s.prevStepDebtDir(sb, StrandDirectionForward))
	trOut := s.transferRateOut(sb)

	totalIn, totalOut := s.zeroIn(), s.zeroOut()
	remainingIn := in

	// savedIns/savedOuts accumulate the per-offer step amounts and are summed
	// smallest-first, matching rippled's flat_multiset<TIn>/<TOut> + sum() in
	// fwdImp (BookStep.cpp:1172-1242). The ascending-order re-sum reproduces
	// rippled's 16-digit intermediate rounding (see revImp).
	var savedIns, savedOuts amountMultiset

	// fwdImp callback: decide full vs partial take, reconcile against the reverse
	// pass cache, consume, and update accumulators.
	// Reference: rippled BookStep.cpp fwdImp callback lines 1175-1240.
	fwdCallback := func(e offerExec) bool {
		// rippled fwd eachOffer: once the input is exhausted, break the do-while
		// (the prior take's step already groomed the trailing run).
		if remainingIn.IsZero() {
			return false
		}
		if e.stpIn.Compare(remainingIn) <= 0 {
			// Full take: rippled sets processMore = true and eachOffer returns
			// true, so the do-while runs offers.step() again and grooms trailing
			// offers.
			s.lastTipFullyConsumed = true
			savedIns.insert(e.stpIn)
			lastOut := e.stpOut
			savedOuts.insert(lastOut)
			totalIn = savedIns.sum(s.zeroIn())
			totalOut = savedOuts.sum(s.zeroOut())

			// Forward > reverse cache check
			if prevCache != nil && totalOut.Compare(prevCache.out) > 0 && totalIn.Compare(prevCache.in) <= 0 {
				savedOuts.erase(lastOut)
				remainingCacheOut := prevCache.out.Sub(savedOuts.sum(s.zeroOut()))
				adjOfrIn, adjOfrOut := e.ofrIn, e.ofrOut
				adjStpOut := remainingCacheOut
				if e.isAMM {
					adjOfrIn, adjOfrOut = e.ammOffer.LimitOut(adjOfrIn, adjOfrOut, adjStpOut, true, s.fixReducedOffersV1)
				} else {
					adjOfrIn, adjOfrOut = e.offerQuality.CeilOutStrict(adjOfrIn, adjOfrOut, adjStpOut, true)
				}
				adjStpIn := MulRatio(adjOfrIn, e.ofrTrIn, QualityOne, true)
				_ = adjOfrOut

				if adjStpIn.Compare(remainingIn) == 0 {
					totalIn = in
					totalOut = prevCache.out
					ownerGivesAdj := MulRatio(adjStpOut, e.ofrTrOut, QualityOne, false)
					if e.isAMM {
						if err := s.consumeAMMOffer(sb, e.ammOffer, adjStpIn, adjOfrIn, adjStpOut, ownerGivesAdj); err != nil {
							throwConsumeFailure(err)
						}
					} else {
						if err := s.consumeOffer(sb, e.clobOffer, adjStpIn, adjOfrIn, adjStpOut, ownerGivesAdj); err != nil {
							throwConsumeFailure(err)
						}
					}
					remainingIn = s.zeroIn()
					return true
				}
				// Reconciliation declined: rippled re-inserts the erased output
				// (BookStep.cpp:1241) so the running totals stay consistent.
				savedOuts.insert(lastOut)
			}

			remainingIn = in.Sub(totalIn)
			if e.isAMM {
				if err := s.consumeAMMOffer(sb, e.ammOffer, e.stpIn, e.ofrIn, e.stpOut, e.ownerGives); err != nil {
					throwConsumeFailure(err)
				}
			} else {
				if err := s.consumeOffer(sb, e.clobOffer, e.stpIn, e.ofrIn, e.stpOut, e.ownerGives); err != nil {
					throwConsumeFailure(err)
				}
			}
		} else {
			// Partial take: limitStepIn
			stpAdjIn := remainingIn
			inLmt := MulRatio(stpAdjIn, QualityOne, e.ofrTrIn, false)
			var ofrAdjIn, ofrAdjOut EitherAmount
			if e.isAMM {
				ofrAdjIn, ofrAdjOut = e.ammOffer.LimitIn(e.ofrIn, e.ofrOut, inLmt, false, s.fixReducedOffersV2)
			} else {
				if s.fixReducedOffersV2 {
					ofrAdjIn, ofrAdjOut = e.offerQuality.CeilInStrict(e.ofrIn, e.ofrOut, inLmt, false)
				} else {
					ofrAdjIn, ofrAdjOut = e.offerQuality.CeilIn(e.ofrIn, e.ofrOut, inLmt)
				}
			}
			stpAdjOut := ofrAdjOut
			ownerGivesAdj := MulRatio(ofrAdjOut, e.ofrTrOut, QualityOne, false)

			// rippled inserts remainingIn into savedIns and stpAdjAmt.out into
			// savedOuts, then sets result.out = sum(savedOuts); result.in = in
			// (BookStep.cpp:1190-1193).
			savedIns.insert(stpAdjIn)
			savedOuts.insert(stpAdjOut)
			totalOut = savedOuts.sum(s.zeroOut())
			totalIn = in

			// Forward > reverse cache check
			if prevCache != nil && totalOut.Compare(prevCache.out) > 0 && totalIn.Compare(prevCache.in) <= 0 {
				savedOuts.erase(stpAdjOut)
				remainingCacheOut := prevCache.out.Sub(savedOuts.sum(s.zeroOut()))
				revOfrIn, revOfrOut := e.ofrIn, e.ofrOut
				revStpOut := remainingCacheOut
				if e.isAMM {
					revOfrIn, revOfrOut = e.ammOffer.LimitOut(revOfrIn, revOfrOut, revStpOut, true, s.fixReducedOffersV1)
				} else {
					revOfrIn, revOfrOut = e.offerQuality.CeilOutStrict(revOfrIn, revOfrOut, revStpOut, true)
				}
				revStpIn := MulRatio(revOfrIn, e.ofrTrIn, QualityOne, true)
				revOwnerGives := MulRatio(revStpOut, e.ofrTrOut, QualityOne, false)
				_ = revOfrOut

				if revStpIn.Compare(remainingIn) == 0 {
					totalIn = in
					totalOut = prevCache.out
					if e.isAMM {
						if err := s.consumeAMMOffer(sb, e.ammOffer, revStpIn, revOfrIn, revStpOut, revOwnerGives); err != nil {
							throwConsumeFailure(err)
						}
					} else {
						if err := s.consumeOffer(sb, e.clobOffer, revStpIn, revOfrIn, revStpOut, revOwnerGives); err != nil {
							throwConsumeFailure(err)
						}
					}
					remainingIn = s.zeroIn()
					return true
				}
				// Reconciliation declined: rippled re-inserts the erased output
				// (BookStep.cpp:1241).
				savedOuts.insert(stpAdjOut)
			}

			remainingIn = s.zeroIn()
			if e.isAMM {
				if err := s.consumeAMMOffer(sb, e.ammOffer, stpAdjIn, ofrAdjIn, stpAdjOut, ownerGivesAdj); err != nil {
					throwConsumeFailure(err)
				}
			} else {
				if err := s.consumeOffer(sb, e.clobOffer, stpAdjIn, ofrAdjIn, stpAdjOut, ownerGivesAdj); err != nil {
					throwConsumeFailure(err)
				}
			}
			// Demand met mid-offer: rippled has processMore = false and returns
			// offer.fully_consumed(), so a still-funded tip breaks the do-while
			// without another offers.step() (no trailing groom).
			s.lastTipFullyConsumed = s.tipFullyConsumed(e)
		}

		// rippled fwd eachOffer return: processMore || offer.fully_consumed(). A
		// full take continues (lastTipFullyConsumed true → another step grooms the
		// trailing run); a partial fill returns offer.fully_consumed().
		return s.lastTipFullyConsumed
	}

	s.forEachOffer(sb, afView, ofrsToRm, trIn, trOut,
		func() bool { return remainingIn.IsZero() }, fwdCallback)

	if s.tooManyOffersDiscard(sb) {
		return s.zeroIn(), s.zeroOut()
	}

	// Handle remainingIn == 0 but totalIn != in
	if remainingIn.IsZero() || remainingIn.IsNegative() {
		totalIn = in
	}

	s.cache = &bookCache{
		in:  totalIn,
		out: totalOut,
	}

	return totalIn, totalOut
}

// CachedIn returns the input from the last Rev() call
func (s *BookStep) CachedIn() *EitherAmount {
	if s.cache == nil {
		return nil
	}
	return &s.cache.in
}

// CachedOut returns the output from the last Rev() call
func (s *BookStep) CachedOut() *EitherAmount {
	if s.cache == nil {
		return nil
	}
	return &s.cache.out
}

// DebtDirection returns the debt direction based on who pays transfer fee
func (s *BookStep) DebtDirection(sb *PaymentSandbox, dir StrandDirection) DebtDirection {
	if s.ownerPaysTransferFee {
		return DebtDirectionIssues
	}
	return DebtDirectionRedeems
}

// IsZero returns true if the amount is zero
func (s *BookStep) IsZero(amt EitherAmount) bool {
	return amt.IsZero()
}

// EqualIn compares input amounts
func (s *BookStep) EqualIn(a, b EitherAmount) bool {
	return a.Compare(b) == 0
}

// EqualOut compares output amounts
func (s *BookStep) EqualOut(a, b EitherAmount) bool {
	return a.Compare(b) == 0
}

// Inactive returns whether this step is inactive
func (s *BookStep) Inactive() bool {
	return s.inactive
}

// OffersUsed returns the number of offers consumed
func (s *BookStep) OffersUsed() uint32 {
	return s.offersUsed
}

// DirectStepAccts returns nil - this is not a direct step
func (s *BookStep) DirectStepAccts() *[2][20]byte {
	return nil
}

// BookStepBook returns the book for this step
func (s *BookStep) BookStepBook() *Book {
	return &s.book
}

// LineQualityIn returns QualityOne for book steps
func (s *BookStep) LineQualityIn(v *PaymentSandbox) uint32 {
	return QualityOne
}

// ValidFwd validates that the step can correctly execute in forward
func (s *BookStep) ValidFwd(sb *PaymentSandbox, afView *PaymentSandbox, in EitherAmount) (bool, EitherAmount) {
	if s.cache == nil {
		return false, ZeroXRPEitherAmount()
	}
	return true, s.cache.out
}

// getCLOBTipQuality gets the best quality from CLOB offers only.
//
// It mirrors rippled's BookTip::step, which advances past EMPTY book
// directories: dirFirst returns false for a directory whose pages hold no
// offer entries, and the BookTip loop then steps to the next quality tier
// (BookTip.cpp:44-76). A stale, empty book directory therefore must not be
// reported as the book tip — its quality is not a quality any offer can be
// crossed at. Reading the first directory's quality unconditionally would
// surface that phantom tier, which (e.g. on an autobridge leg whose first
// directory is a left-over empty tier) makes the strand's quality upper bound
// far better than any real liquidity and wrongly keeps the strand active.
func (s *BookStep) getCLOBTipQuality(sb *PaymentSandbox) *Quality {
	bookBase := s.bookBaseKey()
	bookPrefix := bookBase[:24]

	searchKey := bookBase
	for {
		foundKey, foundData, found, err := sb.Succ(searchKey)
		if err != nil || !found {
			return nil
		}
		if !bytes.Equal(foundKey[:24], bookPrefix) {
			return nil
		}

		if s.bookDirHasEntries(sb, foundKey, foundData) {
			q := QualityFromKey(foundKey)
			return &q
		}

		// Empty directory tier — advance to the next quality, like
		// BookTip::step's fall-through when dirFirst returns false.
		searchKey = foundKey
	}
}

// bookDirHasEntries reports whether the book directory rooted at rootKey holds
// at least one offer index across its page chain. Mirrors rippled's dirFirst,
// which walks the page chain (sfIndexNext) and only succeeds when some page has
// a non-empty sfIndexes vector. rootData is the already-read root page.
func (s *BookStep) bookDirHasEntries(sb *PaymentSandbox, rootKey [32]byte, rootData []byte) bool {
	dir, err := state.ParseDirectoryNode(rootData)
	if err != nil {
		return false
	}
	for {
		if len(dir.Indexes) > 0 {
			return true
		}
		if dir.IndexNext == 0 {
			return false
		}
		pageData, err := sb.Read(keylet.DirPage(rootKey, dir.IndexNext))
		if err != nil || pageData == nil {
			return false
		}
		dir, err = state.ParseDirectoryNode(pageData)
		if err != nil {
			return false
		}
	}
}

// bookBaseKey computes the base key for this BookStep's order book.
// Returns the book prefix (24 bytes) with quality bytes (24-31) zeroed.
// This serves as the lowest possible key for this book, suitable as a
// starting point for Succ()-based iteration.
// Reference: rippled BookTip initializes with book base (quality=0).
func (s *BookStep) bookBaseKey() [32]byte {
	takerPaysCurrency := keylet.CurrencyBytes(s.book.In.Currency)
	takerPaysIssuer := s.book.In.Issuer
	takerGetsCurrency := keylet.CurrencyBytes(s.book.Out.Currency)
	takerGetsIssuer := s.book.Out.Issuer

	var key [32]byte
	if s.domainID != nil {
		key = keylet.BookDirWithDomain(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer, *s.domainID).Key
	} else {
		key = keylet.BookDir(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer).Key
	}
	// Zero out quality bytes (24-31). BookDir returns a full SHA-512Half hash,
	// but actual book directory entries have bytes 24-31 replaced with the quality
	// value. Zero them so Succ() finds the first quality entry.
	for i := 24; i < 32; i++ {
		key[i] = 0
	}
	return key
}

// Check validates the BookStep before use
// Reference: rippled BookStep.cpp check() lines 1343-1380
func (s *BookStep) Check(sb *PaymentSandbox) ter.Result {
	// Check for same in/out issue - this is invalid
	// Reference: rippled BookStep.cpp lines 1346-1351
	if s.book.In.Currency == s.book.Out.Currency && s.book.In.Issuer == s.book.Out.Issuer {
		return ter.TemBAD_PATH
	}

	// Each side's currency and issuer must agree on XRP-ness: a non-XRP currency
	// requires a non-zero issuer and XRP requires the zero issuer. An inconsistent
	// book — e.g. a bare-currency path element that resolved to a zero issuer —
	// cannot be crossed, and this must be rejected before the issuer-exists check
	// so a malformed book yields temBAD_PATH (not a spurious tecNO_ISSUER from
	// reading the zero account).
	// Reference: rippled BookStep.cpp lines 1352-1357 (isConsistent) -> temBAD_PATH
	if !s.book.In.IsConsistent() || !s.book.Out.IsConsistent() {
		return ter.TemBAD_PATH
	}

	// Both the book's in and out issuers must exist on the ledger (XRP exempt).
	// A book that references a deleted or never-created issuer cannot be crossed.
	// Reference: rippled BookStep.cpp lines 1374-1382 (issuerExists) -> tecNO_ISSUER
	issuerExists := func(iss Issue) bool {
		if iss.IsXRP() {
			return true
		}
		data, err := sb.Read(keylet.Account(iss.Issuer))
		return err == nil && data != nil
	}
	if !issuerExists(s.book.In) || !issuerExists(s.book.Out) {
		return ter.TecNO_ISSUER
	}

	// If previous step is a DirectStep, check NoRipple on the trust line
	// between the DirectStep's source and the book's input issuer.
	// Reference: rippled BookStep.cpp lines 1384-1397
	if s.prevStep != nil {
		if prevDirect, ok := s.prevStep.(*DirectStepI); ok {
			prev := prevDirect.src
			cur := s.book.In.Issuer
			if !s.book.In.IsXRP() {
				sleLineKey := keylet.Line(prev, cur, s.book.In.Currency)
				sleLineData, err := sb.Read(sleLineKey)
				if err != nil || sleLineData == nil {
					return ter.TerNO_LINE
				}
				rs, parseErr := state.ParseRippleState(sleLineData)
				if parseErr != nil {
					return ter.TefINTERNAL
				}
				// Check cur's NoRipple flag on the prev-cur trust line
				curIsHigh := state.CompareAccountIDs(cur, prev) > 0
				var noRippleFlag uint32
				if curIsHigh {
					noRippleFlag = state.LsfHighNoRipple
				} else {
					noRippleFlag = state.LsfLowNoRipple
				}
				if rs.Flags&noRippleFlag != 0 {
					return ter.TerNO_RIPPLE
				}
			}
		}
	}

	return ter.TesSUCCESS
}

// LedgerReader is an interface for reading ledger entries.
// PaymentSandbox and other views can implement this.
type LedgerReader interface {
	Read(key keylet.Keylet) ([]byte, error)
}

// GetLedgerReserves reads the reserve values from the ledger's FeeSettings entry.
// Returns (baseReserve, incrementReserve) in drops.
// If FeeSettings cannot be read, returns default values (10 XRP, 2 XRP).
// Reference: rippled View.cpp uses fees keylet to read reserves
func GetLedgerReserves(view LedgerReader) (baseReserve, incrementReserve int64) {
	// Default values (modern mainnet values)
	defaultBase := int64(10_000_000)     // 10 XRP
	defaultIncrement := int64(2_000_000) // 2 XRP

	feesKey := keylet.Fees()
	feesData, err := view.Read(feesKey)
	if err != nil || feesData == nil {
		return defaultBase, defaultIncrement
	}

	feeSettings, err := state.ParseFeeSettings(feesData)
	if err != nil {
		return defaultBase, defaultIncrement
	}

	base := int64(feeSettings.GetReserveBase())
	inc := int64(feeSettings.GetReserveIncrement())
	return base, inc
}

// consumeAMMOffer processes an AMM offer through the pool.
// Checks the pool product invariant, transfers funds, and marks the offer consumed.
// Reference: rippled BookStep.cpp consumeOffer() for AMMOffer
func (s *BookStep) consumeAMMOffer(
	sb *PaymentSandbox,
	ammOffer *AMMOffer,
	consumedInGross, consumedInNet, consumedOut, ownerGives EitherAmount,
) error {
	// Check pool product invariant
	if !ammOffer.CheckInvariant(eitherToAmount(consumedInNet), eitherToAmount(consumedOut)) {
		if s.fixAMMOverflowOffer {
			return errors.New("AMM pool product invariant failed")
		}
	}

	// Transfer input: book.in.account → AMM account.
	// Re-tag with the book's IN issue: the EitherAmount magnitude is correct, but
	// its currency/issuer can carry the strand-destination issue rather than this
	// book's own issue. The AMM send routes by amount.Issuer (unlike the CLOB path,
	// which threads s.book.In explicitly), so a mis-tagged amount would hit the
	// wrong trust line. Mirror the CLOB consumeOffer, which passes s.book.In/Out.
	inAmount := retagToIssue(eitherToAmount(consumedInNet), s.book.In)
	if err := ammOffer.Send(sb, s.book.In.Issuer, ammOffer.Owner(), inAmount); err != nil {
		return err
	}

	// Transfer output: AMM account → book.out.account (re-tagged with book.Out).
	outAmount := retagToIssue(eitherToAmount(ownerGives), s.book.Out)
	if err := ammOffer.Send(sb, ammOffer.Owner(), s.book.Out.Issuer, outAmount); err != nil {
		return err
	}

	// Mark the offer as consumed
	ammOffer.Consume()

	return nil
}

// initAMMLiquidity checks for an AMM pool for this book's in/out issues,
// and if one exists with non-zero LP token balance, creates an AMMLiquidity.
// Reference: rippled BookStep constructor lines 103-112
func (s *BookStep) initAMMLiquidity(
	view *PaymentSandbox,
	ammCtx *AMMContext,
	parentCloseTime uint32,
	fixAMMv1_1, fixAMMv1_2, fixAMMOverflowOffer bool,
) {
	s.fixAMMOverflowOffer = fixAMMOverflowOffer

	// Build keylet::amm(in, out) to look up the AMM SLE
	inIssuer := issueToCurrencyBytes(s.book.In)
	inCurrency := keylet.CurrencyBytes(s.book.In.Currency)
	outIssuer := issueToCurrencyBytes(s.book.Out)
	outCurrency := keylet.CurrencyBytes(s.book.Out.Currency)

	ammKey := keylet.AMM(inIssuer, inCurrency, outIssuer, outCurrency)
	ammData, err := view.Read(ammKey)
	if err != nil || ammData == nil {
		return
	}

	ammEntry, err := amm.ParseAMMData(ammData)
	if err != nil {
		return
	}

	// Check LP token balance is non-zero
	if ammEntry.LPTokenBalance.IsZero() {
		return
	}

	// Get trading fee (may be discounted for auction slot holder)
	tradingFee := getAMMTradingFee(ammEntry, ammCtx.Account(), parentCloseTime)

	s.ammLiquidity = NewAMMLiquidity(
		view,
		ammEntry.Account,
		tradingFee,
		s.book.In, s.book.Out,
		ammCtx,
		fixAMMv1_1, fixAMMv1_2, fixAMMOverflowOffer,
	)
}

// getAMMOffer retrieves a synthetic AMM offer from the AMMLiquidity provider.
// Returns nil if no AMM pool exists or if CLOB quality is better.
// Reference: rippled BookStep::getAMMOffer()
func (s *BookStep) getAMMOffer(view *PaymentSandbox, clobQuality *Quality) *AMMOffer {
	if s.ammLiquidity == nil {
		return nil
	}
	return s.ammLiquidity.GetOffer(view, clobQuality)
}

// getAMMTradingFee returns the trading fee for an AMM, potentially discounted
// if the account holds the auction slot or is an authorized account.
// Reference: rippled AMMUtils.cpp getTradingFee()
func getAMMTradingFee(ammEntry *amm.AMMData, account [20]byte, parentCloseTime uint32) uint16 {
	if ammEntry.AuctionSlot != nil {
		// Check if auction slot is not expired
		if parentCloseTime < ammEntry.AuctionSlot.Expiration {
			// Check if account is the auction slot holder
			if ammEntry.AuctionSlot.Account == account {
				return ammEntry.AuctionSlot.DiscountedFee
			}
			// Check authorized accounts
			if slices.Contains(ammEntry.AuctionSlot.AuthAccounts, account) {
				return ammEntry.AuctionSlot.DiscountedFee
			}
		}
	}
	return ammEntry.TradingFee
}

// issueToCurrencyBytes returns the issuer as [20]byte for keylet.AMM.
func issueToCurrencyBytes(issue Issue) [20]byte {
	return issue.Issuer
}
