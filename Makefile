.PHONY: all build test lint clean install uninstall deb vendor run help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Default target
all: build

# Build the binary
build:
	@echo "Building debswarm $(VERSION)..."
	@mkdir -p build
	go build -ldflags="$(LDFLAGS)" -o build/debswarm ./cmd/debswarm
	@echo "Built: build/debswarm"

# Build for all supported architectures
build-all:
	@echo "Building for all architectures..."
	@mkdir -p build
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o build/debswarm-linux-amd64 ./cmd/debswarm
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o build/debswarm-linux-arm64 ./cmd/debswarm
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o build/debswarm-linux-armv7 ./cmd/debswarm
	@echo "Built all binaries in build/"

# Run tests
test:
	go test -v -race ./...

# Run tests with coverage
test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Run linter
lint:
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run

# Clean build artifacts
clean:
	rm -rf build/
	rm -f coverage.out coverage.html
	rm -rf .gocache/

# Install to system
install: build
	@echo "Installing debswarm..."
	sudo install -m 755 build/debswarm /usr/local/bin/debswarm
	sudo install -m 644 packaging/90debswarm.conf /etc/apt/apt.conf.d/90debswarm.conf
	sudo install -m 644 packaging/debswarm.service /etc/systemd/system/debswarm.service
	sudo mkdir -p /etc/debswarm
	@if [ ! -f /etc/debswarm/config.toml ]; then \
		sudo install -m 644 packaging/config.example.toml /etc/debswarm/config.toml; \
	fi
	sudo systemctl daemon-reload
	@echo ""
	@echo "Installation complete. Run:"
	@echo "  sudo systemctl enable --now debswarm"

# Uninstall from system
uninstall:
	@echo "Uninstalling debswarm..."
	-sudo systemctl stop debswarm
	-sudo systemctl disable debswarm
	sudo rm -f /usr/local/bin/debswarm
	sudo rm -f /etc/apt/apt.conf.d/90debswarm.conf
	sudo rm -f /etc/systemd/system/debswarm.service
	sudo systemctl daemon-reload
	@echo "Uninstalled. Config and cache preserved in /etc/debswarm and /var/cache/debswarm"

# Build Debian package
deb: vendor
	@echo "Building Debian package..."
	dpkg-buildpackage -b -us -uc
	@echo "Package built in parent directory"

# Vendor dependencies
vendor:
	go mod download
	go mod vendor

# Download dependencies
deps:
	go mod download
	go mod tidy

# Run in development mode
run:
	go run ./cmd/debswarm daemon --log-level debug

# Run with race detector
run-race:
	go run -race ./cmd/debswarm daemon --log-level debug

# Format code
fmt:
	go fmt ./...
	gofumpt -w .

# Check if code is formatted
fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "Code not formatted. Run 'make fmt'" && exit 1)

# Generate (if needed in future)
generate:
	go generate ./...

# Show version info
version:
	@echo "Version: $(VERSION)"
	@echo "Commit:  $(COMMIT)"
	@echo "Date:    $(DATE)"

# Help
help:
	@echo "debswarm Makefile"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build        Build binary (default)"
	@echo "  build-all    Build for all architectures"
	@echo "  test         Run tests"
	@echo "  test-coverage Run tests with coverage"
	@echo "  lint         Run linter"
	@echo "  clean        Remove build artifacts"
	@echo "  install      Install to system"
	@echo "  uninstall    Remove from system"
	@echo "  deb          Build Debian package"
	@echo "  vendor       Vendor dependencies"
	@echo "  deps         Download dependencies"
	@echo "  run          Run in development mode"
	@echo "  fmt          Format code"
	@echo "  version      Show version info"
	@echo "  help         Show this help"
