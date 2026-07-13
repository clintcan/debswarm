package sdnotify

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func TestNotify_NoopWithoutSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if Ready() || Watchdog() || Stopping() {
		t.Error("notifications must be no-ops (false) when NOTIFY_SOCKET is unset")
	}
}

func TestWatchdogInterval(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "")
	if _, ok := WatchdogInterval(); ok {
		t.Error("watchdog must be off when WATCHDOG_USEC is unset")
	}

	// 90s window → 45s ping cadence
	t.Setenv("WATCHDOG_USEC", "90000000")
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()))
	interval, ok := WatchdogInterval()
	if !ok {
		t.Fatal("watchdog should be armed")
	}
	if interval != 45*time.Second {
		t.Errorf("interval = %v, want 45s (half the window)", interval)
	}

	// Scoped to another process → not ours
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()+1))
	if _, ok := WatchdogInterval(); ok {
		t.Error("watchdog scoped to another PID must not arm here")
	}

	// Garbage value
	t.Setenv("WATCHDOG_PID", "")
	t.Setenv("WATCHDOG_USEC", "not-a-number")
	if _, ok := WatchdogInterval(); ok {
		t.Error("unparseable WATCHDOG_USEC must not arm the watchdog")
	}
}
