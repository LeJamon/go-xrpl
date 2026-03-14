package pathfinder

import (
	"sort"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
)

// ComputePathRanks evaluates all discovered paths and ranks them by quality,
// liquidity, and length. Subtracts default path liquidity first.
// Reference: rippled Pathfinder::computePathRanks()
func (pf *Pathfinder) ComputePathRanks(maxPaths int) {
	pf.pathRanks = nil

	// Reference: rippled mRemainingAmount = convertAmount(mDstAmount, convert_all_)
	// For convert_all_ (partial payments), use the largest possible amount
	// to find maximum liquidity. Otherwise use the exact destination amount.
	convertedAmount := pf.dstAmount
	if pf.convertAll {
		convertedAmount = largestAmount(pf.dstAmount)
	}

	// Try default path first to see how much it can deliver
	// (empty paths = default path only)
	_, defaultOut, _, _, defaultResult := payment.RippleCalculate(
		pf.ledger,
		pf.srcAccount, pf.dstAccount,
		convertedAmount,
		&pf.srcAmount,
		nil,  // no explicit paths
		true, // add default path
		true, // partial payment allowed (to measure liquidity)
		false,
		[32]byte{}, 0,
	)

	// Calculate remaining amount needed after default path
	pf.remainingAmount = payment.ToEitherAmount(convertedAmount)
	if defaultResult == tx.TesSUCCESS {
		pf.remainingAmount = pf.remainingAmount.Sub(defaultOut)
		if pf.remainingAmount.IsNegative() || pf.remainingAmount.IsZero() {
			// Default path handles everything — no need for explicit paths
			// Still rank them for informational purposes
		}
	}

	pf.rankPaths(maxPaths, pf.completePaths, pf.remainingAmount)
}

// rankPaths evaluates each path and builds the ranked list.
// Reference: rippled Pathfinder::rankPaths()
func (pf *Pathfinder) rankPaths(maxPaths int, paths [][]payment.PathStep, remainingAmount payment.EitherAmount) {
	// Minimum useful amount:
	// - For convert_all_: use largestAmount to find highest liquidity
	// - Otherwise: dstAmount / (maxPaths + 2)
	// Reference: rippled saMinDstAmount in rankPaths()
	var minAmount tx.Amount
	if pf.convertAll {
		minAmount = largestAmount(pf.dstAmount)
	} else {
		minAmount = pf.dstAmount
		divisor := int64(maxPaths + 2)
		if minAmount.IsNative() {
			drops := minAmount.Drops()
			if drops > 0 {
				minAmount = state.NewXRPAmountFromInt(drops / divisor)
			}
		} else {
			f := minAmount.Float64()
			if f > 0 {
				minAmount = state.NewIssuedAmountFromFloat64(f/float64(divisor), minAmount.Currency, minAmount.Issuer)
			}
		}
	}

	for i, path := range paths {
		if len(path) == 0 {
			continue
		}

		liquidity, quality, ok := pf.getPathLiquidity(path, minAmount)
		if !ok {
			continue
		}

		pf.pathRanks = append(pf.pathRanks, PathRank{
			Quality:   quality,
			Length:    len(path),
			Liquidity: liquidity,
			Index:     i,
		})
	}

	// Sort: quality ascending (lower=better), then liquidity descending,
	// then length ascending, then index descending (tiebreaker)
	sort.Slice(pf.pathRanks, func(i, j int) bool {
		ri, rj := pf.pathRanks[i], pf.pathRanks[j]
		if !pf.convertAll && ri.Quality != rj.Quality {
			return ri.Quality < rj.Quality
		}
		cmp := ri.Liquidity.Compare(rj.Liquidity)
		if cmp != 0 {
			return cmp > 0 // Higher liquidity is better
		}
		if ri.Length != rj.Length {
			return ri.Length < rj.Length
		}
		return ri.Index > rj.Index // Higher index breaks ties
	})
}

