package payment

import (
	"bytes"
	"errors"
	"slices"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
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

	// maxOffersToConsume is the limit on offers consumed per execution
	maxOffersToConsume uint32

	// qualityLimit is the worst quality offer that should be consumed.
	// If set, offers with worse quality (higher value) are not crossed.
	// This is used for offer crossing to only cross offers at or better
	// than the taker's quality.
	qualityLimit *Quality

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
}

// bookCache holds cached values from the reverse pass
type bookCache struct {
	in  EitherAmount
	out EitherAmount
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
					s.offersUsed++
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
					s.offersUsed++
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

		return callback(offerExec{
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
		ammOffer := s.getAMMOffer(sb, lobQuality)
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
	for s.offersUsed < s.maxOffersToConsume && !remainingZero() {
		offer, offerKey, err := s.getNextOfferSkipVisited(sb, afView, ofrsToRm, visited)
		if err != nil {
			break
		}
		if offer == nil {
			break
		}
		visited[offerKey] = true

		// Deep freeze check on the input (TakerPays) side.
		// Deep-frozen offers are permanently removed from the order book.
		// Reference: rippled OfferStream.cpp lines 280-292
		{
			offerOwnerDF, _ := state.DecodeAccountID(offer.Account)
			if s.isDeepFrozen(sb, offerOwnerDF, s.book.In.Currency, s.book.In.Issuer) {
				ofrsToRm[offerKey] = true
				s.offersUsed++
				continue
			}
		}

		// Pre-execOffer checks (OfferStream level)
		// Reference: rippled OfferStream::step() reads ownerFunds from view_ (sb).
		ownerFunds := s.getOfferFundedAmount(sb, offer)
		if ownerFunds.IsZero() || offer.TakerGets.IsZero() {
			ofrsToRm[offerKey] = true
			s.offersUsed++
			continue
		}
		if s.shouldRmSmallIncreasedQOffer(sb, offer, ownerFunds) {
			ofrsToRm[offerKey] = true
			s.offersUsed++
			continue
		}

		// On the first funded CLOB offer, try the AMM with the LOB quality.
		if firstCLOB {
			firstCLOB = false
			lobQ := s.offerQuality(offer)
			if !tryAMM(&lobQ) {
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
	}

	// If no CLOB offers found, try the AMM alone.
	if firstCLOB {
		tryAMM(nil)
	}
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
	s.maxOffersToConsume = maxOffersToConsume(sb)

	trIn := s.transferRateIn(sb, s.prevStepDebtDir(sb, StrandDirectionReverse))
	trOut := s.transferRateOut(sb)

	totalIn, totalOut := s.zeroIn(), s.zeroOut()
	remainingOut := out

	// revImp callback: decide full vs partial take, consume, and update accumulators.
	// Reference: rippled BookStep.cpp revImp callback lines 1056-1083.
	revCallback := func(e offerExec) bool {
		if e.stpOut.Compare(remainingOut) <= 0 {
			// Full take
			totalIn = totalIn.Add(e.stpIn)
			totalOut = totalOut.Add(e.stpOut)
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

			totalIn = totalIn.Add(stpAdjIn)
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
		}

		s.offersUsed++
		return true
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
	s.maxOffersToConsume = maxOffersToConsume(sb)

	trIn := s.transferRateIn(sb, s.prevStepDebtDir(sb, StrandDirectionForward))
	trOut := s.transferRateOut(sb)

	totalIn, totalOut := s.zeroIn(), s.zeroOut()
	remainingIn := in

	// fwdImp callback: decide full vs partial take, reconcile against the reverse
	// pass cache, consume, and update accumulators.
	// Reference: rippled BookStep.cpp fwdImp callback lines 1175-1240.
	fwdCallback := func(e offerExec) bool {
		if e.stpIn.Compare(remainingIn) <= 0 {
			totalIn = totalIn.Add(e.stpIn)
			totalOut = totalOut.Add(e.stpOut)

			// Forward > reverse cache check
			if prevCache != nil && totalOut.Compare(prevCache.out) > 0 && totalIn.Compare(prevCache.in) <= 0 {
				remainingCacheOut := prevCache.out.Sub(totalOut.Sub(e.stpOut))
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
					s.offersUsed++
					return true
				}
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

			totalOut = totalOut.Add(stpAdjOut)
			totalIn = in

			// Forward > reverse cache check
			if prevCache != nil && totalOut.Compare(prevCache.out) > 0 && totalIn.Compare(prevCache.in) <= 0 {
				remainingCacheOut := prevCache.out.Sub(totalOut.Sub(stpAdjOut))
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
					s.offersUsed++
					return true
				}
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
		}

		s.offersUsed++
		return true
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
func (s *BookStep) getCLOBTipQuality(sb *PaymentSandbox) *Quality {
	bookBase := s.bookBaseKey()

	foundKey, _, found, err := sb.Succ(bookBase)
	if err != nil || !found {
		return nil
	}

	if !bytes.Equal(foundKey[:24], bookBase[:24]) {
		return nil
	}

	q := QualityFromKey(foundKey)
	return &q
}

// bookBaseKey computes the base key for this BookStep's order book.
// Returns the book prefix (24 bytes) with quality bytes (24-31) zeroed.
// This serves as the lowest possible key for this book, suitable as a
// starting point for Succ()-based iteration.
// Reference: rippled BookTip initializes with book base (quality=0).
func (s *BookStep) bookBaseKey() [32]byte {
	takerPaysCurrency := state.GetCurrencyBytes(s.book.In.Currency)
	takerPaysIssuer := s.book.In.Issuer
	takerGetsCurrency := state.GetCurrencyBytes(s.book.Out.Currency)
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
func (s *BookStep) Check(sb *PaymentSandbox) tx.Result {
	// Check for same in/out issue - this is invalid
	// Reference: rippled BookStep.cpp lines 1346-1351
	if s.book.In.Currency == s.book.Out.Currency && s.book.In.Issuer == s.book.Out.Issuer {
		return tx.TemBAD_PATH
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
					return tx.TerNO_LINE
				}
				rs, parseErr := state.ParseRippleState(sleLineData)
				if parseErr != nil {
					return tx.TefINTERNAL
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
					return tx.TerNO_RIPPLE
				}
			}
		}
	}

	return tx.TesSUCCESS
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
	inCurrency := issueToCurrencyBytesForCurrency(s.book.In)
	outIssuer := issueToCurrencyBytes(s.book.Out)
	outCurrency := issueToCurrencyBytesForCurrency(s.book.Out)

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

// issueToCurrencyBytesForCurrency returns the currency as [20]byte for keylet.AMM.
// For XRP, this is all zeros. For IOUs, the 3-letter code is at bytes 12-14.
func issueToCurrencyBytesForCurrency(issue Issue) [20]byte {
	if issue.IsXRP() {
		return [20]byte{}
	}
	var currency [20]byte
	// Standard 3-letter currency codes go at bytes 12-14 in the 20-byte field
	if len(issue.Currency) == 3 {
		currency[12] = issue.Currency[0]
		currency[13] = issue.Currency[1]
		currency[14] = issue.Currency[2]
	}
	return currency
}
