package pathfinder

import (
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
)

// NodeType represents a type of node in a path search pattern.
// Reference: rippled Pathfinder.h NodeType enum
type NodeType int

const (
	ntSOURCE      NodeType = iota // Source account/currency
	ntACCOUNTS                    // Intermediate accounts via trust lines
	ntBOOKS                       // Order books (any currency)
	ntXRP_BOOK                    // Order books outputting XRP only
	ntDEST_BOOK                   // Order books outputting destination currency
	ntDESTINATION                 // Destination account
)

// PaymentType classifies the direction of a payment.
// Reference: rippled Pathfinder.h PaymentType enum
type PaymentType int

const (
	ptXRP_to_XRP       PaymentType = iota // XRP → XRP (no paths needed)
	ptXRP_to_nonXRP                       // XRP → IOU
	ptNonXRP_to_XRP                       // IOU → XRP
	ptNonXRP_to_same                      // IOU → same currency IOU
	ptNonXRP_to_nonXRP                    // IOU → different currency IOU
)

// PathType is a sequence of NodeTypes describing a search pattern.
type PathType []NodeType

// CostedPath pairs a search level with a path type pattern.
// Paths with lower searchLevel are tried first (cheaper/more common).
type CostedPath struct {
	SearchLevel int
	Type        PathType
}

// PathRank holds ranking data for a discovered complete path.
// Reference: rippled Pathfinder::PathRank
type PathRank struct {
	Quality   uint64               // Exchange rate (lower = better)
	Length    int                  // Number of hops
	Liquidity payment.EitherAmount // How much the path can deliver
	Index     int                  // Index into the completePaths slice
}

// AccountCandidate represents a potential intermediate account in a path.
type AccountCandidate struct {
	Priority int
	Account  [20]byte
}

// highPriority is assigned to direct destination candidates.
// Reference: rippled AccountCandidate::highPriority
const highPriority = 10000

// maxCompletePaths limits the total number of complete paths found by DFS.
// Reference: rippled PATHFINDER_MAX_COMPLETE_PATHS
const maxCompletePaths = 1000

// maxReturnedPaths is the maximum number of path alternatives returned.
// Reference: rippled PathRequest max_paths_ (hardcoded to 4)
const maxReturnedPaths = 4

// maxCandidatesFromSource is the maximum number of intermediate account
// candidates explored from the source account.
const maxCandidatesFromSource = 50

// maxCandidatesFromOther is the maximum number of intermediate account
// candidates explored from non-source accounts.
const maxCandidatesFromOther = 10

// maxAutoSrcCur is the maximum number of auto-discovered source currencies.
// Reference: rippled RPC::Tuning::max_auto_src_cur (88)
const maxAutoSrcCur = 88

// addFlags control what type of links are added during path extension.
// Reference: rippled Pathfinder.h afADD_ACCOUNTS, afADD_BOOKS, etc.
const (
	afADD_ACCOUNTS uint32 = 0x001 // Add ripple paths through accounts
	afADD_BOOKS    uint32 = 0x002 // Add order book paths
	afOB_XRP       uint32 = 0x010 // Restrict to books outputting XRP
	afOB_LAST      uint32 = 0x040 // Restrict to books outputting destination currency
	afAC_LAST      uint32 = 0x080 // Restrict to destination account only
)

// pathTable maps each PaymentType to its list of costed path patterns.
// Reference: rippled Pathfinder::initPathTable()
var pathTable map[PaymentType][]CostedPath

