// Package pathfuzz hosts generative, property-based fuzz harnesses over the
// goXRPL payment engine: cross-currency flow (RippleCalc) and path discovery
// (the Pathfinder).
//
// It is the payment-engine counterpart to internal/testing/enginefuzz (issue
// #682). Where enginefuzz drives the whole transaction engine with direct and
// same-currency payments, this package builds a ledger with multi-hop liquidity
// (two gateways, USD/EUR/XRP order books, an AMM pool) and generates
// cross-currency Payments — with explicit paths, SendMax, DeliverMin and the
// partial-payment flag — so the flow engine's strand execution, book steps,
// auto-bridging, transfer-rate and partial-payment logic are exercised.
//
// Two fuzz targets, both reference-free (no rippled oracle needed — the recorded
// rippled differential is the conformance corpus replayed by #682's
// FuzzEngineDifferential):
//
//   - FuzzPaymentFlow applies generated cross-currency payment sequences and
//     after every apply asserts: the engine never reports an invariant
//     violation (tec/tefINVARIANT_FAILED — the same oracle rippled runs under
//     Antithesis), total XRP is never inflated, each apply terminates within a
//     budget (the flow loop's iteration guard holds — no unbounded loop on
//     pathological books), delivered never exceeds the requested amount,
//     delivered never exceeds a same-currency SendMax, a partial payment
//     delivers at least DeliverMin, and a non-partial success delivers the full
//     requested amount.
//
//   - FuzzPathfinder runs path discovery (pathfinder.PathRequest.Execute, which
//     drives both the Pathfinder and RippleCalc validation) for random
//     source/destination/amount triples against the seeded ledger and asserts
//     it never panics, terminates within a budget, and only ever returns
//     well-formed alternatives (a non-empty path set, a non-negative source
//     amount — no free liquidity).
//
// The harness and fuzz targets live in this package's _test.go files; this file
// exists only so the package has a non-test compilation unit. See issue #685.
package pathfuzz
