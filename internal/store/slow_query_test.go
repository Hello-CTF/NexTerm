package store

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

const slowQuerySecret = "SECRET-BIND-VALUE-42"

func openSlowQueryStore(t *testing.T, buf *bytes.Buffer) *Store {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, err := OpenInMemoryWithOptions(context.Background(), OpenOptions{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSlowQueryCallbackRecordsOnlyAboveThreshold(t *testing.T) {
	var buf bytes.Buffer
	s := openSlowQueryStore(t, &buf)
	SetSlowQueryCapture(true)
	t.Cleanup(func() { SetSlowQueryCapture(false) })
	buf.Reset()

	// Slow: delay between the begin and end callbacks.
	_ = s.db.Callback().Query().Before("gorm:query").After("nexterm:slow_begin_query").Register("test:delay", func(*gorm.DB) { time.Sleep(slowQueryThreshold + 20*time.Millisecond) })
	var n int64
	if err := s.db.Table("sqlite_master").Where("name = ?", slowQuerySecret).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "slow query") || !strings.Contains(out, "op=select") || !strings.Contains(out, "elapsed_ms=") {
		t.Fatalf("slow query not recorded: %q", out)
	}
	if strings.Contains(out, slowQuerySecret) || strings.Contains(strings.ToLower(out), "select count") || !strings.Contains(out, "table=sqlite_master") {
		t.Fatalf("SQL text or values leaked: %q", out)
	}
}

func TestSlowQueryCallbackIgnoresFastAndInactive(t *testing.T) {
	var buf bytes.Buffer
	s := openSlowQueryStore(t, &buf)
	SetSlowQueryCapture(true)
	t.Cleanup(func() { SetSlowQueryCapture(false) })
	buf.Reset()

	// A threshold no query reaches: nothing is logged, even though the capture is on.
	tx := s.db.Session(&gorm.Session{})
	tx.InstanceSet(slowQueryStartKey, time.Now())
	recordSlowQuery(tx, s.logger, "select", time.Hour)
	if buf.Len() != 0 {
		t.Fatalf("fast query logged: %q", buf.String())
	}

	// With the capture off, no start time is taken and nothing is logged however slow.
	SetSlowQueryCapture(false)
	_ = s.db.Callback().Query().Before("gorm:query").After("nexterm:slow_begin_query").Register("test:delay2", func(*gorm.DB) { time.Sleep(slowQueryThreshold + 10*time.Millisecond) })
	var n int64
	if err := s.db.Table("sqlite_master").Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("inactive capture logged: %q", buf.String())
	}
}

func TestSafeTableName(t *testing.T) {
	for in, want := range map[string]string{"assets": "assets", "": "", "a b": "", "x;drop": "", strings.Repeat("a", 65): ""} {
		if got := safeTableName(in); got != want {
			t.Errorf("safeTableName(%q) = %q, want %q", in, got, want)
		}
	}
}
