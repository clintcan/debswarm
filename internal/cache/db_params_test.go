package cache

import (
	"strings"
	"testing"
)

// TestOpenDatabase_WALEnabled verifies the SQLite connection actually runs with
// the intended pragmas. The modernc.org/sqlite driver silently ignores unknown
// DSN parameters, so a wrong parameter spelling (e.g. the mattn-style
// `_journal_mode=WAL`) leaves the database in DELETE-journal mode with
// synchronous=FULL without any error — this test queries the live connection
// so that regression cannot slip through again.
func TestOpenDatabase_WALEnabled(t *testing.T) {
	c, err := New(t.TempDir(), 1<<20, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()

	var mode string
	if err := c.GetDB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	// 1 = NORMAL. WAL is durable-enough at NORMAL and halves fsyncs vs FULL (2).
	var sync int
	if err := c.GetDB().QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if sync != 1 {
		t.Errorf("synchronous = %d, want 1 (NORMAL)", sync)
	}

	var busy int
	if err := c.GetDB().QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busy <= 0 {
		t.Errorf("busy_timeout = %d, want > 0", busy)
	}
}