// getPathLiquidity tests a path via RippleCalculate to determine its quality
// and liquidity.
// Reference: rippled Pathfinder::getPathLiquidity()
func (pf *Pathfinder) getPathLiquidity(path []payment.PathStep, minAmount tx.Amount) (payment.EitherAmount, uint64, bool) {
	// Wrap path in a path set
	paths := [][]payment.PathStep{path}

	// First pass: test with minimum amount
	// For convertAll, minAmount is already set to largestAmount by rankPaths
	testAmount := minAmount

	actualIn, actualOut, _, _, result := payment.RippleCalculate(
		pf.ledger,
		pf.srcAccount, pf.dstAccount,
		testAmount,
		&pf.srcAmount,
		paths,
		false, // no default path
		pf.convertAll,
		false,
		[32]byte{}, 0,
	)

	if result != tx.TesSUCCESS {
		return payment.EitherAmount{}, 0, false
	}

	// Calculate quality from actual amounts
	// Reference: rippled getRate(actualAmountOut, actualAmountIn)
	quality := computeQuality(actualOut, actualIn)
	totalLiquidity := actualOut

	// Second pass: test for full remaining liquidity (unless convertAll)
	if !pf.convertAll {
		remaining := payment.ToEitherAmount(pf.dstAmount).Sub(actualOut)
		if !remaining.IsZero() && !remaining.IsNegative() {
			remainingAmt := payment.FromEitherAmount(remaining)
			_, extraOut, _, _, extraResult := payment.RippleCalculate(
				pf.ledger,
				pf.srcAccount, pf.dstAccount,
				remainingAmt,
				&pf.srcAmount,
				paths,
				false,
				true, // partial payment to measure total liquidity
				false,
				[32]byte{}, 0,
			)
			if extraResult == tx.TesSUCCESS {
				totalLiquidity = totalLiquidity.Add(extraOut)
			}
		}
	}

	return totalLiquidity, quality, true
}

// computeQuality calculates the quality ratio as out/in encoded as uint64.
// Lower values represent better quality.
// Reference: rippled getRate(actualAmountOut, actualAmountIn)
func computeQuality(out, in payment.EitherAmount) uint64 {
	outAmt := payment.FromEitherAmount(out)
	inAmt := payment.FromEitherAmount(in)
	return state.GetRate(outAmt, inAmt)
}

// largestAmount returns the largest possible amount for the given currency.
// For XRP, returns the initial supply (100 billion XRP).
// For IOUs, returns the maximum representable amount.
// Reference: rippled largestAmount() in PathfinderUtils.h
func largestAmount(amt tx.Amount) tx.Amount {
	if amt.IsNative() {
		// INITIAL_XRP = 100 billion XRP = 100,000,000,000 * 1,000,000 drops
		return state.NewXRPAmountFromInt(100_000_000_000_000_000)
	}
	// Maximum IOU amount: 9999999999999999e80
	return state.NewIssuedAmountFromFloat64(9999999999999999e80, amt.Currency, amt.Issuer)
}

