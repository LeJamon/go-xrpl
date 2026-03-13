package pathfinder

import (
	"sort"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/keylet"
)

// Pathfinder discovers payment paths through the XRPL using DFS.
// Reference: rippled Pathfinder class (Pathfinder.h/cpp)
type Pathfinder struct {
	srcAccount   [20]byte
	dstAccount   [20]byte
	effectiveDst [20]byte // Actual destination (may differ from dstAccount if gateway)

	srcCurrency string
	srcIssuer   [20]byte
	dstAmount   tx.Amount
	srcAmount   tx.Amount

	convertAll bool // true for partial payments (tfPartialPayment)

	ledger tx.LedgerView
	cache  *RippleLineCache
	books  *BookIndex

	// source represents the source path element.
	source payment.PathStep

	// completePaths holds all discovered complete paths.
	completePaths [][]payment.PathStep

	// pathRanks holds ranked paths after computePathRanks.
	pathRanks []PathRank

	// remainingAmount is dstAmount minus default path liquidity.
	// Set by ComputePathRanks, used by GetBestPaths.
	remainingAmount payment.EitherAmount

	// paths caches DFS results per PathType.
	paths map[string][][]payment.PathStep

	// pathsOutCount caches getPathsOut results per Issue.
	pathsOutCount map[payment.Issue]int
}

// NewPathfinder creates a new pathfinder for the given payment parameters.
// Reference: rippled Pathfinder constructor
func NewPathfinder(
	ledger tx.LedgerView,
	cache *RippleLineCache,
	srcAccount, dstAccount [20]byte,
	dstAmount tx.Amount,
	srcAmount tx.Amount,
	srcCurrency string,
	srcIssuer [20]byte,
	convertAll bool,
) *Pathfinder {
	// Determine effective destination.
	// If dstAmount is IOU and the issuer differs from dstAccount, the effective
	// destination is the issuer (gateway). Paths must reach the issuer.
	effectiveDst := dstAccount
	if !dstAmount.IsNative() {
		issuerID, err := state.DecodeAccountID(dstAmount.Issuer)
		if err == nil && issuerID != dstAccount {
			effectiveDst = issuerID
		}
	}

	// Build source path element
	source := payment.PathStep{
		Account:  state.EncodeAccountIDSafe(srcAccount),
		Currency: srcCurrency,
	}
	if srcCurrency != "XRP" && srcCurrency != "" {
		source.Issuer = state.EncodeAccountIDSafe(srcIssuer)
	}

	return &Pathfinder{
		srcAccount:    srcAccount,
		dstAccount:    dstAccount,
		effectiveDst:  effectiveDst,
		srcCurrency:   srcCurrency,
		srcIssuer:     srcIssuer,
		dstAmount:     dstAmount,
		srcAmount:     srcAmount,
		convertAll:    convertAll,
		ledger:        ledger,
		cache:         cache,
		books:         NewBookIndex(ledger),
		source:        source,
		paths:         make(map[string][][]payment.PathStep),
		pathsOutCount: make(map[payment.Issue]int),
	}
}

// CompletePaths returns the discovered complete paths.
func (pf *Pathfinder) CompletePaths() [][]payment.PathStep {
	return pf.completePaths
}

// PathRanks returns the ranked paths.
func (pf *Pathfinder) PathRanks() []PathRank {
	return pf.pathRanks
}

// FindPaths runs the DFS path discovery algorithm at the given search level.
// Higher levels explore more path patterns (more expensive).
// Returns true if pathfinding completed (even if no paths found — default path may work).
// Reference: rippled Pathfinder::findPaths()
func (pf *Pathfinder) FindPaths(searchLevel int) bool {
	// Zero destination amount — nothing to do
	if pf.dstAmount.IsZero() {
		return false
	}

	// Same account, same currency — no path needed
	dstCurrency := pf.dstAmount.Currency
	if pf.dstAmount.IsNative() {
		dstCurrency = "XRP"
	}
	if pf.srcAccount == pf.dstAccount &&
		pf.dstAccount == pf.effectiveDst &&
		pf.srcCurrency == dstCurrency {
		return false
	}

	// Source IS the effective destination with same currency — default path
	if pf.srcAccount == pf.effectiveDst && pf.srcCurrency == dstCurrency {
		return true
	}

	// Verify source account exists
	srcExists, _ := pf.ledger.Exists(keylet.Account(pf.srcAccount))
	if !srcExists {
		return false
	}

	// Verify effective destination exists
	if pf.effectiveDst != pf.dstAccount {
		effExists, _ := pf.ledger.Exists(keylet.Account(pf.effectiveDst))
		if !effExists {
			return false
		}
	}

	// Verify destination account exists (for non-XRP, must already exist)
	dstExists, _ := pf.ledger.Exists(keylet.Account(pf.dstAccount))
	if !dstExists {
		if !pf.dstAmount.IsNative() {
			return false
		}
		// For XRP payments, destination can be created if amount >= reserve
		// but we don't check reserve here — just note it may not exist
	}

	// Determine payment type
	pt := pf.classifyPayment()

	// Iterate path type table for this payment type
	table := pathTable[pt]
	for _, cp := range table {
		if cp.SearchLevel > searchLevel {
			continue
		}
		pf.addPathsForType(cp.Type)
		if len(pf.completePaths) > maxCompletePaths {
			break
		}
	}

	return true
}

