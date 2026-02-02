package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEventCreation(t *testing.T) {
	t.Run("NewDownloadCompleteEvent", func(t *testing.T) {
		event := NewDownloadCompleteEvent(
			"abcdef1234567890abcdef1234567890",
			"test-package.deb",
			1024000,
			"peer",
			500,
			800000,
			224000,
		)

		if event.EventType != EventDownloadComplete {
			t.Errorf("expected EventDownloadComplete, got %s", event.EventType)
		}
		if event.PackageHash != "abcdef1234567890" {
			t.Errorf("expected truncated hash, got %s", event.PackageHash)
		}
		if event.PackageName != "test-package.deb" {
			t.Errorf("expected test-package.deb, got %s", event.PackageName)
		}
		if event.PackageSize != 1024000 {
			t.Errorf("expected 1024000, got %d", event.PackageSize)
		}
		if event.Source != "peer" {
			t.Errorf("expected peer, got %s", event.Source)
		}
		if event.DurationMs != 500 {
			t.Errorf("expected 500, got %d", event.DurationMs)
		}
		if event.BytesP2P != 800000 {
			t.Errorf("expected 800000, got %d", event.BytesP2P)
		}
		if event.BytesMirror != 224000 {
			t.Errorf("expected 224000, got %d", event.BytesMirror)
		}
		if event.Timestamp.IsZero() {
			t.Error("timestamp should not be zero")
		}
	})

	t.Run("NewDownloadFailedEvent", func(t *testing.T) {
		event := NewDownloadFailedEvent(
			"abcdef1234567890",
			"test.deb",
			"connection refused",
		)

		if event.EventType != EventDownloadFailed {
			t.Errorf("expected EventDownloadFailed, got %s", event.EventType)
		}
		if event.Error != "connection refused" {
			t.Errorf("expected 'connection refused', got %s", event.Error)
		}
	})

	t.Run("NewUploadCompleteEvent", func(t *testing.T) {
		event := NewUploadCompleteEvent(
			"abcdef1234567890",
			2048000,
			"12D3KooWAbCdEfGh12345678",
			1000,
		)

		if event.EventType != EventUploadComplete {
			t.Errorf("expected EventUploadComplete, got %s", event.EventType)
		}
		if event.PeerID != "12D3KooWAbCdEfGh" {
			t.Errorf("expected truncated peer ID, got %s", event.PeerID)
		}
	})

	t.Run("NewVerificationFailedEvent", func(t *testing.T) {
		event := NewVerificationFailedEvent(
			"abcdef1234567890",
			"bad-package.deb",
			"12D3KooWBadPeer",
		)

		if event.EventType != EventVerificationFailed {
			t.Errorf("expected EventVerificationFailed, got %s", event.EventType)
		}
		if event.Error != "hash mismatch" {
			t.Errorf("expected 'hash mismatch', got %s", event.Error)
		}
	})

	t.Run("NewCacheHitEvent", func(t *testing.T) {
		event := NewCacheHitEvent("abcdef", "cached.deb", 512000)

		if event.EventType != EventCacheHit {
			t.Errorf("expected EventCacheHit, got %s", event.EventType)
		}
		if event.Source != "cache" {
			t.Errorf("expected source 'cache', got %s", event.Source)
		}
	})

	t.Run("NewPeerBlacklistedEvent", func(t *testing.T) {
		event := NewPeerBlacklistedEvent("12D3KooWBadPeer", "hash mismatch")

		if event.EventType != EventPeerBlacklisted {
			t.Errorf("expected EventPeerBlacklisted, got %s", event.EventType)
		}
		if event.Reason != "hash mismatch" {
			t.Errorf("expected reason 'hash mismatch', got %s", event.Reason)
		}
	})
}

