package enginefuzz

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/accountset"
	"github.com/LeJamon/goXRPLd/internal/testing/offer"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// maxSteps bounds the number of generated transactions per fuzz iteration and
// maxCloses the number of ledger closes. Each iteration rebuilds the seeded
// ledger from scratch -- which keeps iterations independent and reproducible --
// so these bounds cap the per-iteration heap, keeping many concurrent fuzz
// workers within memory and the engine the dominant cost rather than setup.
const (
	maxSteps  = 40
	maxCloses = 2
)

// invariantTec and invariantTef are the engine result codes that signal an
// invariant check rejected the transaction. They are the harness's primary
// oracle: the engine runs the full internal/tx/invariants set on every apply
// and squashes any violation into one of these codes.
var (
	invariantTec = tx.TecINVARIANT_FAILED.String()
	invariantTef = tx.TefINVARIANT_FAILED.String()
)

// run drives one fuzz iteration: build a seeded ledger and apply the
// transaction sequence encoded by data, asserting after every apply that the
// engine never reports an invariant violation and never inflates total XRP.
func run(t testing.TB, data []byte) {
	t.Helper()
	sc := newScenario(t)
	s := &stream{data: data}

	baseline := sc.totalXRP()
	closes := 0
	for i := 0; i < maxSteps && !s.drained(); i++ {
		sc.step(s)
		sc.requireXRPNotInflated(baseline)
		// Occasionally close the ledger to advance state and exercise
		// cross-ledger behaviour (sequence threading, owner counts, retries).
		if closes < maxCloses && !s.drained() && s.chance(24) {
			sc.env.Close()
			closes++
		}
	}
}

// scenario is a freshly seeded ledger: a gateway that has issued IOU balances to
// a fixed set of funded user accounts, giving the generated transaction stream
// liquidity to move XRP and IOUs and offers to cross.
type scenario struct {
	t          testing.TB
	env        *jtx.TestEnv
	gw         *jtx.Account
	users      []*jtx.Account
	currencies []string
}

func newScenario(t testing.TB) *scenario {
	t.Helper()
	env := jtx.NewTestEnv(t)

	gw := jtx.NewAccount("gw")
	users := []*jtx.Account{
		jtx.NewAccount("alice"),
		jtx.NewAccount("bob"),
		jtx.NewAccount("carol"),
		jtx.NewAccount("dave"),
	}
	currencies := []string{"USD", "EUR"}

	// Fund the gateway and users generously so reserves rarely block setup and
	// there is headroom for the generated stream.
	env.FundAmount(gw, uint64(jtx.XRP(1_000_000)))
	for _, u := range users {
		env.FundAmount(u, uint64(jtx.XRP(1_000_000)))
	}
	env.Close()

	// Trust the gateway and seed each user with an IOU balance so IOU payments
	// and IOU/XRP offers have something to move. Both helpers self-assert
	// success; gateway-issued IOUs against a high trust limit always succeed.
	for _, u := range users {
		for _, ccy := range currencies {
			env.Trust(u, tx.NewIssuedAmountFromFloat64(1_000_000_000, ccy, gw.Address))
			env.PayIOU(gw, u, gw, ccy, 1_000_000)
		}
	}
	env.Close()

	return &scenario{t: t, env: env, gw: gw, users: users, currencies: currencies}
}

// totalXRP sums the XRP held by every account the scenario can touch. The
// generated transaction set (Payment/AccountSet/TrustSet/Offer*) never escrows
// or locks XRP, so this sum is the full XRP supply under test and may only
// decrease as fees are burned.
func (sc *scenario) totalXRP() uint64 {
	total := sc.env.Balance(sc.env.MasterAccount()) + sc.env.Balance(sc.gw)
	for _, u := range sc.users {
		total += sc.env.Balance(u)
	}
	return total
}

func (sc *scenario) requireXRPNotInflated(baseline uint64) {
	if got := sc.totalXRP(); got > baseline {
		sc.t.Fatalf("XRP inflated: total %d drops exceeds baseline %d drops", got, baseline)
	}
}

// txKind enumerates the transaction shapes the generator emits. Extend this
// (and the switch in step) with new kinds -- e.g. AMM, MPT, NFToken, Escrow --
// to widen invariant coverage; all supported amendments are already enabled in
// the seeded environment.
type txKind int

const (
	kindPaymentXRP txKind = iota
	kindPaymentIOU
	kindAccountSet
	kindTrustSet
	kindOfferCreate
	kindOfferCancel
	numKinds
)

