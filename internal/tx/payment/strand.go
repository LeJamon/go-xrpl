package payment

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// StrandContext tracks state during strand building for loop detection.
// Reference: rippled Steps.h StrandContext
type StrandContext struct {
	View      *PaymentSandbox
	StrandSrc [20]byte
	StrandDst [20]byte
	// seenDirectIssues[0] = source issues, seenDirectIssues[1] = destination issues
	SeenDirectIssues [2]map[Issue]bool
	SeenBookOuts     map[Issue]bool
	// OfferCrossing indicates strand is for offer crossing, not payment.
	// When true, DirectStepI skips trust line checks and creates trust lines on demand.
	// Reference: rippled StrandContext::offerCrossing
	OfferCrossing bool
	// StrandDeliver is the issue the strand delivers (destination issue).
	// Used by XRPEndpointOfferCrossingStep to compute reserve reduction.
	// Reference: rippled StrandContext::strandDeliver
	StrandDeliver Issue
	// IsDefaultPath is true when building from the default path (no explicit path).
	// Used by BookStep for self-cross detection during offer crossing.
	// Reference: rippled StrandContext::isDefaultPath
	IsDefaultPath bool
	// AMMContext tracks AMM state across payment engine iterations.
	// Shared across all strands in a payment. BookStep reads it to
	// initialize AMMLiquidity for synthetic AMM offers.
	// Reference: rippled StrandContext::ammContext
	AMMContext *AMMContext
	// ParentCloseTime is the parent ledger close time (Ripple epoch seconds).
	// Used by BookStep for offer expiration and AMM auction slot expiry checks.
	ParentCloseTime uint32
	// Fix1781 indicates whether the fix1781 amendment is enabled.
	// When true, XRP endpoint steps are included in circular payment loop detection.
	// When false, XRP endpoint loop checks are skipped (pre-amendment behavior).
	// Reference: rippled XRPEndpointStep.cpp check(): ctx.view.rules().enabled(fix1781)
	Fix1781 bool
}

// NewStrandContext creates a new context for strand building
func NewStrandContext(view *PaymentSandbox, src, dst [20]byte) *StrandContext {
	return &StrandContext{
		View:             view,
		StrandSrc:        src,
		StrandDst:        dst,
		SeenDirectIssues: [2]map[Issue]bool{{}, {}},
		SeenBookOuts:     make(map[Issue]bool),
	}
}

// CheckDirectStepLoop checks and records a DirectStep for loops.
// Returns temBAD_PATH_LOOP if a loop is detected.
// Reference: rippled DirectStep.cpp make_DirectStepI() lines 949-955
func (ctx *StrandContext) CheckDirectStepLoop(srcAcct, dstAcct [20]byte, currency string) tx.Result {
	srcIssue := Issue{Currency: currency, Issuer: srcAcct}
	dstIssue := Issue{Currency: currency, Issuer: dstAcct}

	// Check if source issue already seen as source (except for strand endpoints)
	if srcAcct != ctx.StrandSrc && srcAcct != ctx.StrandDst {
		if ctx.SeenDirectIssues[0][srcIssue] {
			return tx.TemBAD_PATH_LOOP
		}
	}

	// Check if dest issue already seen as dest (except for strand endpoints)
	if dstAcct != ctx.StrandSrc && dstAcct != ctx.StrandDst {
		if ctx.SeenDirectIssues[1][dstIssue] {
			return tx.TemBAD_PATH_LOOP
		}
	}

	// Insert into seen sets
	ctx.SeenDirectIssues[0][srcIssue] = true
	ctx.SeenDirectIssues[1][dstIssue] = true

	return tx.TesSUCCESS
}

// CheckBookStepLoop checks and records a BookStep for loops.
// Returns temBAD_PATH_LOOP if a loop is detected.
// Reference: rippled BookStep.cpp make_BookStepI() lines 1357-1372
func (ctx *StrandContext) CheckBookStepLoop(bookOut Issue) tx.Result {
	// Cannot have multiple book steps with same output issue
	if ctx.SeenBookOuts[bookOut] {
		return tx.TemBAD_PATH_LOOP
	}

	// Book output cannot match a direct step source issue
	if ctx.SeenDirectIssues[0][bookOut] {
		return tx.TemBAD_PATH_LOOP
	}

	// Book output cannot match a direct step destination issue
	if ctx.SeenDirectIssues[1][bookOut] {
		return tx.TemBAD_PATH_LOOP
	}

	ctx.SeenBookOuts[bookOut] = true
	return tx.TesSUCCESS
}

// CheckXRPEndpointLoop checks XRP endpoint step for loops.
// This check is gated on the fix1781 amendment. When fix1781 is not enabled,
// the check is skipped entirely (pre-amendment behavior allows circular XRP paths).
// Reference: rippled XRPEndpointStep.cpp lines 365-375
func (ctx *StrandContext) CheckXRPEndpointLoop(isLast bool) tx.Result {
	if !ctx.Fix1781 {
		return tx.TesSUCCESS
	}

	xrpIssue := Issue{Currency: "XRP", Issuer: [20]byte{}}
	issuesIndex := 0
	if !isLast {
		issuesIndex = 1
	}

	if ctx.SeenDirectIssues[issuesIndex][xrpIssue] {
		return tx.TemBAD_PATH_LOOP
	}

	ctx.SeenDirectIssues[issuesIndex][xrpIssue] = true
	return tx.TesSUCCESS
}