// classifyPayment determines the PaymentType based on source/destination currencies.
// Reference: rippled Pathfinder::findPaths() payment type determination
func (pf *Pathfinder) classifyPayment() PaymentType {
	srcXRP := pf.srcCurrency == "XRP" || pf.srcCurrency == ""
	dstXRP := pf.dstAmount.IsNative()

	switch {
	case srcXRP && dstXRP:
		return ptXRP_to_XRP
	case srcXRP && !dstXRP:
		return ptXRP_to_nonXRP
	case !srcXRP && dstXRP:
		return ptNonXRP_to_XRP
	case !srcXRP && !dstXRP:
		dstCurrency := pf.dstAmount.Currency
		if pf.srcCurrency == dstCurrency {
			return ptNonXRP_to_same
		}
		return ptNonXRP_to_nonXRP
	}
	return ptXRP_to_XRP // unreachable
}

// addPathsForType recursively builds paths for the given PathType.
// Uses memoization via pf.paths cache.
// Reference: rippled Pathfinder::addPathsForType()
func (pf *Pathfinder) addPathsForType(pt PathType) [][]payment.PathStep {
	if len(pt) == 0 {
		return [][]payment.PathStep{{}} // Single empty path
	}

	key := pathTypeKey(pt)
	if cached, ok := pf.paths[key]; ok {
		return cached
	}

	// Recursively get parent paths (all nodes except the last)
	parentPaths := pf.addPathsForType(pt[:len(pt)-1])

	lastNode := pt[len(pt)-1]
	var result [][]payment.PathStep

	switch lastNode {
	case ntSOURCE:
		// Source produces a single empty path (start of all paths)
		result = [][]payment.PathStep{{}}

	case ntACCOUNTS:
		result = pf.addLinks(parentPaths, afADD_ACCOUNTS)

	case ntBOOKS:
		result = pf.addLinks(parentPaths, afADD_BOOKS)

	case ntXRP_BOOK:
		result = pf.addLinks(parentPaths, afADD_BOOKS|afOB_XRP)

	case ntDEST_BOOK:
		result = pf.addLinks(parentPaths, afADD_BOOKS|afOB_LAST)

	case ntDESTINATION:
		result = pf.addLinks(parentPaths, afADD_ACCOUNTS|afAC_LAST)
	}

	pf.paths[key] = result
	return result
}

// addLinks extends each parent path by one hop based on addFlags.
// Returns incomplete paths (for further extension by subsequent calls).
// Complete paths are appended to pf.completePaths.
// Reference: rippled Pathfinder::addLink()
func (pf *Pathfinder) addLinks(parentPaths [][]payment.PathStep, addFlags uint32) [][]payment.PathStep {
	var incompletePaths [][]payment.PathStep

	for _, currentPath := range parentPaths {
		pf.addLink(currentPath, &incompletePaths, addFlags)
	}

	return incompletePaths
}

