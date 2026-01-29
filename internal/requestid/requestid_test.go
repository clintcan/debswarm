package requestid

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestGenerate(t *testing.T) {
	id := Generate()

	// Check length (24 hex chars = 12 bytes)
	if len(id) != 24 {
		t.Errorf("Generate() returned ID of length %d, want 24", len(id))
	}

	// Check format (should be valid hex)
	if !IsValid(id) {
		t.Errorf("Generate() returned invalid ID: %s", id)
	}
}

func TestGenerate_Uniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := Generate()
		if seen[id] {
			t.Errorf("Generate() produced duplicate ID: %s", id)
		}
		seen[id] = true
	}
}

func TestGenerate_TimeSortable(t *testing.T) {
	// IDs generated with time gap should be sortable by timestamp prefix
	id1 := Generate()

	// Wait a bit to ensure different timestamp
	time.Sleep(2 * time.Millisecond)

	id2 := Generate()

	// Compare only timestamp portion (first 16 hex chars = 8 bytes)
	// IDs generated later should have >= timestamp prefix
	ts1 := id1[:16]
	ts2 := id2[:16]

	if ts2 < ts1 {
		t.Errorf("Generate() IDs not time-sortable: timestamp %s came after %s but sorts before", ts2, ts1)
	}
}

func TestIsValid(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		valid bool
	}{
		{"valid ID", "0123456789abcdef01234567", true},
		{"valid ID all zeros", "000000000000000000000000", true},
		{"valid ID all f", "ffffffffffffffffffffffff", true},
		{"too short", "0123456789abcdef0123456", false},
		{"too long", "0123456789abcdef012345678", false},
		{"uppercase", "0123456789ABCDEF01234567", false},
		{"invalid chars", "0123456789ghijkl01234567", false},
		{"empty", "", false},
		{"spaces", "0123456789abcdef 1234567", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValid(tt.id); got != tt.valid {
				t.Errorf("IsValid(%q) = %v, want %v", tt.id, got, tt.valid)
			}
		})
	}
}

func TestWithRequestID_FromContext(t *testing.T) {
	ctx := context.Background()
	id := "0123456789abcdef01234567"

	// Initially no ID
	if got := FromContext(ctx); got != "" {
		t.Errorf("FromContext(background) = %q, want empty", got)
	}

	// Add ID
	ctx = WithRequestID(ctx, id)

	// Should retrieve ID
	if got := FromContext(ctx); got != id {
		t.Errorf("FromContext() = %q, want %q", got, id)
	}
}

func TestWithLogger_LoggerFromContext(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)
	fallback := zap.NewNop()

	// Initially returns fallback
	if got := LoggerFromContext(ctx, fallback); got != fallback {
		t.Error("LoggerFromContext(background) did not return fallback")
	}

	// Add logger
	ctx = WithLogger(ctx, logger)

	// Should retrieve logger
	if got := LoggerFromContext(ctx, fallback); got != logger {
		t.Error("LoggerFromContext() did not return stored logger")
	}
}

func TestNewContext(t *testing.T) {
	ctx := context.Background()
	baseLogger := zaptest.NewLogger(t)

	ctx = NewContext(ctx, baseLogger)

	// Should have request ID
	id := FromContext(ctx)
	if id == "" {
		t.Error("NewContext() did not set request ID")
	}
	if !IsValid(id) {
		t.Errorf("NewContext() set invalid request ID: %s", id)
	}

	// Should have scoped logger
	logger := LoggerFromContext(ctx, nil)
	if logger == nil {
		t.Error("NewContext() did not set logger")
	}

	// Logger should be different from base (has field added)
	if logger == baseLogger {
		t.Error("NewContext() did not create scoped logger")
	}
}

func TestNewContextWithID(t *testing.T) {
	ctx := context.Background()
	baseLogger := zaptest.NewLogger(t)
	id := "0123456789abcdef01234567"

	ctx = NewContextWithID(ctx, id, baseLogger)

	// Should have the provided request ID
	if got := FromContext(ctx); got != id {
		t.Errorf("NewContextWithID() set ID %q, want %q", got, id)
	}

	// Should have scoped logger
	logger := LoggerFromContext(ctx, nil)
	if logger == nil {
		t.Error("NewContextWithID() did not set logger")
	}
}
