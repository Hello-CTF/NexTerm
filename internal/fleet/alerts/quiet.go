package alerts

import (
	"errors"
	"fmt"
	"time"
)

const minutesPerDay = 24 * 60

// InQuiet reports whether t falls in the daily window [start, end) of minutes-of-day in loc.
// A window with start > end crosses midnight; start == end is empty.
func InQuiet(t time.Time, start, end int64, loc *time.Location) bool {
	if start == end {
		return false
	}
	if loc == nil {
		loc = time.Local
	}
	local := t.In(loc)
	m := int64(local.Hour()*60 + local.Minute())
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// LastQuietEnd returns the most recent quiet-window end at or before t in ms, or 0 for an empty
// window. The end is rebuilt from the local calendar date so DST shifts keep it at the wall-clock end.
func LastQuietEnd(t time.Time, start, end int64, loc *time.Location) int64 {
	if start == end {
		return 0
	}
	if loc == nil {
		loc = time.Local
	}
	local := t.In(loc)
	endAt := func(day int) time.Time {
		return time.Date(local.Year(), local.Month(), day, int(end/60), int(end%60), 0, 0, loc)
	}
	last := endAt(local.Day())
	if last.After(t) {
		last = endAt(local.Day() - 1)
	}
	return last.UnixMilli()
}

// ResolveLocation loads an IANA zone name; an empty or unloadable name gives time.Local.
func ResolveLocation(tz string) *time.Location {
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Local
	}
	return loc
}

// ValidateQuiet checks quiet-hours settings: minutes in [0, 1439], start != end, and a loadable zone
// (empty means the server's local zone).
func ValidateQuiet(start, end int64, tz string) error {
	if start < 0 || start >= minutesPerDay || end < 0 || end >= minutesPerDay {
		return fmt.Errorf("quiet hours must be minutes of day in [0, %d]", minutesPerDay-1)
	}
	if start == end {
		return errors.New("quiet hours start and end must differ")
	}
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("unknown time zone %q", tz)
		}
	}
	return nil
}
