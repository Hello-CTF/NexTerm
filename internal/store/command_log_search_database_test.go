package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func insertCommands(t *testing.T, db *store.Store, ctx context.Context, inputs ...store.CommandLogInput) {
	t.Helper()
	for _, input := range inputs {
		if err := db.CommandLogInsert(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
}

func searchCommands(t *testing.T, db *store.Store, ctx context.Context, filter store.CommandLogSearch) []string {
	t.Helper()
	rows, _, err := db.CommandLogSearch(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Command)
	}
	return out
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, v := range want {
		seen[v]++
	}
	for _, v := range got {
		seen[v]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestCommandLogSearchMatchingDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	for i, command := range []string{"Docker PS -a", "ls", "echo 中文Ünï", "rm 100%_done!x", "rm 100xxdone", `printf 'a\b'`, "plain"} {
		insertCommands(t, db, ctx, store.CommandLogInput{SessionID: "s", TabID: "t", AssetID: "a", Command: command, Source: "terminal", StartedAt: int64(1000 + i)})
	}
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"docker ps", []string{"Docker PS -a"}},
		{"LS", []string{"ls"}},
		{"Ünï", []string{"echo 中文Ünï"}},
		{"100%_", []string{"rm 100%_done!x"}},
		{"done!x", []string{"rm 100%_done!x"}},
		{`\b`, []string{`printf 'a\b'`}},
		{"zzz", []string{}},
	} {
		if got := searchCommands(t, db, ctx, store.CommandLogSearch{Text: tc.text}); !sameStrings(got, tc.want) {
			t.Errorf("search %q = %q, want %q", tc.text, got, tc.want)
		}
	}
	if got := searchCommands(t, db, ctx, store.CommandLogSearch{}); len(got) != 7 || got[0] != "plain" {
		t.Fatalf("empty text must list most recent first: %q", got)
	}
	if _, _, err := db.CommandLogSearch(ctx, store.CommandLogSearch{Text: string(make([]byte, 257))}); err == nil {
		t.Fatal("expected error for oversized text")
	}
}

func TestCommandLogSearchFiltersAndPagingDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	for i := 0; i < 5; i++ {
		asset := "a1"
		if i%2 == 1 {
			asset = "a2"
		}
		insertCommands(t, db, ctx, store.CommandLogInput{SessionID: "s", TabID: "t", AssetID: asset, Command: fmt.Sprintf("cmd %d", i), Source: "terminal", StartedAt: int64(1000 + i*10)})
	}
	if got := searchCommands(t, db, ctx, store.CommandLogSearch{Text: "cmd", AssetID: auditPtr("a2")}); !sameStrings(got, []string{"cmd 1", "cmd 3"}) {
		t.Fatalf("asset filter: %q", got)
	}
	if got := searchCommands(t, db, ctx, store.CommandLogSearch{StartedFrom: auditPtr(int64(1010)), StartedTo: auditPtr(int64(1030))}); !sameStrings(got, []string{"cmd 1", "cmd 2"}) {
		t.Fatalf("range filter: %q", got)
	}
	rows, more, err := db.CommandLogSearch(ctx, store.CommandLogSearch{Limit: 2})
	if err != nil || len(rows) != 2 || !more || rows[0].Command != "cmd 4" || rows[1].Command != "cmd 3" {
		t.Fatalf("page 1 rows=%+v more=%v err=%v", rows, more, err)
	}
	rows, more, err = db.CommandLogSearch(ctx, store.CommandLogSearch{Limit: 2, Offset: 4})
	if err != nil || len(rows) != 1 || more || rows[0].Command != "cmd 0" {
		t.Fatalf("last page rows=%+v more=%v err=%v", rows, more, err)
	}
}

func TestCommandLogSearchLimitClampDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	inputs := make([]store.CommandLogInput, 0, 501)
	for i := 0; i < 501; i++ {
		inputs = append(inputs, store.CommandLogInput{SessionID: "s", TabID: "t", AssetID: "a", Command: "x", Source: "terminal", StartedAt: int64(i)})
	}
	insertCommands(t, db, ctx, inputs...)
	rows, more, err := db.CommandLogSearch(ctx, store.CommandLogSearch{Limit: 100000})
	if err != nil || len(rows) != 500 || !more {
		t.Fatalf("clamped rows=%d more=%v err=%v", len(rows), more, err)
	}
	rows, more, err = db.CommandLogSearch(ctx, store.CommandLogSearch{})
	if err != nil || len(rows) != 100 || !more {
		t.Fatalf("default rows=%d more=%v err=%v", len(rows), more, err)
	}
}

func TestCommandLogSearchOwnerIsolationDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	for _, user := range []string{"u-a", "u-b"} {
		insertCommands(t, db, ctx, store.CommandLogInput{SessionID: "s-" + user, TabID: "t", AssetID: "a", UserID: user, Command: "uptime", Source: "terminal", StartedAt: 1000})
	}
	rows, _, err := db.CommandLogSearch(ipc.WithUserID(ctx, "u-a"), store.CommandLogSearch{Text: "uptime"})
	if err != nil || len(rows) != 1 || rows[0].SessionID != "s-u-a" {
		t.Fatalf("owner a rows=%+v err=%v", rows, err)
	}
	if got := searchCommands(t, db, ctx, store.CommandLogSearch{Text: "uptime"}); len(got) != 2 {
		t.Fatalf("no owner must see all: %q", got)
	}
}

func TestCommandLogSearchTranscriptIDDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(ctx, "u-a")
	ctxB := ipc.WithUserID(ctx, "u-b")
	start := func(c context.Context, id, session string, at int64) {
		t.Helper()
		if err := db.TranscriptStart(c, store.TranscriptRow{ID: id, SessionID: session, AssetID: "a", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	start(ctxA, "tr-1", "s1", 1000)
	start(ctxA, "tr-2", "s1", 2000)
	start(ctxB, "tr-other", "s3", 500)
	insertCommands(t, db, ctx,
		store.CommandLogInput{SessionID: "s1", TabID: "t", AssetID: "a", UserID: "u-a", Command: "early", Source: "terminal", StartedAt: 1500},
		store.CommandLogInput{SessionID: "s1", TabID: "t", AssetID: "a", UserID: "u-a", Command: "late", Source: "terminal", StartedAt: 2500},
		store.CommandLogInput{SessionID: "s1", TabID: "t", AssetID: "a", UserID: "u-a", Command: "before", Source: "terminal", StartedAt: 100},
		store.CommandLogInput{SessionID: "s2", TabID: "t", AssetID: "a", UserID: "u-a", Command: "none", Source: "terminal", StartedAt: 1500},
		store.CommandLogInput{SessionID: "s3", TabID: "t", AssetID: "a", UserID: "u-a", Command: "foreign", Source: "terminal", StartedAt: 1500},
	)
	rows, _, err := db.CommandLogSearch(ctxA, store.CommandLogSearch{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*string{}
	for _, row := range rows {
		got[row.Command] = row.TranscriptID
	}
	want := map[string]string{"early": "tr-1", "late": "tr-2", "before": "tr-1"}
	for command, id := range want {
		if got[command] == nil || *got[command] != id {
			t.Errorf("%s transcript = %v, want %s", command, got[command], id)
		}
	}
	for _, command := range []string{"none", "foreign"} {
		if got[command] != nil {
			t.Errorf("%s must have no transcript, got %s", command, *got[command])
		}
	}
}
