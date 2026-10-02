package cron

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestScheduleStepCannotOverflow(t *testing.T) {
	expression := fmt.Sprintf("0 0 1/%d * *", int(^uint(0)>>1))
	schedule, err := ParseSchedule(expression, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	if got, _ := schedule.Next(after); !got.Equal(want) {
		t.Fatalf("Next() = %s; want %s", got, want)
	}
}

func TestCalendarScheduleFieldsAndRanges(t *testing.T) {
	schedule, err := ParseSchedule("*/15 9-10 * * MON-FRI", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, time.January, 2, 10, 46, 0, 0, time.UTC)
	want := time.Date(2026, time.January, 5, 9, 0, 0, 0, time.UTC)
	got, ok := schedule.Next(after)
	if !ok || !got.Equal(want) {
		t.Fatalf("Next(%s) = %s, %v; want %s", after, got, ok, want)
	}
}

func TestCalendarDayFieldsUseStandardUnion(t *testing.T) {
	schedule, err := ParseSchedule("0 0 13 * 5", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, time.January, 13, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.January, 16, 0, 0, 0, 0, time.UTC)
	got, ok := schedule.Next(after)
	if !ok || !got.Equal(want) {
		t.Fatalf("Next(%s) = %s, %v; want %s", after, got, ok, want)
	}
}

func TestScheduleAliasesIntervalSundayAndTimezone(t *testing.T) {
	after := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	sunday, err := ParseSchedule("0 0 * * 7", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := sunday.Next(after); got.Weekday() != time.Sunday || got.Hour() != 0 {
		t.Fatalf("numeric Sunday was not honored: %s", got)
	}
	hourly, err := ParseSchedule("@hourly", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := hourly.Next(after); !got.Equal(after.Add(time.Hour)) {
		t.Fatalf("hourly alias returned %s", got)
	}
	interval, err := ParseSchedule("@every 90s", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := interval.Next(after); !got.Equal(after.Add(90 * time.Second)) {
		t.Fatalf("interval returned %s", got)
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	local, err := ParseSchedule("0 9 * * *", location)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := local.Next(after); !got.Equal(time.Date(2026, time.January, 4, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("timezone schedule returned %s", got)
	}
}

func TestScheduleSkipsSpringForwardGapWithTimeout(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := ParseSchedule("30 2 * * *", location)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, time.March, 7, 2, 30, 0, 0, location)
	got, ok := nextWithTimeout(t, schedule, after)
	want := time.Date(2026, time.March, 9, 2, 30, 0, 0, location)
	if !ok || !got.Equal(want) {
		t.Fatalf("Next(%s) = %s, %v; want %s", after, got, ok, want)
	}
}

func TestScheduleFallBackUsesEarliestInstantOnlyWithTimeout(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := ParseSchedule("30 1 * * *", location)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, time.October, 31, 1, 30, 0, 0, location)
	first, ok := nextWithTimeout(t, schedule, after)
	wantFirst := time.Date(2026, time.November, 1, 5, 30, 0, 0, time.UTC)
	if !ok || !first.Equal(wantFirst) {
		t.Fatalf("first ambiguous occurrence = %s, %v; want %s", first, ok, wantFirst)
	}
	second, ok := nextWithTimeout(t, schedule, first)
	wantSecond := time.Date(2026, time.November, 2, 6, 30, 0, 0, time.UTC)
	if !ok || !second.Equal(wantSecond) {
		t.Fatalf("next occurrence = %s, %v; want %s without a repeated instant", second, ok, wantSecond)
	}
}

func nextWithTimeout(t *testing.T, schedule *Schedule, after time.Time) (time.Time, bool) {
	t.Helper()
	type result struct {
		at time.Time
		ok bool
	}
	results := make(chan result, 1)
	go func() {
		at, ok := schedule.Next(after)
		results <- result{at: at, ok: ok}
	}()
	select {
	case result := <-results:
		return result.at, result.ok
	case <-time.After(3 * time.Second):
		t.Fatalf("Next(%s) did not terminate", after)
		return time.Time{}, false
	}
}

func TestInvalidSchedules(t *testing.T) {
	for _, expression := range []string{
		"", "* * * *", "61 * * * *", "* 24 * * *", "0 0 * FOO *",
		"0 0 * * MON-FRI/0", "0 0 10-2 * *", "@every 0s", "@every nope",
	} {
		t.Run(expression, func(t *testing.T) {
			if _, err := ParseSchedule(expression, time.UTC); !errors.Is(err, ErrInvalidSchedule) {
				t.Fatalf("ParseSchedule(%q) error = %v", expression, err)
			}
		})
	}
}