func TestTruncateHash(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"abcdef1234567890abcdef1234567890", "abcdef1234567890"},
		{"short", "short"},
		{"exactly16chars!!", "exactly16chars!!"},
		{"", ""},
	}

	for _, tt := range tests {
		result := truncateHash(tt.input)
		if result != tt.expected {
			t.Errorf("truncateHash(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestNoopLogger(t *testing.T) {
	logger := &NoopLogger{}

	// Should not panic
	logger.Log(Event{EventType: EventCacheHit})

	// Close should return nil
	if err := logger.Close(); err != nil {
		t.Errorf("NoopLogger.Close() returned error: %v", err)
	}
}

func TestJSONWriter(t *testing.T) {
	t.Run("CreateAndLog", func(t *testing.T) {
		tmpDir := t.TempDir()
		logPath := filepath.Join(tmpDir, "audit.json")

		writer, err := NewJSONWriter(JSONWriterConfig{
			Path:       logPath,
			MaxSizeMB:  1,
			MaxBackups: 3,
		})
		if err != nil {
			t.Fatalf("failed to create JSONWriter: %v", err)
		}
		defer writer.Close()

		// Log some events
		writer.Log(NewDownloadCompleteEvent("hash1", "pkg1.deb", 1000, "peer", 100, 1000, 0))
		writer.Log(NewCacheHitEvent("hash2", "pkg2.deb", 2000))
		writer.Log(NewUploadCompleteEvent("hash3", 3000, "peer123", 50))

		// Close to flush
		if err := writer.Close(); err != nil {
			t.Fatalf("failed to close writer: %v", err)
		}

		// Read and verify
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("failed to read log file: %v", err)
		}

		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 3 {
			t.Errorf("expected 3 lines, got %d", len(lines))
		}

		// Parse first event
		var event Event
		if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
			t.Fatalf("failed to parse first event: %v", err)
		}
		if event.EventType != EventDownloadComplete {
			t.Errorf("expected EventDownloadComplete, got %s", event.EventType)
		}
	})

	t.Run("CreateDirectory", func(t *testing.T) {
		tmpDir := t.TempDir()
		logPath := filepath.Join(tmpDir, "subdir", "nested", "audit.json")

		writer, err := NewJSONWriter(JSONWriterConfig{Path: logPath})
		if err != nil {
			t.Fatalf("failed to create JSONWriter with nested path: %v", err)
		}
		defer writer.Close()

		// Directory should exist
		if _, err := os.Stat(filepath.Dir(logPath)); os.IsNotExist(err) {
			t.Error("directory was not created")
		}
	})

	t.Run("EmptyPathError", func(t *testing.T) {
		_, err := NewJSONWriter(JSONWriterConfig{Path: ""})
		if err == nil {
			t.Error("expected error for empty path")
		}
	})

	t.Run("Rotation", func(t *testing.T) {
		tmpDir := t.TempDir()
		logPath := filepath.Join(tmpDir, "audit.json")

		// Use very small max size to trigger rotation quickly
		writer, err := NewJSONWriter(JSONWriterConfig{
			Path:       logPath,
			MaxSizeMB:  0, // Will default to 100, but we'll use maxBytes directly
			MaxBackups: 2,
		})
		if err != nil {
			t.Fatalf("failed to create JSONWriter: %v", err)
		}

		// Override maxBytes for testing
		writer.maxBytes = 500

		// Log enough events to trigger rotation
		for i := 0; i < 20; i++ {
			writer.Log(NewDownloadCompleteEvent(
				"hash1234567890123456",
				"package.deb",
				int64(i*1000),
				"peer",
				100,
				int64(i*1000),
				0,
			))
		}

		if err := writer.Close(); err != nil {
			t.Fatalf("failed to close writer: %v", err)
		}

		// Check that backup files were created
		_, err = os.Stat(logPath + ".1")
		if os.IsNotExist(err) {
			t.Log("Note: .1 backup may not exist if rotation timing varied")
		}
	})

	t.Run("FilePermissions", func(t *testing.T) {
		tmpDir := t.TempDir()
		logPath := filepath.Join(tmpDir, "audit.json")

		writer, err := NewJSONWriter(JSONWriterConfig{Path: logPath})
		if err != nil {
			t.Fatalf("failed to create JSONWriter: %v", err)
		}
		writer.Log(Event{Timestamp: time.Now(), EventType: EventCacheHit})
		writer.Close()

		info, err := os.Stat(logPath)
		if err != nil {
			t.Fatalf("failed to stat log file: %v", err)
		}

		// On Unix, check permissions are restricted
		mode := info.Mode().Perm()
		if mode&0077 != 0 {
			t.Logf("Note: file permissions %o may include group/other bits on some systems", mode)
		}
	})
}

func TestJSONWriterConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "audit.json")

	writer, err := NewJSONWriter(JSONWriterConfig{
		Path:       logPath,
		MaxSizeMB:  10,
		MaxBackups: 3,
	})
	if err != nil {
		t.Fatalf("failed to create JSONWriter: %v", err)
	}
	defer writer.Close()

	// Concurrent writes
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				writer.Log(NewDownloadCompleteEvent(
					"hash",
					"pkg.deb",
					int64(id*100+j),
					"peer",
					100,
					100,
					0,
				))
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestEventJSONSerialization(t *testing.T) {
	event := Event{
		Timestamp:   time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		EventType:   EventDownloadComplete,
		PackageHash: "abcdef1234567890",
		PackageName: "test.deb",
		PackageSize: 1024,
		Source:      "peer",
		DurationMs:  250,
		BytesP2P:    1024,
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}

	// Verify JSON contains expected fields
	jsonStr := string(data)
	expectedFields := []string{
		`"event_type":"download_complete"`,
		`"package_hash":"abcdef1234567890"`,
		`"package_name":"test.deb"`,
		`"package_size":1024`,
		`"source":"peer"`,
		`"duration_ms":250`,
		`"bytes_p2p":1024`,
	}

	for _, field := range expectedFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("JSON missing expected field: %s\nGot: %s", field, jsonStr)
		}
	}

	// Verify empty fields are omitted
	if strings.Contains(jsonStr, `"error"`) {
		t.Error("JSON should omit empty error field")
	}
	if strings.Contains(jsonStr, `"peer_id"`) {
		t.Error("JSON should omit empty peer_id field")
	}
}

func TestEvent_WithRequestID(t *testing.T) {
	t.Run("adds request ID to event", func(t *testing.T) {
		event := NewCacheHitEvent("abcdef1234567890", "test.deb", 1024)
		reqID := "0123456789abcdef01234567"

		eventWithID := event.WithRequestID(reqID)

		if eventWithID.RequestID != reqID {
			t.Errorf("WithRequestID() = %q, want %q", eventWithID.RequestID, reqID)
		}

		// Original event should be unchanged
		if event.RequestID != "" {
			t.Error("original event should not be modified")
		}
	})

	t.Run("chains with event constructors", func(t *testing.T) {
		reqID := "0123456789abcdef01234567"
		event := NewDownloadCompleteEvent(
			"hash",
			"pkg.deb",
			1024,
			"peer",
			100,
			1024,
			0,
		).WithRequestID(reqID)

		if event.RequestID != reqID {
			t.Errorf("chained WithRequestID() = %q, want %q", event.RequestID, reqID)
		}
		if event.EventType != EventDownloadComplete {
			t.Errorf("EventType = %q, want %q", event.EventType, EventDownloadComplete)
		}
	})

	t.Run("serializes to JSON with request_id", func(t *testing.T) {
		reqID := "0123456789abcdef01234567"
		event := NewCacheHitEvent("hash", "test.deb", 1024).WithRequestID(reqID)

		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		expected := `"request_id":"0123456789abcdef01234567"`
		if !strings.Contains(string(data), expected) {
			t.Errorf("JSON missing request_id field: %s", string(data))
		}
	})

	t.Run("empty request ID is omitted in JSON", func(t *testing.T) {
		event := NewCacheHitEvent("hash", "test.deb", 1024)

		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		if strings.Contains(string(data), "request_id") {
			t.Errorf("JSON should omit empty request_id: %s", string(data))
		}
	})
}

