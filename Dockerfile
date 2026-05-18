# Stage 1: Build
FROM golang:1.24-alpine AS builder

# Alpine ships libsecp256k1 only as a shared .so, so build the static .a
# from upstream for the distroless static-linked binary.
ARG LIBSECP256K1_VERSION=v0.5.0

RUN apk add --no-cache \
    git gcc make musl-dev pkgconf autoconf automake libtool \
    openssl-dev openssl-libs-static \
 && git clone --depth 1 --branch ${LIBSECP256K1_VERSION} \
    https://github.com/bitcoin-core/secp256k1.git /tmp/secp256k1 \
 && cd /tmp/secp256k1 \
 && ./autogen.sh \
 && ./configure --disable-shared --enable-static \
    --disable-tests --disable-benchmark --disable-exhaustive-tests \
    --enable-module-recovery=no \
 && make -j"$(nproc)" install \
 && rm -rf /tmp/secp256k1

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 go build \
    -trimpath \
    -ldflags="-s -w -linkmode external -extldflags '-static'" \
    -o /usr/local/bin/goxrpl ./cmd/xrpld

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
