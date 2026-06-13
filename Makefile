# ==========================================
# proc-lens Makefile (Google Engineering Style)
# ==========================================

# Detect current platform (used by default for `make build`)
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

BINARY_NAME=proc-lens
CMD_PATH=./cmd/proc-lens
VERSION=v1.0.0

# Use full static flags for portable binaries (recommended for releases and local use)
LDFLAGS=-ldflags "-s -w -extldflags -static -X github.com/developer1622/proc-lens/pkg/cmd.Version=$(VERSION)"

# Determine output name (add .exe on Windows)
ifeq ($(GOOS),windows)
	BINARY_OUT=$(BINARY_NAME).exe
else
	BINARY_OUT=$(BINARY_NAME)
endif

.PHONY: all build build-all test clean docker-build helm-lint helm-install help

default: help

all: test build docker-build

# Build for the current platform (or override with GOOS/GOARCH)
# Examples:
#   make build                     # native build (linux/amd64 on most Linux machines)
#   make build GOOS=windows GOARCH=amd64
#   make build GOOS=linux GOARCH=arm64
build:
	@echo "==> Building for $(GOOS)/$(GOARCH)..."
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(LDFLAGS) -o $(BINARY_OUT) $(CMD_PATH)
	@echo "Build complete: $(BINARY_OUT)"

# Build all supported platforms (previous default behavior)
build-all:
	@echo "==> Building all platforms..."
	$(MAKE) build GOOS=windows GOARCH=amd64 BINARY_OUT=proc-lens.exe
	$(MAKE) build GOOS=linux GOARCH=amd64
	$(MAKE) build GOOS=darwin GOARCH=amd64 BINARY_OUT=proc-lens-mac-intel
	$(MAKE) build GOOS=darwin GOARCH=arm64 BINARY_OUT=proc-lens-mac-silicon
	@echo "Build complete."

test:
	@echo "==> Running Go unit tests with race detector..."
	go test -race ./... -v

clean:
	@echo "==> Cleaning build artifacts..."
	rm -f proc-lens proc-lens.exe proc-lens-mac-intel proc-lens-mac-silicon
	@echo "Clean complete."

docker-build:
	@echo "==> Building secure distroless Docker image..."
	docker build -t $(BINARY_NAME):latest .

helm-lint:
	@echo "==> Validating Helm chart templates..."
	helm lint deploy/$(BINARY_NAME)

helm-install:
	@echo "==> Deploying proc-lens DaemonSet to Kubernetes..."
	helm upgrade --install $(BINARY_NAME) deploy/$(BINARY_NAME) --namespace kube-system

help:
	@echo "proc-lens CLI developer automation utility."
	@echo ""
	@echo "Targets:"
	@echo "  build          Build for current platform (override with GOOS/GOARCH)"
	@echo "  build-all      Build for Windows + Linux + macOS (all common arches)"
	@echo "  test           Run all Go package unit tests"
	@echo "  clean          Remove compiled binaries"
	@echo "  docker-build   Build minimal distroless Docker container"
	@echo "  helm-lint      Run Helm chart lint validation"
	@echo "  helm-install   Deploy DaemonSet to current K8s cluster kube-system namespace"
	@echo ""