// newXRPEndpointStep creates an XRPEndpointStep appropriate for the context.
// For offer crossing, it computes reserve reduction (matching rippled's
// XRPEndpointOfferCrossingStep). For payments, it creates a standard step.
// isFirst indicates whether this is the first step in the strand (source).
// Reference: rippled XRPEndpointOfferCrossingStep::computeReserveReduction
func (ctx *StrandContext) newXRPEndpointStep(account [20]byte, isLast bool, isFirst bool) *XRPEndpointStep {
	if ctx.OfferCrossing {
		return NewXRPEndpointStepForOfferCrossing(account, isLast, isFirst, ctx.StrandDeliver, ctx.View)
	}
	return NewXRPEndpointStep(account, isLast)
}

// Path flags - indicate what type of element this path step contains
// These match rippled's STPathElement types
const (
	// PathTypeAccount indicates path element has account
	PathTypeAccount uint8 = 0x01
	// PathTypeCurrency indicates path element has currency
	PathTypeCurrency uint8 = 0x10
	// PathTypeIssuer indicates path element has issuer
	PathTypeIssuer uint8 = 0x20
)

// ToStrands converts payment paths to executable strands
// Parameters:
//   - view: PaymentSandbox with ledger state
//   - src: Source account
//   - dst: Destination account
//   - dstAmt: Destination amount/issue
//   - srcAmt: Source amount/issue (optional, from SendMax)
//   - paths: Payment paths from transaction
//   - addDefaultPath: Whether to add the default path (direct)
//   - opts: Optional bools: [0]=offerCrossing, [1]=fix1781
//
// Returns: List of executable strands, error if any path is invalid
// Reference: rippled PaySteps.cpp toStrands()
func ToStrands(
	view *PaymentSandbox,
	src, dst [20]byte,
	dstAmt tx.Amount,
	srcAmt *tx.Amount,
	paths [][]PathStep,
	addDefaultPath bool,
	opts ...bool,
) ([]Strand, tx.Result) {
	isOfferCrossing := len(opts) > 0 && opts[0]
	fix1781 := len(opts) > 1 && opts[1]

	// Validate source and destination are not XRP pseudo-accounts
	// Reference: rippled PaySteps.cpp:148-150
	var xrpAccount [20]byte
	if src == xrpAccount || dst == xrpAccount {
		return nil, tx.TemBAD_PATH
	}

	dstIssue := GetIssue(dstAmt)
	// If dstIssue has zero issuer for non-XRP currency, default to dst.
	// RippleState balances store zero issuer; the destination account is the implied issuer.
	// Reference: rippled treats noAccount() issuer as the destination for deliver amounts.
	if !dstIssue.IsXRP() && dstIssue.Issuer == [20]byte{} {
		dstIssue.Issuer = dst
	}

	var srcIssue *Issue
	if srcAmt != nil {
		issue := GetIssue(*srcAmt)
		// Same fallback for source issue
		if !issue.IsXRP() && issue.Issuer == [20]byte{} {
			issue.Issuer = src
		}
		srcIssue = &issue
	}

	var strands []Strand
	var lastFailResult tx.Result = tx.TesSUCCESS

	// Add default path if requested
	if addDefaultPath {
		strand, result := ToStrandWithLoopCheck(view, src, dst, dstIssue, srcIssue, nil, true, isOfferCrossing, fix1781)
		if result != tx.TesSUCCESS {
			// For tem* errors, fail immediately
			if isTemMalformed(result) || len(paths) == 0 {
				return nil, result
			}
			lastFailResult = result
		} else if len(strand) > 0 {
			strands = append(strands, strand)
		}
	} else if len(paths) == 0 {
		// Reference: rippled PaySteps.cpp:532-537
		return nil, tx.TemRIPPLE_EMPTY
	}

	// Convert each explicit path to a strand
	for _, path := range paths {
		strand, result := ToStrandWithLoopCheck(view, src, dst, dstIssue, srcIssue, path, false, isOfferCrossing, fix1781)
		if result != tx.TesSUCCESS {
			lastFailResult = result
			// For tem* errors, fail immediately
			if isTemMalformed(result) {
				return nil, result
			}
			continue
		}
		if len(strand) > 0 {
			// Check for duplicate strands
			isDuplicate := false
			for _, existing := range strands {
				if strandsEqual(existing, strand) {
					isDuplicate = true
					break
				}
			}
			if !isDuplicate {
				strands = append(strands, strand)
			}
		}
	}

	if len(strands) == 0 {
		return nil, lastFailResult
	}

	return strands, tx.TesSUCCESS
}

// isTemMalformed returns true if the result is a tem* error code
func isTemMalformed(result tx.Result) bool {
	code := result.String()
	return len(code) >= 3 && code[:3] == "tem"
}

// ToStrandWithLoopCheck converts a path to a strand with loop detection.
// opts: [0]=offerCrossing, [1]=fix1781
// Reference: rippled PaySteps.cpp toStrand() with seenDirectIssues and seenBookOuts
func ToStrandWithLoopCheck(
	view *PaymentSandbox,
	src, dst [20]byte,
	dstIssue Issue,
	srcIssue *Issue,
	path []PathStep,
	isDefaultPath bool,
	opts ...bool,
) (Strand, tx.Result) {
	// Create strand context for loop detection
	ctx := NewStrandContext(view, src, dst)
	ctx.StrandDeliver = dstIssue
	ctx.IsDefaultPath = isDefaultPath
	if len(opts) > 0 && opts[0] {
		ctx.OfferCrossing = true
	}
	if len(opts) > 1 && opts[1] {
		ctx.Fix1781 = true
	}

	// Use the context-aware strand builder
	strand, result := ToStrandWithContext(ctx, src, dst, dstIssue, srcIssue, path, isDefaultPath)
	if result != tx.TesSUCCESS {
		return nil, result
	}

	return strand, tx.TesSUCCESS
}