// step builds one transaction from the byte stream and submits it, failing only
// if the engine reports an invariant violation. Any other result code (a
// tem/tec/ter rejection of an implausible transaction) is expected and ignored.
func (sc *scenario) step(s *stream) {
	var (
		built tx.Transaction
		desc  string
	)

	switch txKind(s.intn(int(numKinds))) {
	case kindPaymentXRP:
		from, to := sc.pickPair(s)
		b := payment.Pay(from, to, sc.xrpDrops(s))
		if s.chance(32) {
			b = b.PartialPayment()
		}
		built, desc = b.Build(), "payment-xrp"

	case kindPaymentIOU:
		from, to := sc.pickPair(s)
		amt := tx.NewIssuedAmountFromFloat64(sc.iouValue(s), sc.pickCurrency(s), sc.gw.Address)
		b := payment.PayIssued(from, to, amt)
		if s.chance(32) {
			b = b.PartialPayment()
		}
		built, desc = b.Build(), "payment-iou"

	case kindAccountSet:
		built, desc = sc.buildAccountSet(s), "accountset"

	case kindTrustSet:
		u := sc.pickUser(s)
		limit := fmt.Sprintf("%d", 1+s.intn(1_000_000))
		b := trustset.TrustLine(u, sc.pickCurrency(s), sc.gw, limit)
		switch s.intn(4) {
		case 1:
			b = b.NoRipple()
		case 2:
			b = b.Freeze()
		case 3:
			b = b.ClearFreeze()
		}
		built, desc = b.Build(), "trustset"

	case kindOfferCreate:
		built, desc = sc.buildOfferCreate(s), "offercreate"

	case kindOfferCancel:
		u := sc.pickUser(s)
		built, desc = offer.OfferCancel(u, 1+s.u32()%64).Build(), "offercancel"
	}

	if built == nil {
		return
	}
	res := sc.env.Submit(built)
	if res.Code == invariantTec || res.Code == invariantTef {
		sc.t.Fatalf("engine reported invariant violation %q after %s: %s", res.Code, desc, res.Message)
	}
}

func (sc *scenario) buildAccountSet(s *stream) tx.Transaction {
	b := accountset.AccountSet(sc.pickUser(s))
	switch s.intn(8) {
	case 0:
		b = b.RequireDest()
	case 1:
		b = b.DefaultRipple()
	case 2:
		b = b.DepositAuth()
	case 3:
		b = b.NoFreeze()
	case 4:
		b = b.GlobalFreeze()
	case 5:
		b = b.DisallowXRP()
	default:
		// A bare AccountSet still threads the account root through the engine.
	}
	return b.Build()
}

func (sc *scenario) buildOfferCreate(s *stream) tx.Transaction {
	u := sc.pickUser(s)
	iou := tx.NewIssuedAmountFromFloat64(sc.iouValue(s), sc.pickCurrency(s), sc.gw.Address)
	b := offer.OfferCreateXRP(u, sc.xrpDrops(s), iou, s.chance(128))
	switch s.intn(8) {
	case 1:
		b = b.Passive()
	case 2:
		b = b.ImmediateOrCancel()
	case 3:
		b = b.FillOrKill()
	case 4:
		b = b.Sell()
	}
	return b.Build()
}

func (sc *scenario) pickUser(s *stream) *jtx.Account {
	return sc.users[s.intn(len(sc.users))]
}

// pickPair returns two distinct users (from != to) so self-payments don't
// dominate the stream.
func (sc *scenario) pickPair(s *stream) (from, to *jtx.Account) {
	i := s.intn(len(sc.users))
	j := s.intn(len(sc.users))
	if i == j {
		j = (j + 1) % len(sc.users)
	}
	return sc.users[i], sc.users[j]
}

func (sc *scenario) pickCurrency(s *stream) string {
	return sc.currencies[s.intn(len(sc.currencies))]
}

// xrpDrops returns a bounded XRP amount in drops (1 .. ~10000 XRP) so amounts
// stay plausible and never overflow.
func (sc *scenario) xrpDrops(s *stream) uint64 {
	return 1 + uint64(s.u32())%uint64(jtx.XRP(10_000))
}

// iouValue returns a bounded IOU value with 6 decimal places of precision.
func (sc *scenario) iouValue(s *stream) float64 {
	return float64(int64(s.u32())+1) / 1e6
}

// stream is a deterministic, drainable reader over the fuzzer's input bytes.
// Each accessor consumes bytes; once the input is exhausted drained reports true
// and the accessors return zero values, terminating generation. Being driven
// purely by the input bytes (with a ManualClock and name-derived deterministic
// accounts) makes every iteration fully reproducible.
type stream struct {
	data []byte
	pos  int
}

func (s *stream) drained() bool { return s.pos >= len(s.data) }

func (s *stream) u8() byte {
	if s.pos >= len(s.data) {
		s.pos++
		return 0
	}
	b := s.data[s.pos]
	s.pos++
	return b
}

func (s *stream) u32() uint32 {
	b0 := uint32(s.u8())
	b1 := uint32(s.u8())
	b2 := uint32(s.u8())
	b3 := uint32(s.u8())
	return b0<<24 | b1<<16 | b2<<8 | b3
}

// intn returns a value in [0, n); n must be positive.
func (s *stream) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(s.u8()) % n
}

// chance returns true with probability ~ num/256.
func (s *stream) chance(num int) bool {
	return int(s.u8()) < num
}
