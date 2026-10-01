# XRP conservation boundaries

`xrp_conservation.json` contains exact decimal totals shared by the Go invariant
regressions and the local rippled 3.4.1 comparison. Each positive or negative
total is split into XRP-bearing images of at most 100,000,000,000,000,000 drops.
The Go test exercises AccountRoot.Balance, native Escrow.Amount,
PayChannel.Amount minus Balance, and optional Sponsorship.FeeAmount, in both
accumulation orders. The expected predicate is `net <= 0 && -net == fee`.

These are injected invariant inputs. Totals above the XRP supply cannot be
produced by redistributing a valid parent ledger's XRP. They demonstrate the
conservation safeguard's arithmetic behavior, not a valid-ledger minting exploit.
The `issue_2012` case also uses a synthetic fee. Companion engine regressions
inject application changes into existing accounts to test rollback; they do not
claim that a production transaction handler can create those changes.

The two `go_*fee` cases exercise the Go check's uint64 fee argument above
INT64_MAX. They are outside rippled XRPAmount's signed fee domain and normal
transaction fee limits. All other cases have C++-representable per-image values
and fees, although many cannot originate from a valid ledger.

Oracle: xrpld-private tag `3.4.1`, commit
`d147fccf54a500fce586522f28d6044c37fd8d29`.

Local comparison executed the pinned `XRPNotCreated` class and unchanged
`visitEntry`/`finalize` method bodies against the shared fixture: 160 checks
passed (20 cases × four entry types × two orders), along with two deleted
AccountRoot final-balance cases. The harness supplied small
field/view/journal stand-ins; this verifies the extracted invariant, not the
full C++ engine, serialization, or STAmount implementation. The two Go-only
unsigned-fee cases were excluded explicitly.
