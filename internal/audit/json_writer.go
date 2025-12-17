package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// JSONWriter writes audit events to a JSON file with rotation support
type JSONWriter struct {
	path       string
	maxBytes   int64
	maxBackups int

	file    *os.File
	encoder *json.Encoder
	written int64
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
	w.encoder = json.NewEncoder(f)
	w.written = info.Size()

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
	if w.written >= w.maxBytes {
		if err := w.rotate(); err != nil {
			// Log rotation failed, but continue writing
			// The file may grow beyond maxBytes
			return
		}
	}

	// Encode and write the event
	if err := w.encoder.Encode(event); err != nil {
		return
	}

	// Estimate bytes written (JSON + newline)
	// This is approximate but good enough for rotation decisions
	w.written += 200 // Rough average per event
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
	w.encoder = nil
	return err
}

// Ensure JSONWriter implements Logger
var _ Logger = (*JSONWriter)(nil)
