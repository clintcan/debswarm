// Package scheduler provides time-based download scheduling with rate limiting.
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Window represents a configured time window for sync operations.
type Window struct {
	Days      []string `toml:"days"`       // "monday", "tuesday", etc. or "weekday", "weekend"
	StartTime string   `toml:"start_time"` // "09:00" (24h format)
	EndTime   string   `toml:"end_time"`   // "17:00"
}

// ParsedWindow is a pre-parsed time window for efficient evaluation.
type ParsedWindow struct {
	Days      map[time.Weekday]bool
	StartHour int
	StartMin  int
	EndHour   int
	EndMin    int
	SpansDay  bool // true if window crosses midnight (e.g., 22:00 - 06:00)
}

// ParseWindow parses a Window configuration into a ParsedWindow.
func ParseWindow(w Window) (*ParsedWindow, error) {
	pw := &ParsedWindow{
		Days: make(map[time.Weekday]bool),
	}

	// Parse days
	for _, day := range w.Days {
		switch strings.ToLower(day) {
		case "monday", "mon":
			pw.Days[time.Monday] = true
		case "tuesday", "tue":
			pw.Days[time.Tuesday] = true
		case "wednesday", "wed":
			pw.Days[time.Wednesday] = true
		case "thursday", "thu":
			pw.Days[time.Thursday] = true
		case "friday", "fri":
			pw.Days[time.Friday] = true
		case "saturday", "sat":
			pw.Days[time.Saturday] = true
		case "sunday", "sun":
			pw.Days[time.Sunday] = true
		case "weekday", "weekdays":
			pw.Days[time.Monday] = true
			pw.Days[time.Tuesday] = true
			pw.Days[time.Wednesday] = true
			pw.Days[time.Thursday] = true
			pw.Days[time.Friday] = true
		case "weekend", "weekends":
			pw.Days[time.Saturday] = true
			pw.Days[time.Sunday] = true
		case "all", "everyday", "daily":
			for d := time.Sunday; d <= time.Saturday; d++ {
				pw.Days[d] = true
			}
		default:
			return nil, fmt.Errorf("invalid day: %s", day)
		}
	}

	// Parse start time
	startHour, startMin, err := parseTime(w.StartTime)
	if err != nil {
		return nil, fmt.Errorf("invalid start_time %q: %w", w.StartTime, err)
	}
	pw.StartHour = startHour
	pw.StartMin = startMin

	// Parse end time
	endHour, endMin, err := parseTime(w.EndTime)
	if err != nil {
		return nil, fmt.Errorf("invalid end_time %q: %w", w.EndTime, err)
	}
	pw.EndHour = endHour
	pw.EndMin = endMin

	// Determine if window spans midnight
	startMins := pw.StartHour*60 + pw.StartMin
	endMins := pw.EndHour*60 + pw.EndMin
	// De Morgan: !(startMins == 0 && endMins == 0) => (startMins != 0 || endMins != 0)
	pw.SpansDay = endMins <= startMins && (startMins != 0 || endMins != 0)

	return pw, nil
}

// parseTime parses a time string in "HH:MM" format.
func parseTime(s string) (hour, min int, err error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM format")
	}

	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour: %s", parts[0])
	}

	min, err = strconv.Atoi(parts[1])
	if err != nil || min < 0 || min > 59 {
		return 0, 0, fmt.Errorf("invalid minute: %s", parts[1])
	}

	return hour, min, nil
}

// Contains checks if the given time falls within this window.
func (pw *ParsedWindow) Contains(t time.Time) bool {
	currentMins := t.Hour()*60 + t.Minute()
	startMins := pw.StartHour*60 + pw.StartMin
	endMins := pw.EndHour*60 + pw.EndMin

	today := pw.Days[t.Weekday()]

	if !pw.SpansDay {
		// Normal window (e.g., 09:00 - 17:00): must be a configured day and
		// start <= current < end.
		return today && currentMins >= startMins && currentMins < endMins
	}

	// Spanning window (e.g., 22:00 - 06:00). A window opens at startMins on a
	// configured day D and closes at endMins the next morning (D+1). So the two
	// halves belong to *different* days and must be checked separately — using the
	// same OR test for both (the previous bug) let a spanning window match the
	// evening of an unconfigured day and the morning after an unconfigured day.
	//
	// In-window if either:
	//   - today is configured and we're at/after the start (this evening's half), or
	//   - yesterday was configured and we're before the end (that window's morning half).
	if today && currentMins >= startMins {
		return true
	}
	yesterday := pw.Days[t.Add(-24*time.Hour).Weekday()]
	return yesterday && currentMins < endMins
}

// NextStart returns the next time this window opens, relative to the given time.
// Returns zero time if no valid days are configured.
func (pw *ParsedWindow) NextStart(from time.Time) time.Time {
	if len(pw.Days) == 0 {
		return time.Time{}
	}

	// Start checking from current time
	candidate := from

	// Check up to 8 days ahead (covers all cases including spanning midnight)
	for i := 0; i < 8; i++ {
		checkDay := candidate.Weekday()

		if pw.Days[checkDay] {
			// This day is valid, calculate window start time
			windowStart := time.Date(
				candidate.Year(), candidate.Month(), candidate.Day(),
				pw.StartHour, pw.StartMin, 0, 0,
				candidate.Location(),
			)

			// If window start is in the future, return it
			if windowStart.After(from) {
				return windowStart
			}

			// If we're currently in this window, the next start is the same time tomorrow
			// (or the next valid day)
		}

		// Move to next day at midnight
		candidate = time.Date(
			candidate.Year(), candidate.Month(), candidate.Day()+1,
			0, 0, 0, 0,
			candidate.Location(),
		)
	}

	// Should not reach here if days are configured
	return time.Time{}
}
