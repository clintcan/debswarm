// Package requestid provides request ID generation and context utilities
// for end-to-end request tracing across logs and audit events.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"time"

	"go.uber.org/zap"
)

// contextKey is an unexported type for context keys to prevent collisions
type contextKey int

const (
	requestIDKey contextKey = iota
	loggerKey
)

// validIDRegex validates request ID format: 24 hex characters
var validIDRegex = regexp.MustCompile(`^[0-9a-f]{24}$`)

// Generate creates a new time-sortable request ID.
// Format: 24 hex characters (8 bytes timestamp + 4 bytes random)
// The timestamp prefix ensures IDs are time-sortable for log ordering.
func Generate() string {
	// 8 bytes for millisecond timestamp (covers until year 10889)
	ts := time.Now().UnixMilli()

	// 4 bytes random suffix for uniqueness within same millisecond
	randomBytes := make([]byte, 4)
	_, _ = rand.Read(randomBytes)

	// Combine timestamp and random bytes
	id := make([]byte, 12)
	id[0] = byte(ts >> 56)
	id[1] = byte(ts >> 48)
	id[2] = byte(ts >> 40)
	id[3] = byte(ts >> 32)
	id[4] = byte(ts >> 24)
	id[5] = byte(ts >> 16)
	id[6] = byte(ts >> 8)
	id[7] = byte(ts)
	copy(id[8:], randomBytes)

	return hex.EncodeToString(id)
}

// IsValid checks if a string is a valid request ID format.
// Valid IDs are 24 lowercase hex characters.
func IsValid(id string) bool {
	return validIDRegex.MatchString(id)
}

// WithRequestID adds a request ID to the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// FromContext retrieves the request ID from context.
// Returns empty string if no request ID is present.
func FromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// WithLogger adds a request-scoped logger to the context.
func WithLogger(ctx context.Context, logger *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// LoggerFromContext retrieves the request-scoped logger from context.
// Returns the fallback logger if no request logger is present.
func LoggerFromContext(ctx context.Context, fallback *zap.Logger) *zap.Logger {
	if logger, ok := ctx.Value(loggerKey).(*zap.Logger); ok {
		return logger
	}
	return fallback
}

// NewContext creates a new context with a request ID and scoped logger.
// This is the primary entry point for initializing request tracing.
// It generates a new request ID and creates a logger with the ID field.
func NewContext(ctx context.Context, baseLogger *zap.Logger) context.Context {
	id := Generate()
	ctx = WithRequestID(ctx, id)

	// Create logger with request ID field
	scopedLogger := baseLogger.With(zap.String("requestID", id))
	ctx = WithLogger(ctx, scopedLogger)

	return ctx
}

// NewContextWithID creates a context with an existing request ID and scoped logger.
// Use this when preserving an incoming request ID (e.g., from X-Request-ID header).
func NewContextWithID(ctx context.Context, id string, baseLogger *zap.Logger) context.Context {
	ctx = WithRequestID(ctx, id)

	// Create logger with request ID field
	scopedLogger := baseLogger.With(zap.String("requestID", id))
	ctx = WithLogger(ctx, scopedLogger)

	return ctx
}
