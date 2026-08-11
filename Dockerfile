# Stage 1: Build
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache \
    autoconf automake bash cmake g++ git libtool linux-headers make \
    musl-dev perl pipx pkgconf \
 && pipx install conan==2.18.1

WORKDIR /src

ENV CGO_ENABLED=1 \
    PATH="/root/.local/bin:${PATH}" \
    PKG_CONFIG_PATH="/src/.mpt-crypto"

COPY go.mod go.sum ./
RUN go mod download

COPY conan-mpt-crypto.txt conan-mpt-crypto.lock ./
COPY scripts/setup-mpt-crypto.sh ./scripts/setup-mpt-crypto.sh
RUN ./scripts/setup-mpt-crypto.sh setup

COPY . .

ARG VERSION

RUN if [ -z "${VERSION}" ] || [ "${VERSION}" = "dev" ]; then \
      echo "VERSION must identify a release or commit" >&2; \
      exit 2; \
    fi \
 && go build -tags mptcrypto \
    -trimpath \
    -ldflags="-s -w -linkmode external -extldflags '-static' -X=github.com/LeJamon/go-xrpl/version.Version=${VERSION}" \
    -o /usr/local/bin/goxrpl ./cmd/xrpld \
 && /usr/local/bin/goxrpl version | grep -Fx "Confidential MPT crypto: available"

# Stage 2: Runtime
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /usr/local/bin/goxrpl /usr/local/bin/goxrpl

# 5005  = RPC admin
# 5555  = RPC public
# 6005  = WebSocket public
# 6006  = WebSocket admin
# 51235 = peer protocol
EXPOSE 5005 5555 6005 6006 51235

ENTRYPOINT ["goxrpl"]
CMD ["server", "--conf", "/etc/goxrpl/xrpld.toml"]