// ToStrandWithContext converts a single path to an executable strand with context-aware loop detection.
// Reference: rippled PaySteps.cpp toStrand()
func ToStrandWithContext(
	ctx *StrandContext,
	src, dst [20]byte,
	dstIssue Issue,
	srcIssue *Issue,
	path []PathStep,
	isDefaultPath bool,
) (Strand, tx.Result) {
	view := ctx.View
	// Build the normalized path following rippled's approach
	// The normalized path includes implicit nodes for source, sendMax issuer, etc.

	// Determine the starting currency issue
	// Per rippled: Issue{currency, src} - source account is the initial "issuer" context
	var curIssue Issue
	if srcIssue != nil {
		curIssue = Issue{Currency: srcIssue.Currency, Issuer: src}
	} else {
		curIssue = Issue{Currency: dstIssue.Currency, Issuer: src}
	}

	if curIssue.IsXRP() {
		curIssue.Issuer = [20]byte{} // XRP pseudo-account
	}

	// Build normalized path as a list of PathElement-like nodes
	type normNode struct {
		account     [20]byte
		currency    string
		issuer      [20]byte
		hasAccount  bool
		hasCurrency bool
		hasIssuer   bool
	}

	var normPath []normNode

	// Add source node
	normPath = append(normPath, normNode{
		account:     src,
		currency:    curIssue.Currency,
		issuer:      curIssue.Issuer,
		hasAccount:  true,
		hasCurrency: true,
		hasIssuer:   true,
	})

	// If sendMaxIssue has a different account (issuer) than src, insert it
	// This is the key for cross-issuer ripple payments!
	// Skip for XRP - the XRP pseudo-account (zero bytes) is not a real account
	// and shouldn't be inserted as an intermediate node.
	if srcIssue != nil && srcIssue.Issuer != src && !srcIssue.IsXRP() {
		// Check if first path element isn't already this account
		needsInsert := true
		if len(path) > 0 && hasAccount(path[0]) {
			firstAccount := accountFromPathElement(path[0], src)
			if firstAccount == srcIssue.Issuer {
				needsInsert = false
			}
		}
		if needsInsert {
			normPath = append(normPath, normNode{
				account:    srcIssue.Issuer,
				hasAccount: true,
			})
		}
	}

	// Add explicit path elements
	for _, elem := range path {
		var node normNode
		if hasAccount(elem) {
			node.account = accountFromPathElement(elem, src)
			node.hasAccount = true
		}
		if hasCurrency(elem) {
			node.currency = elem.Currency
			node.hasCurrency = true
		}
		if hasIssuer(elem) {
			issuerBytes, err := state.DecodeAccountID(elem.Issuer)
			if err == nil {
				node.issuer = issuerBytes
				node.hasIssuer = true
			}
		}
		normPath = append(normPath, node)
	}

	// Find the last element with a currency to check if we need a currency/issuer step.
	// Reference: rippled PaySteps.cpp lines 219-231
	lastCurrency := curIssue.Currency
	var lastCurrencyIssuer [20]byte
	lastCurrencyIssuerSet := false
	for i := len(normPath) - 1; i >= 0; i-- {
		if normPath[i].hasCurrency {
			lastCurrency = normPath[i].currency
			if normPath[i].hasIssuer {
				lastCurrencyIssuer = normPath[i].issuer
				lastCurrencyIssuerSet = true
			} else if normPath[i].hasAccount {
				lastCurrencyIssuer = normPath[i].account
				lastCurrencyIssuerSet = true
			}
			break
		}
	}

	// Add currency/issuer step if currency differs, or if offer crossing
	// and the issuer differs. For offer crossing, a book step between same
	// currency different issuers IS valid (unlike regular payments which use rippling).
	// Reference: rippled PaySteps.cpp lines 224-230:
	//   if ((lastCurrency.getCurrency() != deliver.currency) ||
	//       (offerCrossing &&
	//        lastCurrency.getIssuerID() != deliver.account))
	needCurrencyStep := lastCurrency != dstIssue.Currency
	if !needCurrencyStep && ctx.OfferCrossing && lastCurrencyIssuerSet {
		needCurrencyStep = lastCurrencyIssuer != dstIssue.Issuer
	}
	if needCurrencyStep {
		normPath = append(normPath, normNode{
			currency:    dstIssue.Currency,
			issuer:      dstIssue.Issuer,
			hasCurrency: true,
			hasIssuer:   true,
		})
	}

	// Add destination issuer account if needed (for multi-hop through issuer)
	// Only if the last element isn't already that account AND dst != dstIssue.Issuer
	// Skip for XRP destination - the XRP pseudo-account [0..0] is not a real account
	lastIsAccount := len(normPath) > 0 && normPath[len(normPath)-1].hasAccount
	lastAccount := src
	if lastIsAccount {
		lastAccount = normPath[len(normPath)-1].account
	}

	if !dstIssue.IsXRP() && !((lastIsAccount && lastAccount == dstIssue.Issuer) || (dst == dstIssue.Issuer)) {
		normPath = append(normPath, normNode{
			account:    dstIssue.Issuer,
			hasAccount: true,
		})
	}

	// Add destination if not already the last account
	if !lastIsAccount || normPath[len(normPath)-1].account != dst {
		// Check the updated last element
		if len(normPath) > 0 {
			lastNode := normPath[len(normPath)-1]
			if !lastNode.hasAccount || lastNode.account != dst {
				normPath = append(normPath, normNode{
					account:    dst,
					hasAccount: true,
				})
			}
		}
	}

	if len(normPath) < 2 {
		return nil, tx.TemBAD_PATH
	}

	// Now convert normalized path to steps with loop detection
	var strand Strand
	var prevStep Step

	// Reset curIssue for step creation
	if srcIssue != nil {
		curIssue = Issue{Currency: srcIssue.Currency, Issuer: src}
	} else {
		curIssue = Issue{Currency: dstIssue.Currency, Issuer: src}
	}
	if curIssue.IsXRP() {
		curIssue.Issuer = [20]byte{}
	}

	for i := 0; i < len(normPath)-1; i++ {
		cur := normPath[i]
		next := normPath[i+1]
		isLast := i == len(normPath)-2

		// Update current issue based on current node
		if cur.hasAccount {
			curIssue.Issuer = cur.account
		} else if cur.hasIssuer {
			curIssue.Issuer = cur.issuer
		}
		if cur.hasCurrency {
			curIssue.Currency = cur.currency
			if curIssue.IsXRP() {
				curIssue.Issuer = [20]byte{}
			}
		}

		// Handle account-to-account transitions (DirectStep or implied steps)
		if cur.hasAccount && next.hasAccount {
			// Check if we need an implied account step
			// Per rippled: if curIssue.account != cur.account AND curIssue.account != next.account
			if !curIssue.IsXRP() && curIssue.Issuer != cur.account && curIssue.Issuer != next.account {
				// Insert implied DirectStep to curIssue.Issuer first
				// Check for loop BEFORE creating step
				if result := ctx.CheckDirectStepLoop(cur.account, curIssue.Issuer, curIssue.Currency); result != tx.TesSUCCESS {
					return nil, result
				}
				directStep := ctx.newDirectStepI(cur.account, curIssue.Issuer, curIssue.Currency, prevStep, len(strand) == 0, false)
				// Check NoRipple constraint
				if result := ctx.checkDirectStep(directStep, view, prevStep); result != tx.TesSUCCESS {
					return nil, result
				}
				strand = append(strand, directStep)
				prevStep = directStep

				// Check for loop BEFORE creating step
				if result := ctx.CheckDirectStepLoop(curIssue.Issuer, next.account, curIssue.Currency); result != tx.TesSUCCESS {
					return nil, result
				}
				// Now create step from curIssue.Issuer to next
				directStep = ctx.newDirectStepI(curIssue.Issuer, next.account, curIssue.Currency, prevStep, false, isLast)
				// Check NoRipple constraint
				if result := ctx.checkDirectStep(directStep, view, prevStep); result != tx.TesSUCCESS {
					return nil, result
				}
				strand = append(strand, directStep)
				prevStep = directStep
			} else {
				// Direct step from cur to next
				if curIssue.IsXRP() {
					// XRP endpoint step
					if i == 0 {
						// Check for XRP loop
						if result := ctx.CheckXRPEndpointLoop(false); result != tx.TesSUCCESS {
							return nil, result
						}
						step := ctx.newXRPEndpointStep(cur.account, false, true) // source, isFirst=true
						strand = append(strand, step)
						prevStep = step
					}
					if isLast {
						// Check for XRP loop
						if result := ctx.CheckXRPEndpointLoop(true); result != tx.TesSUCCESS {
							return nil, result
						}
						step := ctx.newXRPEndpointStep(next.account, true, false) // destination, isFirst=false
						strand = append(strand, step)
					}
				} else {
					// Check for loop BEFORE creating step
					if result := ctx.CheckDirectStepLoop(cur.account, next.account, curIssue.Currency); result != tx.TesSUCCESS {
						return nil, result
					}
					directStep := ctx.newDirectStepI(cur.account, next.account, curIssue.Currency, prevStep, len(strand) == 0, isLast)
					// Check NoRipple constraint
					if result := ctx.checkDirectStep(directStep, view, prevStep); result != tx.TesSUCCESS {
						return nil, result
					}
					strand = append(strand, directStep)
					prevStep = directStep
				}
			}
		} else if cur.hasAccount && !next.hasAccount && (next.hasCurrency || next.hasIssuer) {
			// Account to offer (currency change)
			// Reference: rippled PaySteps.cpp toStep()

			// Determine output issue first (needed for XRP continue check)
			outCurrency := curIssue.Currency
			if next.hasCurrency {
				outCurrency = next.currency
			}
			outIssuer := curIssue.Issuer
			if next.hasIssuer {
				outIssuer = next.issuer
			}
			outIssue := Issue{Currency: outCurrency, Issuer: outIssuer}
			// XRP must have zero issuer
			if outIssue.IsXRP() {
				outIssue.Issuer = [20]byte{}
			}

			// If source is XRP, need XRPEndpointStep first (only if this is the first element)
			// Reference: rippled PaySteps.cpp toStep() lines 80-85: creates XRPEndpointStep
			// and returns immediately. The book step is created in the next iteration.
			if curIssue.IsXRP() && i == 0 {
				// Check for XRP loop
				if result := ctx.CheckXRPEndpointLoop(false); result != tx.TesSUCCESS {
					return nil, result
				}
				xrpStep := ctx.newXRPEndpointStep(cur.account, false, true) // source, isFirst=true
				strand = append(strand, xrpStep)
				prevStep = xrpStep
				// If output is also XRP, defer book step to next iteration
				// (matching rippled's toStep which returns after XRPEndpointStep).
				// The next iteration handles the offer→offer transition.
				if outIssue.IsXRP() {
					continue
				}
			} else if !curIssue.IsXRP() && curIssue.Issuer != cur.account {
				// May need implied DirectStep first for IOU
				// Check for loop BEFORE creating step
				if result := ctx.CheckDirectStepLoop(cur.account, curIssue.Issuer, curIssue.Currency); result != tx.TesSUCCESS {
					return nil, result
				}
				directStep := ctx.newDirectStepI(cur.account, curIssue.Issuer, curIssue.Currency, prevStep, len(strand) == 0, false)
				// Check NoRipple constraint
				if result := ctx.checkDirectStep(directStep, view, prevStep); result != tx.TesSUCCESS {
					return nil, result
				}
				strand = append(strand, directStep)
				prevStep = directStep
			}

			// Create book step for offer path elements
			// Reference: rippled PaySteps.cpp toStep() creates BookStep
			if curIssue.IsXRP() && outIssue.IsXRP() {
				return nil, tx.TemBAD_PATH // Invalid: XRP to XRP book
			}
			// Same in/out issue means an invalid book (book_.in == book_.out).
			// Reference: rippled BookStep::check() line 1346: returns temBAD_PATH
			if curIssue.Currency == outIssue.Currency && curIssue.Issuer == outIssue.Issuer {
				return nil, tx.TemBAD_PATH
			}
			// Check for book loop BEFORE creating step
			if result := ctx.CheckBookStepLoop(outIssue); result != tx.TesSUCCESS {
				return nil, result
			}
			bookStep := NewBookStep(curIssue, outIssue, src, dst, prevStep, false)
			bookStep.defaultPath = ctx.IsDefaultPath
			// Validate book step (noRipple, issuer existence, etc.)
			// Reference: rippled BookStep.cpp make_BookStepHelper() calls check(ctx)
			if result := bookStep.Check(view); result != tx.TesSUCCESS {
				return nil, result
			}
			strand = append(strand, bookStep)
			prevStep = bookStep
			curIssue = outIssue
		} else if !cur.hasAccount && next.hasAccount {
			// Offer to account
			if curIssue.IsXRP() {
				// XRP coming out of a book — need XRPEndpointStep for the recipient
				// Check for XRP loop
				if result := ctx.CheckXRPEndpointLoop(true); result != tx.TesSUCCESS {
					return nil, result
				}
				step := ctx.newXRPEndpointStep(next.account, true, false) // destination, isFirst=false
				strand = append(strand, step)
			} else if curIssue.Issuer != next.account {
				// IOU: implied DirectStep from curIssue.Issuer to next account
				// Check for loop BEFORE creating step
				if result := ctx.CheckDirectStepLoop(curIssue.Issuer, next.account, curIssue.Currency); result != tx.TesSUCCESS {
					return nil, result
				}
				directStep := ctx.newDirectStepI(curIssue.Issuer, next.account, curIssue.Currency, prevStep, len(strand) == 0, isLast)
				// Check NoRipple constraint
				if result := ctx.checkDirectStep(directStep, view, prevStep); result != tx.TesSUCCESS {
					return nil, result
				}
				strand = append(strand, directStep)
				prevStep = directStep
			}
		} else if !cur.hasAccount && !next.hasAccount && (next.hasCurrency || next.hasIssuer) {
			// Offer to offer (consecutive currency changes)
			// Reference: rippled PaySteps.cpp toStep() lines 105-130
			outCurrency := curIssue.Currency
			if next.hasCurrency {
				outCurrency = next.currency
			}
			outIssuer := curIssue.Issuer
			if next.hasIssuer {
				outIssuer = next.issuer
			}
			outIssue := Issue{Currency: outCurrency, Issuer: outIssuer}
			// XRP must have zero issuer
			if outIssue.IsXRP() {
				outIssue.Issuer = [20]byte{}
			}

			// Always create book step for offer path elements
			// Reference: rippled PaySteps.cpp toStep() always creates BookStep,
			// then check() validates it (returns temBAD_PATH for same in/out issue)
			if curIssue.IsXRP() && outIssue.IsXRP() {
				return nil, tx.TemBAD_PATH // Invalid: XRP to XRP book
			}
			// Same in/out issue means an invalid book (book_.in == book_.out).
			// Reference: rippled BookStep::check() line 1346: returns temBAD_PATH
			if curIssue.Currency == outIssue.Currency && curIssue.Issuer == outIssue.Issuer {
				return nil, tx.TemBAD_PATH
			}
			// Check for book loop BEFORE creating step
			if result := ctx.CheckBookStepLoop(outIssue); result != tx.TesSUCCESS {
				return nil, result
			}
			bookStep := NewBookStep(curIssue, outIssue, src, dst, prevStep, false)
			bookStep.defaultPath = ctx.IsDefaultPath
			// Validate book step (noRipple, issuer existence, etc.)
			// Reference: rippled BookStep.cpp make_BookStepHelper() calls check(ctx)
			if result := bookStep.Check(view); result != tx.TesSUCCESS {
				return nil, result
			}
			strand = append(strand, bookStep)
			prevStep = bookStep
			curIssue = outIssue
		}
	}

	// Validate the strand
	if err := validateStrand(strand, view); err != nil {
		return nil, tx.TemBAD_PATH
	}

	return strand, tx.TesSUCCESS
}

