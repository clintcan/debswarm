# GitHub Repository Organization

## Recommended Structure

```
debswarm/
├── .github/
│   ├── workflows/
│   │   ├── ci.yml              # Build & test on every push/PR
│   │   ├── release.yml         # Build releases on tag
│   │   └── debian.yml          # Build .deb packages
│   ├── ISSUE_TEMPLATE/
│   │   ├── bug_report.md
│   │   └── feature_request.md
│   ├── PULL_REQUEST_TEMPLATE.md
│   ├── dependabot.yml
│   └── FUNDING.yml
│
├── cmd/
│   └── debswarm/
│       └── main.go             # CLI entry point
│
├── internal/                   # Private packages
│   ├── cache/
│   ├── config/
│   ├── downloader/
│   ├── index/
│   ├── metrics/
│   ├── mirror/
│   ├── p2p/
│   ├── peers/
│   ├── proxy/
│   └── timeouts/
│
├── pkg/                        # Public packages (if any)
│   └── (empty for now)
│
├── debian/                     # Debian packaging
│   ├── changelog
│   ├── control
│   ├── copyright
│   ├── debswarm.service
│   ├── docs
│   ├── gbp.conf
│   ├── install
│   ├── postinst
│   ├── postrm
│   ├── prerm
│   ├── rules
│   ├── source/
│   │   ├── format
│   │   └── options
│   └── watch
│
├── dist/                       # Distribution files
│   ├── 90debswarm.conf         # APT proxy config
│   ├── debswarm.service        # Standalone systemd service
│   └── config.example.toml     # Example configuration
│
├── docs/                       # Documentation
│   ├── architecture.md
│   ├── configuration.md
│   ├── metrics.md
│   ├── security.md
│   └── images/
│       └── diagram.png
│
├── scripts/                    # Helper scripts
│   ├── install.sh              # Quick install script
│   ├── prepare-source.sh       # Debian source prep
│   └── bootstrap-node.sh       # Set up bootstrap node
│
├── test/                       # Integration tests
│   ├── integration_test.go
│   └── testdata/
│
├── .gitignore
├── .goreleaser.yml             # GoReleaser config
├── go.mod
├── go.sum
├── LICENSE
├── Makefile
├── README.md
├── BUILDING.md
├── CONTRIBUTING.md
├── CHANGELOG.md
└── SECURITY.md
```

## Branch Strategy

```
main                    # Stable, release-ready code
├── develop             # Integration branch
├── feature/*           # New features
├── fix/*               # Bug fixes
├── release/v0.2.0      # Release preparation
└── debian/main         # Debian packaging branch (for gbp)
```

## Key Files

### `.github/workflows/ci.yml`

```yaml
name: CI

on:
  push:
    branches: [main, develop]
  pull_request:
    branches: [main]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      
      - name: Install dependencies
        run: sudo apt-get install -y libsqlite3-dev
      
      - name: Build
        run: go build -v ./...
      
      - name: Test
        run: go test -v -race ./...
      
      - name: Lint
        uses: golangci/golangci-lint-action@v4

  build-matrix:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        goos: [linux]
        goarch: [amd64, arm64, arm]
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      
      - name: Build
        env:
          GOOS: ${{ matrix.goos }}
          GOARCH: ${{ matrix.goarch }}
          CGO_ENABLED: 0
        run: |
          go build -ldflags="-s -w" -o debswarm-${{ matrix.goos }}-${{ matrix.goarch }} ./cmd/debswarm
      
      - uses: actions/upload-artifact@v4
        with:
          name: debswarm-${{ matrix.goos }}-${{ matrix.goarch }}
          path: debswarm-*
```

### `.github/workflows/release.yml`

```yaml
name: Release

on:
  push:
    tags:
      - 'v*'

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      
      - name: Install dependencies
        run: sudo apt-get install -y libsqlite3-dev
      
      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@v5
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

### `.github/workflows/debian.yml`

```yaml
name: Debian Package

on:
  push:
    tags:
      - 'v*'
  workflow_dispatch:

jobs:
  build-deb:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        arch: [amd64, arm64]
    
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      
      - name: Install build dependencies
        run: |
          sudo apt-get update
          sudo apt-get install -y \
            devscripts \
            debhelper \
            libsqlite3-dev \
            pkg-config
      
      - name: Prepare source
        run: |
          go mod download
          go mod vendor
      
      - name: Build package
        run: |
          dpkg-buildpackage -b -us -uc
      
      - name: Upload artifact
        uses: actions/upload-artifact@v4
        with:
          name: debswarm-${{ matrix.arch }}.deb
          path: ../debswarm_*.deb
      
      - name: Upload to release
        if: startsWith(github.ref, 'refs/tags/')
        uses: softprops/action-gh-release@v1
        with:
          files: ../debswarm_*.deb
```

### `.goreleaser.yml`

```yaml
version: 1

before:
  hooks:
    - go mod tidy

