package aicontext

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func TestBuildInjectsTimeAnchorIntoVolatileOnly(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 10, 5, 20, 48, 0, 0, time.FixedZone("CST", 8*3600))
	builder := NewBuilder(Dependencies{Now: func() time.Time { return fixed }})
	prompt := builder.Build(context.Background(), tools.Scope{}, "")
	for _, expected := range []string{"[当前时间]", "2026-10-05T20:48:00+08:00", "2026", "UTC+08:00", "CST", "星期一"} {
		if !strings.Contains(prompt.Volatile, expected) {
			t.Errorf("missing %q in volatile context: %q", expected, prompt.Volatile)
		}
	}
	if !prompt.Bundle.Now.Equal(fixed) {
		t.Errorf("bundle.Now = %v, want %v", prompt.Bundle.Now, fixed)
	}
	for _, forbidden := range []string{"2026-10-05", "当前时间", "星期一"} {
		if strings.Contains(prompt.Stable, forbidden) {
			t.Errorf("stable prompt must not contain time anchor %q: %q", forbidden, prompt.Stable)
		}
	}
}

func TestTimeAnchorRebuildsEachRun(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	builder := NewBuilder(Dependencies{Now: func() time.Time { return now }})
	first := builder.Build(context.Background(), tools.Scope{}, "")
	now = now.Add(2 * time.Hour)
	second := builder.Build(context.Background(), tools.Scope{}, "")
	if !strings.Contains(first.Volatile, "2026-10-05T08:00:00Z") || !strings.Contains(second.Volatile, "2026-10-05T10:00:00Z") {
		t.Fatalf("anchor not rebuilt per run: first=%q second=%q", first.Volatile, second.Volatile)
	}
	if first.Stable != second.Stable {
		t.Fatal("stable prompt changed between runs")
	}
}

func TestTimeAnchorFollowsTZEnvironment(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	t.Setenv("TZ", "Asia/Shanghai")
	original := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = original })

	builder := NewBuilder(Dependencies{})
	prompt := builder.Build(context.Background(), tools.Scope{}, "")
	if !strings.Contains(prompt.Volatile, "UTC+08:00") || !strings.Contains(prompt.Volatile, "+08:00") {
		t.Errorf("volatile missing TZ-derived zone: %q", prompt.Volatile)
	}
	if year := time.Now().Format("2006"); !strings.Contains(prompt.Volatile, year) {
		t.Errorf("volatile missing current year %q: %q", year, prompt.Volatile)
	}
	if weekday := weekdayNames[time.Now().Weekday()]; !strings.Contains(prompt.Volatile, weekday) {
		t.Errorf("volatile missing current weekday %q: %q", weekday, prompt.Volatile)
	}
}
