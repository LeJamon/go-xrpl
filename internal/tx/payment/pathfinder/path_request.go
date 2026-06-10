package pathfinder

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
)

// Pathfinding search levels mirror rippled's config defaults
// (Config.h: PATH_SEARCH_FAST=2, PATH_SEARCH=2, PATH_SEARCH_MAX=3).
// A fast update runs at SearchLevelFast and a full update at
// SearchLevelDefault; repeated updates never exceed SearchLevelMax
// (PathRequest::doUpdate). Tests ported from rippled's Path_test raise
// the level to 7, matching that suite's pathTestEnv configuration.
const (
	SearchLevelFast    = 2
	SearchLevelDefault = 2
	SearchLevelMax     = 3
)

// PathAlternative represents one possible payment path with its cost.
type PathAlternative struct {
	// SourceAmount is the amount the source must send.
	SourceAmount tx.Amount
	// DestinationAmount is the amount actually delivered. Only meaningful
	// to report for convert-all requests, where it is the discovered
	// maximum instead of the requested amount.
	DestinationAmount tx.Amount
	// PathsComputed is the set of paths found for this alternative.
	PathsComputed [][]payment.PathStep
}

// PathRequestResult holds the complete result of a pathfinding request.
type PathRequestResult struct {
	Alternatives          []PathAlternative
	DestinationCurrencies []string
	// SourceCurrencyOverflow reports that auto-discovery found more than
	// maxAutoSrcCur source currencies. rippled fails the whole update with
	// rpcINTERNAL in this case (PathRequest::findPaths).
	SourceCurrencyOverflow bool
}

// PathRequest represents a single pathfinding request.
// Reference: rippled PathRequest class
type PathRequest struct {
	srcAccount       [20]byte
	dstAccount       [20]byte
	dstAmount        tx.Amount
	sendMax          *tx.Amount
	sourceCurrencies []payment.Issue // Explicit source currencies (or auto-discovered)
	convertAll       bool
	maxPaths         int
	searchLevel      int
}

// NewPathRequest creates a new path request from the given parameters.
func NewPathRequest(
	srcAccount, dstAccount [20]byte,
	dstAmount tx.Amount,
	sendMax *tx.Amount,
	sourceCurrencies []payment.Issue,
	convertAll bool,
) *PathRequest {
	return &PathRequest{
		srcAccount:       srcAccount,
		dstAccount:       dstAccount,
		dstAmount:        dstAmount,
		sendMax:          sendMax,
		sourceCurrencies: sourceCurrencies,
		convertAll:       convertAll,
		maxPaths:         maxReturnedPaths,
		searchLevel:      SearchLevelDefault,
	}
}

// SetSearchLevel overrides the search depth used by Execute. Mirrors
// raising PATH_SEARCH in rippled's config (e.g. Path_test's pathTestEnv
// uses 7).
func (pr *PathRequest) SetSearchLevel(level int) {
	pr.searchLevel = level
}