// GetBestPaths selects the best paths from the ranked list.
// Returns up to maxPaths paths, plus optionally a fullLiquidityPath that
// can cover the entire payment by itself.
// Reference: rippled Pathfinder::getBestPaths()
func (pf *Pathfinder) GetBestPaths(maxPaths int, extraPaths [][]payment.PathStep, srcIssuer [20]byte) (bestPaths [][]payment.PathStep, fullLiquidityPath []payment.PathStep) {
	issuerIsSender := (pf.srcCurrency == "XRP" || pf.srcCurrency == "") || srcIssuer == pf.srcAccount

	// Rank extra paths
	var extraRanks []PathRank
	if len(extraPaths) > 0 {
		// Create a temporary pathfinder-like ranking
		for i, path := range extraPaths {
			if len(path) == 0 {
				continue
			}
			var minAmount tx.Amount
			if pf.convertAll {
				minAmount = largestAmount(pf.dstAmount)
			} else {
				minAmount = pf.dstAmount
				divisor := int64(maxPaths + 2)
				if minAmount.IsNative() {
					drops := minAmount.Drops()
					if drops > 0 {
						minAmount = state.NewXRPAmountFromInt(drops / divisor)
					}
				} else {
					f := minAmount.Float64()
					if f > 0 {
						minAmount = state.NewIssuedAmountFromFloat64(f/float64(divisor), minAmount.Currency, minAmount.Issuer)
					}
				}
			}
			liquidity, quality, ok := pf.getPathLiquidity(path, minAmount)
			if ok {
				extraRanks = append(extraRanks, PathRank{
					Quality:   quality,
					Length:    len(path),
					Liquidity: liquidity,
					Index:     i,
				})
			}
		}
		sort.Slice(extraRanks, func(i, j int) bool {
			ri, rj := extraRanks[i], extraRanks[j]
			if !pf.convertAll && ri.Quality != rj.Quality {
				return ri.Quality < rj.Quality
			}
			cmp := ri.Liquidity.Compare(rj.Liquidity)
			if cmp != 0 {
				return cmp > 0
			}
			return ri.Length < rj.Length
		})
	}

	// Merge-iterate both ranked lists
	// Reference: rippled Pathfinder::getBestPaths()
	pi := 0 // index into pf.pathRanks
	ei := 0 // index into extraRanks
	remaining := pf.remainingAmount
	dstEither := payment.ToEitherAmount(pf.dstAmount)

	for pi < len(pf.pathRanks) || ei < len(extraRanks) {
		pathsLeft := maxPaths - len(bestPaths)
		if !(pathsLeft > 0 || len(fullLiquidityPath) == 0) {
			break
		}

		// Select the better of the two lists
		// Reference: rippled can advance both when quality and liquidity are equal
		var rank PathRank
		var path []payment.PathStep
		usePath := false
		useExtraPath := false

		if pi >= len(pf.pathRanks) {
			useExtraPath = true
		} else if ei >= len(extraRanks) {
			usePath = true
		} else if extraRanks[ei].Quality < pf.pathRanks[pi].Quality {
			useExtraPath = true
		} else if extraRanks[ei].Quality > pf.pathRanks[pi].Quality {
			usePath = true
		} else if extraRanks[ei].Liquidity.Compare(pf.pathRanks[pi].Liquidity) > 0 {
			useExtraPath = true
		} else if extraRanks[ei].Liquidity.Compare(pf.pathRanks[pi].Liquidity) < 0 {
			usePath = true
		} else {
			// Equal quality and liquidity — advance both
			useExtraPath = true
			usePath = true
		}

		if usePath {
			rank = pf.pathRanks[pi]
			path = pf.completePaths[rank.Index]
		} else {
			rank = extraRanks[ei]
			path = extraPaths[rank.Index]
		}

		if useExtraPath {
			ei++
		}
		if usePath {
			pi++
		}

		if len(path) == 0 {
			continue
		}

		// For non-sender issuers, check if path starts with the right issuer
		// Reference: rippled isDefaultPath + issuer check
		startsWithIssuer := false
		if !issuerIsSender && usePath {
			// isDefaultPath: path.size() == 1
			if len(path) == 1 {
				continue
			}
			firstAcct, _ := state.DecodeAccountID(path[0].Account)
			if firstAcct != srcIssuer {
				continue
			}
			startsWithIssuer = true
		}

		finalPath := path
		if startsWithIssuer && len(path) > 1 {
			// Remove issuer from path start
			finalPath = path[1:]
		}

		if pathsLeft > 1 || (pathsLeft > 0 && rank.Liquidity.Compare(remaining) >= 0) {
			bestPaths = append(bestPaths, finalPath)
			remaining = remaining.Sub(rank.Liquidity)
		} else if pathsLeft == 0 && rank.Liquidity.Compare(dstEither) >= 0 && fullLiquidityPath == nil {
			fullLiquidityPath = finalPath
		}
	}

	return bestPaths, fullLiquidityPath
}
