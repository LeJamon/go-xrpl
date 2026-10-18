# NFTokenPage codec allocations

Issue #2082 replaces the generic nested NFT array path with owned typed values. IDs remain fixed-size bytes and URI presence remains independent of URI length. Decode and encode avoid per-token JSON maps, field sorting and intermediate hex strings; metadata and JSON projection still use the existing public shapes. Outer-field encoding remains in the generic codec.

The generated `entry.NFTokenPage.NFTokens` field and its raw `SetNFTokens` setter now accept `[]NFTokenValue` instead of `[]any`. This is a source API change for direct field/raw setter users. `GetNFTokens`, `SetNFTokensValue`, `ToMap` and the public binarycodec JSON interfaces retain their signatures. There is no duplicate decoded representation or shared object cache. Callers receive independently owned URI bytes.

## Method

Baseline production code is main `96f2712b045940cdce6c4c6426d0bc84bc9234a8`, with the same timed benchmark implementation as the optimized build. After baseline measurement, failure cleanup and an additional between-round promotion assertion were added outside the timed regions; the final fixture passed a separate baseline sanity run. This is newer than the incident revision `63a5330e2`. Measurements use Go 1.24.4, Darwin arm64, Apple M3 Pro, 11 Go scheduler processors and CGO/OpenSSL. Three samples per case ran sequentially without other deliberate build/test jobs. Microbenchmarks run for 100 ms per sample; each service sample accepts 100 ledgers per arm. Tables report medians of the three samples; the [CSV](nftoken-page-codec-results.csv) contains every sample.

The microbenchmark matrix covers 1, 16 and 32 tokens with absent, 64-byte and 256-byte URIs. Decode includes the typed getter's ownership copy. State parse/serialize and modified-node metadata projection/serialization are measured separately. The metadata workload replaces one token ID and updates threading, so it exercises NFT array changes. Fixture and metadata digests match between baseline and optimized runs.

The service workload is synthetic: a funded seller starts with 30 NFTs carrying 64-byte URIs. One signed Batch contains two mint/sell-offer/buy-offer/brokered-accept cycles, eight inner operations total, with 256-byte URIs on new tokens. The benchmark uses real signature verification and `Service.AcceptConsensusResult`. Each iteration reloads the same parent from rotating Pebble storage. It compares isolated acceptance with concurrent online-delete state refresh using one worker and an injected 100-microsecond delay per promotion call. It waits for refresh to reach promotion before acceptance starts and requires positive promotion work. Between iterations the parent is promoted, rotated and reopened so later samples do not silently skip refresh.

The parent includes 4,096 opaque synthetic non-NFT SHAMap leaves to exercise multiple refresh batches. It is a storage contention fixture, not a validated historical ledger. Timed regions exclude fixture construction, service startup, between-round promotion/rotation, assertion work and waiting for refresh after acceptance. GC CPU and allocated-byte deltas are sampled around acceptance only, including concurrent process work; allocated bytes divided by summed acceptance time is allocation rate, not retained heap. GC CPU is reported per accepted ledger. Runtime counters can be noisy for short operations.

## Results

For a 32-token page with 256-byte URIs (before → after):

| Operation | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Decode + typed getter | 110,417 → 4,289 | 109,461 → 24,320 | 943 → 73 |
| Encode | 73,591 → 2,344 | 123,971 → 10,792 | 802 → 21 |
| State parse | 206,042 → 11,593 | 177,046 → 48,480 | 1,076 → 109 |
| State serialize | 165,643 → 13,561 | 228,115 → 40,936 | 1,447 → 122 |
| Build modified metadata node | 209,492 → 42,518 | 209,133 → 147,861 | 1,678 → 733 |
| Build + serialize metadata | 379,028 → 187,770 | 547,301 → 486,026 | 3,322 → 2,377 |

Synthetic end-to-end Batch acceptance (before → after):

