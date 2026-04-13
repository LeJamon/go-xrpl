package payment

import (
	"bytes"
	"errors"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/amm"
	"github.com/LeJamon/goXRPLd/internal/tx/permissioneddomain"
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

// GetQualityFunc returns the QualityFunction for this step.
// For BookStep, this examines whether the tip offer is CLOB or AMM and returns
// the appropriate QualityFunction, adjusted for transfer fees.
// Reference: rippled BookStep.cpp getQualityFunc() lines 608-648
func (s *BookStep) GetQualityFunc(v *PaymentSandbox, prevStepDir DebtDirection) (*QualityFunction, DebtDirection) {
	dir := s.DebtDirection(v, StrandDirectionForward)

	res := s.tipOfferQualityF(v)
	if res == nil {
		return nil, dir
	}

	// AMM (non-constant quality function)
	if !res.IsConst() {
		// Check if transfer fees need to be composed in.
		// For payments: adjustQualityWithFees with WaiveTransferFee::Yes and qOne
		// Reference: rippled BookStep.cpp lines 620-636
		qOne := qualityFromFloat64(1.0)
		q := s.adjustQualityWithFeesForQF(v, qOne, prevStepDir, true)
		if q.Value == qOne.Value {
			// No fee adjustment needed
			return res, dir
		}
		// Compose fee QF with AMM QF
		feeQF := NewCLOBLikeQualityFunction(q)
		if feeQF == nil {
			return res, dir
		}
		feeQF.Combine(*res)
		return feeQF, dir
	}

	// CLOB (constant quality function)
	// Reference: rippled BookStep.cpp lines 639-647
	q := s.adjustQualityWithFeesForQF(v, *res.quality, prevStepDir, false)
	return NewCLOBLikeQualityFunction(q), dir
}

// tipOfferQualityF returns the QualityFunction for the tip (best) offer,
// choosing between CLOB and AMM. Returns nil if no offer exists.
//
// For CLOB offers: returns a CLOB-like QF with the tip quality.
// For AMM offers: returns the AMM's QualityFunction (may be non-constant
// for single-path or constant for multi-path).
//
// Reference: rippled BookStep.cpp tipOfferQualityF() lines 990-1000
func (s *BookStep) tipOfferQualityF(sb *PaymentSandbox) *QualityFunction {
	// Call tip() equivalent: determine if CLOB or AMM is the tip
	// Reference: rippled BookStep.cpp tip() lines 938-974
	lobQuality := s.getCLOBTipQuality(sb)

	if s.ammLiquidity != nil {
		// With fixAMMv1_1, pass a quality threshold to getAMMOffer so the AMM
		// doesn't generate tiny offers when its quality barely exceeds CLOB.
		// This prevents the payment engine from going into many iterations.
		// Reference: rippled BookStep.cpp tip() lines 962-967
		var qualityThreshold *Quality
		if s.ammLiquidity.fixAMMv1_1 && lobQuality != nil {
			qualityThreshold = s.tipQualityThreshold(*lobQuality)
		}

		ammOffer := s.getAMMOffer(sb, qualityThreshold)
		if ammOffer != nil {
			ammQ := ammOffer.Quality()
			if lobQuality == nil || ammQ.BetterThan(*lobQuality) {
				// AMM is tip. Return AMM's quality function.
				// Reference: rippled AMMOffer.cpp getQualityFunc()
				return s.ammOfferGetQualityFunc(ammOffer)
			}
		}
	}

	// CLOB is tip (or no offers at all)
	if lobQuality == nil {
		return nil
	}
	return NewCLOBLikeQualityFunction(*lobQuality)
}

// tipQualityThreshold returns the quality threshold to use for AMM offer
// generation in tipOfferQualityF. For offer crossing, if the taker's quality
// limit is better than the CLOB tip, don't use a threshold (let AMM generate
// max offer). Otherwise use the CLOB quality as threshold.
// For payments, always use the CLOB quality.
// Reference: rippled BookOfferCrossingStep::qualityThreshold() lines 479-486
// Reference: rippled BookPaymentStep::qualityThreshold() line 305
func (s *BookStep) tipQualityThreshold(lobQuality Quality) *Quality {
	// For offer crossing with AMM in single-path mode:
	// if qualityLimit is strictly better than lobQuality, return nil
	// so AMM generates its max offer (limitOut handles the quality cap)
	if s.qualityLimit != nil && s.ammLiquidity != nil &&
		!s.ammLiquidity.ammContext.MultiPath() &&
		s.qualityLimit.BetterThan(lobQuality) {
		return nil
	}
	q := lobQuality
	return &q
}

// ammOfferGetQualityFunc returns the QualityFunction for an AMM offer.
// Multi-path: returns CLOB-like QF (constant quality).
// Single-path: returns AMM QF (non-constant, slope-based).
// Reference: rippled AMMOffer.cpp getQualityFunc() lines 130-137
func (s *BookStep) ammOfferGetQualityFunc(offer *AMMOffer) *QualityFunction {
	if offer.ammLiquidity.ammContext.MultiPath() {
		return NewCLOBLikeQualityFunction(offer.Quality())
	}
	return NewAMMQualityFunction(offer.balanceIn, offer.balanceOut, offer.ammLiquidity.tradingFee)
}

// adjustQualityWithFeesForQF adjusts a quality with transfer fees for the
// QualityFunction calculation. This implements the payment variant of
// adjustQualityWithFees.
//
// For payments: charges transfer fee based on prevStepDir and ownerPaysTransferFee.
// waiveOutFee=true waives the output transfer fee (used for AMM offers).
//
// Reference: rippled BookPaymentStep::adjustQualityWithFees() lines 328-359
func (s *BookStep) adjustQualityWithFeesForQF(v *PaymentSandbox, ofrQ Quality, prevStepDir DebtDirection, waiveOutFee bool) Quality {
	// trIn: charge transfer fee when previous step redeems
	trIn := QualityOne
	if Redeems(prevStepDir) && !s.book.In.IsXRP() {
		trIn = s.GetAccountTransferRate(v, s.book.In.Issuer)
		// If issuer == strandDst, no fee (parityRate)
		if s.book.In.Issuer == s.strandDst {
			trIn = QualityOne
		}
	}

	// trOut: charge transfer fee only if ownerPaysTransferFee and fee is not waived
	trOut := QualityOne
	if s.ownerPaysTransferFee && !waiveOutFee && !s.book.Out.IsXRP() {
		trOut = s.GetAccountTransferRate(v, s.book.Out.Issuer)
		if s.book.Out.Issuer == s.strandDst {
			trOut = QualityOne
		}
	}

	// q1 = getRate(STAmount(trOut), STAmount(trIn)) = trIn / trOut
	trOutAmt := NewIOUEitherAmount(state.NewIssuedAmountFromValue(int64(trOut), 0, "", ""))
	trInAmt := NewIOUEitherAmount(state.NewIssuedAmountFromValue(int64(trIn), 0, "", ""))
	q1 := QualityFromAmounts(trInAmt, trOutAmt)

	return q1.Compose(ofrQ)
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

// transferRateIn returns the transfer rate for incoming currency.
// No fee when: XRP, issuer is strandDst, or previous step issues.
// Reference: rippled BookStep.cpp forEachOffer() rate lambda (lines 728-731) + trIn (line 734-735)
func (s *BookStep) transferRateIn(sb *PaymentSandbox, prevStepDir DebtDirection) uint32 {
	if s.book.In.IsXRP() || s.book.In.Issuer == s.strandDst {
		return QualityOne
	}

	// Only charge transfer fee when previous step redeems
	if !Redeems(prevStepDir) {
		return QualityOne
	}

	return s.GetAccountTransferRate(sb, s.book.In.Issuer)
}

// transferRateOut returns the transfer rate for outgoing currency.
// No fee when: XRP, issuer is strandDst, or ownerPaysTransferFee is false.
// Reference: rippled BookStep.cpp forEachOffer() rate lambda (lines 728-731) + trOut (line 737-738)
func (s *BookStep) transferRateOut(sb *PaymentSandbox) uint32 {
	if s.book.Out.IsXRP() || s.book.Out.Issuer == s.strandDst {
		return QualityOne
	}

	if !s.ownerPaysTransferFee {
		return QualityOne
	}

	return s.GetAccountTransferRate(sb, s.book.Out.Issuer)
}

// getOfrInRate returns the per-offer input transfer rate.
// In offer crossing mode, exempts transfer fee when offer owner == strand source
// (i.e., the taker is crossing their own offer from the input side).
// Reference: rippled BookOfferCrossingStep::getOfrInRate() (BookStep.cpp lines 491-502)
func (s *BookStep) getOfrInRate(offerOwner [20]byte, trIn uint32) uint32 {
	if !s.ownerPaysTransferFee {
		return trIn // Payment mode — no exemption
	}
	// Offer crossing: check if offer owner == previous DirectStep's source
	if directStep, ok := s.prevStep.(*DirectStepI); ok {
		if offerOwner == directStep.src {
			return QualityOne // Self-pay exemption
		}
	}
	return trIn
}

// getOfrOutRate returns the per-offer output transfer rate.
// In offer crossing mode, exempts transfer fee when offer owner == strand destination
// AND the previous step is a BookStep (i.e., bridged crossing, second leg).
// Reference: rippled BookOfferCrossingStep::getOfrOutRate() (BookStep.cpp lines 506-517)
func (s *BookStep) getOfrOutRate(offerOwner [20]byte, trOut uint32) uint32 {
	if !s.ownerPaysTransferFee {
		return trOut // Payment mode — no exemption
	}
	// Offer crossing: check if previous step is BookStep AND owner == strandDst
	if _, ok := s.prevStep.(*BookStep); ok {
		if offerOwner == s.strandDst {
			return QualityOne // Self-pay exemption
		}
	}
	return trOut
}

// GetAccountTransferRate gets the transfer rate from an account
func (s *BookStep) GetAccountTransferRate(sb *PaymentSandbox, issuer [20]byte) uint32 {
	accountKey := keylet.Account(issuer)
	data, err := sb.Read(accountKey)
	if err != nil || data == nil {
		return QualityOne
	}

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return QualityOne
	}

	if account.TransferRate == 0 {
		return QualityOne
	}
	return account.TransferRate
}

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

// offerTakerGets returns what the taker gets from this offer
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

// offerQuality returns the quality of an offer by extracting it from the BookDirectory key.
// The quality is stored in the last 8 bytes of the BookDirectory, encoded as big-endian uint64.
// This is the original quality set when the offer was created, which remains constant
// even as the offer is partially filled.
// Reference: rippled's getQuality() in Indexes.cpp
func (s *BookStep) offerQuality(offer *state.LedgerOffer) Quality {
	// Compute quality from actual TakerPays/TakerGets for precision.
	// The BookDirectory quality is a "price tier" for ordering, but for
	// accurate calculations we need the exact ratio from the offer amounts.
	// Reference: rippled calculates quality as in/out for flow calculations
	takerPays := s.offerTakerPays(offer)
	takerGets := s.offerTakerGets(offer)
	return QualityFromAmounts(takerPays, takerGets)
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
		offer.PreviousTxnID = txHash
		offer.PreviousTxnLgrSeq = ledgerSeq
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

// adjustOwnerCount adjusts the OwnerCount on an account.
// Also records the change via AdjustOwnerCount for the PaymentSandbox's
// OwnerCountHook, which returns the maximum count seen.
// Reference: rippled View.cpp adjustOwnerCount() calls adjustOwnerCountHook()
func (s *BookStep) adjustOwnerCount(sb *PaymentSandbox, account [20]byte, delta int, txHash [32]byte, ledgerSeq uint32) error {
	// Read the current owner count BEFORE modifying so we can record it.
	accountKey := keylet.Account(account)
	data, err := sb.Read(accountKey)
	if err != nil || data == nil {
		return nil
	}
	acct, err := state.ParseAccountRoot(data)
	if err != nil {
		return err
	}
	curOC := acct.OwnerCount
	newOC := int(curOC) + delta
	if newOC < 0 {
		newOC = 0
	}

	// Record via AdjustOwnerCount hook so OwnerCountHook returns the maximum.
	sb.AdjustOwnerCount(account, curOC, uint32(newOC))

	// Perform the actual modification.
	return tx.AdjustOwnerCountWithTx(sb, account, delta, txHash, ledgerSeq)
}

// transferFunds transfers an amount between two accounts.
func (s *BookStep) transferFunds(sb *PaymentSandbox, from, to [20]byte, amount EitherAmount, issue Issue) error {
	if from == to {
		return nil
	}

	if amount.IsZero() {
		return nil
	}

	txHash, ledgerSeq := sb.GetTransactionContext()

	if issue.IsXRP() {
		return s.transferXRP(sb, from, to, amount.XRP, txHash, ledgerSeq)
	}

	return s.transferIOU(sb, from, to, amount.IOU, issue, txHash, ledgerSeq)
}

// transferFundsWithFee transfers an IOU amount with transfer fee handling.
// grossAmount is debited from sender, netAmount is credited to receiver.
// This handles the XRPL transfer fee mechanism where sender pays more than receiver gets.
func (s *BookStep) transferFundsWithFee(sb *PaymentSandbox, from, to [20]byte, grossAmount, netAmount EitherAmount, issue Issue) error {
	if from == to {
		return nil
	}

	if grossAmount.IsZero() || netAmount.IsZero() {
		return nil
	}

	txHash, ledgerSeq := sb.GetTransactionContext()

	// For XRP, there's no transfer fee - just use regular transfer
	if issue.IsXRP() {
		return s.transferXRP(sb, from, to, grossAmount.XRP, txHash, ledgerSeq)
	}

	// For IOUs: debit sender by gross, credit receiver by net
	issuer := issue.Issuer

	// Special case: if from is issuer, just credit receiver
	if from == issuer {
		return s.creditTrustline(sb, to, issuer, netAmount.IOU, txHash, ledgerSeq)
	}
	// Special case: if to is issuer, just debit sender
	if to == issuer {
		return s.debitTrustline(sb, from, issuer, grossAmount.IOU, txHash, ledgerSeq)
	}

	// Normal case: debit sender by gross, credit receiver by net
	if err := s.debitTrustline(sb, from, issuer, grossAmount.IOU, txHash, ledgerSeq); err != nil {
		return err
	}
	return s.creditTrustline(sb, to, issuer, netAmount.IOU, txHash, ledgerSeq)
}

// transferXRP transfers XRP between accounts.
// When from or to is the XRP pseudo-account (zero), that side is skipped.
// The XRPEndpointStep handles the actual source/destination account balance changes.
// Reference: rippled View.cpp accountSend() lines 1904-1939
func (s *BookStep) transferXRP(sb *PaymentSandbox, from, to [20]byte, drops int64, txHash [32]byte, ledgerSeq uint32) error {
	var xrpAccount [20]byte
	amount := tx.NewXRPAmount(drops)

	// Debit sender (skip if XRP pseudo-account)
	if from != xrpAccount {
		fromKey := keylet.Account(from)
		fromData, err := sb.Read(fromKey)
		if err != nil {
			return err
		}
		if fromData == nil {
			return errors.New("sender account not found")
		}

		fromAccount, err := state.ParseAccountRoot(fromData)
		if err != nil {
			return err
		}

		if int64(fromAccount.Balance) < drops {
			return errors.New("insufficient XRP balance")
		}

		// Record the credit via CreditHook BEFORE updating balance
		preCreditBalance := tx.NewXRPAmount(int64(fromAccount.Balance))
		sb.CreditHook(from, xrpAccount, amount, preCreditBalance)

		fromAccount.Balance -= uint64(drops)
		fromAccount.PreviousTxnID = txHash
		fromAccount.PreviousTxnLgrSeq = ledgerSeq

		fromAccountData, err := state.SerializeAccountRoot(fromAccount)
		if err != nil {
			return err
		}
		if err := sb.Update(fromKey, fromAccountData); err != nil {
			return err
		}
	}

	// Credit receiver (skip if XRP pseudo-account)
	if to != xrpAccount {
		toKey := keylet.Account(to)
		toData, err := sb.Read(toKey)
		if err != nil {
			return err
		}
		if toData == nil {
			return errors.New("receiver account not found")
		}

		toAccount, err := state.ParseAccountRoot(toData)
		if err != nil {
			return err
		}

		// Record the credit to receiver
		receiverPreBalance := tx.NewXRPAmount(-int64(toAccount.Balance))
		sb.CreditHook(xrpAccount, to, amount, receiverPreBalance)

		toAccount.Balance += uint64(drops)
		toAccount.PreviousTxnID = txHash
		toAccount.PreviousTxnLgrSeq = ledgerSeq

		toAccountData, err := state.SerializeAccountRoot(toAccount)
		if err != nil {
			return err
		}
		if err := sb.Update(toKey, toAccountData); err != nil {
			return err
		}
	}

	return nil
}

// transferIOU transfers IOU between accounts via trustline
func (s *BookStep) transferIOU(sb *PaymentSandbox, from, to [20]byte, amount tx.Amount, issue Issue, txHash [32]byte, ledgerSeq uint32) error {
	issuer := issue.Issuer

	if from == issuer {
		return s.creditTrustline(sb, to, issuer, amount, txHash, ledgerSeq)
	}
	if to == issuer {
		return s.debitTrustline(sb, from, issuer, amount, txHash, ledgerSeq)
	}

	if err := s.debitTrustline(sb, from, issuer, amount, txHash, ledgerSeq); err != nil {
		return err
	}
	return s.creditTrustline(sb, to, issuer, amount, txHash, ledgerSeq)
}

// creditTrustline increases an account's IOU balance.
// If the trust line doesn't exist (e.g., during offer crossing), creates one automatically.
// Reference: rippled View.cpp rippleCredit() → trustCreate()
func (s *BookStep) creditTrustline(sb *PaymentSandbox, account, issuer [20]byte, amount tx.Amount, txHash [32]byte, ledgerSeq uint32) error {
	lineKey := keylet.Line(account, issuer, amount.Currency)
	lineData, err := sb.Read(lineKey)
	if err != nil {
		return err
	}
	if lineData == nil {
		// Trust line doesn't exist — create one (offer crossing creates trust lines on demand).
		// Reference: rippled rippleCredit() → trustCreate() in View.cpp
		return s.trustCreateForCredit(sb, account, issuer, amount, txHash, ledgerSeq)
	}

	rs, err := state.ParseRippleState(lineData)
	if err != nil {
		return err
	}

	accountIsLow := state.CompareAccountIDsForLine(account, issuer) < 0

	// Compute sender's (issuer's) balance BEFORE update, from issuer's perspective.
	// The sender here is 'issuer' (crediting account means issuer sends to account).
	// Reference: rippled rippleCreditIOU() line 1672-1675
	issuerIsLow := !accountIsLow
	var preCreditIssuerBalance tx.Amount
	if issuerIsLow {
		preCreditIssuerBalance = rs.Balance
	} else {
		preCreditIssuerBalance = rs.Balance.Negate()
	}
	// Record deferred credit: issuer (sender) → account (receiver).
	// Reference: rippled View.cpp rippleCreditIOU() line 1675:
	//   view.creditHook(uSenderID, uReceiverID, saAmount, saBalance)
	sb.CreditHook(issuer, account, amount, preCreditIssuerBalance)

	if accountIsLow {
		rs.Balance, _ = rs.Balance.Add(amount)
	} else {
		rs.Balance, _ = rs.Balance.Sub(amount)
	}

	rs.PreviousTxnID = txHash
	rs.PreviousTxnLgrSeq = ledgerSeq

	lineDataNew, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}
	return sb.Update(lineKey, lineDataNew)
}

// trustCreateForCredit creates a new trust line between account and issuer with initial balance.
// This is used when creditTrustline encounters a missing trust line during offer crossing.
// Reference: rippled View.cpp trustCreate() lines 1329-1445
func (s *BookStep) trustCreateForCredit(sb *PaymentSandbox, account, issuer [20]byte, amount tx.Amount, txHash [32]byte, ledgerSeq uint32) error {
	// Determine low and high accounts
	accountIsLow := state.CompareAccountIDsForLine(account, issuer) < 0
	var lowAccountID, highAccountID [20]byte
	if accountIsLow {
		lowAccountID = account
		highAccountID = issuer
	} else {
		lowAccountID = issuer
		highAccountID = account
	}

	lowAccountStr := state.EncodeAccountIDSafe(lowAccountID)
	highAccountStr := state.EncodeAccountIDSafe(highAccountID)

	// Calculate the initial balance from low account's perspective
	// The issuer sends to account:
	// - If account is LOW: issuer (HIGH) pays account (LOW) → balance increases (positive)
	// - If account is HIGH: issuer (LOW) pays account (HIGH) → balance decreases (negative)
	var balance tx.Amount
	if accountIsLow {
		balance = amount // account is low, receives credit → positive balance
	} else {
		balance = amount.Negate() // account is high, receives credit → negative balance
	}

	// Check receiver account's DefaultRipple flag for NoRipple setting
	var noRipple bool
	accountKey := keylet.Account(account)
	accountData, err := sb.Read(accountKey)
	if err == nil && accountData != nil {
		acct, parseErr := state.ParseAccountRoot(accountData)
		if parseErr == nil {
			const lsfDefaultRipple = 0x00800000
			noRipple = (acct.Flags & lsfDefaultRipple) == 0
		}
	}

	// Build the trust line flags — set reserve flag for the receiver (account) side
	var flags uint32
	if accountIsLow {
		// account is LOW
		if noRipple {
			flags |= state.LsfLowNoRipple
		}
		flags |= state.LsfLowReserve
	} else {
		// account is HIGH
		if noRipple {
			flags |= state.LsfHighNoRipple
		}
		flags |= state.LsfHighReserve
	}

	// Create the RippleState
	rs := &state.RippleState{
		Balance:           tx.NewIssuedAmount(balance.IOU().Mantissa(), balance.IOU().Exponent(), amount.Currency, state.AccountOneAddress),
		LowLimit:          tx.NewIssuedAmount(0, -100, amount.Currency, lowAccountStr),
		HighLimit:         tx.NewIssuedAmount(0, -100, amount.Currency, highAccountStr),
		Flags:             flags,
		LowNode:           0,
		HighNode:          0,
		PreviousTxnID:     txHash,
		PreviousTxnLgrSeq: ledgerSeq,
	}

	lineKey := keylet.Line(account, issuer, amount.Currency)

	// Insert into LOW account's owner directory
	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDirResult, err := state.DirInsert(sb, lowDirKey, lineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return err
	}

	// Insert into HIGH account's owner directory
	highDirKey := keylet.OwnerDir(highAccountID)
	highDirResult, err := state.DirInsert(sb, highDirKey, lineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return err
	}

	// Set directory node hints
	rs.LowNode = lowDirResult.Page
	rs.HighNode = highDirResult.Page

	// Serialize and insert
	lineData, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	if err := sb.Insert(lineKey, lineData); err != nil {
		return err
	}

	// Increment receiver's OwnerCount
	return s.adjustOwnerCountForTrustCreate(sb, account, 1, txHash, ledgerSeq)
}

// adjustOwnerCountForTrustCreate modifies an account's OwnerCount by delta during trust line creation.
func (s *BookStep) adjustOwnerCountForTrustCreate(sb *PaymentSandbox, account [20]byte, delta int32, txHash [32]byte, ledgerSeq uint32) error {
	// Read current owner count and record via hook before modifying.
	accountKey := keylet.Account(account)
	data, err := sb.Read(accountKey)
	if err == nil && data != nil {
		if acct, pErr := state.ParseAccountRoot(data); pErr == nil {
			curOC := acct.OwnerCount
			newOC := int(curOC) + int(delta)
			if newOC < 0 {
				newOC = 0
			}
			sb.AdjustOwnerCount(account, curOC, uint32(newOC))
		}
	}
	return tx.AdjustOwnerCountWithTx(sb, account, int(delta), txHash, ledgerSeq)
}

// debitTrustline decreases an account's IOU balance.
// After updating, checks if the trust line should be deleted (zero balance, auto-created).
// Reference: rippled View.cpp rippleCreditIOU() lines 1688-1745
func (s *BookStep) debitTrustline(sb *PaymentSandbox, account, issuer [20]byte, amount tx.Amount, txHash [32]byte, ledgerSeq uint32) error {
	lineKey := keylet.Line(account, issuer, amount.Currency)
	lineData, err := sb.Read(lineKey)
	if err != nil {
		return err
	}
	if lineData == nil {
		return errors.New("trustline not found for debit")
	}

	rs, err := state.ParseRippleState(lineData)
	if err != nil {
		return err
	}

	// The "sender" is account (their balance decreases)
	accountIsLow := state.CompareAccountIDsForLine(account, issuer) < 0

	// Compute sender's (account's) balance BEFORE update (from sender's perspective)
	var saBefore tx.Amount
	if accountIsLow {
		saBefore = rs.Balance
	} else {
		saBefore = rs.Balance.Negate()
	}

	// Record deferred credit: account (sender) → issuer (receiver).
	// Reference: rippled View.cpp rippleCreditIOU() line 1675:
	//   view.creditHook(uSenderID, uReceiverID, saAmount, saBalance)
	sb.CreditHook(account, issuer, amount, saBefore)

	// Update balance
	if accountIsLow {
		rs.Balance, _ = rs.Balance.Sub(amount)
	} else {
		rs.Balance, _ = rs.Balance.Add(amount)
	}

	// Compute sender's balance AFTER update
	var saBalance tx.Amount
	if accountIsLow {
		saBalance = rs.Balance
	} else {
		saBalance = rs.Balance.Negate()
	}

	// Check trust line deletion conditions
	// Reference: rippled rippleCreditIOU() lines 1688-1745
	bDelete := false
	uFlags := rs.Flags

	if saBefore.Signum() > 0 && saBalance.Signum() <= 0 {
		var senderReserve, senderNoRipple, senderFreeze uint32
		var senderLimit tx.Amount
		var senderQualityIn, senderQualityOut uint32

		if accountIsLow {
			senderReserve = state.LsfLowReserve
			senderNoRipple = state.LsfLowNoRipple
			senderFreeze = state.LsfLowFreeze
			senderLimit = rs.LowLimit
			senderQualityIn = rs.LowQualityIn
			senderQualityOut = rs.LowQualityOut
		} else {
			senderReserve = state.LsfHighReserve
			senderNoRipple = state.LsfHighNoRipple
			senderFreeze = state.LsfHighFreeze
			senderLimit = rs.HighLimit
			senderQualityIn = rs.HighQualityIn
			senderQualityOut = rs.HighQualityOut
		}

		// Read sender's DefaultRipple flag
		senderDefaultRipple := false
		senderKey := keylet.Account(account)
		senderData, sErr := sb.Read(senderKey)
		if sErr == nil && senderData != nil {
			senderAcct, pErr := state.ParseAccountRoot(senderData)
			if pErr == nil {
				senderDefaultRipple = (senderAcct.Flags & state.LsfDefaultRipple) != 0
			}
		}

		hasNoRipple := (uFlags & senderNoRipple) != 0
		noRippleMatchesDefault := hasNoRipple != senderDefaultRipple

		if (uFlags&senderReserve) != 0 &&
			noRippleMatchesDefault &&
			(uFlags&senderFreeze) == 0 &&
			senderLimit.Signum() == 0 &&
			senderQualityIn == 0 &&
			senderQualityOut == 0 {
			// Clear sender's reserve flag and decrement OwnerCount
			rs.Flags &= ^senderReserve
			s.adjustOwnerCount(sb, account, -1, txHash, ledgerSeq)

			// Check final deletion condition
			var receiverReserve uint32
			if accountIsLow {
				receiverReserve = state.LsfHighReserve
			} else {
				receiverReserve = state.LsfLowReserve
			}
			bDelete = saBalance.Signum() == 0 && (uFlags&receiverReserve) == 0
		}
	}

	rs.PreviousTxnID = txHash
	rs.PreviousTxnLgrSeq = ledgerSeq

	lineDataNew, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	if bDelete {
		// Update first (for metadata), then delete
		sb.Update(lineKey, lineDataNew)

		var lowAccount, highAccount [20]byte
		if accountIsLow {
			lowAccount = account
			highAccount = issuer
		} else {
			lowAccount = issuer
			highAccount = account
		}
		return trustDeleteLine(sb, lineKey, rs, lowAccount, highAccount)
	}

	return sb.Update(lineKey, lineDataNew)
}

// subtractFromAmount subtracts consumed from the original amount
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