// addLink extends a single path by one hop.
// Reference: rippled Pathfinder::addLink()
func (pf *Pathfinder) addLink(currentPath []payment.PathStep, incompletePaths *[][]payment.PathStep, addFlags uint32) {
	// Get current path endpoint
	var endAccount [20]byte
	var endCurrency string
	var endIssuer [20]byte

	if len(currentPath) == 0 {
		// Path is empty — use source
		endAccount = pf.srcAccount
		endCurrency = pf.srcCurrency
		endIssuer = pf.srcIssuer
	} else {
		last := currentPath[len(currentPath)-1]
		if last.Account != "" {
			endAccount, _ = state.DecodeAccountID(last.Account)
		}
		endCurrency = last.Currency
		if endCurrency == "" {
			endCurrency = pf.srcCurrency
		}
		if last.Issuer != "" {
			endIssuer, _ = state.DecodeAccountID(last.Issuer)
		} else if last.Account != "" {
			endIssuer = endAccount
		}
	}

	bOnXRP := endCurrency == "XRP" || endCurrency == ""
	hasEffectiveDst := pf.effectiveDst != pf.dstAccount
	dstCurrency := pf.dstAmount.Currency
	if pf.dstAmount.IsNative() {
		dstCurrency = "XRP"
	}

	// Handle account paths (rippling through trust lines)
	if addFlags&afADD_ACCOUNTS != 0 {
		pf.addAccountLinks(currentPath, incompletePaths, addFlags, endAccount, endCurrency, endIssuer, bOnXRP, hasEffectiveDst, dstCurrency)
	}

	// Handle order book paths
	if addFlags&afADD_BOOKS != 0 {
		pf.addBookLinks(currentPath, incompletePaths, addFlags, endCurrency, endIssuer, bOnXRP, hasEffectiveDst, dstCurrency)
	}
}

// addAccountLinks extends a path through trust lines.
// Reference: rippled Pathfinder::addLink() afADD_ACCOUNTS section
func (pf *Pathfinder) addAccountLinks(
	currentPath []payment.PathStep,
	incompletePaths *[][]payment.PathStep,
	addFlags uint32,
	endAccount [20]byte,
	endCurrency string,
	endIssuer [20]byte,
	bOnXRP bool,
	hasEffectiveDst bool,
	dstCurrency string,
) {
	if bOnXRP {
		// On XRP — if destination is XRP and path is non-empty, it's complete
		if pf.dstAmount.IsNative() && len(currentPath) > 0 {
			pf.addUniquePath(currentPath)
		}
		return
	}

	// Check if endpoint account exists
	acctData, err := pf.ledger.Read(keylet.Account(endAccount))
	if err != nil || acctData == nil {
		return
	}
	acctRoot, err := state.ParseAccountRoot(acctData)
	if err != nil {
		return
	}

	bRequireAuth := acctRoot.Flags&state.LsfRequireAuth != 0
	bIsEndCurrency := endCurrency == dstCurrency
	bIsNoRippleOut := pf.isNoRippleOut(currentPath)
	bDestOnly := addFlags&afAC_LAST != 0

	// Get trust lines from cache
	lineDirection := LineDirectionOutgoing
	if bIsNoRippleOut {
		lineDirection = LineDirectionIncoming
	}
	lines := pf.cache.GetRippleLines(endAccount, lineDirection)

	// Peer direction is the opposite of the direction used to retrieve lines.
	// Reference: rippled rs.getDirectionPeer()
	peerDirection := LineDirectionIncoming
	if bIsNoRippleOut {
		peerDirection = LineDirectionOutgoing
	}

	var candidates []AccountCandidate

	for _, line := range lines {
		peerAcct := line.AccountIDPeer

		// Skip if effective destination exists and this is the gateway account
		if hasEffectiveDst && peerAcct == pf.dstAccount {
			continue
		}

		bToDestination := peerAcct == pf.effectiveDst

		// If DESTINATION only, skip non-destination accounts
		if bDestOnly && !bToDestination {
			continue
		}

		// Currency must match
		if line.Currency != endCurrency {
			continue
		}

		// Check for path loops
		if pathHasSeen(currentPath, peerAcct, endCurrency) {
			continue
		}

		// Check if there's usable credit on this line
		balSig := line.Balance.Signum()
		if balSig <= 0 {
			// No positive balance — check if peer extends credit
			if line.LimitPeer.IsZero() || line.LimitPeer.IsNegative() {
				continue // No credit available
			}
			negBal := line.Balance.Negate()
			if negBal.Compare(line.LimitPeer) >= 0 {
				continue // Credit line fully used
			}
			if bRequireAuth && !line.Auth {
				continue // Requires auth we don't have
			}
		}

		// No-ripple check
		if bIsNoRippleOut && line.NoRipple {
			continue
		}

		if bToDestination {
			if bIsEndCurrency {
				// Complete path!
				if len(currentPath) > 0 {
					pf.addUniquePath(currentPath)
				}
			} else if !bDestOnly {
				// High priority candidate
				candidates = append(candidates, AccountCandidate{
					Priority: highPriority,
					Account:  peerAcct,
				})
			}
		} else if peerAcct == pf.srcAccount {
			// Skip — can't loop back to source
			continue
		} else {
			// Regular candidate — score by paths out
			out := pf.getPathsOut(
				payment.Issue{Currency: endCurrency, Issuer: peerAcct},
				peerDirection,
				bIsEndCurrency,
			)
			if out > 0 {
				candidates = append(candidates, AccountCandidate{
					Priority: out,
					Account:  peerAcct,
				})
			}
		}
	}

	if len(candidates) == 0 {
		return
	}

	// Sort candidates by priority (descending)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Priority > candidates[j].Priority
	})

	// Limit candidates
	maxCandidates := maxCandidatesFromSource
	if endAccount != pf.srcAccount && len(candidates) > maxCandidatesFromOther {
		maxCandidates = maxCandidatesFromOther
	}
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	// Add each candidate as a path extension
	for _, c := range candidates {
		newPath := make([]payment.PathStep, len(currentPath), len(currentPath)+1)
		copy(newPath, currentPath)
		newPath = append(newPath, payment.PathStep{
			Account:  state.EncodeAccountIDSafe(c.Account),
			Currency: endCurrency,
			Issuer:   state.EncodeAccountIDSafe(c.Account),
			Type:     0x01, // typeAccount
		})
		*incompletePaths = append(*incompletePaths, newPath)
	}
}

