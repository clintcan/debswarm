package audit

// Logger is the interface for audit logging
type Logger interface {
	// Log records an audit event
	Log(event Event)

	// Close flushes any pending writes and closes the logger
	Close() error
}

// NoopLogger is a no-op implementation of Logger for when auditing is disabled
type NoopLogger struct{}

// Log does nothing
func (n *NoopLogger) Log(_ Event) {}

// Close does nothing and returns nil
func (n *NoopLogger) Close() error {
	return nil
}

// Ensure NoopLogger implements Logger
var _ Logger = (*NoopLogger)(nil)
