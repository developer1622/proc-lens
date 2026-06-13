# ==========================================
# proc-lens Makefile (Google Engineering Style)
# ==========================================

BINARY_NAME=proc-lens
CMD_PATH=./cmd/proc-lens
VERSION=v1.0.0

LDFLAGS_STATIC=-ldflags "-s -w -extldflags -static -X github.com/developer1622/proc-lens/pkg/cmd.Version=$(VERSION)"
LDFLAGS_DYN=-ldflags "-s -w -X github.com/developer1622/proc-lens/pkg/cmd.Version=$(VERSION)"

.PHONY: all build test clean docker-build helm-lint helm-install help

default: help

all: test build docker-build

build:
	@echo "==> Compiling local Windows binary..."
	go build $(LDFLAGS_DYN) -o $(BINARY_NAME).exe $(CMD_PATH)
	@echo "==> Compiling fully static Linux binary (CGO disabled)..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS_STATIC) -o $(BINARY_NAME) $(CMD_PATH)
	@echo "==> Compiling macOS Intel binary..."
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS_DYN) -o $(BINARY_NAME)-mac-intel $(CMD_PATH)
	@echo "==> Compiling macOS Apple Silicon binary..."
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS_DYN) -o $(BINARY_NAME)-mac-silicon $(CMD_PATH)
	@echo "Build complete."

test:
	@echo "==> Running Go unit tests with race detector..."
	go test -race ./... -v

clean:
	@echo "==> Cleaning build artifacts..."
	rm -f $(BINARY_NAME) $(BINARY_NAME).exe $(BINARY_NAME)-mac-intel $(BINARY_NAME)-mac-silicon
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
	@echo "  build          Compile Windows, Linux static, and macOS binaries"
	@echo "  test           Run all Go package unit tests"
	@echo "  clean          Remove compiled binaries"
	@echo "  docker-build   Build minimal distroless Docker container"
	@echo "  helm-lint      Run Helm chart lint validation"
	@echo "  helm-install   Deploy DaemonSet to current K8s cluster kube-system namespace"
	@echo ""