// ToStrand converts a single path to an executable strand
// This matches rippled's toStrand() implementation in PaySteps.cpp
func ToStrand(
	view *PaymentSandbox,
	src, dst [20]byte,
	dstIssue Issue,
	srcIssue *Issue,
	path []PathStep,
	isDefaultPath bool,
) (Strand, error) {
	ctx := NewStrandContext(view, src, dst)
	strand, result := ToStrandWithContext(ctx, src, dst, dstIssue, srcIssue, path, isDefaultPath)
	if result != tx.TesSUCCESS {
		return nil, nil // Return nil error for backward compatibility
	}
	return strand, nil
}

// ToStrandLegacy is the old implementation without loop detection.
// Kept for reference but deprecated.
func ToStrandLegacy(
	view *PaymentSandbox,
	src, dst [20]byte,
	dstIssue Issue,
	srcIssue *Issue,
	path []PathStep,
	isDefaultPath bool,
) (Strand, error) {
	// Build the normalized path following rippled's approach
	// The normalized path includes implicit nodes for source, sendMax issuer, etc.

	// Determine the starting currency issue
	// Per rippled: Issue{currency, src} - source account is the initial "issuer" context
	var curIssue Issue
	if srcIssue != nil {
		curIssue = Issue{Currency: srcIssue.Currency, Issuer: src}
	} else {
		curIssue = Issue{Currency: dstIssue.Currency, Issuer: src}
	}

	if curIssue.IsXRP() {
		curIssue.Issuer = [20]byte{} // XRP pseudo-account
	}

	// Build normalized path as a list of PathElement-like nodes
	type normNode struct {
		account     [20]byte
		currency    string
		issuer      [20]byte
		hasAccount  bool
		hasCurrency bool
		hasIssuer   bool
	}

	var normPath []normNode

	// Add source node
	normPath = append(normPath, normNode{
		account:     src,
		currency:    curIssue.Currency,
		issuer:      curIssue.Issuer,
		hasAccount:  true,
		hasCurrency: true,
		hasIssuer:   true,
	})

	// If sendMaxIssue has a different account (issuer) than src, insert it
	// This is the key for cross-issuer ripple payments!
	// Skip for XRP - the XRP pseudo-account (zero bytes) is not a real account
	// and shouldn't be inserted as an intermediate node.
	if srcIssue != nil && srcIssue.Issuer != src && !srcIssue.IsXRP() {
		// Check if first path element isn't already this account
		needsInsert := true
		if len(path) > 0 && hasAccount(path[0]) {
			firstAccount := accountFromPathElement(path[0], src)
			if firstAccount == srcIssue.Issuer {
				needsInsert = false
			}
		}
		if needsInsert {
			normPath = append(normPath, normNode{
				account:    srcIssue.Issuer,
				hasAccount: true,
			})
		}
	}

	// Add explicit path elements
	for _, elem := range path {
		var node normNode
		if hasAccount(elem) {
			node.account = accountFromPathElement(elem, src)
			node.hasAccount = true
		}
		if hasCurrency(elem) {
			node.currency = elem.Currency
			node.hasCurrency = true
		}
		if hasIssuer(elem) {
			issuerBytes, err := state.DecodeAccountID(elem.Issuer)
			if err == nil {
				node.issuer = issuerBytes
				node.hasIssuer = true
			}
		}
		normPath = append(normPath, node)
	}

	// Find the last currency in the path to check if we need currency change
	lastCurrency := curIssue.Currency
	for i := len(normPath) - 1; i >= 0; i-- {
		if normPath[i].hasCurrency {
			lastCurrency = normPath[i].currency
			break
		}
	}

	// Add currency/issuer step if currency differs
	// Note: For regular payments (not offer crossing), different issuers
	// with same currency do NOT need a book step - they use rippling
	if lastCurrency != dstIssue.Currency {
		normPath = append(normPath, normNode{
			currency:    dstIssue.Currency,
			issuer:      dstIssue.Issuer,
			hasCurrency: true,
			hasIssuer:   true,
		})
	}

	// Add destination issuer account if needed (for multi-hop through issuer)
	// Only if the last element isn't already that account AND dst != dstIssue.Issuer
	// Skip for XRP destination - the XRP pseudo-account [0..0] is not a real account
	lastIsAccount := len(normPath) > 0 && normPath[len(normPath)-1].hasAccount
	lastAccount := src
	if lastIsAccount {
		lastAccount = normPath[len(normPath)-1].account
	}

	if !dstIssue.IsXRP() && !((lastIsAccount && lastAccount == dstIssue.Issuer) || (dst == dstIssue.Issuer)) {
		normPath = append(normPath, normNode{
			account:    dstIssue.Issuer,
			hasAccount: true,
		})
	}

	// Add destination if not already the last account
	if !lastIsAccount || normPath[len(normPath)-1].account != dst {
		// Check the updated last element
		if len(normPath) > 0 {
			lastNode := normPath[len(normPath)-1]
			if !lastNode.hasAccount || lastNode.account != dst {
				normPath = append(normPath, normNode{
					account:    dst,
					hasAccount: true,
				})
			}
		}
	}

	if len(normPath) < 2 {
		return nil, nil // Invalid path
	}

	// Now convert normalized path to steps
	var strand Strand
	var prevStep Step

	// Reset curIssue for step creation
	if srcIssue != nil {
		curIssue = Issue{Currency: srcIssue.Currency, Issuer: src}
	} else {
		curIssue = Issue{Currency: dstIssue.Currency, Issuer: src}
	}
	if curIssue.IsXRP() {
		curIssue.Issuer = [20]byte{}
	}

	for i := 0; i < len(normPath)-1; i++ {
		cur := normPath[i]
		next := normPath[i+1]
		isLast := i == len(normPath)-2

		// Update current issue based on current node
		if cur.hasAccount {
			curIssue.Issuer = cur.account
		} else if cur.hasIssuer {
			curIssue.Issuer = cur.issuer
		}
		if cur.hasCurrency {
			curIssue.Currency = cur.currency
			if curIssue.IsXRP() {
				curIssue.Issuer = [20]byte{}
			}
		}

		// Handle account-to-account transitions (DirectStep or implied steps)
		if cur.hasAccount && next.hasAccount {
			// Check if we need an implied account step
			// Per rippled: if curIssue.account != cur.account AND curIssue.account != next.account
			if !curIssue.IsXRP() && curIssue.Issuer != cur.account && curIssue.Issuer != next.account {
				// Insert implied DirectStep to curIssue.Issuer first
				directStep := NewDirectStepI(cur.account, curIssue.Issuer, curIssue.Currency, prevStep, len(strand) == 0, false)
				strand = append(strand, directStep)
				prevStep = directStep
				// Now create step from curIssue.Issuer to next
				directStep = NewDirectStepI(curIssue.Issuer, next.account, curIssue.Currency, prevStep, false, isLast)
				strand = append(strand, directStep)
				prevStep = directStep
			} else {
				// Direct step from cur to next
				if curIssue.IsXRP() {
					// XRP endpoint step
					if i == 0 {
						step := NewXRPEndpointStep(cur.account, false) // source
						strand = append(strand, step)
						prevStep = step
					}
					if isLast {
						step := NewXRPEndpointStep(next.account, true) // destination
						strand = append(strand, step)
					}
				} else {
					directStep := NewDirectStepI(cur.account, next.account, curIssue.Currency, prevStep, len(strand) == 0, isLast)
					strand = append(strand, directStep)
					prevStep = directStep
				}
			}
		} else if cur.hasAccount && !next.hasAccount && (next.hasCurrency || next.hasIssuer) {
			// Account to offer (currency change)
			// May need implied DirectStep first
			if !curIssue.IsXRP() && curIssue.Issuer != cur.account {
				directStep := NewDirectStepI(cur.account, curIssue.Issuer, curIssue.Currency, prevStep, len(strand) == 0, false)
				strand = append(strand, directStep)
				prevStep = directStep
			}

			// Determine output issue
			outCurrency := curIssue.Currency
			if next.hasCurrency {
				outCurrency = next.currency
			}
			outIssuer := curIssue.Issuer
			if next.hasIssuer {
				outIssuer = next.issuer
			}
			outIssue := Issue{Currency: outCurrency, Issuer: outIssuer}

			// Create book step if currencies differ
			if curIssue.Currency != outCurrency {
				if curIssue.IsXRP() && outIssue.IsXRP() {
					return nil, nil // Invalid: XRP to XRP book
				}
				bookStep := NewBookStep(curIssue, outIssue, src, dst, prevStep, false)
				// Validate book step (noRipple, issuer existence, etc.)
				// Reference: rippled BookStep.cpp make_BookStepHelper() calls check(ctx)
				if result := bookStep.Check(view); result != tx.TesSUCCESS {
					return nil, nil
				}
				strand = append(strand, bookStep)
				prevStep = bookStep
				curIssue = outIssue
			}
		} else if !cur.hasAccount && next.hasAccount {
			// Offer to account
			if curIssue.IsXRP() {
				// XRP coming out of a book — need XRPEndpointStep for the recipient
				step := NewXRPEndpointStep(next.account, true)
				strand = append(strand, step)
			} else if curIssue.Issuer != next.account {
				// IOU: implied DirectStep from curIssue.Issuer to next
				directStep := NewDirectStepI(curIssue.Issuer, next.account, curIssue.Currency, prevStep, len(strand) == 0, isLast)
				strand = append(strand, directStep)
				prevStep = directStep
			}
		}
	}

	// Validate the strand
	if err := validateStrand(strand, view); err != nil {
		return nil, err
	}

	return strand, nil
}

