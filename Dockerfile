# Stage 1: Build
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git gcc musl-dev pkgconf openssl-dev openssl-libs-static

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