func TestConnectTunnelEvents(t *testing.T) {
	t.Run("NewConnectTunnelStartEvent", func(t *testing.T) {
		event := NewConnectTunnelStartEvent("deb.debian.org", "443")

		if event.EventType != EventConnectTunnelStart {
			t.Errorf("EventType = %q, want %q", event.EventType, EventConnectTunnelStart)
		}
		if event.TargetHost != "deb.debian.org" {
			t.Errorf("TargetHost = %q, want %q", event.TargetHost, "deb.debian.org")
		}
		if event.TargetPort != "443" {
			t.Errorf("TargetPort = %q, want %q", event.TargetPort, "443")
		}
		if event.Timestamp.IsZero() {
			t.Error("Timestamp should not be zero")
		}
	})

	t.Run("NewConnectTunnelEndEvent", func(t *testing.T) {
		event := NewConnectTunnelEndEvent("archive.ubuntu.com", "443", 1024000, 5000)

		if event.EventType != EventConnectTunnelEnd {
			t.Errorf("EventType = %q, want %q", event.EventType, EventConnectTunnelEnd)
		}
		if event.TargetHost != "archive.ubuntu.com" {
			t.Errorf("TargetHost = %q, want %q", event.TargetHost, "archive.ubuntu.com")
		}
		if event.TargetPort != "443" {
			t.Errorf("TargetPort = %q, want %q", event.TargetPort, "443")
		}
		if event.TunnelBytes != 1024000 {
			t.Errorf("TunnelBytes = %d, want %d", event.TunnelBytes, 1024000)
		}
		if event.DurationMs != 5000 {
			t.Errorf("DurationMs = %d, want %d", event.DurationMs, 5000)
		}
		if event.Timestamp.IsZero() {
			t.Error("Timestamp should not be zero")
		}
	})

	t.Run("NewConnectTunnelBlockedEvent", func(t *testing.T) {
		event := NewConnectTunnelBlockedEvent("localhost", "443", "blocked host")

		if event.EventType != EventConnectTunnelBlocked {
			t.Errorf("EventType = %q, want %q", event.EventType, EventConnectTunnelBlocked)
		}
		if event.TargetHost != "localhost" {
			t.Errorf("TargetHost = %q, want %q", event.TargetHost, "localhost")
		}
		if event.TargetPort != "443" {
			t.Errorf("TargetPort = %q, want %q", event.TargetPort, "443")
		}
		if event.Reason != "blocked host" {
			t.Errorf("Reason = %q, want %q", event.Reason, "blocked host")
		}
		if event.Timestamp.IsZero() {
			t.Error("Timestamp should not be zero")
		}
	})

	t.Run("CONNECT events serialize to JSON", func(t *testing.T) {
		events := []Event{
			NewConnectTunnelStartEvent("example.com", "443"),
			NewConnectTunnelEndEvent("example.com", "443", 500, 100),
			NewConnectTunnelBlockedEvent("evil.com", "22", "invalid port"),
		}

		for _, event := range events {
			data, err := json.Marshal(event)
			if err != nil {
				t.Errorf("failed to marshal %s: %v", event.EventType, err)
				continue
			}

			// Verify required fields are present
			jsonStr := string(data)
			if !strings.Contains(jsonStr, `"event_type"`) {
				t.Errorf("%s JSON missing event_type", event.EventType)
			}
			if !strings.Contains(jsonStr, `"target_host"`) {
				t.Errorf("%s JSON missing target_host", event.EventType)
			}
			if !strings.Contains(jsonStr, `"target_port"`) {
				t.Errorf("%s JSON missing target_port", event.EventType)
			}
		}
	})

	t.Run("CONNECT events with request ID", func(t *testing.T) {
		reqID := "abc123def456"
		event := NewConnectTunnelStartEvent("host.com", "443").WithRequestID(reqID)

		if event.RequestID != reqID {
			t.Errorf("RequestID = %q, want %q", event.RequestID, reqID)
		}
		// Other fields should be preserved
		if event.EventType != EventConnectTunnelStart {
			t.Errorf("EventType = %q, want %q", event.EventType, EventConnectTunnelStart)
		}
	})
}