| Arm | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| isolated | 8,808,668 → 5,321,778 | 9,031,927 → 6,234,102 | 100,532 → 57,825 |
| online-delete-refresh | 9,301,797 → 5,643,202 | 9,310,156 → 6,394,512 | 101,990 → 58,630 |

| Arm | p50 (ms) | p95 (ms) | p99 (ms) | GC CPU (ms/op) | Allocation rate (MiB/s) |
| --- | ---: | ---: | ---: | ---: | ---: |
| isolated | 8.74 → 5.27 | 9.75 → 6.35 | 10.33 → 6.99 | 7.90 → 5.20 | 1017 → 1189 |
| online-delete-refresh | 9.30 → 5.52 | 10.42 → 6.58 | 11.03 → 6.78 | 8.36 → 5.77 | 990 → 1153 |

Median mean acceptance time decreases 39.6% in isolation and 39.3% with refresh. Allocated bytes per acceptance decrease about 31%, allocation counts about 42%, and GC CPU per acceptance about 31–34%. Allocation **rate** increases because the smaller amount of allocation occurs in a substantially shorter time. Each reported percentile is the median of three independent 100-operation sample percentiles; these are descriptive measurements, not confidence intervals.

## Pinned execution parity

Every iteration checks all nine transaction records (one Batch plus eight inner transactions), exact raw transaction bytes, `tesSUCCESS`, inner `ParentBatchID`, NFT page/offer metadata effects and the following digests. The values were captured on unchanged baseline code and remain fixed in the benchmark.

| Artifact | Digest |
| --- | --- |
| Ledger hash | `F350CF78F6344DADCA107BF0C6C277747E8350A52DF4DB27491C8A3555D845B9` |
| State root | `CADF52511D6879C5C58F3D133D51824278E3EC909228D5D2732EE21D3C784670` |
| Transaction root | `DFBD24515E1B3F1C0D635E198272D8FF49D6FE7C82137B72E6E48BD4F32170A5` |
| Concatenated metadata SHA-256 | `DF9802B3C458671CE51735EE3A0FBD4BD82DD976030260E0FC03CC87D88D0D94` |
| Transaction workload SHA-256 | `DF7D211F2D8D38528AE36CB3C7FDF7652DCCFE9335EA774C8A95A6A01FE9A58E` |

This does not replay incident ledgers 21612600/21612604: no verified historical parent snapshot was available. Their 97 Batches/776 inner operations per ledger are much larger than this fixture. The results establish improvement and byte/hash parity for the pinned synthetic workload, not incident latency or network recovery behavior. Invariant decode deduplication (#2083) and background refresh policy (#2066) are separate changes; their savings cannot be added to these timings.

## Reproduction and validation

Run from the repository root with the native dependencies required by the justfile:

```sh
GOTOOLCHAIN=go1.24.4 just test-pkg './ledger/entry ./internal/ledger/state ./internal/tx/applystate -run ^$ -bench NFTokenPage -benchmem -benchtime=100ms -count=3'
GOTOOLCHAIN=go1.24.4 just test-pkg './internal/ledger/service -run ^$ -bench ^BenchmarkService_Issue2082NFTBatchAcceptance$ -benchmem -benchtime=100x -count=3 -timeout=20m'
```

For the baseline, copy only the four benchmark files onto the pinned main revision. Keep the Go toolchain, scheduler processor count and commands identical, and run the two implementations sequentially. Comparing the emitted fixture/metadata digests is required alongside performance numbers.

Validation includes canonical-byte differential tests over all page fields, absent/empty URI projection and metadata, URI length prefix boundaries, missing/duplicate/unknown fields, every truncated prefix, the existing 512-element encode limit, ownership tests and differential fuzzing against the generic nested decoder. The specialized parser has no recursive object path. Regeneration, affected race suites, full build/vet and CI-pinned lint are checked. Protocol source review uses the session-requested local rippled 3.3.0 commit `00a178fb92ca49521b937ae1a99d863765ea8a90`; no C++ executable differential replay is claimed.