func init() {
	pathTable = make(map[PaymentType][]CostedPath)

	// XRP → XRP: no paths needed (default path only)
	pathTable[ptXRP_to_XRP] = nil

	// XRP → non-XRP
	pathTable[ptXRP_to_nonXRP] = []CostedPath{
		{1, PathType{ntSOURCE, ntDEST_BOOK, ntDESTINATION}},                                    // sfd
		{3, PathType{ntSOURCE, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},                        // sfad
		{5, PathType{ntSOURCE, ntDEST_BOOK, ntACCOUNTS, ntACCOUNTS, ntDESTINATION}},            // sfaad
		{6, PathType{ntSOURCE, ntBOOKS, ntDEST_BOOK, ntDESTINATION}},                           // sbfd
		{8, PathType{ntSOURCE, ntBOOKS, ntACCOUNTS, ntDEST_BOOK, ntDESTINATION}},               // sbafd
		{9, PathType{ntSOURCE, ntBOOKS, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},               // sbfad
		{10, PathType{ntSOURCE, ntBOOKS, ntACCOUNTS, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},  // sbafad
	}

	// non-XRP → XRP
	pathTable[ptNonXRP_to_XRP] = []CostedPath{
		{1, PathType{ntSOURCE, ntXRP_BOOK, ntDESTINATION}},                                           // sxd
		{2, PathType{ntSOURCE, ntACCOUNTS, ntXRP_BOOK, ntDESTINATION}},                               // saxd
		{6, PathType{ntSOURCE, ntACCOUNTS, ntACCOUNTS, ntXRP_BOOK, ntDESTINATION}},                   // saaxd
		{7, PathType{ntSOURCE, ntBOOKS, ntXRP_BOOK, ntDESTINATION}},                                  // sbxd
		{8, PathType{ntSOURCE, ntACCOUNTS, ntBOOKS, ntXRP_BOOK, ntDESTINATION}},                      // sabxd
		{9, PathType{ntSOURCE, ntACCOUNTS, ntBOOKS, ntACCOUNTS, ntXRP_BOOK, ntDESTINATION}},          // sabaxd
	}

	// non-XRP → same non-XRP
	pathTable[ptNonXRP_to_same] = []CostedPath{
		{1, PathType{ntSOURCE, ntACCOUNTS, ntDESTINATION}},                                                       // sad
		{1, PathType{ntSOURCE, ntDEST_BOOK, ntDESTINATION}},                                                      // sfd
		{4, PathType{ntSOURCE, ntACCOUNTS, ntDEST_BOOK, ntDESTINATION}},                                          // safd
		{4, PathType{ntSOURCE, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},                                          // sfad
		{5, PathType{ntSOURCE, ntACCOUNTS, ntACCOUNTS, ntDESTINATION}},                                           // saad
		{5, PathType{ntSOURCE, ntBOOKS, ntDEST_BOOK, ntDESTINATION}},                                             // sbfd
		{6, PathType{ntSOURCE, ntXRP_BOOK, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},                              // sxfad
		{6, PathType{ntSOURCE, ntACCOUNTS, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},                              // safad
		{6, PathType{ntSOURCE, ntACCOUNTS, ntXRP_BOOK, ntDEST_BOOK, ntDESTINATION}},                              // saxfd
		{6, PathType{ntSOURCE, ntACCOUNTS, ntXRP_BOOK, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},                  // saxfad
		{6, PathType{ntSOURCE, ntACCOUNTS, ntBOOKS, ntDEST_BOOK, ntDESTINATION}},                                 // sabfd
		{7, PathType{ntSOURCE, ntACCOUNTS, ntACCOUNTS, ntACCOUNTS, ntDESTINATION}},                               // saaad
	}

	// non-XRP → different non-XRP
	pathTable[ptNonXRP_to_nonXRP] = []CostedPath{
		{1, PathType{ntSOURCE, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},                                          // sfad
		{1, PathType{ntSOURCE, ntACCOUNTS, ntDEST_BOOK, ntDESTINATION}},                                          // safd
		{3, PathType{ntSOURCE, ntACCOUNTS, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},                              // safad
		{4, PathType{ntSOURCE, ntXRP_BOOK, ntDEST_BOOK, ntDESTINATION}},                                          // sxfd
		{5, PathType{ntSOURCE, ntACCOUNTS, ntXRP_BOOK, ntDEST_BOOK, ntDESTINATION}},                              // saxfd
		{5, PathType{ntSOURCE, ntXRP_BOOK, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},                              // sxfad
		{5, PathType{ntSOURCE, ntBOOKS, ntDEST_BOOK, ntDESTINATION}},                                             // sbfd
		{6, PathType{ntSOURCE, ntACCOUNTS, ntXRP_BOOK, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},                  // saxfad
		{6, PathType{ntSOURCE, ntACCOUNTS, ntBOOKS, ntDEST_BOOK, ntDESTINATION}},                                 // sabfd
		{7, PathType{ntSOURCE, ntACCOUNTS, ntACCOUNTS, ntDEST_BOOK, ntDESTINATION}},                              // saafd
		{8, PathType{ntSOURCE, ntACCOUNTS, ntACCOUNTS, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}},                  // saafad
		{9, PathType{ntSOURCE, ntACCOUNTS, ntDEST_BOOK, ntACCOUNTS, ntACCOUNTS, ntDESTINATION}},                  // safaad
	}
}

// pathTypeKey returns a string key for caching path type results.
func pathTypeKey(pt PathType) string {
	b := make([]byte, len(pt))
	for i, n := range pt {
		b[i] = byte(n)
	}
	return string(b)
}
