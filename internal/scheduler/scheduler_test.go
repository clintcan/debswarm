package scheduler

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestParseWindow(t *testing.T) {
	tests := []struct {
		name    string
		window  Window
		wantErr bool
	}{
		{
			name: "valid weekday window",
			window: Window{
				Days:      []string{"weekday"},
				StartTime: "09:00",
				EndTime:   "17:00",
			},
			wantErr: false,
		},
		{
			name: "valid weekend window",
			window: Window{
				Days:      []string{"weekend"},
				StartTime: "00:00",
				EndTime:   "23:59",
			},
			wantErr: false,
		},
		{
			name: "valid night window spanning midnight",
			window: Window{
				Days:      []string{"monday", "tuesday"},
				StartTime: "22:00",
				EndTime:   "06:00",
			},
			wantErr: false,
		},
		{
			name: "invalid day",
			window: Window{
				Days:      []string{"funday"},
				StartTime: "09:00",
				EndTime:   "17:00",
			},
			wantErr: true,
		},
		{
			name: "invalid start time",
			window: Window{
				Days:      []string{"monday"},
				StartTime: "25:00",
				EndTime:   "17:00",
			},
			wantErr: true,
		},
		{
			name: "invalid end time format",
			window: Window{
				Days:      []string{"monday"},
				StartTime: "09:00",
				EndTime:   "5pm",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseWindow(tt.window)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseWindow() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParsedWindowContains(t *testing.T) {
	// Create a window for weekdays 09:00-17:00
	pw, err := ParseWindow(Window{
		Days:      []string{"weekday"},
		StartTime: "09:00",
		EndTime:   "17:00",
	})
	if err != nil {
		t.Fatalf("ParseWindow() error = %v", err)
	}

	// Use a fixed location for testing
	loc := time.UTC

	tests := []struct {
		name string
		time time.Time
		want bool
	}{
		{
			name: "Monday 10:00 - in window",
			time: time.Date(2025, 1, 6, 10, 0, 0, 0, loc), // Monday
			want: true,
		},
		{
			name: "Monday 08:59 - before window",
			time: time.Date(2025, 1, 6, 8, 59, 0, 0, loc),
			want: false,
		},
		{
			name: "Monday 17:00 - at end (exclusive)",
			time: time.Date(2025, 1, 6, 17, 0, 0, 0, loc),
			want: false,
		},
		{
			name: "Saturday 12:00 - weekend not in weekday window",
			time: time.Date(2025, 1, 4, 12, 0, 0, 0, loc), // Saturday
			want: false,
		},
		{
			name: "Friday 16:59 - in window",
			time: time.Date(2025, 1, 10, 16, 59, 0, 0, loc), // Friday
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pw.Contains(tt.time); got != tt.want {
				t.Errorf("Contains() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParsedWindowContainsSpanningMidnight(t *testing.T) {
	// Create a window for weekdays 22:00-06:00 (spans midnight)
	pw, err := ParseWindow(Window{
		Days:      []string{"weekday"},
		StartTime: "22:00",
		EndTime:   "06:00",
	})
	if err != nil {
		t.Fatalf("ParseWindow() error = %v", err)
	}

	loc := time.UTC

	tests := []struct {
		name string
		time time.Time
		want bool
	}{
		{
			name: "Monday 23:00 - in window (after start)",
			time: time.Date(2025, 1, 6, 23, 0, 0, 0, loc), // Monday
			want: true,
		},
		{
			name: "Tuesday 03:00 - in window (before end, day started Monday night)",
			time: time.Date(2025, 1, 7, 3, 0, 0, 0, loc), // Tuesday
			want: true,
		},
		{
			name: "Monday 21:59 - before window",
			time: time.Date(2025, 1, 6, 21, 59, 0, 0, loc),
			want: false,
		},
		{
			name: "Monday 12:00 - middle of day",
			time: time.Date(2025, 1, 6, 12, 0, 0, 0, loc),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pw.Contains(tt.time); got != tt.want {
				t.Errorf("Contains() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSchedulerNil(t *testing.T) {
	var s *Scheduler

	// All methods should handle nil gracefully
	if !s.IsInWindow() {
		t.Error("nil scheduler should always be in window")
	}
	if s.GetCurrentRate(false) != 0 {
		t.Error("nil scheduler should return unlimited rate")
	}
	if !s.NextWindowStart().IsZero() {
		t.Error("nil scheduler should return zero time for next window")
	}
}

func TestSchedulerDisabled(t *testing.T) {
	logger := zap.NewNop()
	cfg := &Config{Enabled: false}

	s, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if s != nil {
		t.Error("disabled scheduler should return nil")
	}
}

func TestSchedulerNoWindows(t *testing.T) {
	logger := zap.NewNop()
	cfg := &Config{
		Enabled:           true,
		Windows:           []Window{}, // No windows
		OutsideWindowRate: 100 * 1024,
		InsideWindowRate:  0,
	}

	s, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// With no windows, should always be "in window" (no restrictions)
	if !s.IsInWindow() {
		t.Error("scheduler with no windows should always be in window")
	}
}

func TestSchedulerGetCurrentRate(t *testing.T) {
	logger := zap.NewNop()

	// Create scheduler with a window that's definitely not active
	// (all windows on a day we're not on)
	cfg := &Config{
		Enabled: true,
		Windows: []Window{
			{
				Days:      []string{"sunday"}, // Only Sunday
				StartTime: "00:00",
				EndTime:   "01:00",
			},
		},
		Timezone:          "UTC",
		OutsideWindowRate: 100 * 1024, // 100KB/s outside
		InsideWindowRate:  0,          // unlimited inside
		UrgentFullSpeed:   true,
	}

	s, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Check that urgent requests always get full speed
	if rate := s.GetCurrentRate(true); rate != 0 {
		t.Errorf("urgent request should get unlimited rate, got %d", rate)
	}
}

func TestIsSecurityUpdate(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{
			url:  "http://security.debian.org/debian-security/pool/updates/main/b/bash/bash_5.1.deb",
			want: true,
		},
		{
			url:  "http://archive.ubuntu.com/ubuntu/pool/main/b/bash/bash_5.1.deb",
			want: false,
		},
		{
			url:  "http://deb.debian.org/debian/pool/main/n/nginx/nginx_1.18-security.deb",
			want: true,
		},
		{
			url:  "http://archive.ubuntu.com/ubuntu-updates/pool/main/l/linux/linux_5.4.deb",
			want: true,
		},
		{
			url:  "http://deb.debian.org/debian/pool/main/c/curl/curl_7.74.deb",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := IsSecurityUpdate(tt.url); got != tt.want {
				t.Errorf("IsSecurityUpdate(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestSchedulerStatus(t *testing.T) {
	logger := zap.NewNop()
	cfg := &Config{
		Enabled: true,
		Windows: []Window{
			{
				Days:      []string{"all"},
				StartTime: "00:00",
				EndTime:   "23:59",
			},
		},
		Timezone:          "UTC",
		OutsideWindowRate: 100 * 1024,
		InsideWindowRate:  0,
	}

	s, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	status := s.Status()
	if status.WindowCount != 1 {
		t.Errorf("expected 1 window, got %d", status.WindowCount)
	}
	if status.Timezone != "UTC" {
		t.Errorf("expected UTC timezone, got %s", status.Timezone)
	}
}

func TestParseDays(t *testing.T) {
	tests := []struct {
		days     []string
		wantDays int // Number of days that should be true
	}{
		{[]string{"monday"}, 1},
		{[]string{"mon"}, 1},
		{[]string{"weekday"}, 5},
		{[]string{"weekend"}, 2},
		{[]string{"all"}, 7},
		{[]string{"everyday"}, 7},
		{[]string{"monday", "wednesday", "friday"}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.days[0], func(t *testing.T) {
			pw, err := ParseWindow(Window{
				Days:      tt.days,
				StartTime: "09:00",
				EndTime:   "17:00",
			})
			if err != nil {
				t.Fatalf("ParseWindow() error = %v", err)
			}

			count := 0
			for d := time.Sunday; d <= time.Saturday; d++ {
				if pw.Days[d] {
					count++
				}
			}
			if count != tt.wantDays {
				t.Errorf("got %d days, want %d", count, tt.wantDays)
			}
		})
	}
}
