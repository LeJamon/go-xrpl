package payment

import (
	"bytes"
	"errors"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/amm"
	"github.com/LeJamon/goXRPLd/keylet"
)

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
	inactive_ bool

	// offersUsed tracks offers consumed in last execution
	offersUsed_ uint32

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
		maxOffersToConsume:   1000, // fix1515 limit
		qualityLimit:         nil,
		inactive_:            false,
		offersUsed_:          0,
		cache:                nil,
	}
}

// NewBookStepWithQualityLimit creates a new BookStep with a quality limit.
// Offers with worse quality than the limit will not be consumed.
func NewBookStepWithQualityLimit(inIssue, outIssue Issue, strandSrc, strandDst [20]byte, prevStep Step, ownerPaysTransferFee bool, qualityLimit *Quality) *BookStep {
	step := NewBookStep(inIssue, outIssue, strandSrc, strandDst, prevStep, ownerPaysTransferFee)
	step.qualityLimit = qualityLimit
	return step
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
	s.offersUsed_ = 0

	// Get transfer rates
	// When there is no previous step (BookStep is the first step in the strand,
	// meaning the strand source IS the issuer of the input currency), default to
	// DebtDirectionIssues so no transfer fee is charged on the input side.
	// Reference: rippled BookStep.cpp revImp() lines 1085-1089
	prevStepDebtDir := DebtDirectionIssues
	if s.prevStep != nil {
		prevStepDebtDir = s.prevStep.DebtDirection(sb, StrandDirectionReverse)
	}

	trIn := s.transferRateIn(sb, prevStepDebtDir)
	trOut := s.transferRateOut(sb)

	// Initialize accumulators
	var totalIn, totalOut EitherAmount
	if s.book.In.IsXRP() {
		totalIn = ZeroXRPEitherAmount()
	} else {
		totalIn = ZeroIOUEitherAmount(s.book.In.Currency, state.EncodeAccountIDSafe(s.book.In.Issuer))
	}
	if s.book.Out.IsXRP() {
		totalOut = ZeroXRPEitherAmount()
	} else {
		totalOut = ZeroIOUEitherAmount(s.book.Out.Currency, state.EncodeAccountIDSafe(s.book.Out.Issuer))
	}

	remainingOut := out

	// Track visited offers
	visited := make(map[[32]byte]bool)

	// Track the current quality level — forEachOffer processes one quality at a time.
	// Reference: rippled BookStep.cpp forEachOffer lines 751-754:
	//   if (!ofrQ) ofrQ = offer.quality();
	//   else if (*ofrQ != offer.quality()) return false;
	var currentQuality *Quality
	offerAttempted := false

	// AMM-aware offer iteration — combined forEachOffer + revImp callback.
	// Reference: rippled BookStep.cpp forEachOffer lines 836-873
	//
	// The pattern is:
	//   1. Get first CLOB offer quality (lobQuality)
	//   2. tryAMM(lobQuality) — try AMM offer with CLOB quality as threshold
	//   3. Iterate CLOB offers at the same quality level
	//   4. If no CLOB offers, tryAMM(nullopt) — try AMM alone
	ammProcessed := false

	// execOffer processes a single offer (CLOB or AMM) through the rev callback.
	// Returns false to stop iteration.
	execOffer := func(ofrIn, ofrOut EitherAmount, offerQuality Quality,
		ofrTrIn, ofrTrOut uint32, _ bool, isAMM bool,
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
					s.offersUsed_++
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
					s.offersUsed_++
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

		// === revImp callback: decide full take vs partial take ===
		if stpOut.Compare(remainingOut) <= 0 {
			// Full take
			totalIn = totalIn.Add(stpIn)
			totalOut = totalOut.Add(stpOut)
			remainingOut = out.Sub(totalOut)

			if isAMM {
				if err := s.consumeAMMOffer(sb, ammOffer, stpIn, ofrIn, stpOut, ownerGives); err != nil {
					return false
				}
			} else {
				if err := s.consumeOffer(sb, clobOffer, stpIn, ofrIn, stpOut, ownerGives); err != nil {
					return false
				}
			}
		} else {
			// Partial take: limitStepOut
			stpAdjOut := remainingOut
			var ofrAdjIn, ofrAdjOut EitherAmount
			if isAMM {
				ofrAdjIn, ofrAdjOut = ammOffer.LimitOut(ofrIn, ofrOut, stpAdjOut, true, s.fixReducedOffersV1)
			} else {
				if s.fixReducedOffersV1 {
					ofrAdjIn, ofrAdjOut = offerQuality.CeilOutStrict(ofrIn, ofrOut, stpAdjOut, true)
				} else {
					ofrAdjIn, ofrAdjOut = offerQuality.CeilOut(ofrIn, ofrOut, stpAdjOut)
				}
			}
			stpAdjIn := MulRatio(ofrAdjIn, ofrTrIn, QualityOne, true)
			ownerGivesAdj := MulRatio(stpAdjOut, ofrTrOut, QualityOne, false)
			_ = ofrAdjOut

			totalIn = totalIn.Add(stpAdjIn)
			totalOut = out
			remainingOut = s.zeroOut()

			if isAMM {
				if err := s.consumeAMMOffer(sb, ammOffer, stpAdjIn, ofrAdjIn, stpAdjOut, ownerGivesAdj); err != nil {
					return false
				}
			} else {
				if err := s.consumeOffer(sb, clobOffer, stpAdjIn, ofrAdjIn, stpAdjOut, ownerGivesAdj); err != nil {
					return false
				}
			}
		}

		s.offersUsed_++
		return true
	}

	// tryAMM attempts to process an AMM offer with optional CLOB quality threshold.
	// Reference: rippled BookStep.cpp forEachOffer lines 838-853
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
		return execOffer(ofrIn, ofrOut, offerQ, trIn, trOut, true, true,
			ammOffer, nil, [32]byte{})
	}

	// Main CLOB iteration with AMM interleaving
	// Reference: rippled BookStep.cpp forEachOffer lines 855-873
	fundedCount := 0
	unfundedCount := 0

	firstCLOB := true
	for s.offersUsed_ < s.maxOffersToConsume && !remainingOut.IsZero() {
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
				s.offersUsed_++
				continue
			}
		}

		// Pre-execOffer checks (OfferStream level)
		// Pre-execOffer checks (OfferStream level)
		// Reference: rippled OfferStream::step() reads ownerFunds from view_ (sb).
		ownerFunds := s.getOfferFundedAmount(sb, offer)
		if ownerFunds.IsEffectivelyZero() || offer.TakerGets.IsZero() {
			ofrsToRm[offerKey] = true
			s.offersUsed_++
			unfundedCount++
			continue
		}
		if s.shouldRmSmallIncreasedQOffer(sb, offer, ownerFunds) {
			ofrsToRm[offerKey] = true
			s.offersUsed_++
			continue
		}

		// On first funded CLOB offer, try AMM with LOB quality
		if firstCLOB {
			firstCLOB = false
			lobQ := s.offerQuality(offer)
			if !tryAMM(&lobQ) {
				break
			}
		}

		fundedCount++

		// Process this CLOB offer through execOffer
		offerOwner, _ := state.DecodeAccountID(offer.Account)
		ofrTrIn := s.getOfrInRate(offerOwner, trIn)
		ofrTrOut := s.getOfrOutRate(offerOwner, trOut)
		ofrIn := s.offerTakerPays(offer)
		ofrOut := s.offerTakerGets(offer)
		offerQ := s.offerQuality(offer)
		if !execOffer(ofrIn, ofrOut, offerQ, ofrTrIn, ofrTrOut, false, false,
			nil, offer, offerKey) {
			break
		}
	}

	// If no CLOB offers found, try AMM alone
	if firstCLOB {
		tryAMM(nil)
	}

	_ = fundedCount
	_ = unfundedCount

	// Check if we should become inactive
	if s.offersUsed_ >= s.maxOffersToConsume {
		s.inactive_ = true
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
	s.offersUsed_ = 0

	// Get transfer rates
	// When there is no previous step, default to DebtDirectionIssues
	// (no transfer fee on input). Reference: rippled BookStep.cpp fwdImp() lines 1256-1260
	prevStepDebtDir := DebtDirectionIssues
	if s.prevStep != nil {
		prevStepDebtDir = s.prevStep.DebtDirection(sb, StrandDirectionForward)
	}

	trIn := s.transferRateIn(sb, prevStepDebtDir)
	trOut := s.transferRateOut(sb)

	// Initialize accumulators
	var totalIn, totalOut EitherAmount
	if s.book.In.IsXRP() {
		totalIn = ZeroXRPEitherAmount()
	} else {
		totalIn = ZeroIOUEitherAmount(s.book.In.Currency, state.EncodeAccountIDSafe(s.book.In.Issuer))
	}
	if s.book.Out.IsXRP() {
		totalOut = ZeroXRPEitherAmount()
	} else {
		totalOut = ZeroIOUEitherAmount(s.book.Out.Currency, state.EncodeAccountIDSafe(s.book.Out.Issuer))
	}

	remainingIn := in

	visited := make(map[[32]byte]bool)

	// Track the current quality level — forEachOffer processes one quality at a time.
	// Reference: rippled BookStep.cpp forEachOffer lines 751-754
	var currentQuality *Quality
	offerAttempted := false

	// AMM-aware offer iteration — combined forEachOffer + fwdImp callback.
	ammProcessed := false

	// execOfferFwd processes a single offer (CLOB or AMM) through the fwd callback.
	execOfferFwd := func(ofrIn, ofrOut EitherAmount, offerQuality Quality,
		ofrTrIn, ofrTrOut uint32, _ bool, isAMM bool,
		ammOffer *AMMOffer, clobOffer *state.LedgerOffer, clobKey [32]byte,
	) bool {
		// Quality tracking
		if currentQuality == nil {
			currentQuality = &offerQuality
		} else if currentQuality.Value != offerQuality.Value {
			return false
		}

		// Self-cross detection (CLOB only)
		if !isAMM && s.defaultPath && s.qualityLimit != nil {
			offerOwner, ownerErr := state.DecodeAccountID(clobOffer.Account)
			if ownerErr == nil {
				if !offerQuality.WorseThan(*s.qualityLimit) &&
					s.strandSrc == offerOwner && s.strandDst == offerOwner {
					ofrsToRm[clobKey] = true
					s.offersUsed_++
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
					s.offersUsed_++
					if !offerAttempted {
						currentQuality = nil
					}
					return true
				}
			}
		}

		if s.qualityLimit != nil && offerQuality.WorseThan(*s.qualityLimit) {
			return false
		}

		offerAttempted = true

		if isAMM {
			ofrTrIn, ofrTrOut = ammOffer.AdjustRates(ofrTrIn, ofrTrOut)
		}

		stpIn := MulRatio(ofrIn, ofrTrIn, QualityOne, true)
		stpOut := ofrOut
		ownerGives := MulRatio(ofrOut, ofrTrOut, QualityOne, false)

		// Funding cap (CLOB only)
		// Reference: rippled OfferStream reads ownerFunds from view_ (sb).
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

		// fwdImp callback
		if stpIn.Compare(remainingIn) <= 0 {
			totalIn = totalIn.Add(stpIn)
			totalOut = totalOut.Add(stpOut)

			// Forward > reverse cache check
			if prevCache != nil && totalOut.Compare(prevCache.out) > 0 && totalIn.Compare(prevCache.in) <= 0 {
				remainingCacheOut := prevCache.out.Sub(totalOut.Sub(stpOut))
				adjOfrIn, adjOfrOut := ofrIn, ofrOut
				adjStpOut := remainingCacheOut
				if isAMM {
					adjOfrIn, adjOfrOut = ammOffer.LimitOut(adjOfrIn, adjOfrOut, adjStpOut, true, s.fixReducedOffersV1)
				} else {
					adjOfrIn, adjOfrOut = offerQuality.CeilOutStrict(adjOfrIn, adjOfrOut, adjStpOut, true)
				}
				adjStpIn := MulRatio(adjOfrIn, ofrTrIn, QualityOne, true)
				_ = adjOfrOut

				if adjStpIn.Compare(remainingIn) == 0 {
					totalIn = in
					totalOut = prevCache.out
					ownerGivesAdj := MulRatio(adjStpOut, ofrTrOut, QualityOne, false)
					if isAMM {
						if err := s.consumeAMMOffer(sb, ammOffer, adjStpIn, adjOfrIn, adjStpOut, ownerGivesAdj); err != nil {
							return false
						}
					} else {
						if err := s.consumeOffer(sb, clobOffer, adjStpIn, adjOfrIn, adjStpOut, ownerGivesAdj); err != nil {
							return false
						}
					}
					remainingIn = s.zeroIn()
					s.offersUsed_++
					return true
				}
			}

			remainingIn = in.Sub(totalIn)
			if isAMM {
				if err := s.consumeAMMOffer(sb, ammOffer, stpIn, ofrIn, stpOut, ownerGives); err != nil {
					return false
				}
			} else {
				if err := s.consumeOffer(sb, clobOffer, stpIn, ofrIn, stpOut, ownerGives); err != nil {
					return false
				}
			}
		} else {
			// Partial take: limitStepIn
			stpAdjIn := remainingIn
			inLmt := MulRatio(stpAdjIn, QualityOne, ofrTrIn, false)
			var ofrAdjIn, ofrAdjOut EitherAmount
			if isAMM {
				ofrAdjIn, ofrAdjOut = ammOffer.LimitIn(ofrIn, ofrOut, inLmt, false, s.fixReducedOffersV2)
			} else {
				if s.fixReducedOffersV2 {
					ofrAdjIn, ofrAdjOut = offerQuality.CeilInStrict(ofrIn, ofrOut, inLmt, false)
				} else {
					ofrAdjIn, ofrAdjOut = offerQuality.CeilIn(ofrIn, ofrOut, inLmt)
				}
			}
			stpAdjOut := ofrAdjOut
			ownerGivesAdj := MulRatio(ofrAdjOut, ofrTrOut, QualityOne, false)

			totalOut = totalOut.Add(stpAdjOut)
			totalIn = in

			// Forward > reverse cache check
			if prevCache != nil && totalOut.Compare(prevCache.out) > 0 && totalIn.Compare(prevCache.in) <= 0 {
				remainingCacheOut := prevCache.out.Sub(totalOut.Sub(stpAdjOut))
				revOfrIn, revOfrOut := ofrIn, ofrOut
				revStpOut := remainingCacheOut
				if isAMM {
					revOfrIn, revOfrOut = ammOffer.LimitOut(revOfrIn, revOfrOut, revStpOut, true, s.fixReducedOffersV1)
				} else {
					revOfrIn, revOfrOut = offerQuality.CeilOutStrict(revOfrIn, revOfrOut, revStpOut, true)
				}
				revStpIn := MulRatio(revOfrIn, ofrTrIn, QualityOne, true)
				revOwnerGives := MulRatio(revStpOut, ofrTrOut, QualityOne, false)
				_ = revOfrOut

				if revStpIn.Compare(remainingIn) == 0 {
					totalIn = in
					totalOut = prevCache.out
					if isAMM {
						if err := s.consumeAMMOffer(sb, ammOffer, revStpIn, revOfrIn, revStpOut, revOwnerGives); err != nil {
							return false
						}
					} else {
						if err := s.consumeOffer(sb, clobOffer, revStpIn, revOfrIn, revStpOut, revOwnerGives); err != nil {
							return false
						}
					}
					remainingIn = s.zeroIn()
					s.offersUsed_++
					return true
				}
			}

			remainingIn = s.zeroIn()
			if isAMM {
				if err := s.consumeAMMOffer(sb, ammOffer, stpAdjIn, ofrAdjIn, stpAdjOut, ownerGivesAdj); err != nil {
					return false
				}
			} else {
				if err := s.consumeOffer(sb, clobOffer, stpAdjIn, ofrAdjIn, stpAdjOut, ownerGivesAdj); err != nil {
					return false
				}
			}
		}

		s.offersUsed_++
		return true
	}

	// tryAMM for Fwd
	tryAMMFwd := func(lobQuality *Quality) bool {
		if ammProcessed || s.ammLiquidity == nil {
			return true
		}
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
		return execOfferFwd(ofrIn, ofrOut, offerQ, trIn, trOut, true, true,
			ammOffer, nil, [32]byte{})
	}

	// Main CLOB iteration with AMM interleaving
	firstCLOB := true
	for s.offersUsed_ < s.maxOffersToConsume && !remainingIn.IsZero() {
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
				s.offersUsed_++
				continue
			}
		}

		// Reference: rippled OfferStream::step() reads ownerFunds from view_ (sb).
		ownerFunds := s.getOfferFundedAmount(sb, offer)
		if ownerFunds.IsEffectivelyZero() || offer.TakerGets.IsZero() {
			ofrsToRm[offerKey] = true
			s.offersUsed_++
			continue
		}
		if s.shouldRmSmallIncreasedQOffer(sb, offer, ownerFunds) {
			ofrsToRm[offerKey] = true
			s.offersUsed_++
			continue
		}

		if firstCLOB {
			firstCLOB = false
			lobQ := s.offerQuality(offer)
			if !tryAMMFwd(&lobQ) {
				break
			}
		}

		offerOwner, _ := state.DecodeAccountID(offer.Account)
		ofrTrIn := s.getOfrInRate(offerOwner, trIn)
		ofrTrOut := s.getOfrOutRate(offerOwner, trOut)
		ofrIn := s.offerTakerPays(offer)
		ofrOut := s.offerTakerGets(offer)
		offerQ := s.offerQuality(offer)
		if !execOfferFwd(ofrIn, ofrOut, offerQ, ofrTrIn, ofrTrOut, false, false,
			nil, offer, offerKey) {
			break
		}
	}

	if firstCLOB {
		tryAMMFwd(nil)
	}

	if s.offersUsed_ >= s.maxOffersToConsume {
		s.inactive_ = true
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

// QualityUpperBound returns the worst-case quality for this step
func (s *BookStep) QualityUpperBound(v *PaymentSandbox, prevStepDir DebtDirection) (*Quality, DebtDirection) {
	// Get the tip of the order book
	tipQuality := s.getTipQuality(v)
	if tipQuality == nil {
		return nil, s.DebtDirection(v, StrandDirectionForward)
	}
	return tipQuality, s.DebtDirection(v, StrandDirectionForward)
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
	return s.inactive_
}

// OffersUsed returns the number of offers consumed
func (s *BookStep) OffersUsed() uint32 {
	return s.offersUsed_
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

// getTipQuality gets the best quality available, considering both CLOB and AMM offers.
// Reference: rippled BookStep.cpp tip() returns the better of CLOB tip and AMM offer quality.
func (s *BookStep) getTipQuality(sb *PaymentSandbox) *Quality {
	lobQuality := s.getCLOBTipQuality(sb)

	// If we have AMM liquidity, check if AMM quality is better
	if s.ammLiquidity != nil {
		ammOffer := s.getAMMOffer(sb, nil)
		if ammOffer != nil {
			ammQ := ammOffer.Quality()
			if lobQuality == nil || ammQ.BetterThan(*lobQuality) {
				return &ammQ
			}
		}
	}

	return lobQuality
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

	// Transfer input: book.in.account → AMM account
	inAmount := eitherToAmount(consumedInNet)
	if err := ammOffer.Send(sb, s.book.In.Issuer, ammOffer.Owner(), inAmount); err != nil {
		return err
	}

	// Transfer output: AMM account → book.out.account
	outAmount := eitherToAmount(ownerGives)
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
			for _, authAcct := range ammEntry.AuctionSlot.AuthAccounts {
				if authAcct == account {
					return ammEntry.AuctionSlot.DiscountedFee
				}
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
