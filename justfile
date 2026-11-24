# go-xrpl development tasks. Run `just` to list recipes.
#
# Install just: `brew install just` or `cargo install just`.

# Honor an existing PKG_CONFIG_PATH; otherwise resolve Homebrew openssl@3
# + secp256k1 on macOS so the CGO shims build out of the box. Linux's
# distro -dev packages already register on the default pkg-config path.
export PKG_CONFIG_PATH := env_var_or_default("PKG_CONFIG_PATH", `command -v brew >/dev/null 2>&1 && { paths=""; for pkg in openssl@3 secp256k1; do p=$(brew --prefix $pkg 2>/dev/null); [ -d "$p/lib/pkgconfig" ] && paths="${paths:+$paths:}$p/lib/pkgconfig"; done; echo "$paths"; } || echo ""`)
export CGO_ENABLED := env_var_or_default("CGO_ENABLED", "1")

golangci_version := "v2.11.3"

# List all recipes.
default:
    @just --list --unsorted

# Check the native CGO dependencies used by the daemon and secp256k1 package.
check-native:
    ./scripts/require-native.sh

# Build the goxrpl binary into ../tmp/goxrpl (CGO + OpenSSL + libsecp256k1).
build:
    ./scripts/build.sh

# Compile every package in the module.
build-all: check-native
    go build ./...

# Install the locked optional mpt-crypto dependency into a project-local cache.
setup-mpt-crypto:
    ./scripts/setup-mpt-crypto.sh setup

# Print the environment needed by an mptcrypto-tagged build.
mpt-crypto-env:
    @./scripts/setup-mpt-crypto.sh env

# Build mpt-crypto from its lockfile and run the native backend tests.
test-mpt-crypto package="./amendment/... ./crypto/mptcrypto/... ./crypto/secp256k1/... ./internal/tx/mpt/... ./internal/testing/mpt/...":
    ./scripts/setup-mpt-crypto.sh test {{package}}

# Run every test in the module.
test:
    go test ./...

# The CI groups below come from scripts/test-shard.sh, which partitions every
# package in the module across the shards CI runs.

# CI groups: integration tests (both the offer shard and the rest).
test-integration:
    packages=$(./scripts/test-shard.sh integration-offer integration) && go test $packages

# CI group: transaction-engine tests.
test-tx:
    packages=$(./scripts/test-shard.sh tx) && go test $packages

# CI group: remaining node packages and tools.
test-core:
    packages=$(./scripts/test-shard.sh core) && go test $packages

# CI group: libraries and oracle-backed test utilities.
test-libs:
    packages=$(./scripts/test-shard.sh libs) && go test $packages

# Test a single package: `just test-pkg ./internal/peermanagement/...`
test-pkg pkg:
    go test -v {{pkg}}

# Build and verify the pinned private oracle, then exercise the production peer path.
test-docker:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; . internal/testutil/rippled/pin.env; set +a
    export EVIDENCE_DIR="${EVIDENCE_DIR:-$(mktemp -d)}"
    docker build --platform linux/amd64 -t "$ORACLE_IMAGE" scripts/peer-interop
    scripts/acceptance/oracle-image.sh
    read -r image_id < "$EVIDENCE_DIR/oracle-image-id.txt"
    PEERTLS_DOCKER_INTEROP=1 PEERTLS_LEDGER_NODE_DEPTH_INTEROP=1 PEERTLS_VALIDATOR_LIST_INTEROP=1 \
      PEERTLS_RIPPLED_VERSION="$ORACLE_TAG" PEERTLS_RIPPLED_IMAGE="$image_id" \
      go test -race -tags docker -count=1 -timeout 10m -v \
      -run 'Test(Handshake|Manifest|LedgerNodeDepth|ValidatorListCollection)_Interop_RippledDocker' ./internal/peermanagement/

# PostgreSQL backend integration tests. Needs a reachable server; the DSN
# points at a throwaway database (its tables are truncated between tests).
# e.g. GOXRPL_TEST_POSTGRES_DSN='postgres://xrpl:xrpl@localhost:5432/xrpl_test?sslmode=disable' just test-postgres
test-postgres:
    go test -tags postgres -v ./storage/relationaldb/postgres/

# Stateful invariant fuzzer over the transaction engine (issue #682, scope 1).
# Generates randomized transaction sequences, applies them through the engine,
# and asserts the invariant oracle never fires. e.g. `just fuzz-engine 5m`.
fuzz-engine fuzztime="60s":
    go test -run '^$' -fuzz '^FuzzEngineInvariants$' -fuzztime {{fuzztime}} ./internal/testing/enginefuzz/

