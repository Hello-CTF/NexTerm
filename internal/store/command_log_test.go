package store

import (
	"context"
	"testing"
	"time"
)

func TestCommandLogInsertQueryAndFilters(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)

	now := time.Now().UnixMilli()
	exit := 0
	inputs := []CommandLogInput{
		{SessionID: "sess-1", TabID: "tab-1", AssetID: "asset-1", UserID: "u-1", Command: "ls -la", Source: "terminal", ExitCode: &exit, StartedAt: now - 100, FinishedAt: now},
		{SessionID: "sess-1", TabID: "tab-2", AssetID: "asset-1", Command: "df -h", Source: "terminal", StartedAt: now - 200, FinishedAt: now - 150},
		{SessionID: "sess-2", TabID: "tab-3", AssetID: "asset-2", UserID: "u-2", Command: "Get-Service Winmgmt", Source: "exec", ExitCode: &exit, StartedAt: now - 300, FinishedAt: now - 250},
	}
	for _, input := range inputs {
		if err := db.CommandLogInsert(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	for _, testCase := range []struct {
		name  string
		query CommandLogQuery
		want  int64
	}{
		{"no filters", CommandLogQuery{}, 3},
		{"session", CommandLogQuery{SessionID: ptr("sess-1")}, 2},
		{"asset", CommandLogQuery{AssetID: ptr("asset-1")}, 2},
		{"user", CommandLogQuery{UserID: ptr("u-1")}, 1},
		{"session and asset", CommandLogQuery{SessionID: ptr("sess-1"), AssetID: ptr("asset-1")}, 2},
		{"user without id", CommandLogQuery{UserID: ptr("missing")}, 0},
		{"unmatched session", CommandLogQuery{SessionID: ptr("missing")}, 0},
	} {
		count, err := db.CommandLogCount(ctx, testCase.query)
		if err != nil || count != testCase.want {
			t.Fatalf("%s: count=%d want=%d err=%v", testCase.name, count, testCase.want, err)
		}
	}

	rows, err := db.CommandLogQuery(ctx, CommandLogQuery{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("query without filters rows=%d err=%v", len(rows), err)
	}
	if rows[0].Command != "ls -la" || rows[1].Command != "df -h" || rows[2].Command != "Get-Service Winmgmt" {
		t.Fatalf("query order not started_at DESC, id DESC: %+v", rows)
	}
	if rows[0].UserID == nil || *rows[0].UserID != "u-1" {
		t.Fatalf("user id must round-trip: %+v", rows[0])
	}
	if rows[1].UserID != nil {
		t.Fatalf("empty user id must scan as nil: %+v", rows[1])
	}
	if rows[1].ExitCode != nil {
		t.Fatalf("missing exit code must scan as nil: %+v", rows[1])
	}
	if rows[0].ExitCode == nil || *rows[0].ExitCode != 0 {
		t.Fatalf("exit code 0 must round-trip: %+v", rows[0])
	}
	if rows[0].SessionID != "sess-1" || rows[0].TabID != "tab-1" || rows[0].AssetID != "asset-1" || rows[0].Source != "terminal" {
		t.Fatalf("attribution = %+v", rows[0])
	}

	rows, err = db.CommandLogQuery(ctx, CommandLogQuery{AssetID: ptr("asset-2"), UserID: ptr("u-2")})
	if err != nil || len(rows) != 1 || rows[0].Command != "Get-Service Winmgmt" {
		t.Fatalf("query by asset+user rows=%+v err=%v", rows, err)
	}

	firstPage, err := db.CommandLogQuery(ctx, CommandLogQuery{Limit: 2})
	if err != nil || len(firstPage) != 2 || firstPage[0].Command != "ls -la" {
		t.Fatalf("first page rows=%+v err=%v", firstPage, err)
	}
	secondPage, err := db.CommandLogQuery(ctx, CommandLogQuery{Limit: 2, Offset: 2})
	if err != nil || len(secondPage) != 1 || secondPage[0].Command != "Get-Service Winmgmt" {
		t.Fatalf("second page rows=%+v err=%v", secondPage, err)
	}
}

func TestCommandLogRetentionAgeAndCount(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)

	now := time.Now()
	old := now.Add(-3 * time.Hour).UnixMilli()
	recent := now.Add(-10 * time.Minute).UnixMilli()
	for _, input := range []CommandLogInput{
		{SessionID: "sess-1", TabID: "tab-1", AssetID: "asset-1", Command: "old-1", Source: "terminal", StartedAt: old, FinishedAt: old + 10},
		{SessionID: "sess-1", TabID: "tab-1", AssetID: "asset-1", Command: "old-2", Source: "terminal", StartedAt: old + 20, FinishedAt: old + 30},
		{SessionID: "sess-1", TabID: "tab-1", AssetID: "asset-1", Command: "recent-1", Source: "terminal", StartedAt: recent - 20, FinishedAt: recent - 10},
		{SessionID: "sess-1", TabID: "tab-1", AssetID: "asset-1", Command: "recent-2", Source: "terminal", StartedAt: recent, FinishedAt: recent + 10},
	} {
		if err := db.CommandLogInsert(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	result, err := db.EnforceRetention(ctx, RetentionPolicy{CommandMaxAge: time.Hour, CommandMaxCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.CommandsDeleted != 3 {
		t.Fatalf("retention result=%+v, want three command deletions", result)
	}
	rows, err := db.CommandLogQuery(ctx, CommandLogQuery{})
	if err != nil || len(rows) != 1 || rows[0].Command != "recent-2" {
		t.Fatalf("remaining commands=%+v err=%v", rows, err)
	}

	repeated, err := db.EnforceRetention(ctx, RetentionPolicy{CommandMaxAge: time.Hour, CommandMaxCount: 1})
	if err != nil || repeated != (RetentionResult{}) {
		t.Fatalf("repeat result=%+v err=%v", repeated, err)
	}
	if _, err := db.EnforceRetention(ctx, RetentionPolicy{CommandMaxAge: -time.Second}); err == nil {
		t.Fatal("negative command max age must be rejected")
	}
	if _, err := db.EnforceRetention(ctx, RetentionPolicy{CommandMaxCount: -1}); err == nil {
		t.Fatal("negative command max count must be rejected")
	}
}
