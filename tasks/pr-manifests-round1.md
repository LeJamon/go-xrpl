# PR: Validator manifests + master-key translation (issue #265)

Implements the first round of validator manifest infrastructure: ingest/verify/store TMManifests, translate ephemeral signing keys to master keys before UNL / quorum decisions, and back the `manifest` RPC.

Out-of-band manifest publication (validator sites, validators.txt) is out of scope — to be covered in a follow-up PR.

## Rippled reference map

| Concern | Rippled file:line |
|---|---|
| STObject parse, revoked / verify | `src/xrpld/app/misc/detail/Manifest.cpp:53-241` |
| `ManifestCache::applyManifest` | `src/xrpld/app/misc/detail/Manifest.cpp:382-580` |
| `getMasterKey` / `getSigningKey` | `src/xrpld/app/misc/detail/Manifest.cpp:310-332` |
| Peer inbound `TMManifests` | `src/xrpld/overlay/detail/PeerImp.cpp:1068-1085` |
| Overlay batch-apply + gossip relay | `src/xrpld/overlay/detail/OverlayImpl.cpp:633-686` |
| Ephemeral→master for validations | `src/xrpld/app/consensus/RCLValidations.cpp:165-186` |
| `getTrustedKey` lookup | `src/xrpld/app/misc/detail/ValidatorList.cpp:1479-1494` |
| Manifest RPC | `src/xrpld/rpc/handlers/DoManifest.cpp:29-76` |
| Signing blob for verify | `src/libxrpl/protocol/Sign.cpp:47-62` (`addWithoutSigningFields` + `HashPrefix::manifest`) |

## Design decisions

- **Package**: new `internal/manifest/` with exported type `Cache`. Consumed by the consensus router (ingest), validation tracker (lookup), and RPC handlers. Kept `internal/` until we have a reason to export — nothing else in the tree exports consensus infrastructure.
- **Decode path**: reuse `codec/binarycodec`. `Decode(hex)` produces a JSON map; re-`Encode` after `removeNonSigningFields` gives the canonical signing preimage — the same path rippled's `addWithoutSigningFields` takes. No new STObject walker needed.
- **Signature verify**: `HashPrefix::manifest` (0x4D414E00, already defined as `protocol.HashPrefixManifest`) prepended to the signing preimage; dispatch on key-type prefix byte (0xED → ed25519, 0x02/0x03 → secp256k1) via existing `crypto/ed25519` and `crypto/secp256k1` `Validate`. Both already match rippled's hash/no-hash semantics for their respective key types.
- **Revocation marker**: `sequence == math.MaxUint32`. Revoked manifests must NOT carry SigningPubKey or Signature (rippled Manifest.cpp:120-129).
- **Higher-seq-wins**: strictly greater than stored seq (rippled uses `<=` reject — so we accept only `new.seq > stored.seq`).
- **Invariants enforced on apply** (all matching rippled):
  - master key may not appear as another manifest's ephemeral key (`badMasterKey`);
  - ephemeral key may not appear as another's ephemeral or master (`badEphemeralKey`);
  - signing key != master key;
  - signature must verify before storage.
- **Relay**: on `accepted` disposition, re-broadcast the raw manifest payload to all peers except the origin. Reuse a small `OverlayBroadcastExcept` helper (add if absent) rather than per-validator squelch — rippled uses overlay-wide foreach for manifests, not the squelch-filtered gossip path.
- **Translation seam in consensus**: `ValidationTracker.SetManifestResolver(func(NodeID) NodeID)` injected at construction time. `Add()` (and the callers of `IsFullyValidated`/`GetTrustedValidationCount` on the read side) route the signing `NodeID` through the resolver before looking up `trusted[]`. This keeps the manifest dependency one-directional (tracker depends on a function, not on the `manifest` package) and leaves the existing tests unchanged when the resolver is the identity function.

## Files

