// Package sdnotify implements the systemd sd_notify protocol (readiness and
// watchdog notifications) with no dependencies. Every function is a no-op
// when NOTIFY_SOCKET is unset — non-systemd Linux, containers without
// systemd, and Windows all work unchanged.
package sdnotify

import (
	"net"
	"os"
	"strconv"
	"time"
)

// notify sends one sd_notify datagram. Returns false when not running under
// systemd (NOTIFY_SOCKET unset) or when the send fails.
func notify(state string) bool {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return false
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return false
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err == nil
}

// Ready tells systemd the service has finished starting (Type=notify).
func Ready() bool { return notify("READY=1") }

// Watchdog sends a keep-alive ping (WatchdogSec).
func Watchdog() bool { return notify("WATCHDOG=1") }

// Stopping tells systemd an orderly shutdown has begun.
func Stopping() bool { return notify("STOPPING=1") }

// WatchdogInterval returns the recommended ping cadence (half the
// WATCHDOG_USEC window) and whether the systemd watchdog is armed for this
// process.
func WatchdogInterval() (time.Duration, bool) {
	usecStr := os.Getenv("WATCHDOG_USEC")
	if usecStr == "" {
		return 0, false
	}
	// WATCHDOG_PID, when set, scopes the watchdog to a specific process.
	if pidStr := os.Getenv("WATCHDOG_PID"); pidStr != "" {
		if pid, err := strconv.Atoi(pidStr); err == nil && pid != os.Getpid() {
			return 0, false
		}
	}
	usec, err := strconv.ParseInt(usecStr, 10, 64)
	if err != nil || usec <= 0 {
		return 0, false
	}
	return time.Duration(usec) * time.Microsecond / 2, true
}
