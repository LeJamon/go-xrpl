package pathfuzz

import (
	"fmt"
	"math"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	accountsetb "github.com/LeJamon/go-xrpl/internal/testing/accountset"
	offerb "github.com/LeJamon/go-xrpl/internal/testing/offer"
	paymentb "github.com/LeJamon/go-xrpl/internal/testing/payment"
	trustsetb "github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// maxSteps bounds the number of generated transactions per fuzz iteration.
// Each iteration rebuilds the seeded ledger from scratch, so this caps the
// per-iteration heap and keeps the flow engine the dominant cost.
const maxSteps = 30

// stepBudget bounds a single payment apply. The flow loop has internal guards
// (maxTries = 1000, maxOffersToConsider = 1500 in flow.go), so a well-behaved
// payment terminates in well under a millisecond on this small ledger; blowing
// the budget means a guard failed to bound the work — a pathological,
// near-unbounded loop. A genuine non-terminating loop manifests as a fuzz/CI
// hang on the saved crasher input.
const stepBudget = 5 * time.Second

// invariantTec / invariantTef are the engine result codes that signal an
// invariant check rejected the transaction — the same oracle internal/tx/invariants
// runs on every apply (and that rippled exercises under Antithesis). A hit is by
// construction a consensus-safety bug.
var (
	invariantTec = tx.TecINVARIANT_FAILED.String()
	invariantTef = tx.TefINVARIANT_FAILED.String()
)

// gateway is an issuer paired with the single currency it issues in the scenario.
type gateway struct {
	acct     *jtx.Account
	currency string
}

// scenario is a freshly seeded ledger with multi-hop liquidity: two gateways
// (USD and EUR), four funded users that trust both gateways and hold IOU
// balances, baseline order books across USD/XRP, EUR/XRP and USD/EUR, and a
// transfer rate on the USD gateway so flow exercises transfer fees.
type scenario struct {
	t     testing.TB
	env   *jtx.TestEnv
	gws   []gateway
	users []*jtx.Account
}

func newScenario(t testing.TB) *scenario {
	t.Helper()
	env := jtx.NewTestEnv(t)

	gws := []gateway{
		{acct: jtx.NewAccount("pf-gw-usd"), currency: "USD"},
		{acct: jtx.NewAccount("pf-gw-eur"), currency: "EUR"},
	}
	users := []*jtx.Account{
		jtx.NewAccount("pf-alice"),
		jtx.NewAccount("pf-bob"),
		jtx.NewAccount("pf-carol"),
		jtx.NewAccount("pf-dave"),
	}

	// Fund generously so reserves rarely block setup or the generated stream.
	for _, g := range gws {
		env.FundAmount(g.acct, uint64(jtx.XRP(1_000_000)))
	}
	for _, u := range users {
		env.FundAmount(u, uint64(jtx.XRP(1_000_000)))
	}
	env.Close()

	sc := &scenario{t: t, env: env, gws: gws, users: users}

	// A transfer rate on the USD gateway makes USD ripple through it pay a fee,
	// exercising the flow engine's transfer-rate math.
	env.SetTransferRate(gws[0].acct, 1_005_000_000) // 100.5%

	// Trust both gateways and seed each user with an IOU balance so IOU and
	// cross-currency payments and offers have something to move.
	for _, u := range users {
		for _, g := range gws {
			env.Trust(u, tx.NewIssuedAmountFromFloat64(1_000_000_000, g.currency, g.acct.Address))
			env.PayIOU(g.acct, u, g.acct, g.currency, 100_000)
		}
	}
	env.Close()

	sc.seedBooks()
	env.Close()

	return sc
}

// seedBooks lays down a baseline of offers in each order book so the first
// generated payments have liquidity to cross before the stream adds more.
func (sc *scenario) seedBooks() {
	u := sc.users
	// USD/XRP at ~1:1.
	sc.env.CreateOffer(u[0], sc.usd(1000), xrpAmt(uint64(jtx.XRP(1000)))) // sell USD for XRP
	sc.env.CreateOffer(u[1], xrpAmt(uint64(jtx.XRP(1000))), sc.usd(1000)) // sell XRP for USD
	// EUR/XRP at ~1:0.9.
	sc.env.CreateOffer(u[2], sc.eur(1000), xrpAmt(uint64(jtx.XRP(900))))
	sc.env.CreateOffer(u[3], xrpAmt(uint64(jtx.XRP(900))), sc.eur(1000))
	// USD/EUR direct, ~1:0.95.
	sc.env.CreateOffer(u[0], sc.usd(1000), sc.eur(950))
	sc.env.CreateOffer(u[2], sc.eur(950), sc.usd(1000))
}

func (sc *scenario) usd(v float64) tx.Amount { return sc.gws[0].acct.IOU("USD", v) }
func (sc *scenario) eur(v float64) tx.Amount { return sc.gws[1].acct.IOU("EUR", v) }

func xrpAmt(drops uint64) tx.Amount { return tx.NewXRPAmount(int64(drops)) }

// totalXRP sums the XRP held by every account in the scenario. The generated
// transactions (payments, offers, trust sets, account sets) never escrow or lock
// XRP and never create new accounts, so this fixed set is the full XRP supply
// under test and may only decrease as fees burn.
func (sc *scenario) totalXRP() uint64 {
	total := sc.env.Balance(sc.env.MasterAccount())
	for _, g := range sc.gws {
		total += sc.env.Balance(g.acct)
	}
	for _, u := range sc.users {
		total += sc.env.Balance(u)
	}
	return total
}

// deliveryCheck captures what a generated payment promised so the oracle can
// validate the delivered amount against it after the apply.
type deliveryCheck struct {
	desc       string
	requested  tx.Amount
	sendMax    *tx.Amount
	deliverMin *tx.Amount
	partial    bool
}

// run drives one FuzzPaymentFlow iteration: build a seeded ledger and apply the
// transaction sequence encoded by data, asserting the payment-engine property
// oracles after every apply.
func run(t testing.TB, data []byte) {
	t.Helper()
	sc := newScenario(t)
	s := &stream{data: data}

	baseline := sc.totalXRP()
	closes := 0
	for i := 0; i < maxSteps && !s.drained(); i++ {
		sc.step(s, baseline)
		if closes < 2 && !s.drained() && s.chance(24) {
			sc.env.Close()
			closes++
		}
	}
}

// step builds one transaction from the byte stream, submits it under the
// property oracles, and (for payments) validates the delivered amount.
func (sc *scenario) step(s *stream, baseline uint64) {
	built, dc := sc.gen(s)
	if built == nil {
		return
	}

	start := time.Now()
	res := sc.env.Submit(built)
	if d := time.Since(start); d > stepBudget {
		sc.t.Fatalf("apply exceeded budget (%s > %s) on %s: flow loop did not stay bounded", d, stepBudget, dcDesc(dc))
	}

	if res.Code == invariantTec || res.Code == invariantTef {
		sc.t.Fatalf("engine reported invariant violation %q on %s: %s", res.Code, dcDesc(dc), res.Message)
	}
	if got := sc.totalXRP(); got > baseline {
		sc.t.Fatalf("XRP inflated to %d drops (baseline %d) after %s", got, baseline, dcDesc(dc))
	}
	if dc != nil && res.Success {
		sc.checkDelivery(res, dc)
	}
}

// checkDelivery validates the delivered amount of a successful payment against
// the conservation and partial-payment properties. It only runs when the engine
// recorded a DeliveredAmount (always present for cross-currency / partial
// payments; absent for trivial same-asset XRP sends, where delivery equals the
// requested amount by construction).
func (sc *scenario) checkDelivery(res jtx.TxResult, dc *deliveryCheck) {
	if res.Metadata == nil || res.Metadata.DeliveredAmount == nil {
		return
	}
	delivered := *res.Metadata.DeliveredAmount

	// Conservation: never deliver more than requested.
	if ok, cmp := leq(delivered, dc.requested); cmp && !ok {
		sc.t.Fatalf("delivered %s exceeds requested %s on %s", fmtAmt(delivered), fmtAmt(dc.requested), dc.desc)
	}
	// A same-currency SendMax also bounds the delivered amount.
	if dc.sendMax != nil && sameAsset(*dc.sendMax, delivered) {
		if ok, cmp := leq(delivered, *dc.sendMax); cmp && !ok {
			sc.t.Fatalf("delivered %s exceeds SendMax %s on %s", fmtAmt(delivered), fmtAmt(*dc.sendMax), dc.desc)
		}
	}
	if dc.partial {
		if dc.deliverMin != nil {
			if ok, cmp := less(delivered, *dc.deliverMin); cmp && ok {
				sc.t.Fatalf("partial payment delivered %s below DeliverMin %s on %s", fmtAmt(delivered), fmtAmt(*dc.deliverMin), dc.desc)
			}
		}
		return
	}
	// A non-partial success must deliver the full requested amount.
	if !approxEq(delivered, dc.requested) {
		sc.t.Fatalf("non-partial success delivered %s, want full %s on %s", fmtAmt(delivered), fmtAmt(dc.requested), dc.desc)
	}
}

// gen builds one transaction from the stream. Payments (the bulk of the mix)
// return a deliveryCheck; offers, trust sets and account-set rate changes keep
// the books and gateway state lively and return a nil check.
func (sc *scenario) gen(s *stream) (tx.Transaction, *deliveryCheck) {
	switch s.intn(8) {
	case 0, 1, 2, 3:
		return sc.genPayment(s)
	case 4:
		return sc.genOffer(s), nil
	case 5:
		return offerb.OfferCancel(sc.pickUser(s), 1+s.u32()%64).Build(), nil
	case 6:
		u := sc.pickUser(s)
		g := sc.pickGateway(s)
		b := trustsetb.TrustLine(u, g.currency, g.acct, fmt.Sprintf("%d", 1+s.intn(1_000_000)))
		switch s.intn(3) {
		case 1:
			b = b.Freeze()
		case 2:
			b = b.ClearFreeze()
		}
		return b.Build(), nil
	default:
		g := sc.pickGateway(s)
		rate := 1_000_000_000 + s.u32()%50_000_000 // 100% .. ~105%
		return accountsetb.AccountSet(g.acct).TransferRate(rate).Build(), nil
	}
}

// genPayment builds one of several cross-currency / same-currency payment shapes
// and records what it promised so the delivery oracle can check it.
func (sc *scenario) genPayment(s *stream) (tx.Transaction, *deliveryCheck) {
	from, to := sc.pickPair(s)
	dc := &deliveryCheck{}
	var b *paymentb.PaymentBuilder

	switch s.intn(5) {
	case 0: // Same-currency USD payment (rippling through the gateway).
		amt := sc.usd(iouValue(s))
		dc.requested, dc.desc = amt, "pay-usd-direct"
		b = paymentb.PayIssued(from, to, amt)
		if s.chance(128) {
			sm := sc.usd(iouValue(s))
			b, dc.sendMax = b.SendMax(sm), &sm
		}

	case 1: // USD -> EUR via the direct USD/EUR book.
		amt := sc.eur(iouValue(s))
		sm := sc.usd(iouValue(s) + amt.Float64())
		dc.requested, dc.sendMax, dc.desc = amt, &sm, "pay-usd->eur-direct"
		b = paymentb.PayIssued(from, to, amt).SendMax(sm).PathsCurrency("EUR", sc.gws[1].acct)

	case 2: // USD -> EUR bridged through XRP.
		amt := sc.eur(iouValue(s))
		sm := sc.usd(iouValue(s) + amt.Float64())
		dc.requested, dc.sendMax, dc.desc = amt, &sm, "pay-usd->eur-xrpbridge"
		b = paymentb.PayIssued(from, to, amt).SendMax(sm).PathsXRP()

	case 3: // USD -> XRP.
		drops := xrpDrops(s)
		amt := xrpAmt(drops)
		sm := sc.usd(iouValue(s) + 1)
		dc.requested, dc.sendMax, dc.desc = amt, &sm, "pay-usd->xrp"
		b = paymentb.Pay(from, to, drops).SendMax(sm).PathsCurrency("XRP", nil)

	default: // XRP -> USD via the direct XRP/USD book (default path).
		amt := sc.usd(iouValue(s))
		sm := xrpAmt(xrpDrops(s))
		dc.requested, dc.sendMax, dc.desc = amt, &sm, "pay-xrp->usd"
		b = paymentb.PayIssued(from, to, amt).SendMax(sm)
	}

	// Randomly make it a partial payment with a DeliverMin at half the target.
	if s.chance(64) {
		dc.partial = true
		b = b.PartialPayment()
		if s.chance(160) {
			dm := scaleAmount(dc.requested, 0.5)
			b, dc.deliverMin = b.DeliverMin(dm), &dm
		}
	}
	if s.chance(32) {
		b = b.LimitQuality()
	}
	return b.Build(), dc
}

// genOffer builds an OfferCreate in one of the three books, in a random
// direction, occasionally with a time-in-force flag.
func (sc *scenario) genOffer(s *stream) tx.Transaction {
	u := sc.pickUser(s)
	var b *offerb.OfferCreateBuilder
	switch s.intn(3) {
	case 0:
		b = offerb.OfferCreateXRP(u, xrpDrops(s), sc.usd(iouValue(s)+1), s.chance(128))
	case 1:
		b = offerb.OfferCreateXRP(u, xrpDrops(s), sc.eur(iouValue(s)+1), s.chance(128))
	default:
		usd, eur := sc.usd(iouValue(s)+1), sc.eur(iouValue(s)+1)
		if s.chance(128) {
			b = offerb.OfferCreate(u, usd, eur)
		} else {
			b = offerb.OfferCreate(u, eur, usd)
		}
	}
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

func (sc *scenario) pickUser(s *stream) *jtx.Account { return sc.users[s.intn(len(sc.users))] }

func (sc *scenario) pickGateway(s *stream) gateway { return sc.gws[s.intn(len(sc.gws))] }

// pickPair returns two distinct users so self-payments don't dominate.
func (sc *scenario) pickPair(s *stream) (from, to *jtx.Account) {
	i, j := s.intn(len(sc.users)), s.intn(len(sc.users))
	if i == j {
		j = (j + 1) % len(sc.users)
	}
	return sc.users[i], sc.users[j]
}

// iouValue returns a bounded positive IOU value (~0.001 .. 1000, 3 decimals).
func iouValue(s *stream) float64 { return float64(int64(s.u32())%1_000_000+1) / 1e3 }

// xrpDrops returns a bounded XRP amount in drops (1 .. ~1000 XRP).
func xrpDrops(s *stream) uint64 { return 1 + uint64(s.u32())%uint64(jtx.XRP(1000)) }

// scaleAmount returns frac * a, preserving the asset (XRP or issued currency).
func scaleAmount(a tx.Amount, frac float64) tx.Amount {
	if a.IsNative() {
		return tx.NewXRPAmount(int64(float64(a.Drops()) * frac))
	}
	return tx.NewIssuedAmountFromFloat64(a.Float64()*frac, a.Currency, a.Issuer)
}

// sameAsset reports whether two amounts are the same XRP/issued asset.
func sameAsset(a, b tx.Amount) bool {
	if a.IsNative() || b.IsNative() {
		return a.IsNative() && b.IsNative()
	}
	return a.Currency == b.Currency && a.Issuer == b.Issuer
}

// leq reports whether a <= b. cmp is false when the two are not the same asset
// kind (and so not numerically comparable), in which case ok is meaningless.
func leq(a, b tx.Amount) (ok, cmp bool) {
	if a.IsNative() != b.IsNative() {
		return false, false
	}
	if a.IsNative() {
		return a.Drops() <= b.Drops(), true
	}
	return a.Float64() <= b.Float64()*(1+1e-9)+1e-12, true
}

// less reports whether a < b, with the same comparability convention as leq.
func less(a, b tx.Amount) (ok, cmp bool) {
	if a.IsNative() != b.IsNative() {
		return false, false
	}
	if a.IsNative() {
		return a.Drops() < b.Drops(), true
	}
	return a.Float64() < b.Float64()*(1-1e-9)-1e-12, true
}

// approxEq reports whether a and b are equal within a small relative tolerance.
func approxEq(a, b tx.Amount) bool {
	if a.IsNative() != b.IsNative() {
		return false
	}
	if a.IsNative() {
		return a.Drops() == b.Drops()
	}
	av, bv := a.Float64(), b.Float64()
	return math.Abs(av-bv) <= 1e-9*math.Max(1, math.Abs(bv))
}

func fmtAmt(a tx.Amount) string {
	if a.IsNative() {
		return fmt.Sprintf("%d drops", a.Drops())
	}
	return fmt.Sprintf("%g %s/%s", a.Float64(), a.Currency, a.Issuer)
}

func dcDesc(dc *deliveryCheck) string {
	if dc == nil {
		return "non-payment tx"
	}
	return dc.desc
}

// stream is a deterministic, drainable reader over the fuzzer's input bytes.
// Each accessor consumes bytes; once exhausted, drained reports true and the
// accessors return zero values, terminating generation. Being driven purely by
// the input (with name-derived deterministic accounts and a manual clock) makes
// every iteration fully reproducible.
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
	return uint32(s.u8())<<24 | uint32(s.u8())<<16 | uint32(s.u8())<<8 | uint32(s.u8())
}

// intn returns a value in [0, n); n must be positive.
func (s *stream) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(s.u8()) % n
}

// chance returns true with probability ~ num/256.
func (s *stream) chance(num int) bool { return int(s.u8()) < num }

// seedCorpus returns deterministic byte inputs that drive varied transaction /
// query sequences. They seed both fuzz targets and back the smoke tests.
func seedCorpus() [][]byte {
	ramp := make([]byte, 256)
	for i := range ramp {
		ramp[i] = byte(i)
	}
	rep := func(b []byte, n int) []byte {
		out := make([]byte, 0, len(b)*n)
		for i := 0; i < n; i++ {
			out = append(out, b...)
		}
		return out
	}
	return [][]byte{
		{},
		rep([]byte{0x00}, 16),
		ramp,
		rep([]byte{0x01, 0x40, 0x9a, 0x7f, 0x10, 0x33}, 24),
		rep([]byte{0xff, 0x00, 0x80, 0x2a}, 32),
	}
}
