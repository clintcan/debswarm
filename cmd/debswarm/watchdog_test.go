package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestRunWatchdog verifies the watchdog loop end-to-end against a real
// unixgram notify socket: it pings systemd while the health endpoint answers,
// withholds pings once it stops answering (so systemd can act), and exits on
// context cancellation.
func TestRunWatchdog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unixgram sockets are not available on Windows")
	}

	// Fake systemd notify socket
	sockPath := filepath.Join(t.TempDir(), "notify.sock")
	notifyConn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
	}
	defer notifyConn.Close()
	t.Setenv("NOTIFY_SOCKET", sockPath)

	// Fake /health endpoint
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	host, portStr, err := net.SplitHostPort(healthy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runWatchdog(ctx, 50*time.Millisecond, host, port, zap.NewNop())
		close(done)
	}()

	// While the health endpoint answers, watchdog pings must arrive.
	_ = notifyConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, err := notifyConn.Read(buf)
	if err != nil {
		t.Fatalf("no watchdog ping while healthy: %v", err)
	}
	if string(buf[:n]) != "WATCHDOG=1" {
		t.Fatalf("ping = %q, want WATCHDOG=1", buf[:n])
	}

	// Once the health endpoint is gone, pings must stop — that silence is
	// what lets systemd kill and restart a hung daemon.
	healthy.Close()
	// Drain any ping already in flight, then expect silence.
	_ = notifyConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _ = notifyConn.Read(buf)
	_ = notifyConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if n, err := notifyConn.Read(buf); err == nil {
		t.Errorf("received %q after the health endpoint died; pings must be withheld", buf[:n])
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWatchdog did not exit on context cancellation")
	}
}
