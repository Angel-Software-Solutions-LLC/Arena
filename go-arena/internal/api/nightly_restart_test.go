package api

import (
	"testing"
	"time"
)

func TestParseRestartClock(t *testing.T) {
	cases := []struct {
		in           string
		hour, minute int
		ok           bool
	}{
		{"00:00", 0, 0, true},
		{"23:45", 23, 45, true},
		{" 04:30 ", 4, 30, true},
		{"midnight", 0, 0, false},
		{"24:00", 0, 0, false},
		{"12:60", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		hour, minute, err := parseRestartClock(c.in)
		if c.ok != (err == nil) {
			t.Errorf("parseRestartClock(%q) error = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && (hour != c.hour || minute != c.minute) {
			t.Errorf("parseRestartClock(%q) = %d:%d, want %d:%d", c.in, hour, minute, c.hour, c.minute)
		}
	}
}

func TestNextNightlyRestart(t *testing.T) {
	loc := time.UTC

	// Before today's slot: fires later the same day.
	now := time.Date(2026, 8, 10, 9, 15, 0, 0, loc)
	if got, want := nextNightlyRestart(now, 23, 30, loc), time.Date(2026, 8, 10, 23, 30, 0, 0, loc); !got.Equal(want) {
		t.Errorf("same-day: got %v, want %v", got, want)
	}

	// After today's slot (the midnight default at any time of day): tomorrow.
	if got, want := nextNightlyRestart(now, 0, 0, loc), time.Date(2026, 8, 11, 0, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("next-day: got %v, want %v", got, want)
	}

	// Exactly at the slot: strictly after now, so tomorrow — a restart must
	// never re-fire the moment the process comes back up.
	atSlot := time.Date(2026, 8, 10, 0, 0, 0, 0, loc)
	if got, want := nextNightlyRestart(atSlot, 0, 0, loc), time.Date(2026, 8, 11, 0, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("at-slot: got %v, want %v", got, want)
	}

	// Month rollover.
	eom := time.Date(2026, 8, 31, 12, 0, 0, 0, loc)
	if got, want := nextNightlyRestart(eom, 0, 0, loc), time.Date(2026, 9, 1, 0, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("month rollover: got %v, want %v", got, want)
	}

	// DST transitions must still land on a valid local wall-clock instant.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata available: %v", err)
	}
	// Spring forward 2026-03-08: 02:30 EST does not exist; time.Date
	// normalizes it instead of hanging the scheduler.
	before := time.Date(2026, 3, 8, 1, 0, 0, 0, ny)
	got := nextNightlyRestart(before, 2, 30, ny)
	if !got.After(before) || got.Sub(before) > 26*time.Hour {
		t.Errorf("spring-forward: got %v, not within a day after %v", got, before)
	}
	// Fall back 2026-11-01: the 25-hour day still schedules midnight once.
	fall := time.Date(2026, 10, 31, 23, 0, 0, 0, ny)
	got = nextNightlyRestart(fall, 0, 0, ny)
	want := time.Date(2026, 11, 1, 0, 0, 0, 0, ny)
	if !got.Equal(want) {
		t.Errorf("fall-back: got %v, want %v", got, want)
	}
}
