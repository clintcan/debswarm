package sdnotify

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

// TestNotify_SendsDatagrams verifies the actual sd_notify wire protocol
// against a real unixgram socket, exactly as systemd receives it.
func TestNotify_SendsDatagrams(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", sockPath)

	recv := func() string {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read datagram: %v", err)
		}
		return string(buf[:n])
	}

	if !Ready() {
		t.Fatal("Ready() = false with a live socket")
	}
	if got := recv(); got != "READY=1" {
		t.Errorf("Ready sent %q, want READY=1", got)
	}

	if !Watchdog() {
		t.Fatal("Watchdog() = false with a live socket")
	}
	if got := recv(); got != "WATCHDOG=1" {
		t.Errorf("Watchdog sent %q, want WATCHDOG=1", got)
	}

	if !Stopping() {
		t.Fatal("Stopping() = false with a live socket")
	}
	if got := recv(); got != "STOPPING=1" {
		t.Errorf("Stopping sent %q, want STOPPING=1", got)
	}
}
