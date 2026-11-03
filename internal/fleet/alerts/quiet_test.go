package alerts

import (
	"testing"
	"time"
)

func TestInQuiet(t *testing.T) {
	utc := time.UTC
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	at := func(loc *time.Location, h, m int) time.Time { return time.Date(2027, 1, 15, h, m, 0, 0, loc) }
	cases := []struct {
		name       string
		t          time.Time
		start, end int64
		loc        *time.Location
		want       bool
	}{
		{"same-day inside", at(utc, 10, 0), 9 * 60, 18 * 60, utc, true},
		{"same-day start inclusive", at(utc, 9, 0), 9 * 60, 18 * 60, utc, true},
		{"same-day end exclusive", at(utc, 18, 0), 9 * 60, 18 * 60, utc, false},
		{"same-day outside", at(utc, 8, 59), 9 * 60, 18 * 60, utc, false},
		{"midnight evening", at(utc, 23, 30), 19 * 60, 9 * 60, utc, true},
		{"midnight early", at(utc, 3, 0), 19 * 60, 9 * 60, utc, true},
		{"midnight end exclusive", at(utc, 9, 0), 19 * 60, 9 * 60, utc, false},
		{"midnight start inclusive", at(utc, 19, 0), 19 * 60, 9 * 60, utc, true},
		{"midnight daytime", at(utc, 12, 0), 19 * 60, 9 * 60, utc, false},
		{"start equals end", at(utc, 12, 0), 600, 600, utc, false},
		{"08:00Z is quiet in UTC", at(utc, 8, 0), 19 * 60, 9 * 60, utc, true},
		{"same instant is 16:00 in Shanghai", at(utc, 8, 0), 19 * 60, 9 * 60, sh, false},
		{"instant converted to zone", at(utc, 12, 0), 19 * 60, 9 * 60, sh, true}, // 20:00 Shanghai
	}
	for _, c := range cases {
		if got := InQuiet(c.t, c.start, c.end, c.loc); got != c.want {
			t.Errorf("%s: InQuiet = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestResolveLocation(t *testing.T) {
	if ResolveLocation("") != time.Local || ResolveLocation("No/Such_Zone") != time.Local {
		t.Fatal("empty or invalid zone must fall back to time.Local")
	}
	if loc := ResolveLocation("Asia/Shanghai"); loc.String() != "Asia/Shanghai" {
		t.Fatalf("location = %v", loc)
	}
}

func TestValidateQuiet(t *testing.T) {
	cases := []struct {
		name       string
		start, end int64
		tz         string
		ok         bool
	}{
		{"valid", 1140, 540, "Asia/Shanghai", true},
		{"empty tz", 0, 60, "", true},
		{"max", 0, 1439, "UTC", true},
		{"equal", 60, 60, "UTC", false},
		{"negative start", -1, 60, "UTC", false},
		{"end too large", 0, 1440, "UTC", false},
		{"start too large", 1440, 60, "UTC", false},
		{"bad zone", 0, 60, "Mars/Olympus", false},
	}
	for _, c := range cases {
		if err := ValidateQuiet(c.start, c.end, c.tz); (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

func TestLastQuietEnd(t *testing.T) {
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	at := func(loc *time.Location, day, h, m int) time.Time { return time.Date(2027, 1, day, h, m, 0, 0, loc) }
	cases := []struct {
		name       string
		now        time.Time
		start, end int64
		loc        *time.Location
		want       time.Time
	}{
		{"before the end, crossing midnight", at(sh, 15, 8, 59), 19 * 60, 9 * 60, sh, at(sh, 14, 9, 0)},
		{"exactly at the end", at(sh, 15, 9, 0), 19 * 60, 9 * 60, sh, at(sh, 15, 9, 0)},
		{"daytime", at(sh, 15, 12, 0), 19 * 60, 9 * 60, sh, at(sh, 15, 9, 0)},
		{"evening, inside the window", at(sh, 15, 23, 0), 19 * 60, 9 * 60, sh, at(sh, 15, 9, 0)},
		{"early morning after midnight", at(sh, 15, 3, 0), 19 * 60, 9 * 60, sh, at(sh, 14, 9, 0)},
		{"same-day window", at(sh, 15, 20, 0), 9 * 60, 18 * 60, sh, at(sh, 15, 18, 0)},
		{"same-day window before its end", at(sh, 15, 10, 0), 9 * 60, 18 * 60, sh, at(sh, 14, 18, 0)},
		{"instant is converted to the zone", at(time.UTC, 15, 1, 0), 19 * 60, 9 * 60, sh, at(sh, 15, 9, 0)},
		{"month boundary", at(sh, 1, 3, 0), 19 * 60, 9 * 60, sh, time.Date(2026, 12, 31, 9, 0, 0, 0, sh)},
		// 2027-03-14 is the US spring-forward day: 09:00 local is UTC-4 while the previous day's is UTC-5.
		{"dst day", time.Date(2027, 3, 14, 12, 0, 0, 0, ny), 19 * 60, 9 * 60, ny, time.Date(2027, 3, 14, 9, 0, 0, 0, ny)},
		{"dst day before the end", time.Date(2027, 3, 14, 5, 0, 0, 0, ny), 19 * 60, 9 * 60, ny, time.Date(2027, 3, 13, 9, 0, 0, 0, ny)},
		{"empty window", at(sh, 15, 12, 0), 600, 600, sh, time.Time{}},
	}
	for _, c := range cases {
		want := int64(0)
		if !c.want.IsZero() {
			want = c.want.UnixMilli()
		}
		got := LastQuietEnd(c.now, c.start, c.end, c.loc)
		if got != want {
			t.Errorf("%s: LastQuietEnd = %d, want %d", c.name, got, want)
		}
		if got > c.now.UnixMilli() {
			t.Errorf("%s: end %d is after now", c.name, got)
		}
	}
}
