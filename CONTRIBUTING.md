# Contributing to debswarm

Thank you for your interest in contributing to debswarm!

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/your-username/debswarm.git`
3. Create a branch: `git checkout -b my-feature`
4. Make your changes
5. Run tests: `make test`
6. Run linter: `make lint`
7. Commit: `git commit -m "Add my feature"`
8. Push: `git push origin my-feature`
9. Create a Pull Request

## Development Setup

### Prerequisites

- Go 1.24 or later
- Make
- golangci-lint (for linting)

Note: No C compiler required - debswarm uses pure Go SQLite.

### Building

```bash
# Install dependencies
go mod download

# Build
make build

# Run tests with race detector
make test

# Run linter
make lint

# Format code
make fmt
```

### Project Structure

```
debswarm/
├── cmd/debswarm/       # CLI application
├── internal/
│   ├── cache/          # Content-addressed package cache
│   ├── config/         # Configuration handling
│   ├── dashboard/      # Real-time web dashboard
│   ├── downloader/     # Parallel chunked download engine
│   ├── index/          # Debian Packages file parser
│   ├── metrics/        # Prometheus metrics
│   ├── mirror/         # HTTP mirror fetcher
│   ├── p2p/            # libp2p networking & DHT
│   ├── peers/          # Peer scoring system
│   ├── proxy/          # HTTP proxy server
│   ├── ratelimit/      # Bandwidth limiting
│   └── timeouts/       # Adaptive timeout manager
├── packaging/          # Debian packaging files
├── debian/             # Debian build configuration
└── docs/               # Documentation
```

## Coding Guidelines

### General

- Follow standard Go code formatting (`go fmt` or `make fmt`)
- Write tests for new functionality
- Keep commits focused and atomic
- Write clear commit messages
- Document exported functions and types

### Error Handling

Error handling is critical for maintainability. Follow these conventions:

```go
// GOOD: Always handle errors explicitly
maxSize, err := config.ParseSize(cfg.Cache.MaxSize)
if err != nil {
    return fmt.Errorf("invalid cache max_size: %w", err)
}

// GOOD: Use %w for error wrapping to preserve error chain
if err := db.Query(...); err != nil {
    return fmt.Errorf("failed to query database: %w", err)
}

// BAD: Never silently ignore errors
maxSize, _ := config.ParseSize(cfg.Cache.MaxSize)  // Don't do this!

// ACCEPTABLE: Ignore errors only in defer cleanup
defer file.Close()  // OK - cleanup errors rarely actionable
```

**Rules:**
- Never ignore errors with `_` except in defer cleanup
- Always wrap errors with context using `fmt.Errorf("context: %w", err)`
- Validate inputs at boundaries (config loading, user input, external APIs)
- Use the config helper methods (e.g., `cfg.Cache.MaxSizeBytes()`) instead of parsing manually

### Logging

Use zap structured logging, not `fmt.Println`:

```go
// GOOD: Structured logging with context
logger.Info("Package downloaded",
    zap.String("hash", hash),
    zap.Int64("size", size),
    zap.Duration("elapsed", elapsed))

// GOOD: Error logging with error field
logger.Error("Failed to connect", zap.Error(err))

// BAD: Don't use fmt for logging
fmt.Printf("Downloaded %s\n", hash)  // Don't do this!
```

### Testing

- All new code should include tests
- Use the race detector: `go test -race ./...`
- Test error paths, not just happy paths
- Use table-driven tests for multiple cases

### Code Style

- Keep functions under 100 lines when practical
- Prefer returning errors over panicking
- Use meaningful variable names
- Add comments for non-obvious logic

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