// accountFromPathElement extracts the account from a path element
func accountFromPathElement(elem PathStep, defaultAccount [20]byte) [20]byte {
	if elem.Account != "" {
		accountBytes, err := state.DecodeAccountID(elem.Account)
		if err == nil {
			return accountBytes
		}
	}
	return defaultAccount
}

// hasAccount returns true if the path element specifies an account
func hasAccount(elem PathStep) bool {
	return elem.Account != "" || (elem.Type&int(PathTypeAccount)) != 0
}

// hasCurrency returns true if the path element specifies a currency
func hasCurrency(elem PathStep) bool {
	return elem.Currency != "" || (elem.Type&int(PathTypeCurrency)) != 0
}

// hasIssuer returns true if the path element specifies an issuer
func hasIssuer(elem PathStep) bool {
	return elem.Issuer != "" || (elem.Type&int(PathTypeIssuer)) != 0
}

// issuesEqual compares two Issues for equality
func issuesEqual(a, b Issue) bool {
	if a.IsXRP() != b.IsXRP() {
		return false
	}
	if a.IsXRP() {
		return true // Both XRP
	}
	return a.Currency == b.Currency && a.Issuer == b.Issuer
}

// strandsEqual compares two strands for equality
func strandsEqual(a, b Strand) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !stepsEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// stepsEqual compares two steps for equality
func stepsEqual(a, b Step) bool {
	// Compare based on step type and key attributes
	aAccts := a.DirectStepAccts()
	bAccts := b.DirectStepAccts()

	if aAccts != nil && bAccts != nil {
		return *aAccts == *bAccts
	}

	aBook := a.BookStepBook()
	bBook := b.BookStepBook()

	if aBook != nil && bBook != nil {
		return issuesEqual(aBook.In, bBook.In) && issuesEqual(aBook.Out, bBook.Out)
	}

	// Different types of steps
	return false
}