// addBookLinks extends a path through order books.
// Reference: rippled Pathfinder::addLink() afADD_BOOKS section
func (pf *Pathfinder) addBookLinks(
	currentPath []payment.PathStep,
	incompletePaths *[][]payment.PathStep,
	addFlags uint32,
	endCurrency string,
	endIssuer [20]byte,
	bOnXRP bool,
	hasEffectiveDst bool,
	dstCurrency string,
) {
	endIssue := payment.Issue{Currency: endCurrency, Issuer: endIssuer}

	if addFlags&afOB_XRP != 0 {
		// XRP book only — add path through book to XRP
		if !bOnXRP && pf.books.IsBookToXRP(endIssue) {
			newPath := make([]payment.PathStep, len(currentPath), len(currentPath)+1)
			copy(newPath, currentPath)
			newPath = append(newPath, payment.PathStep{
				Currency: "XRP",
				Type:     0x10, // typeCurrency
			})
			if pf.dstAmount.IsNative() {
				// Destination is XRP — complete!
				pf.addUniquePath(newPath)
			} else {
				*incompletePaths = append(*incompletePaths, newPath)
			}
		}
		return
	}

	// All order books (or destination-only books)
	bDestOnly := addFlags&afOB_LAST != 0

	bookOutputs := pf.books.GetBooksByTakerPays(endIssue)
	for _, bookOut := range bookOutputs {
		// Check path hasn't seen this issue (with xrpAccount as first arg — matches book steps)
		if pathHasSeenBookIssue(currentPath, bookOut) {
			continue
		}

		// Skip if book output matches the source origin
		// Reference: rippled Pathfinder::issueMatchesOrigin()
		if pf.issueMatchesOrigin(bookOut) {
			continue
		}

		// If destination only, restrict to destination currency
		if bDestOnly && bookOut.Currency != dstCurrency {
			continue
		}

		newPath := make([]payment.PathStep, len(currentPath), len(currentPath)+2)
		copy(newPath, currentPath)

		if bookOut.IsXRP() {
			// Book leads to XRP
			newPath = append(newPath, payment.PathStep{
				Currency: "XRP",
				Type:     0x10, // typeCurrency
			})
			if pf.dstAmount.IsNative() {
				// Complete!
				pf.addUniquePath(newPath)
			} else {
				*incompletePaths = append(*incompletePaths, newPath)
			}
		} else {
			// Check if we've already seen this issuer account with this currency
			if pathHasSeen(currentPath, bookOut.Issuer, bookOut.Currency) {
				continue
			}

			issuerStr := state.EncodeAccountIDSafe(bookOut.Issuer)

			// Path compression: book -> account -> book
			// If the last two steps are an offer followed by an account,
			// replace the account step with this book step.
			// Reference: rippled Pathfinder::addLink() book compression
			if len(newPath) >= 2 &&
				newPath[len(newPath)-1].Type == 0x01 && // typeAccount
				(newPath[len(newPath)-2].Type == 0x10 || newPath[len(newPath)-2].Type == 0x30) { // typeCurrency or typeCurrency|typeIssuer
				newPath[len(newPath)-1] = payment.PathStep{
					Currency: bookOut.Currency,
					Issuer:   issuerStr,
					Type:     0x30, // typeCurrency | typeIssuer
				}
			} else {
				// Add currency+issuer step
				newPath = append(newPath, payment.PathStep{
					Currency: bookOut.Currency,
					Issuer:   issuerStr,
					Type:     0x30, // typeCurrency | typeIssuer
				})
			}

			if hasEffectiveDst && bookOut.Issuer == pf.dstAccount && bookOut.Currency == dstCurrency {
				// Would skip required issuer — skip this book
				continue
			}

			if bookOut.Issuer == pf.effectiveDst && bookOut.Currency == dstCurrency {
				// Complete!
				pf.addUniquePath(newPath)
			} else {
				// Add issuer as account step for further extension
				newPath2 := make([]payment.PathStep, len(newPath), len(newPath)+1)
				copy(newPath2, newPath)
				newPath2 = append(newPath2, payment.PathStep{
					Account:  issuerStr,
					Currency: bookOut.Currency,
					Issuer:   issuerStr,
					Type:     0x01, // typeAccount
				})
				*incompletePaths = append(*incompletePaths, newPath2)
			}
		}
	}
}

