package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Schedule struct {
	expression string
	location   *time.Location
	every      time.Duration
	at         time.Time
	oneShot    bool
	minutes    []bool
	hours      []bool
	days       []bool
	months     []bool
	weekdays   []bool
	dayAny     bool
	weekdayAny bool
}

const atPrefix = "@at "

func OneShot(expression string) bool {
	return strings.HasPrefix(strings.TrimSpace(expression), atPrefix)
}

func AtExpression(at time.Time) string {
	return atPrefix + at.UTC().Format(time.RFC3339)
}

func ParseSchedule(expression string, location *time.Location) (*Schedule, error) {
	if location == nil {
		location = time.UTC
	}
	expression = strings.TrimSpace(expression)
	schedule := &Schedule{expression: expression, location: location}
	if strings.HasPrefix(expression, atPrefix) {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(strings.TrimPrefix(expression, atPrefix)))
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidSchedule, expression)
		}
		schedule.at = at
		schedule.oneShot = true
		return schedule, nil
	}
	if strings.HasPrefix(expression, "@every ") {
		every, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(expression, "@every ")))
		if err != nil || every <= 0 {
			return nil, fmt.Errorf("%w: %q", ErrInvalidSchedule, expression)
		}
		schedule.every = every
		return schedule, nil
	}
	aliases := map[string]string{
		"@hourly":  "0 * * * *",
		"@daily":   "0 0 * * *",
		"@weekly":  "0 0 * * 0",
		"@monthly": "0 0 1 * *",
		"@yearly":  "0 0 1 1 *",
	}
	if alias, ok := aliases[strings.ToLower(expression)]; ok {
		expression = alias
	}
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSchedule, expression)
	}
	var err error
	if schedule.minutes, _, err = parseField(fields[0], 0, 59, nil, false); err != nil {
		return nil, scheduleFieldError(expression, "minute", err)
	}
	if schedule.hours, _, err = parseField(fields[1], 0, 23, nil, false); err != nil {
		return nil, scheduleFieldError(expression, "hour", err)
	}
	if schedule.days, schedule.dayAny, err = parseField(fields[2], 1, 31, nil, false); err != nil {
		return nil, scheduleFieldError(expression, "day of month", err)
	}
	months := map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	if schedule.months, _, err = parseField(fields[3], 1, 12, months, false); err != nil {
		return nil, scheduleFieldError(expression, "month", err)
	}
	weekdays := map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
	if schedule.weekdays, schedule.weekdayAny, err = parseField(fields[4], 0, 7, weekdays, true); err != nil {
		return nil, scheduleFieldError(expression, "day of week", err)
	}
	return schedule, nil
}

func scheduleFieldError(expression, field string, err error) error {
	return fmt.Errorf("%w: %q: %s: %v", ErrInvalidSchedule, expression, field, err)
}

func parseField(expression string, minimum, maximum int, names map[string]int, sundaySeven bool) ([]bool, bool, error) {
	values := make([]bool, maximum+1)
	any := expression == "*"
	for _, item := range strings.Split(expression, ",") {
		if item == "" {
			return nil, false, fmt.Errorf("empty list item")
		}
		base, stepText, stepped := strings.Cut(item, "/")
		if stepped && strings.Contains(stepText, "/") {
			return nil, false, fmt.Errorf("invalid step in %q", item)
		}
		step := 1
		if stepped {
			parsed, err := strconv.Atoi(stepText)
			if err != nil || parsed <= 0 {
				return nil, false, fmt.Errorf("invalid step in %q", item)
			}
			step = parsed
		}
		start, end := minimum, maximum
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			left, right, found := strings.Cut(base, "-")
			if !found || strings.Contains(right, "-") {
				return nil, false, fmt.Errorf("invalid range in %q", item)
			}
			var err error
			if start, err = parseScheduleValue(left, names); err != nil {
				return nil, false, err
			}
			if end, err = parseScheduleValue(right, names); err != nil {
				return nil, false, err
			}
		default:
			value, err := parseScheduleValue(base, names)
			if err != nil {
				return nil, false, err
			}
			start = value
			if stepped {
				end = maximum
			} else {
				end = value
			}
		}
		if start < minimum || start > maximum || end < minimum || end > maximum || start > end {
			return nil, false, fmt.Errorf("value outside %d-%d in %q", minimum, maximum, item)
		}
		for value := start; ; {
			normalized := value
			if sundaySeven && normalized == 7 {
				normalized = 0
			}
			values[normalized] = true
			if step > end-value {
				break
			}
			value += step
		}
	}
	return values, any, nil
}