builds:
  - id: debswarm
    main: ./cmd/debswarm
    binary: debswarm
    env:
      - CGO_ENABLED=0
    goos:
      - linux
    goarch:
      - amd64
      - arm64
      - arm
    goarm:
      - 7
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}

archives:
  - id: default
    name_template: '{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}'
    format: tar.gz
    files:
      - README.md
      - LICENSE
      - dist/90debswarm.conf
      - dist/debswarm.service
      - dist/config.example.toml

checksum:
  name_template: 'checksums.txt'

changelog:
  sort: asc
  filters:
    exclude:
      - '^docs:'
      - '^test:'
      - '^ci:'

release:
  github:
    owner: debswarm
    name: debswarm
  draft: false
  prerelease: auto
```

### `Makefile`

```makefile
VERSION ?= $(shell git describe --tags --always --dirty)
COMMIT  ?= $(shell git rev-parse --short HEAD)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: all build test lint clean install deb

all: build

build:
	go build -ldflags="$(LDFLAGS)" -o build/debswarm ./cmd/debswarm

build-all:
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o build/debswarm-linux-amd64 ./cmd/debswarm
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o build/debswarm-linux-arm64 ./cmd/debswarm
	GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="$(LDFLAGS)" -o build/debswarm-linux-armv7 ./cmd/debswarm

test:
	go test -v -race ./...

test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint run

clean:
	rm -rf build/ coverage.out coverage.html

install: build
	sudo cp build/debswarm /usr/bin/
	sudo cp dist/90debswarm.conf /etc/apt/apt.conf.d/
	sudo cp dist/debswarm.service /etc/systemd/system/
	sudo systemctl daemon-reload

uninstall:
	sudo systemctl stop debswarm || true
	sudo rm -f /usr/bin/debswarm
	sudo rm -f /etc/apt/apt.conf.d/90debswarm.conf
	sudo rm -f /etc/systemd/system/debswarm.service
	sudo systemctl daemon-reload

deb:
	./scripts/prepare-source.sh
	dpkg-buildpackage -b -us -uc

vendor:
	go mod download
	go mod vendor

run:
	go run ./cmd/debswarm daemon --log-level debug

.PHONY: help
help:
	@echo "Available targets:"
	@echo "  build       - Build binary"
	@echo "  build-all   - Build for all architectures"
	@echo "  test        - Run tests"
	@echo "  lint        - Run linter"
	@echo "  clean       - Remove build artifacts"
	@echo "  install     - Install to system"
	@echo "  uninstall   - Remove from system"
	@echo "  deb         - Build Debian package"
	@echo "  vendor      - Vendor dependencies"
	@echo "  run         - Run in development mode"
```

### `CHANGELOG.md`

```markdown
# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2024-12-12

### Added
- Parallel chunked downloads from multiple peers
- Adaptive timeout system
- Peer scoring and selection
- QUIC transport preference
- Prometheus metrics endpoint
- JSON stats endpoint

### Changed
- Improved NAT traversal with QUIC
- Better peer selection algorithm

### Fixed
- Connection handling edge cases

## [0.1.0] - 2024-12-01

### Added
- Initial release
- HTTP proxy for APT
- P2P downloads via libp2p
- Kademlia DHT for peer discovery
- SHA256 verification
- Mirror fallback
- mDNS local discovery
- SQLite-backed cache
```

### `SECURITY.md`

```markdown
# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.2.x   | :white_check_mark: |
| < 0.2   | :x:                |

## Reporting a Vulnerability

Please report security vulnerabilities to: security@example.com

Do NOT open public issues for security vulnerabilities.

We will acknowledge receipt within 48 hours and provide a detailed
response within 7 days indicating next steps.

## Security Model

debswarm maintains APT's security guarantees:

1. Release files are always fetched from mirrors (GPG-signed)
2. Package hashes come from signed Packages index
3. All P2P downloads are verified against SHA256
4. Hash mismatches result in peer blacklisting
5. No trust is placed in peers
```

## Tags and Releases

```bash
# Create a release
git tag -a v0.2.0 -m "Release v0.2.0"
git push origin v0.2.0

# This triggers:
# 1. ci.yml - runs tests
# 2. release.yml - creates GitHub release with binaries
# 3. debian.yml - builds and uploads .deb packages
```

## Repository Settings

### Branch Protection (main)
- Require pull request reviews
- Require status checks (CI must pass)
- Require branches to be up to date

### Secrets Needed
- `GITHUB_TOKEN` (automatic)
- Optional: `GPG_PRIVATE_KEY` for signed releases

## Initial Setup Commands

```bash
# Clone template
git clone https://github.com/debswarm/debswarm.git
cd debswarm

# Initialize
git init
git add .
git commit -m "Initial commit"

# Create GitHub repo and push
gh repo create debswarm/debswarm --public
git remote add origin git@github.com:debswarm/debswarm.git
git push -u origin main

# Create develop branch
git checkout -b develop
git push -u origin develop

# First release
git tag -a v0.2.0 -m "Initial release"
git push origin v0.2.0
```