// validateStrand checks that a strand is valid
func validateStrand(strand Strand, view *PaymentSandbox) error {
	if len(strand) == 0 {
		return nil
	}

	// Check each step
	for _, step := range strand {
		// DirectStep validation - check trust line exists
		if accts := step.DirectStepAccts(); accts != nil {
			src, dst := accts[0], accts[1]
			// Skip if either is the zero account (XRP pseudo-account)
			var zeroAccount [20]byte
			if src == zeroAccount || dst == zeroAccount {
				continue
			}
			// Trust line check is done by the Check method
		}

		// BookStep validation - check order book has liquidity
		if book := step.BookStepBook(); book != nil {
			// This would check for order book existence
			// Simplified for now
		}
	}

	return nil
}

// CheckStrand validates all steps in a strand
func CheckStrand(strand Strand, view *PaymentSandbox) tx.Result {
	for _, step := range strand {
		// Check if step implements Check method
		if checker, ok := step.(interface {
			Check(*PaymentSandbox) tx.Result
		}); ok {
			result := checker.Check(view)
			if result != tx.TesSUCCESS {
				return result
			}
		}
	}
	return tx.TesSUCCESS
}

// GetStrandQuality calculates the worst-case quality for a strand
func GetStrandQuality(strand Strand, view *PaymentSandbox) *Quality {
	if len(strand) == 0 {
		return nil
	}

	// Compose qualities from all steps
	// Start with quality 1.0 (identity for multiplication)
	// Must use proper STAmount encoding, not raw QualityOne value
	composedQuality := qualityFromFloat64(1.0)
	prevDir := DebtDirectionIssues

	for _, step := range strand {
		stepQuality, stepDir := step.QualityUpperBound(view, prevDir)
		if stepQuality == nil {
			return nil // Dry step
		}
		composedQuality = composedQuality.Compose(*stepQuality)
		prevDir = stepDir
	}

	return &composedQuality
}

