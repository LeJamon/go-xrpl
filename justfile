# goXRPL development tasks. Run `just` to list recipes.
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

# Build the xrpld binary into ../tmp/main (CGO + OpenSSL).
build:
    go build -v -o ../tmp/main ./cmd/xrpld

# Compile every package in the module.
build-all:
    go build ./...

# Verify the !cgo path still compiles (uses peertls stub).
build-nocgo:
    CGO_ENABLED=0 go build ./...

# Run every test in the module.
test:
    go test ./...

# CI group: integration tests.
test-integration:
    go test ./internal/testing/...

# CI group: transaction-engine tests.
test-tx:
    go test ./internal/tx/...

# CI group: ledger / txq / rpc / consensus / peermanagement.
test-core:
    go test ./internal/ledger/... ./internal/txq/... ./internal/rpc/... ./internal/consensus/... ./internal/peermanagement/...

# CI group: codec / crypto / shamap / storage / etc.
test-libs:
    go test ./codec/... ./crypto/... ./shamap/... ./storage/... ./keylet/... ./ledger/... ./amendment/... ./drops/... ./protocol/... ./config/...

# Test a single package: `just test-pkg ./internal/peermanagement/...`
test-pkg pkg:
    go test -v {{pkg}}

# Live rippled handshake interop (Docker + xrpllabsofficial/xrpld:latest).
test-docker:
    PEERTLS_DOCKER_INTEROP=1 go test -tags docker -timeout 300s -v -run TestHandshake_Interop_RippledDocker ./internal/peermanagement/peertls/

# PostgreSQL backend integration tests. Needs a reachable server; the DSN
# points at a throwaway database (its tables are truncated between tests).
# e.g. XRPLD_TEST_POSTGRES_DSN='postgres://xrpl:xrpl@localhost:5432/xrpl_test?sslmode=disable' just test-postgres
test-postgres:
    go test -tags postgres -v ./storage/relationaldb/postgres/

# Run go vet on the module. The stdmethods analyzer is disabled because
# it false-positives on gomock-generated recorder types: a recorder
# method must be named after the mocked interface method (e.g.
# ReadByte) but returns *gomock.Call, which stdmethods compares against
# io.ByteReader's signature. See golang/mock#648. Every other analyzer
# stays enabled.
vet:
    go vet -stdmethods=false ./...

# Run golangci-lint pinned to the CI version (auto-installs if missing).
lint:
    @command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{golangci_version}}
    golangci-lint run

# gofmt -w the entire module.
fmt:
    gofmt -w .

# go mod tidy.
tidy:
    go mod tidy

# Conformance summary; args pass through. e.g. `just conformance --failing`.
conformance *args:
    ./scripts/conformance-summary.sh {{args}}

# Hot-reload dev server (needs `air`).
dev:
    cd cmd/xrpld && air

# Run the server without hot-reload.
run:
    go run ./cmd/xrpld

# Regenerate all generated docs (RPC catalog, tx/amendment catalogs, and the
# conformance-status snapshot). The conformance step runs the full suite and is
# slow (~300s); it is the only way docs/conformance-status.md is refreshed.
docs-gen:
    go run ./scripts/docsgen/rpcmethods
    go run ./scripts/docsgen/registries
    ./scripts/docsgen/conformance.sh

# Regenerate only the fast, registry-derived docs (no conformance suite).
docs-gen-fast:
    go run ./scripts/docsgen/rpcmethods
    go run ./scripts/docsgen/registries

# Fail if the registry-derived docs are stale (run in CI). Excludes
# conformance-status.md, whose counts change with the full suite and which is
# refreshed only via `just docs-gen`.
docs-check: docs-gen-fast
    git diff --exit-code -- docs/rpc-methods.md docs/supported-transactions.md docs/amendments.md