// getPathsOut returns the number of outgoing paths from the given issue.
// Higher values mean more routing options through this account/currency.
// The direction parameter controls which trust lines are considered for the peer.
// Reference: rippled Pathfinder::getPathsOut()
func (pf *Pathfinder) getPathsOut(issue payment.Issue, direction LineDirection, isDstCurrency bool) int {
	if cached, ok := pf.pathsOutCount[issue]; ok {
		return cached
	}

	// Check if account exists and is not globally frozen
	acctData, err := pf.ledger.Read(keylet.Account(issue.Issuer))
	if err != nil || acctData == nil {
		pf.pathsOutCount[issue] = 0
		return 0
	}
	acctRoot, err := state.ParseAccountRoot(acctData)
	if err != nil {
		pf.pathsOutCount[issue] = 0
		return 0
	}
	if acctRoot.Flags&state.LsfGlobalFreeze != 0 {
		pf.pathsOutCount[issue] = 0
		return 0
	}

	bRequireAuth := acctRoot.Flags&state.LsfRequireAuth != 0
	count := 0

	// Count order books for this issue
	bookOutputs := pf.books.GetBooksByTakerPays(issue)
	count += len(bookOutputs)

	// Count usable trust lines
	lines := pf.cache.GetRippleLines(issue.Issuer, direction)
	for _, line := range lines {
		if line.Currency != issue.Currency {
			continue
		}

		// Check if there's credit available
		balSig := line.Balance.Signum()
		if balSig <= 0 {
			if line.LimitPeer.IsZero() || line.LimitPeer.IsNegative() {
				continue
			}
			negBal := line.Balance.Negate()
			if negBal.Compare(line.LimitPeer) >= 0 {
				continue
			}
			if bRequireAuth && !line.Auth {
				continue
			}
		}

		// Direct path to destination = high priority
		if isDstCurrency && line.AccountIDPeer == pf.effectiveDst {
			count += highPriority
		} else if line.NoRipplePeer {
			// Peer has no-ripple — not useful for routing
			continue
		} else if line.FreezePeer {
			// Peer frozen — not useful
			continue
		} else {
			count++
		}
	}

	pf.pathsOutCount[issue] = count
	return count
}

// isNoRippleOut checks if the last hop of the path has no-ripple set
// on the outgoing side.
// Reference: rippled Pathfinder::isNoRippleOut()
func (pf *Pathfinder) isNoRippleOut(currentPath []payment.PathStep) bool {
	if len(currentPath) == 0 {
		return false
	}
	last := currentPath[len(currentPath)-1]
	if last.Account == "" {
		return false
	}

	// Get "from" account
	var fromAccount [20]byte
	if len(currentPath) == 1 {
		fromAccount = pf.srcAccount
	} else {
		prev := currentPath[len(currentPath)-2]
		if prev.Account != "" {
			fromAccount, _ = state.DecodeAccountID(prev.Account)
		}
	}

	toAccount, _ := state.DecodeAccountID(last.Account)
	return pf.isNoRipple(fromAccount, toAccount, last.Currency)
}

