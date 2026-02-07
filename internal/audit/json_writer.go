package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// countingWriter wraps an os.File and tracks bytes written for accurate rotation decisions.
type countingWriter struct {
	file    *os.File
	written int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.file.Write(p)
	cw.written += int64(n)
	return n, err
}

// JSONWriter writes audit events to a JSON file with rotation support
type JSONWriter struct {
	path       string
	maxBytes   int64
	maxBackups int

	file    *os.File
	counter *countingWriter
	encoder *json.Encoder
	mu      sync.Mutex
}

// JSONWriterConfig configures the JSON audit writer
type JSONWriterConfig struct {
	// Path is the file path for the audit log
	Path string

	// MaxSizeMB is the maximum file size before rotation (default: 100)
	MaxSizeMB int

	// MaxBackups is the number of rotated files to keep (default: 5)
	MaxBackups int
}

// NewJSONWriter creates a new JSON audit log writer
func NewJSONWriter(cfg JSONWriterConfig) (*JSONWriter, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("audit log path is required")
	}

	// Set defaults
	if cfg.MaxSizeMB <= 0 {
		cfg.MaxSizeMB = 100
	}
	if cfg.MaxBackups <= 0 {
		cfg.MaxBackups = 5
	}

	// Ensure directory exists
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create audit log directory: %w", err)
	}

	w := &JSONWriter{
		path:       cfg.Path,
		maxBytes:   int64(cfg.MaxSizeMB) * 1024 * 1024,
		maxBackups: cfg.MaxBackups,
	}

	// Open or create the log file
	if err := w.openFile(); err != nil {
		return nil, err
	}

	return w, nil
}

// openFile opens the audit log file for appending
func (w *JSONWriter) openFile() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open audit log: %w", err)
	}

	// Get current file size
	info, err := f.Stat()
	if err != nil {
		if closeErr := f.Close(); closeErr != nil {
			return fmt.Errorf("failed to stat audit log: %w (also failed to close: %v)", err, closeErr)
		}
		return fmt.Errorf("failed to stat audit log: %w", err)
	}

	w.file = f
	w.counter = &countingWriter{file: f, written: info.Size()}
	w.encoder = json.NewEncoder(w.counter)

	return nil
}

// Log writes an audit event to the JSON file
func (w *JSONWriter) Log(event Event) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return
	}

	// Check if rotation is needed
	if w.counter.written >= w.maxBytes {
		if err := w.rotate(); err != nil {
			// Rotation failed — continue writing rather than silently dropping
			// the event. The file may grow beyond maxBytes, which is acceptable.
		}
	}

	// Encode and write the event (byte count tracked automatically by countingWriter)
	if err := w.encoder.Encode(event); err != nil {
		return
	}
}

// rotate rotates the log file
func (w *JSONWriter) rotate() error {
	// Close current file
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return fmt.Errorf("failed to close audit log for rotation: %w", err)
		}
	}

	// Rotate existing backups
	for i := w.maxBackups - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", w.path, i)
		newPath := fmt.Sprintf("%s.%d", w.path, i+1)
		// Ignore errors - files may not exist
		_ = os.Rename(oldPath, newPath)
	}

	// Move current log to .1
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		// Try to reopen the original file
		if openErr := w.openFile(); openErr != nil {
			return fmt.Errorf("failed to rotate audit log: %w (also failed to reopen: %v)", err, openErr)
		}
		return fmt.Errorf("failed to rotate audit log: %w", err)
	}

	// Delete oldest backup if it exceeds maxBackups
	oldestPath := fmt.Sprintf("%s.%d", w.path, w.maxBackups+1)
	_ = os.Remove(oldestPath)

	// Open new log file
	return w.openFile()
}

// Close closes the JSON writer
func (w *JSONWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}

	err := w.file.Close()
	w.file = nil
	w.counter = nil
	w.encoder = nil
	return err
}

// Ensure JSONWriter implements Logger
var _ Logger = (*JSONWriter)(nil)
