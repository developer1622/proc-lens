FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

# Build args for cross compilation (used by goreleaser and docker buildx)
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=v1.0.0

WORKDIR /build

# Copy dependencies manifest
COPY go.mod go.sum ./
RUN go mod download

# Copy source code files
COPY cmd/ cmd/
COPY pkg/ pkg/

# Compile completely self-contained static binary
# - CGO_ENABLED=0 disables dynamic C linking
# - ldflags "-s -w" strips debug symbols to minimize file size
# - ldflags "-extldflags -static" forces static linking
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags="-s -w -extldflags -static -X github.com/developer1622/proc-lens/pkg/cmd.Version=${VERSION}" \
    -o proc-lens ./cmd/proc-lens

# ==========================================
# Stage 2: Final minimal distroless runner
# ==========================================
# Distroless static contains only absolute base files (SSL certs, users)
# and has no shell, package manager, or dynamic link interpreters.
FROM gcr.io/distroless/static-debian12

# Copy the compiled static binary from builder stage
COPY --from=builder /build/proc-lens /proc-lens

# Run as non-root user (nobody:nobody)
USER 65534:65534

# Declare container metadata
LABEL org.opencontainers.image.title="proc-lens" \
      org.opencontainers.image.description="Universal Process Intelligence observability agent" \
      org.opencontainers.image.authors="observability-engineers"

# Run the scanner in daemon loop mode by default, logging predictions to stdout
ENTRYPOINT ["/proc-lens"]
CMD ["scan", "--loop", "--interval", "10s"]