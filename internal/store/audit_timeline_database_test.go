package store_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

type timelineKey struct {
	stream string
	id     int64
}

func timelineRank(stream string) int {
	switch stream {
	case "audit":
		return 3
	case "command":
		return 2
	}
	return 1
}

func timelineAudit(t *testing.T, database *store.Store, owner, source, kind string, ts int64, asset string) int64 {
	t.Helper()
	row := store.AuditRow{TS: ts, Source: source, Kind: kind, PayloadJSON: "{}", OwnerID: owner}
	if asset != "" {
		row.AssetID = &asset
	}
	if err := query.Use(database.GORM()).AuditRow.WithContext(context.Background()).Create(&row); err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func timelineCommand(t *testing.T, database *store.Store, owner string, ts int64, asset string) int64 {
	t.Helper()
	row := store.CommandLogRow{SessionID: "s", TabID: "t", AssetID: asset, Command: "ls", Source: "terminal", StartedAt: ts, FinishedAt: ts + 5}
	if owner != "" {
		row.UserID = &owner
	}
	if err := query.Use(database.GORM()).CommandLog.WithContext(context.Background()).Create(&row); err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func timelineSeq(items []store.AuditTimelineItem) []timelineKey {
	out := make([]timelineKey, 0, len(items))
	for _, item := range items {
		out = append(out, timelineKey{item.Stream, item.ID})
	}
	return out
}

func timelineAll(t *testing.T, ctx context.Context, database *store.Store, q store.AuditTimelineQuery) []store.AuditTimelineItem {
	t.Helper()
	var all []store.AuditTimelineItem
	for page := 0; page < 1000; page++ {
		items, next, err := database.AuditTimeline(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, items...)
		if next == nil {
			return all
		}
		q.Cursor = next
	}
	t.Fatal("paging did not terminate")
	return nil
}

func TestAuditTimelineMergesStreamsDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	type seed struct {
		timelineKey
		ts     int64
		origin string
	}
	var seeds []seed
	for _, ts := range []int64{100, 100, 200, 300, 300} {
		id := timelineAudit(t, database, "", "user", "x", ts, "a1")
		seeds = append(seeds, seed{timelineKey{"audit", id}, ts, "user"})
	}
	aiID := timelineAudit(t, database, "", "ai", "outcome", 300, "a1")
	seeds = append(seeds, seed{timelineKey{"audit", aiID}, 300, "ai"})
	termID := timelineAudit(t, database, "", "fleet", "device_terminal", 200, "d1")
	seeds = append(seeds, seed{timelineKey{"audit", termID}, 200, "user"})
	for _, ts := range []int64{100, 200, 300, 300} {
		id := timelineCommand(t, database, "", ts, "a1")
		seeds = append(seeds, seed{timelineKey{"command", id}, ts, "user"})
	}
	for _, kind := range []string{"device_online", "device_offline", "device_failover"} {
		for _, ts := range []int64{100, 300, 300} {
			id := timelineAudit(t, database, "", "fleet", kind, ts, "d1")
			seeds = append(seeds, seed{timelineKey{"device", id}, ts, "device"})
		}
	}
	sort.Slice(seeds, func(i, j int) bool {
		a, b := seeds[i], seeds[j]
		if a.ts != b.ts {
			return a.ts > b.ts
		}
		if ra, rb := timelineRank(a.stream), timelineRank(b.stream); ra != rb {
			return ra > rb
		}
		return a.id > b.id
	})
	for _, limit := range []int64{2, 3, 100} {
		got := timelineAll(t, ctx, database, store.AuditTimelineQuery{Limit: limit})
		if len(got) != len(seeds) {
			t.Fatalf("limit %d: got %d items, want %d", limit, len(got), len(seeds))
		}
		for i, item := range got {
			want := seeds[i]
			if item.Stream != want.stream || item.ID != want.id || item.TS != want.ts || item.Origin != want.origin {
				t.Fatalf("limit %d item %d = %s/%d ts=%d origin=%s, want %s/%d ts=%d origin=%s",
					limit, i, item.Stream, item.ID, item.TS, item.Origin, want.stream, want.id, want.ts, want.origin)
			}
			if (item.Stream == "command") != (item.Command != nil) || (item.Stream != "command") != (item.Audit != nil) {
				t.Fatalf("item %d carries the wrong row payload", i)
			}
		}
	}
	items, next, err := database.AuditTimeline(ctx, store.AuditTimelineQuery{Limit: 2})
	if err != nil || len(items) != 2 || next == nil {
		t.Fatalf("first page items=%d next=%v err=%v", len(items), next, err)
	}
}

func TestAuditTimelinePagingStableUnderInsertsDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		timelineAudit(t, database, "", "user", "x", int64(100+i), "a")
		timelineCommand(t, database, "", int64(100+i), "a")
	}
	baseline := timelineAll(t, ctx, database, store.AuditTimelineQuery{Limit: 100})
	first, next, err := database.AuditTimeline(ctx, store.AuditTimelineQuery{Limit: 3})
	if err != nil || next == nil {
		t.Fatalf("first page next=%v err=%v", next, err)
	}
	newAudit := timelineAudit(t, database, "", "user", "x", 5000, "a")
	newCommand := timelineCommand(t, database, "", 5001, "a")
	seen := map[timelineKey]bool{}
	for _, item := range first {
		seen[timelineKey{item.Stream, item.ID}] = true
	}
	rest := timelineAll(t, ctx, database, store.AuditTimelineQuery{Limit: 2, Cursor: next})
	for _, item := range rest {
		key := timelineKey{item.Stream, item.ID}
		if seen[key] {
			t.Fatalf("duplicate %v after insert", key)
		}
		if key == (timelineKey{"audit", newAudit}) || key == (timelineKey{"command", newCommand}) {
			t.Fatalf("newer record %v leaked into later page", key)
		}
		seen[key] = true
	}
	for _, item := range baseline {
		if !seen[timelineKey{item.Stream, item.ID}] {
			t.Fatalf("old record %s/%d missing", item.Stream, item.ID)
		}
	}
}

func TestAuditTimelineFiltersDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	userAudit := timelineAudit(t, database, "", "user", "x", 100, "host1")
	aiAudit := timelineAudit(t, database, "", "ai", "outcome", 200, "host1")
	online := timelineAudit(t, database, "", "fleet", "device_online", 300, "dev1")
	devCmd := timelineCommand(t, database, "", 400, "device/dev1")
	hostCmd := timelineCommand(t, database, "", 500, "host1")

	run := func(q store.AuditTimelineQuery) map[timelineKey]bool {
		t.Helper()
		out := map[timelineKey]bool{}
		for _, k := range timelineSeq(timelineAll(t, ctx, database, q)) {
			out[k] = true
		}
		return out
	}
	expect := func(name string, got map[timelineKey]bool, want ...timelineKey) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
		for _, k := range want {
			if !got[k] {
				t.Fatalf("%s: got %v, missing %v", name, got, k)
			}
		}
	}
	ua, ai, on := timelineKey{"audit", userAudit}, timelineKey{"audit", aiAudit}, timelineKey{"device", online}
	dc, hc := timelineKey{"command", devCmd}, timelineKey{"command", hostCmd}

	expect("user", run(store.AuditTimelineQuery{Origins: []string{"user"}}), ua, dc, hc)
	expect("ai", run(store.AuditTimelineQuery{Origins: []string{"ai"}}), ai)
	expect("device", run(store.AuditTimelineQuery{Origins: []string{"device"}}), on)
	expect("user+device", run(store.AuditTimelineQuery{Origins: []string{"user", "device"}}), ua, dc, hc, on)
	expect("all", run(store.AuditTimelineQuery{Origins: []string{"user", "ai", "device"}}), ua, ai, on, dc, hc)

	_, _, err := database.AuditTimeline(ctx, store.AuditTimelineQuery{Origins: []string{"bogus"}})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("unknown origin error = %v, want bad param", err)
	}
	_, _, err = database.AuditTimeline(ctx, store.AuditTimelineQuery{Cursor: &store.AuditTimelineCursor{TS: 1, Stream: "nope", ID: 1}})
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("bad cursor error = %v, want bad param", err)
	}

	host := "host1"
	expect("host asset", run(store.AuditTimelineQuery{AssetID: &host}), ua, ai, hc)
	for _, id := range []string{"dev1", "device/dev1"} {
		id := id
		expect("device "+id, run(store.AuditTimelineQuery{AssetID: &id}), on, dc)
	}

	from, to := int64(200), int64(400)
	expect("range", run(store.AuditTimelineQuery{From: &from, To: &to}), ai, on)
}

func TestAuditTimelineOwnerIsolationDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	a := ipc.WithUserID(context.Background(), "owner-a")
	b := ipc.WithUserID(context.Background(), "owner-b")
	aAudit := timelineAudit(t, database, "owner-a", "user", "x", 100, "h")
	aCmd := timelineCommand(t, database, "owner-a", 110, "h")
	aDev := timelineAudit(t, database, "owner-a", "fleet", "device_online", 120, "d")
	bAudit := timelineAudit(t, database, "owner-b", "user", "x", 130, "h")
	bCmd := timelineCommand(t, database, "owner-b", 140, "h")
	bDev := timelineAudit(t, database, "owner-b", "fleet", "device_offline", 150, "d")

	toSet := func(ctx context.Context) map[timelineKey]bool {
		out := map[timelineKey]bool{}
		for _, k := range timelineSeq(timelineAll(t, ctx, database, store.AuditTimelineQuery{Limit: 2})) {
			out[k] = true
		}
		return out
	}
	check := func(name string, got map[timelineKey]bool, want ...timelineKey) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", name, got, want)
		}
		for _, k := range want {
			if !got[k] {
				t.Fatalf("%s: missing %v in %v", name, k, got)
			}
		}
	}
	check("a", toSet(a), timelineKey{"audit", aAudit}, timelineKey{"command", aCmd}, timelineKey{"device", aDev})
	check("b", toSet(b), timelineKey{"audit", bAudit}, timelineKey{"command", bCmd}, timelineKey{"device", bDev})
	if got := toSet(context.Background()); len(got) != 6 {
		t.Fatalf("ownerless sees %d items, want 6", len(got))
	}
}

func TestAuditTimelineLimitClampDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	rows := make([]*store.AuditRow, 0, 520)
	for i := 0; i < 520; i++ {
		rows = append(rows, &store.AuditRow{TS: int64(i), Source: "user", Kind: "x", PayloadJSON: "{}"})
	}
	if err := query.Use(database.GORM()).AuditRow.WithContext(ctx).CreateInBatches(rows, 100); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ limit, want int64 }{{0, 100}, {-3, 100}, {7, 7}, {501, 500}, {100000, 500}} {
		items, next, err := database.AuditTimeline(ctx, store.AuditTimelineQuery{Limit: c.limit})
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(items)) != c.want || next == nil {
			t.Fatalf("limit %d: got %d items next=%v, want %d", c.limit, len(items), next, c.want)
		}
	}
}
