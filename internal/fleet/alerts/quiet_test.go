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