// newDirectStepI creates a DirectStepI with the context's offerCrossing flag set.
func (ctx *StrandContext) newDirectStepI(src, dst [20]byte, currency string, prevStep Step, isFirst, isLast bool) *DirectStepI {
	step := NewDirectStepI(src, dst, currency, prevStep, isFirst, isLast)
	step.offerCrossing = ctx.OfferCrossing
	return step
}

// checkDirectStep validates a DirectStepI during strand building.
// For offer crossing, skips trust line existence and authorization checks
// but STILL runs the freeze check (common base class checks).
// Reference: rippled DirectStepI<TDerived>::check() runs checkFreeze before
// delegating to DirectIOfferCrossingStep::check() which returns tesSUCCESS.
func (ctx *StrandContext) checkDirectStep(step *DirectStepI, view *PaymentSandbox, prevStep Step) tx.Result {
	if ctx.OfferCrossing {
		// Run common checks that apply to both payments and offer crossing.
		// Reference: rippled DirectStepI<TDerived>::check() lines 906-912
		if !(step.isFirst && step.isLast) {
			if result := checkFreeze(view, step.src, step.dst, step.currency); result != tx.TesSUCCESS {
				return result
			}
		}
		return tx.TesSUCCESS
	}
	return step.CheckWithPrevStep(view, prevStep)
}