func parseScheduleValue(raw string, names map[string]int) (int, error) {
	if names != nil {
		if value, ok := names[strings.ToLower(raw)]; ok {
			return value, nil
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", raw)
	}
	return value, nil
}

func (s *Schedule) Next(after time.Time) (time.Time, bool) {
	if s.oneShot {
		if s.at.After(after) {
			return s.at, true
		}
		return time.Time{}, false
	}
	if s.every > 0 {
		return after.Add(s.every), true
	}
	local := after.In(s.location)
	year, month, day := local.Date()
	wall := time.Date(year, month, day, local.Hour(), local.Minute(), 0, 0, time.UTC)
	cursor := wall.Add(time.Minute)
	limit := cursor.AddDate(10, 0, 0)
	for !cursor.After(limit) {
		candidate := cursor
		switch {
		case !s.months[int(cursor.Month())]:
			year, month, _ := cursor.Date()
			candidate = time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
		case !s.matchesDay(cursor):
			year, month, day := cursor.Date()
			candidate = time.Date(year, month, day+1, 0, 0, 0, 0, time.UTC)
		case !s.hours[cursor.Hour()]:
			hour, ok := nextScheduleValue(s.hours, cursor.Hour()+1)
			if !ok {
				year, month, day := cursor.Date()
				candidate = time.Date(year, month, day+1, 0, 0, 0, 0, time.UTC)
			} else {
				year, month, day := cursor.Date()
				candidate = time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
			}
		case !s.minutes[cursor.Minute()]:
			minute, ok := nextScheduleValue(s.minutes, cursor.Minute()+1)
			if !ok {
				year, month, day := cursor.Date()
				candidate = time.Date(year, month, day, cursor.Hour()+1, 0, 0, 0, time.UTC)
			} else {
				year, month, day := cursor.Date()
				candidate = time.Date(year, month, day, cursor.Hour(), minute, 0, 0, time.UTC)
			}
		default:
			if occurrence, ok := resolveWallTime(cursor, s.location); ok && occurrence.After(after) {
				return occurrence, true
			}
			candidate = cursor.Add(time.Minute)
		}
		if !candidate.After(cursor) {
			return time.Time{}, false
		}
		cursor = candidate
	}
	return time.Time{}, false
}

func resolveWallTime(wall time.Time, location *time.Location) (time.Time, bool) {
	year, month, day := wall.Date()
	probe := time.Date(year, month, day, wall.Hour(), wall.Minute(), 0, 0, location)
	probes := []time.Time{probe, probe.Add(-6 * time.Hour), probe.Add(6 * time.Hour), probe.Add(-24 * time.Hour), probe.Add(24 * time.Hour)}
	seen := make(map[int]struct{}, len(probes))
	var earliest time.Time
	for _, value := range probes {
		_, offset := value.In(location).Zone()
		if _, exists := seen[offset]; exists {
			continue
		}
		seen[offset] = struct{}{}
		candidate := wall.Add(-time.Duration(offset) * time.Second).In(location)
		if !sameWallTime(candidate, wall) {
			continue
		}
		if earliest.IsZero() || candidate.Before(earliest) {
			earliest = candidate
		}
	}
	return earliest, !earliest.IsZero()
}

func sameWallTime(value, wall time.Time) bool {
	valueYear, valueMonth, valueDay := value.Date()
	wallYear, wallMonth, wallDay := wall.Date()
	return valueYear == wallYear && valueMonth == wallMonth && valueDay == wallDay &&
		value.Hour() == wall.Hour() && value.Minute() == wall.Minute() && value.Second() == 0
}

func nextScheduleValue(values []bool, start int) (int, bool) {
	for value := start; value < len(values); value++ {
		if values[value] {
			return value, true
		}
	}
	return 0, false
}

func (s *Schedule) matchesDay(value time.Time) bool {
	day := s.days[value.Day()]
	weekday := s.weekdays[int(value.Weekday())]
	switch {
	case s.dayAny && s.weekdayAny:
		return true
	case s.dayAny:
		return weekday
	case s.weekdayAny:
		return day
	default:
		return day || weekday
	}
}

func loadSchedule(expression, timezone string) (*Schedule, error) {
	if timezone == "" {
		timezone = "UTC"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("%w: timezone %q: %v", ErrInvalidSchedule, timezone, err)
	}
	return ParseSchedule(expression, location)
}
