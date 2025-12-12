# Contributing to debswarm

Thank you for your interest in contributing to debswarm!

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/your-username/apt-p2p.git`
3. Create a branch: `git checkout -b my-feature`
4. Make your changes
5. Run tests: `make test`
6. Commit: `git commit -m "Add my feature"`
7. Push: `git push origin my-feature`
8. Create a Pull Request

## Development Setup

### Prerequisites

- Go 1.22 or later
- GCC (for SQLite)
- Make

### Building

```bash
# Install dependencies
go mod download

# Build
make build

# Run tests
make test

# Run linter
make lint
```

### Project Structure

```
apt-p2p/
├── cmd/
│   └── apt-p2p/      # CLI application
│       └── main.go
├── internal/
│   ├── cache/        # Content-addressed package cache
│   ├── config/       # Configuration handling
│   ├── index/        # Debian Packages file parser
│   ├── mirror/       # HTTP mirror fetcher
│   ├── p2p/          # libp2p networking
│   └── proxy/        # HTTP proxy server
├── go.mod
├── Makefile
└── README.md
```

## Coding Guidelines

- Follow standard Go code formatting (use `go fmt`)
- Write tests for new functionality
- Keep commits focused and atomic
- Write clear commit messages
- Document exported functions and types

## Testing

### Running Tests

```bash
# Run all tests
make test

# Run tests with coverage
go test -cover ./...

# Run specific package tests
go test -v ./internal/cache/
```

### Manual Testing

```bash
# Start daemon in debug mode
make run

# In another terminal, test with APT
sudo apt-get update
sudo apt-get install --reinstall hello
```

## Bug Reports

When filing a bug report, please include:

- Operating system and version
- Go version
- apt-p2p version
- Steps to reproduce
- Expected behavior
- Actual behavior
- Relevant log output

## Feature Requests

Feature requests are welcome! Please describe:

- The problem you're trying to solve
- Proposed solution
- Alternative approaches considered

## Code Review

All submissions require review. We use GitHub pull requests for this purpose.

## License

By contributing, you agree that your contributions will be licensed under the GPL-2.0 license.