### New
- `internal/manifest/manifest.go` — `Manifest` struct (masterKey, signingKey, sequence, domain, serialized, masterSig, ephemeralSig) + `Deserialize([]byte)` + `Verify()` + `Revoked()`.
- `internal/manifest/cache.go` — `Cache` type, `ApplyManifest(*Manifest) Disposition`, `GetMasterKey(ephemeral [33]byte) [33]byte`, `GetSigningKey(master [33]byte) ([33]byte, bool)`, `GetManifest(master [33]byte) ([]byte, bool)`, `GetSequence(master [33]byte) (uint32, bool)`, `GetDomain(master [33]byte) (string, bool)`, `Revoked(master [33]byte) bool`. `Disposition` enum: `Accepted`, `Stale`, `Invalid`, `BadMasterKey`, `BadEphemeralKey`, `Revoked`.
- `internal/manifest/manifest_test.go` — wire-decode golden-vector tests; applyManifest sequence / revocation / conflict tests.
- `internal/consensus/rcl/manifest_resolver_test.go` — asserts `ValidationTracker` routes lookups through the resolver.

### Modified
- `internal/consensus/rcl/validations.go` — add `resolver func(NodeID) NodeID` field + `SetManifestResolver`; thread resolver through `Add`, `GetTrustedValidations`, `GetTrustedValidationCount`, `ProposersValidated`. Identity default preserves existing behavior.
- `internal/consensus/adaptor/router.go` — add `handleManifests(msg)`; dispatch from `handleMessage` on `TypeManifests`. Construct & hold `*manifest.Cache` (passed via `NewRouter`).
- `internal/consensus/adaptor/startup.go` — instantiate cache, pass to `NewRouter`, wire `cache.GetMasterKey` into the tracker via `SetManifestResolver`.
- `internal/peermanagement/overlay.go` — add `BroadcastExcept(PeerID, []byte)` if not already reachable via existing helper; otherwise call through existing API.
- `internal/rpc/handlers/manifest.go` — drop stub; accept a `ManifestCache` dependency on the handler and implement the lookup + response shape from DoManifest.cpp.
- `internal/rpc/types/services.go` — add `ManifestCache` interface (`GetMasterKey`, `GetSigningKey`, `GetManifest`, `GetSequence`, `GetDomain`).
- `internal/rpc/server.go` (or wherever handlers are registered) — plumb the cache into the context.

## Acceptance tests

All in `internal/testing/manifest/` (matches per-feature directory convention). Use deterministic ed25519 keypairs built from seeded randomness so tests are repro across runs.

1. **`TestManifest_WireDecode_ValidMasterSig_Accepted`** — construct a valid manifest (seq=1), serialize, verify `Deserialize` + `Verify` + `Cache.ApplyManifest` all succeed.
2. **`TestManifest_WireDecode_BadMasterSig_Rejected`** — flip a byte in the master signature after serialization; `Cache.ApplyManifest` returns `Invalid`; cache is empty after.
3. **`TestManifest_HigherSeq_Overrides`** — apply seq=1, then seq=2 with a new ephemeral key; `GetSigningKey(master)` returns the seq=2 ephemeral; applying the seq=1 again returns `Stale`.
4. **`TestManifest_RevokedMasterKey_Rejected`** — apply a revocation manifest (seq=MaxUint32, no ephemeral fields); subsequent `GetSigningKey(master)` returns `!ok` and `GetMasterKey(oldEphemeral)` no longer resolves.
5. **`TestConsensus_EphemeralSigningKey_TranslatedForQuorum`** — build a `ValidationTracker` with `SetTrusted([master])` and `SetManifestResolver(cache.GetMasterKey)`; `Add` a validation signed by the ephemeral key (so its `NodeID == ephemeral`); assert `IsFullyValidated` fires with quorum=1 even though `ephemeral` is not in `trusted`.
6. **`TestRPC_Manifest_ReturnsStoredState`** — apply a manifest to the cache; call the RPC with `public_key=ephemeral_base58`; assert response `details.master_key`, `details.ephemeral_key`, `details.seq`, and `manifest` (base64) match the input.

## Out of scope (deferred, will link in follow-up)

- Out-of-band manifest publication (fetching from validator sites / `validators.txt`).
- Persistence of manifests to disk across restarts (rippled writes to Wallet DB for UNL-member masters).
- `pubManifest` subscription stream.
- Emitting our own manifest (we don't rotate ephemeral keys yet).

## Verification plan

```
go build ./...
go test ./internal/manifest/...
go test ./internal/consensus/rcl/... -run ManifestResolver
go test ./internal/testing/manifest/...
go test ./internal/rpc/handlers/... -run Manifest
./scripts/conformance-summary.sh | tail -20    # no regressions in existing suites
```