// Execute runs the pathfinding algorithm and returns the result.
// Reference: rippled PathRequest::doUpdate()
func (pr *PathRequest) Execute(ledger tx.LedgerView) *PathRequestResult {
	cache := NewRippleLineCache(ledger)

	// Determine source currencies
	srcCurrencies := pr.sourceCurrencies
	if len(srcCurrencies) == 0 && pr.sendMax != nil {
		// Use send_max currency
		issue := issueFromTxAmount(*pr.sendMax)
		srcCurrencies = []payment.Issue{issue}
	}
	if len(srcCurrencies) == 0 {
		// Auto-discover from account's trust lines.
		// Reference: rippled PathRequest::findPaths() auto-discovery.
		// rippled's accountSourceCurrencies returns just Currency values (no issuer).
		// The issuer is then set to the SOURCE ACCOUNT for non-XRP currencies:
		//   sourceCurrencies.insert({c, c.isZero() ? xrpAccount() : *raSrcAccount});
		discovered := AccountSourceCurrencies(pr.srcAccount, cache)
		sameAccount := pr.srcAccount == pr.dstAccount
		dstCurrency := pr.dstAmount.Currency
		if pr.dstAmount.IsNative() {
			dstCurrency = "XRP"
		}
		// Track unique currencies (not issues) to avoid duplicates
		seenCurrencies := make(map[string]bool)
		for issue := range discovered {
			if seenCurrencies[issue.Currency] {
				continue
			}
			seenCurrencies[issue.Currency] = true
			// Skip if same account sending same currency to itself
			if sameAccount && issue.Currency == dstCurrency {
				continue
			}
			// More than maxAutoSrcCur auto-discovered currencies fails the
			// whole request, matching rippled PathRequest::findPaths.
			if len(srcCurrencies) >= maxAutoSrcCur {
				return &PathRequestResult{SourceCurrencyOverflow: true}
			}
			// Build issue with source account as issuer (matching rippled)
			var srcIssue payment.Issue
			if issue.Currency == "XRP" || issue.Currency == "" {
				srcIssue = payment.Issue{Currency: "XRP"}
			} else {
				srcIssue = payment.Issue{Currency: issue.Currency, Issuer: pr.srcAccount}
			}
			srcCurrencies = append(srcCurrencies, srcIssue)
		}
	}

	result := &PathRequestResult{}

	// Compute destination currencies
	destCurrencies := AccountDestCurrencies(pr.dstAccount, cache)
	for issue := range destCurrencies {
		result.DestinationCurrencies = append(result.DestinationCurrencies, issue.Currency)
	}

	// Track previously found paths per source currency (mContext in rippled)
	context := make(map[payment.Issue][][]payment.PathStep)

	// When convertAll is true, replace destination amount with largest possible.
	// Reference: rippled convertAmount(saDstAmount, convert_all_) in PathRequest::findPaths()
	// and Pathfinder::computePathRanks()
	effectiveDstAmount := pr.dstAmount
	if pr.convertAll {
		effectiveDstAmount = largestAmount(pr.dstAmount)
	}

	for _, srcIssue := range srcCurrencies {
		// Determine source amount for this currency
		var srcAmount tx.Amount
		if pr.sendMax != nil {
			srcAmount = *pr.sendMax
		} else if srcIssue.IsXRP() {
			srcAmount = state.NewXRPAmountFromInt(int64(99999999999)) // Max XRP
		} else {
			// Max IOU amount
			srcAmount = state.NewIssuedAmountFromFloat64(9999999999999999e80, srcIssue.Currency, state.EncodeAccountIDSafe(srcIssue.Issuer))
		}

		// Run pathfinding
		pf := NewPathfinder(
			ledger, cache,
			pr.srcAccount, pr.dstAccount,
			effectiveDstAmount, srcAmount,
			srcIssue.Currency, srcIssue.Issuer,
			pr.convertAll,
		)

		if !pf.FindPaths(pr.searchLevel) {
			continue
		}

		pf.ComputePathRanks(pr.maxPaths)

		extraPaths := context[srcIssue]
		bestPaths, fullLiquidityPath := pf.GetBestPaths(pr.maxPaths, extraPaths, srcIssue.Issuer)
		context[srcIssue] = bestPaths

		// An empty path set is still tried: rippled runs rippleCalc with
		// default paths allowed, so a working default path yields an
		// alternative with empty paths_computed (PathRequest::findPaths).

		// Validate the paths via RippleCalculate.
		// Reference: rippled PathRequest::findPaths() line 592 — default paths ARE allowed
		// (rcInput is only set for convert_all_, otherwise pInputs is null → defaultPathsAllowed=true).
		// Use effectiveDstAmount (which is largestAmount for convertAll).
		_, actualOut, _, _, calcResult := payment.RippleCalculate(
			ledger,
			pr.srcAccount, pr.dstAccount,
			effectiveDstAmount,
			&srcAmount,
			bestPaths,
			true, // add default path (matches rippled PathRequest behavior)
			pr.convertAll,
			false,
			[32]byte{}, 0,
		)

		// If insufficient and we have a full-liquidity path, try adding it
		if !pr.convertAll && len(fullLiquidityPath) > 0 &&
			(calcResult != tx.TesSUCCESS || actualOut.Compare(payment.ToEitherAmount(effectiveDstAmount)) < 0) {
			bestPaths = append(bestPaths, fullLiquidityPath)
			_, _, _, _, calcResult = payment.RippleCalculate(
				ledger,
				pr.srcAccount, pr.dstAccount,
				effectiveDstAmount,
				&srcAmount,
				bestPaths,
				true, // add default path
				false,
				false,
				[32]byte{}, 0,
			)
		}

		if calcResult == tx.TesSUCCESS {
			// Re-run to get actual source and delivered amounts
			actualIn, finalOut, _, _, _ := payment.RippleCalculate(
				ledger,
				pr.srcAccount, pr.dstAccount,
				effectiveDstAmount,
				&srcAmount,
				bestPaths,
				true, // add default path
				pr.convertAll,
				false,
				[32]byte{}, 0,
			)

			// Set the source amount's issuer the way rippled does
			// (rc.actualAmountIn.setIssuer(sourceAccount)): the explicit
			// issue account, or the source account itself for IOUs.
			sourceAmount := payment.FromEitherAmount(actualIn)
			if !sourceAmount.IsNative() {
				if srcIssue.Issuer != ([20]byte{}) {
					sourceAmount.Issuer = state.EncodeAccountIDSafe(srcIssue.Issuer)
				} else {
					sourceAmount.Issuer = state.EncodeAccountIDSafe(pr.srcAccount)
				}
			}

			alt := PathAlternative{
				SourceAmount:      sourceAmount,
				DestinationAmount: payment.FromEitherAmount(finalOut),
				PathsComputed:     bestPaths,
			}
			result.Alternatives = append(result.Alternatives, alt)
		}
	}

	return result
}

// issueFromTxAmount extracts an Issue from a tx.Amount.
func issueFromTxAmount(amt tx.Amount) payment.Issue {
	if amt.IsNative() {
		return payment.Issue{Currency: "XRP"}
	}
	issuer, _ := state.DecodeAccountID(amt.Issuer)
	return payment.Issue{Currency: amt.Currency, Issuer: issuer}
}

// AccountExists checks if an account exists in the ledger.
func AccountExists(ledger tx.LedgerView, account [20]byte) bool {
	exists, _ := ledger.Exists(keylet.Account(account))
	return exists
}
