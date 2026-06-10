package pathfuzz

import (
	"fmt"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	ammb "github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
)

// maxPathQueries bounds the number of path-discovery requests per fuzz iteration.
const maxPathQueries = 8

// pathBudget bounds a single path-discovery request. Discovery searches at
// level 7 over the auto-discovered source currencies, but the
// scenario ledger is small, so a request completes in milliseconds; blowing the
// budget means the search failed to stay bounded.
const pathBudget = 8 * time.Second

// newPathScenario extends the offer-based scenario with an XRP/USD AMM pool so
// path discovery (and the RippleCalc it runs to validate paths) also considers
// AMM liquidity.
func newPathScenario(t testing.TB) *scenario {
	t.Helper()
	sc := newScenario(t)
	creator := sc.users[0]
	res := sc.env.Submit(ammb.AMMCreate(creator, ammb.XRPAmount(1000), ammb.IOUAmount(sc.gws[0].acct, "USD", 1000)).Build())
	if !res.Success {
		t.Fatalf("AMM pool setup failed: %s %s", res.Code, res.Message)
	}
	sc.env.Close()
	return sc
}

// runPathfinder drives one FuzzPathfinder iteration: build the seeded ledger and
// run the path-discovery query sequence encoded by data, asserting discovery
// never panics, stays within budget, and returns only well-formed alternatives.
func runPathfinder(t testing.TB, data []byte) {
	t.Helper()
	sc := newPathScenario(t)
	s := &stream{data: data}
	for i := 0; i < maxPathQueries && !s.drained(); i++ {
		sc.pathQuery(s)
	}
}

func (sc *scenario) pathQuery(s *stream) {
	from, to := sc.pickPair(s)

	var dst tx.Amount
	switch s.intn(3) {
	case 0:
		dst = sc.usd(iouValue(s) + 1)
	case 1:
		dst = sc.eur(iouValue(s) + 1)
	default:
		dst = xrpAmt(xrpDrops(s))
	}
	var sendMax *tx.Amount
	if s.chance(96) {
		sm := sc.usd(iouValue(s)*2 + 1)
		sendMax = &sm
	}

	start := time.Now()
	res := sc.execute(from, to, dst, sendMax)
	if d := time.Since(start); d > pathBudget {
		sc.t.Fatalf("pathfinding exceeded budget (%s > %s): %s -> %s deliver %s", d, pathBudget, from.Name, to.Name, fmtAmt(dst))
	}
	if res == nil {
		return
	}

	for i, alt := range res.Alternatives {
		// An alternative with empty paths_computed is valid: it means the
		// default path alone delivers the amount (rippled returns these too).
		// A returned alternative is one RippleCalc validated as delivering the
		// target, so its source cost must be non-negative — a negative source
		// amount would mean the path manufactures value.
		if alt.SourceAmount.Signum() < 0 {
			sc.t.Fatalf("path alternative %d has negative source amount %s (%s -> %s deliver %s)", i, fmtAmt(alt.SourceAmount), from.Name, to.Name, fmtAmt(dst))
		}
	}
}

// execute runs one path-discovery request, converting a panic into a test
// failure naming the inputs that reproduce it.
func (sc *scenario) execute(from, to *jtx.Account, dst tx.Amount, sendMax *tx.Amount) (res *pathfinder.PathRequestResult) {
	defer func() {
		if r := recover(); r != nil {
			sc.t.Fatalf("pathfinder panic: %s -> %s deliver %s: %v", from.Name, to.Name, fmtAmt(dst), r)
		}
	}()
	pr := pathfinder.NewPathRequest(from.ID, to.ID, dst, sendMax, nil, false)
	pr.SetSearchLevel(7)
	return pr.Execute(sc.env.Ledger())
}

// FuzzPathfinder runs path discovery for generated source/destination/amount
// triples and fails on a panic, a budget overrun, or a malformed alternative.
// Run it with, e.g.:
//
//	go test -run x -fuzz FuzzPathfinder ./internal/testing/pathfuzz/
//
// See the package doc and issue #685.
func FuzzPathfinder(f *testing.F) {
	for _, seed := range seedCorpus() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		runPathfinder(t, data)
	})
}

// TestPathfinder_SeedCorpus runs the seed corpus deterministically so the
// harness is exercised by plain `go test` / CI without the -fuzz flag.
func TestPathfinder_SeedCorpus(t *testing.T) {
	for i, seed := range seedCorpus() {
		t.Run(fmt.Sprintf("seed-%d", i), func(t *testing.T) {
			runPathfinder(t, seed)
		})
	}
}