// StrandSourceIssue returns the source issue for a strand
func StrandSourceIssue(strand Strand) Issue {
	if len(strand) == 0 {
		return Issue{}
	}

	// First step determines source
	step := strand[0]

	// Check if XRP endpoint
	if accts := step.DirectStepAccts(); accts != nil {
		// Check if source is zero (XRP pseudo-account)
		var zeroAccount [20]byte
		if accts[0] == zeroAccount || accts[1] == zeroAccount {
			return Issue{Currency: "XRP"}
		}
	}

	// Check if book step
	if book := step.BookStepBook(); book != nil {
		return book.In
	}

	// Default to unknown
	return Issue{}
}

// StrandDestIssue returns the destination issue for a strand
func StrandDestIssue(strand Strand) Issue {
	if len(strand) == 0 {
		return Issue{}
	}

	// Last step determines destination
	step := strand[len(strand)-1]

	// Check if XRP endpoint
	if accts := step.DirectStepAccts(); accts != nil {
		var zeroAccount [20]byte
		if accts[0] == zeroAccount || accts[1] == zeroAccount {
			return Issue{Currency: "XRP"}
		}
	}

	// Check if book step
	if book := step.BookStepBook(); book != nil {
		return book.Out
	}

	return Issue{}
}

// Line returns a keylet for a trust line between two accounts
func Line(src, dst [20]byte, currency string) keylet.Keylet {
	return keylet.Line(src, dst, currency)
}