// isNoRipple checks if no-ripple is set between two accounts for a currency.
// Reference: rippled Pathfinder::isNoRipple()
func (pf *Pathfinder) isNoRipple(fromAccount, toAccount [20]byte, currency string) bool {
	if currency == "XRP" || currency == "" {
		return false
	}
	lineKey := keylet.Line(toAccount, fromAccount, currency)
	data, err := pf.ledger.Read(lineKey)
	if err != nil || data == nil {
		return false
	}
	rs, err := state.ParseRippleState(data)
	if err != nil {
		return false
	}

	// Determine which side toAccount is on
	lowAcct, _ := state.DecodeAccountID(rs.LowLimit.Issuer)
	if toAccount == lowAcct {
		// toAccount is low → check LowNoRipple
		return rs.Flags&state.LsfLowNoRipple != 0
	}
	// toAccount is high → check HighNoRipple
	return rs.Flags&state.LsfHighNoRipple != 0
}

// addUniquePath adds a path to completePaths if it's not already present.
// Path steps are sanitized: account-type steps keep only Account,
// currency/issuer-type steps keep only Currency and Issuer.
// This matches rippled's STPathElement serialization where typeAccount
// elements do not include currency/issuer fields.
func (pf *Pathfinder) addUniquePath(path []payment.PathStep) {
	// Sanitize first so duplicate checking works correctly
	pathCopy := make([]payment.PathStep, len(path))
	for i, step := range path {
		if step.Type == 0x01 { // typeAccount
			// Account-only step: strip Currency and Issuer
			pathCopy[i] = payment.PathStep{
				Account: step.Account,
				Type:    step.Type,
			}
		} else {
			pathCopy[i] = step
		}
	}
	for _, existing := range pf.completePaths {
		if pathsEqual(existing, pathCopy) {
			return
		}
	}
	pf.completePaths = append(pf.completePaths, pathCopy)
}

// issueMatchesOrigin returns true if the given issue matches the source currency/issuer.
// Reference: rippled Pathfinder::issueMatchesOrigin()
func (pf *Pathfinder) issueMatchesOrigin(issue payment.Issue) bool {
	matchingCurrency := issue.Currency == pf.srcCurrency
	matchingAccount := issue.IsXRP() ||
		(pf.srcIssuer != [20]byte{} && issue.Issuer == pf.srcIssuer) ||
		issue.Issuer == pf.srcAccount
	return matchingCurrency && matchingAccount
}

// pathHasSeenBookIssue checks if a book step with this currency+issuer already exists.
// This uses a zero account for matching (book steps don't have real accounts).
// Reference: rippled STPath::hasSeen(xrpAccount(), currency, issuer)
func pathHasSeenBookIssue(path []payment.PathStep, issue payment.Issue) bool {
	issuerStr := state.EncodeAccountIDSafe(issue.Issuer)
	for _, step := range path {
		// Match book steps (typeCurrency or typeCurrency|typeIssuer)
		if step.Account == "" && step.Currency == issue.Currency {
			if issue.IsXRP() || step.Issuer == issuerStr {
				return true
			}
		}
	}
	return false
}

// pathHasSeen returns true if the path already visits the given account+currency.
func pathHasSeen(path []payment.PathStep, account [20]byte, currency string) bool {
	acctStr := state.EncodeAccountIDSafe(account)
	for _, step := range path {
		if step.Account == acctStr {
			stepCurrency := step.Currency
			if stepCurrency == "" {
				stepCurrency = "XRP"
			}
			if stepCurrency == currency {
				return true
			}
		}
	}
	return false
}

// pathHasSeenIssue returns true if the path already visits the given issue.
func pathHasSeenIssue(path []payment.PathStep, issue payment.Issue) bool {
	issuerStr := state.EncodeAccountIDSafe(issue.Issuer)
	for _, step := range path {
		if step.Currency == issue.Currency {
			if step.Issuer == issuerStr || step.Account == issuerStr {
				return true
			}
		}
	}
	return false
}

// pathsEqual returns true if two paths have the same steps.
func pathsEqual(a, b []payment.PathStep) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
