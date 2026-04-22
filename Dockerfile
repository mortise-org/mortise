# Build the UI
FROM node:22-alpine AS ui-builder
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ .
RUN npm run build

# Build the operator binary
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Embed the built UI static files into the location the internal/ui package expects
RUN rm -rf internal/ui/build
COPY --from=ui-builder /ui/build ./internal/ui/build

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o observer ./cmd/observer

# Final image — debian-slim instead of distroless/alpine because Railpack
# auto-downloads mise (a glibc-linked binary) at runtime for framework detection.
FROM debian:bookworm-slim AS operator
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git && \
    rm -rf /var/lib/apt/lists/* && \
    useradd -u 65532 -m mortise && \
    mkdir -p /tmp/railpack && chown mortise:mortise /tmp/railpack
WORKDIR /
COPY --from=builder /workspace/manager .
USER mortise

ENTRYPOINT ["/manager"]

# Observer image — minimal Alpine, no extra dependencies.
FROM alpine:3.21 AS observer
RUN apk add --no-cache ca-certificates && \
    adduser -u 65532 -D observer && \
    mkdir -p /data && chown observer:observer /data
WORKDIR /
COPY --from=builder /workspace/observer .
USER observer
VOLUME ["/data"]

ENTRYPOINT ["/observer"]
