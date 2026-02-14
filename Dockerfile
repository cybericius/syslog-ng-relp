# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build

COPY go.mod ./
COPY cmd/ cmd/

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo 'dev')" \
    -o relp-forwarder ./cmd/relp-forwarder/ && \
    CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo 'dev')" \
    -o relp-listener ./cmd/relp-listener/

# Runtime stage
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/relp-forwarder /relp-forwarder
COPY --from=builder /build/relp-listener /relp-listener

ENTRYPOINT ["/relp-forwarder"]
