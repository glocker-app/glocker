package utils

import (
	"slices"
	"time"

	"glocker/internal/config"
)

// IsValidTime checks if a time string is in valid HH:MM format.
// Returns true if the string can be parsed as a time, false otherwise.
func IsValidTime(timeStr string) bool {
	_, err := time.Parse("15:04", timeStr)
	return err == nil
}

// IsInTimeWindow checks if the current time falls within a time window.
// Handles wraparound cases where the end time is before the start time (e.g., 22:00-02:00).
// All times should be in HH:MM format.
func IsInTimeWindow(current, start, end string) bool {
	// Simple string comparison works for HH:MM format
	if start <= end {
		// Normal case: 09:00 - 17:00
		return current >= start && current <= end
	}
	// Wraparound case: 22:00 - 02:00
	return current >= start || current <= end
}

// InAnyWindow reports whether t falls inside any of the given windows, honoring
// day-of-week membership and overnight wraparound. A window whose end is before
// its start (e.g. 22:00-02:00) belongs to the day it *started* on, so the early
// morning portion is matched against yesterday's weekday.
func InAnyWindow(windows []config.TimeWindow, t time.Time) bool {
	day := t.Weekday().String()[:3]
	clock := t.Format("15:04")
	for _, w := range windows {
		dayToCheck := day
		if w.Start > w.End && clock <= w.End {
			dayToCheck = t.AddDate(0, 0, -1).Weekday().String()[:3]
		}
		if !slices.Contains(w.Days, dayToCheck) {
			continue
		}
		if IsInTimeWindow(clock, w.Start, w.End) {
			return true
		}
	}
	return false
}