# Determinism/fork fuzzer (issue #682, scope 3): builds a ledger from a
# generated tx-set twice and asserts identical close hashes. e.g. `just
# fuzz-determinism 5m`.
fuzz-determinism fuzztime="60s":
    go test -run '^$' -fuzz '^FuzzEngineDeterminism$' -fuzztime {{fuzztime}} ./internal/testing/enginefuzz/

# Differential-vs-rippled fuzzer (issue #682, scope 2): replays recorded rippled
# fixtures and diffs goXRPL's TER + post-state. Uses the pinned conformance
# corpus (ORACLE_CORPUS in internal/testutil/rippled/pin.env) unless
# GOXRPL_FIXTURES_DIR selects another. e.g. `just fuzz-differential 5m`.
fuzz-differential fuzztime="60s":
    go test -run '^$' -fuzz '^FuzzEngineDifferential$' -fuzztime {{fuzztime}} ./internal/testing/conformance/

# Generative property fuzzer over the payment/flow engine (issue #685): applies
# cross-currency payments (paths, SendMax, DeliverMin, partial) through a
# multi-hop ledger and asserts the no-invariant-violation, XRP-conservation,
# bounded-runtime and delivered-amount oracles. e.g. `just fuzz-payment 5m`.
fuzz-payment fuzztime="60s":
    go test -run '^$' -fuzz '^FuzzPaymentFlow$' -fuzztime {{fuzztime}} ./internal/testing/pathfuzz/

# Property fuzzer over path discovery (issue #685): runs the Pathfinder for
# random source/destination/amount triples and asserts it never panics, stays
# bounded, and returns only well-formed alternatives. e.g. `just fuzz-pathfinder 5m`.
fuzz-pathfinder fuzztime="60s":
    go test -run '^$' -fuzz '^FuzzPathfinder$' -fuzztime {{fuzztime}} ./internal/testing/pathfuzz/

# Discovery fuzzing: run every native Go fuzz target with mutation for a bounded
# duration and fail on any new crasher. Non-deterministic, so this is the
# scheduled job — not a required PR check. Override the per-target budget with
# fuzztime and restrict to packages with extra args. e.g. `just fuzz 2m ./shamap`.
fuzz fuzztime='30s' *pkgs:
    FUZZTIME={{fuzztime}} ./scripts/fuzz.sh {{pkgs}}

# Regression fuzzing: replay the committed fuzz seed corpus (testdata/fuzz +
# f.Add seeds) with no mutation. Deterministic and fast — this is the PR gate.
fuzz-corpus *pkgs:
    ./scripts/fuzz.sh --corpus {{pkgs}}

# Run go vet on the module. The stdmethods analyzer is disabled because
# it false-positives on gomock-generated recorder types: a recorder
# method must be named after the mocked interface method (e.g.
# ReadByte) but returns *gomock.Call, which stdmethods compares against
# io.ByteReader's signature. See golang/mock#648. Every other analyzer
# stays enabled.
vet:
    go vet -stdmethods=false ./...

# Run the lint configuration CI gates merges on (auto-installs the pinned version).
lint:
    @command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{golangci_version}}
    golangci-lint run --config .golangci.yml

# Run the broader advisory lint configuration used in release evidence.
lint-advisory:
    @command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{golangci_version}}
    golangci-lint run --config .golangci-warnings.yml

# Report changes to the public packages' exported API since a base ref.
api-diff base="origin/main":
    ./scripts/apidiff.sh {{quote(base)}}

lint-fix: 
    @command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{golangci_version}} 
    golangci-lint run --fix --config .golangci.yml

# gofmt -w the entire module.
fmt:
    gofmt -w .

# go mod tidy.
tidy:
    go mod tidy

# Required final-oracle conformance summary. Runs the pinned corpus
# (ORACLE_CORPUS in internal/testutil/rippled/pin.env) unless `--corpus PATH` or
# GOXRPL_FIXTURES_DIR selects another.
conformance *args:
    ./scripts/conformance-summary.sh {{args}}

# Hot-reload dev server (needs `air`).
dev:
    cd cmd/goxrpl && air

# Run the server without hot-reload.
run:
    go run ./cmd/goxrpl

# Regenerate the registry catalogs and the pinned corpus status page.
docs-gen:
    go run ./scripts/docsgen/rpcmethods
    go run ./scripts/docsgen/registries
    ./scripts/docsgen/conformance.sh

# Regenerate only the fast, registry-derived docs (no conformance suite).
docs-gen-fast:
    go run ./scripts/docsgen/rpcmethods
    go run ./scripts/docsgen/registries

# Check generated docs for drift.
docs-check: docs-gen
    git diff --exit-code -- docs/rpc-methods.md docs/supported-transactions.md docs/amendments.md docs/ledger-entries.md docs/conformance-status.md
